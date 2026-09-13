package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Options struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

type DatabasePoolMetrics struct {
	AcquiredConns int32
	IdleConns     int32
	TotalConns    int32
	MaxConns      int32
	AcquireCount  int64
}

type DatabaseQueryMetric struct {
	Operation       string
	Count           uint64
	Errors          uint64
	DurationSeconds float64
}

type Postgres struct {
	pool         *pgxpool.Pool
	queryMetrics *queryMetrics
}

func Open(ctx context.Context, databaseURL string) (*Postgres, error) {
	return OpenWithOptions(ctx, databaseURL, Options{})
}

func OpenWithOptions(ctx context.Context, databaseURL string, options Options) (*Postgres, error) {
	configuration, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	if options.MaxConns > 0 {
		configuration.MaxConns = options.MaxConns
	}
	if options.MinConns >= 0 && options.MinConns <= configuration.MaxConns {
		configuration.MinConns = options.MinConns
	}
	if options.MaxConnLifetime > 0 {
		configuration.MaxConnLifetime = options.MaxConnLifetime
	}
	if options.MaxConnIdleTime > 0 {
		configuration.MaxConnIdleTime = options.MaxConnIdleTime
	}
	metrics := &queryMetrics{values: make(map[string]DatabaseQueryMetric)}
	configuration.ConnConfig.Tracer = metrics
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	return &Postgres{pool: pool, queryMetrics: metrics}, nil
}

func (store *Postgres) Close() { store.pool.Close() }

func (store *Postgres) Ping(ctx context.Context) error {
	if err := store.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return nil
}

func (store *Postgres) MigrationVersion(ctx context.Context) (int64, bool, error) {
	var version int64
	var applied bool
	err := store.pool.QueryRow(ctx, `
		SELECT version_id, is_applied
		FROM goose_db_version
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&version, &applied)
	if err != nil {
		return 0, false, fmt.Errorf("read migration version: %w", err)
	}
	return version, applied, nil
}

func (store *Postgres) PublishedEventCount(ctx context.Context) (int64, error) {
	var count int64
	err := store.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM events
		WHERE publication_status = 'published'
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count published events: %w", err)
	}
	return count, nil
}

func (store *Postgres) DatabaseMetrics() (DatabasePoolMetrics, []DatabaseQueryMetric) {
	stats := store.pool.Stat()
	poolMetrics := DatabasePoolMetrics{
		AcquiredConns: stats.AcquiredConns(), IdleConns: stats.IdleConns(),
		TotalConns: stats.TotalConns(), MaxConns: stats.MaxConns(), AcquireCount: stats.AcquireCount(),
	}
	return poolMetrics, store.queryMetrics.snapshot()
}

type queryStartedAtKey struct{}

type queryMetrics struct {
	mu     sync.Mutex
	values map[string]DatabaseQueryMetric
}

func (metrics *queryMetrics) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartedAtKey{}, queryTrace{startedAt: time.Now(), operation: sqlOperation(data.SQL)})
}

func (metrics *queryMetrics) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(queryStartedAtKey{}).(queryTrace)
	if !ok {
		return
	}
	metrics.mu.Lock()
	value := metrics.values[trace.operation]
	value.Operation = trace.operation
	value.Count++
	value.DurationSeconds += time.Since(trace.startedAt).Seconds()
	if data.Err != nil {
		value.Errors++
	}
	metrics.values[trace.operation] = value
	metrics.mu.Unlock()
}

type queryTrace struct {
	startedAt time.Time
	operation string
}

func (metrics *queryMetrics) snapshot() []DatabaseQueryMetric {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	result := make([]DatabaseQueryMetric, 0, len(metrics.values))
	for _, value := range metrics.values {
		result = append(result, value)
	}
	return result
}

func sqlOperation(statement string) string {
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return "unknown"
	}
	operation := strings.ToLower(fields[0])
	switch operation {
	case "select", "insert", "update", "delete", "begin", "commit", "rollback":
		return operation
	default:
		return "other"
	}
}
