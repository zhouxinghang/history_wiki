package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/httpapi"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

const eventCount = 100_000

type scenarioResult struct {
	Name               string  `json:"name"`
	Samples            int     `json:"samples"`
	Status             int     `json:"status"`
	P95MS              float64 `json:"p95Ms"`
	Threshold          float64 `json:"thresholdMs"`
	ReturnedEvents     *int    `json:"returnedEvents,omitempty"`
	TotalMatching      *int    `json:"totalMatching,omitempty"`
	ReturnedProminence *int    `json:"returnedProminence,omitempty"`
	Passed             bool    `json:"passed"`
	Description        string  `json:"description"`
}

type report struct {
	GeneratedAt         string           `json:"generatedAt"`
	PostgreSQLVersion   string           `json:"postgresqlVersion"`
	Events              int              `json:"events"`
	EmptyAfterMigration bool             `json:"emptyAfterMigration"`
	PoolMaxConnections  int32            `json:"poolMaxConnections"`
	Scenarios           []scenarioResult `json:"scenarios"`
	Peak100QPS          peakResult       `json:"peak100Qps"`
	OversizedResult     oversizedResult  `json:"oversizedResult"`
	Passed              bool             `json:"passed"`
}

type peakResult struct {
	Requests  int     `json:"requests"`
	ElapsedMS float64 `json:"elapsedMs"`
	P95MS     float64 `json:"p95Ms"`
	Errors    int     `json:"errors"`
	Passed    bool    `json:"passed"`
}

type oversizedResult struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Passed bool   `json:"passed"`
}

type scenarioDefinition struct {
	name, path, description string
	threshold               float64
	expectedEvents          *int
	expectedTotal           *int
	expectedProminence      *int
	expectedID              string
	expectedMetadata        bool
}

func main() {
	databaseURL := flag.String("database-url", os.Getenv("PERFORMANCE_DATABASE_URL"), "dedicated PostgreSQL URL; its database name must contain test or perf")
	output := flag.String("output", "", "optional JSON report path")
	samples := flag.Int("samples", 20, "samples per scenario after warmup")
	reset := flag.Bool("reset-dedicated-database", false, "required acknowledgement that the target database may be erased")
	flag.Parse()
	if !*reset || *databaseURL == "" || *samples < 5 {
		fatal(errors.New("provide -database-url, -samples >= 5 and -reset-dedicated-database"))
	}
	parsed, err := url.Parse(*databaseURL)
	if err != nil {
		fatal(errors.New("invalid performance database URL"))
	}
	databaseName := strings.ToLower(strings.TrimPrefix(parsed.Path, "/"))
	if !regexp.MustCompile(`(^|[-_])(test|perf)([-_]|$)`).MatchString(databaseName) {
		fatal(errors.New("refusing to erase a database whose name does not contain test or perf"))
	}
	ctx := context.Background()
	if err := resetAndMigrate(ctx, *databaseURL); err != nil {
		fatal(err)
	}
	pool, err := pgxpool.New(ctx, *databaseURL)
	if err != nil {
		fatal(err)
	}
	if err := assertProductionCatalogEmpty(ctx, pool); err != nil {
		fatal(err)
	}
	if err := seed(ctx, pool); err != nil {
		fatal(err)
	}
	var postgresVersion string
	if err := pool.QueryRow(ctx, `SHOW server_version`).Scan(&postgresVersion); err != nil {
		fatal(err)
	}
	pool.Close()

	database, err := store.OpenWithOptions(ctx, *databaseURL, store.Options{MaxConns: 20, MinConns: 2, MaxConnLifetime: 30 * time.Minute, MaxConnIdleTime: 5 * time.Minute})
	if err != nil {
		fatal(err)
	}
	defer database.Close()
	handler := httpapi.New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)))
	region := "30000000-0000-7000-8000-000000000001"
	period := "40000000-0000-7000-8000-000000000001"
	figure := "50000000-0000-7000-8000-000000000001"
	scenarios := []scenarioDefinition{
		{name: "wide-range", path: "/api/v1/events?from=-3000&to=5000", description: "宽范围自动降为 L1，返回 4,000 条", threshold: 200, expectedEvents: intPointer(4000), expectedTotal: intPointer(100000), expectedProminence: intPointer(1)},
		{name: "narrow-high-density", path: "/api/v1/events?from=99.5&to=100.5", description: "窄范围高密度，返回 4,020 条", threshold: 200, expectedEvents: intPointer(4020), expectedTotal: intPointer(4020), expectedProminence: intPointer(3)},
		{name: "chinese-substring", path: "/api/v1/events?from=-3000&to=5000&q=" + url.QueryEscape("丝绸交流"), description: "中文字面子串搜索，命中 1,000 条", threshold: 200, expectedEvents: intPointer(1000), expectedTotal: intPointer(1000), expectedProminence: intPointer(1)},
		{name: "multi-dimension", path: fmt.Sprintf("/api/v1/events?from=-3000&to=5000&period=%s&region=%s&figure=%s&category=%s", period, region, figure, url.QueryEscape("政治")), description: "时期/地区/人物/主分类多维筛选", threshold: 200, expectedEvents: intPointer(666), expectedTotal: intPointer(16666), expectedProminence: intPointer(1)},
		{name: "event-detail", path: "/api/v1/events/10000000-0000-7000-8000-000000000001", description: "不可变 ID 详情", threshold: 100, expectedID: "10000000-0000-7000-8000-000000000001"},
		{name: "metadata", path: "/api/v1/event-metadata", description: "公开筛选元数据", threshold: 100, expectedMetadata: true},
	}
	result := report{GeneratedAt: time.Now().UTC().Format(time.RFC3339), PostgreSQLVersion: postgresVersion, Events: eventCount, EmptyAfterMigration: true, PoolMaxConnections: 20, Passed: true}
	for scenarioIndex, scenario := range scenarios {
		for warmup := 0; warmup < 3; warmup++ {
			perform(handler, scenario.path, scenarioIndex*1000+warmup)
		}
		durations := make([]time.Duration, 0, *samples)
		status := http.StatusOK
		semanticOK := true
		var returnedEvents, totalMatching, returnedProminence *int
		for sample := 0; sample < *samples; sample++ {
			currentStatus, body, duration := perform(handler, scenario.path, scenarioIndex*1000+100+sample)
			durations = append(durations, duration)
			if currentStatus != http.StatusOK {
				status = currentStatus
				semanticOK = false
				continue
			}
			observedEvents, observedTotal, observedProminence, valid := validateScenario(body, scenario)
			if !valid {
				semanticOK = false
			}
			returnedEvents, totalMatching, returnedProminence = observedEvents, observedTotal, observedProminence
		}
		p95 := percentile95(durations)
		item := scenarioResult{Name: scenario.name, Samples: *samples, Status: status, P95MS: milliseconds(p95), Threshold: scenario.threshold, ReturnedEvents: returnedEvents, TotalMatching: totalMatching, ReturnedProminence: returnedProminence, Passed: semanticOK && milliseconds(p95) < scenario.threshold, Description: scenario.description}
		result.Scenarios = append(result.Scenarios, item)
		result.Passed = result.Passed && item.Passed
	}
	management := measureManagementWrites(handler, *samples)
	result.Scenarios = append(result.Scenarios, management)
	result.Passed = result.Passed && management.Passed
	status, body, _ := perform(handler, "/api/v1/events?from=99&to=101", 9000)
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &problem)
	result.OversizedResult = oversizedResult{Status: status, Code: problem.Code, Passed: status == http.StatusUnprocessableEntity && problem.Code == "result_set_too_large"}
	result.Passed = result.Passed && result.OversizedResult.Passed
	result.Peak100QPS = runPeak(handler)
	result.Passed = result.Passed && result.Peak100QPS.Passed

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal(err)
	}
	encoded = append(encoded, '\n')
	if *output != "" {
		if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(*output, encoded, 0o644); err != nil {
			fatal(err)
		}
	}
	_, _ = os.Stdout.Write(encoded)
	if !result.Passed {
		os.Exit(1)
	}
}

func resetAndMigrate(ctx context.Context, databaseURL string) error {
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		return fmt.Errorf("reset database: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(database, "migrations")
}

func assertProductionCatalogEmpty(ctx context.Context, pool *pgxpool.Pool) error {
	var events, users, revisions, audits int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM events),
		(SELECT count(*) FROM users),
		(SELECT count(*) FROM event_revisions),
		(SELECT count(*) FROM audit_logs)`).Scan(&events, &users, &revisions, &audits); err != nil {
		return fmt.Errorf("verify empty migrated database: %w", err)
	}
	if events != 0 || users != 0 || revisions != 0 || audits != 0 {
		return fmt.Errorf("migrations loaded production data: events=%d users=%d revisions=%d audits=%d", events, users, revisions, audits)
	}
	return nil
}

func seed(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	const userID = "70000000-0000-7000-8000-000000000001"
	if _, err := tx.Exec(ctx, `INSERT INTO users (id,email,normalized_email,password_hash,role) VALUES ($1,'perf@example.invalid','perf@example.invalid','$argon2id$fixture','administrator')`, userID); err != nil {
		return err
	}
	entities := []string{
		`INSERT INTO regions (id,name,status,created_by,updated_by) VALUES ('30000000-0000-7000-8000-000000000001','性能地区','active',$1,$1)`,
		`INSERT INTO historical_periods (id,name,status,created_by,updated_by) VALUES ('40000000-0000-7000-8000-000000000001','性能时期','active',$1,$1)`,
		`INSERT INTO historical_figures (id,name,status,created_by,updated_by) VALUES ('50000000-0000-7000-8000-000000000001','性能人物','active',$1,$1)`,
		`INSERT INTO places (id,name,status,created_by,updated_by) VALUES ('60000000-0000-7000-8000-000000000001','性能地点','active',$1,$1)`,
		`INSERT INTO topic_tags (id,name,status,created_by,updated_by) VALUES ('80000000-0000-7000-8000-000000000001','性能主题','active',$1,$1)`,
	}
	for _, statement := range entities {
		if _, err := tx.Exec(ctx, statement, userID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO period_regions (historical_period_id,region_id) VALUES ('40000000-0000-7000-8000-000000000001','30000000-0000-7000-8000-000000000001')`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO place_regions (place_id,region_id) VALUES ('60000000-0000-7000-8000-000000000001','30000000-0000-7000-8000-000000000001')`); err != nil {
		return err
	}
	sessionDigest := auth.TokenDigest("performance-session-token")
	csrfDigest := auth.TokenDigest("performance-csrf-token")
	if _, err := tx.Exec(ctx, `INSERT INTO sessions (id,token_digest,csrf_token_digest,user_id,idle_expires_at,absolute_expires_at) VALUES ('90000000-0000-7000-8000-000000000001',$1,$2,$3,CURRENT_TIMESTAMP + interval '1 hour',CURRENT_TIMESTAMP + interval '2 hours')`, sessionDigest[:], csrfDigest[:], userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO events (id,slug,publication_status,created_by,updated_by)
		SELECT ('10000000-0000-7000-8000-' || lpad(to_hex(n),12,'0'))::uuid,
		       'performance-event-' || n, 'published', $1, $1
		FROM generate_series(1,$2) n`, userID, eventCount); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event_revisions (
			id,event_id,revision_no,title,summary,narrative,time_kind,start_era,start_year,
			start_circa,end_circa,start_coordinate,end_coordinate,primary_category,prominence,
			display_order,published_by,published_at)
		SELECT ('20000000-0000-7000-8000-' || lpad(to_hex(n),12,'0'))::uuid,
		       ('10000000-0000-7000-8000-' || lpad(to_hex(n),12,'0'))::uuid,
		       1,
		       CASE WHEN n % 100 = 0 THEN '丝绸交流性能事件 ' || n ELSE '历史性能事件 ' || n END,
		       '确定性的十万事件性能摘要 ' || n,
		       '用于真实 PostgreSQL 查询验证的历史事件正文 ' || n,
		       'year','CE', CASE WHEN n <= 4000 THEN 100 WHEN n <= 10000 THEN 101 ELSE 1 + (n % 4500) END,
		       false,false,
		       CASE WHEN n <= 4000 THEN 100 WHEN n <= 10000 THEN 101 ELSE 1 + (n % 4500) END,
		       CASE WHEN n <= 4000 THEN 100 WHEN n <= 10000 THEN 101 ELSE 1 + (n % 4500) END,
		       (ARRAY['政治','军事','文化','科技','社会','交流']::primary_category[])[1 + (n % 6)],
		       CASE WHEN n % 25 = 0 THEN 1 WHEN n % 5 = 0 THEN 2 ELSE 3 END,
		       n,$1,CURRENT_TIMESTAMP
		FROM generate_series(1,$2) n`, userID, eventCount); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE events SET current_revision_id = ('20000000-0000-7000-8000-' || substring(id::text from 25))::uuid`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO published_event_search (event_id,event_revision_id,searchable_text) SELECT event_id,id,title || ' ' || summary || ' ' || narrative || ' 性能地点 性能人物 性能主题' FROM event_revisions`); err != nil {
		return err
	}
	relations := []string{
		`INSERT INTO event_revision_regions SELECT id,'30000000-0000-7000-8000-000000000001' FROM event_revisions`,
		`INSERT INTO event_revision_periods SELECT id,'40000000-0000-7000-8000-000000000001' FROM event_revisions`,
		`INSERT INTO event_revision_figures SELECT id,'50000000-0000-7000-8000-000000000001' FROM event_revisions`,
		`INSERT INTO event_revision_places SELECT id,'60000000-0000-7000-8000-000000000001' FROM event_revisions`,
		`INSERT INTO event_revision_topic_tags SELECT id,'80000000-0000-7000-8000-000000000001' FROM event_revisions`,
	}
	for _, statement := range relations {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `ANALYZE`)
	return err
}

func appendRequest(values []time.Duration, handler http.Handler, path string, client int) (int, []byte, []time.Duration) {
	status, body, duration := perform(handler, path, client)
	return status, body, append(values, duration)
}

func validateScenario(body []byte, scenario scenarioDefinition) (*int, *int, *int, bool) {
	if scenario.expectedEvents != nil {
		var response struct {
			Events             []json.RawMessage `json:"events"`
			TotalMatching      int               `json:"totalMatching"`
			ReturnedProminence int               `json:"returnedProminence"`
		}
		if json.Unmarshal(body, &response) != nil {
			return nil, nil, nil, false
		}
		count := len(response.Events)
		valid := count == *scenario.expectedEvents && response.TotalMatching == *scenario.expectedTotal && response.ReturnedProminence == *scenario.expectedProminence
		return &count, &response.TotalMatching, &response.ReturnedProminence, valid
	}
	if scenario.expectedID != "" {
		var response struct {
			ID string `json:"id"`
		}
		return nil, nil, nil, json.Unmarshal(body, &response) == nil && response.ID == scenario.expectedID
	}
	if scenario.expectedMetadata {
		var response struct {
			Regions      []json.RawMessage `json:"regions"`
			PeriodGroups []json.RawMessage `json:"periodGroups"`
			Figures      []json.RawMessage `json:"figures"`
		}
		return nil, nil, nil, json.Unmarshal(body, &response) == nil && len(response.Regions) == 1 && len(response.PeriodGroups) == 1 && len(response.Figures) == 1
	}
	return nil, nil, nil, false
}

func measureManagementWrites(handler http.Handler, samples int) scenarioResult {
	for warmup := 0; warmup < 3; warmup++ {
		performManagementWrite(handler, -warmup-1)
	}
	durations := make([]time.Duration, 0, samples)
	status := http.StatusCreated
	valid := true
	for sample := 0; sample < samples; sample++ {
		currentStatus, body, duration := performManagementWrite(handler, sample)
		durations = append(durations, duration)
		if currentStatus != http.StatusCreated || !strings.Contains(string(body), fmt.Sprintf("性能写入地区 %d", sample)) {
			status = currentStatus
			valid = false
		}
	}
	p95 := milliseconds(percentile95(durations))
	return scenarioResult{Name: "management-write", Samples: samples, Status: status, P95MS: p95, Threshold: 500, Passed: valid && p95 < 500, Description: "真实 Session、CSRF、审计和事务边界下创建地区"}
}

func performManagementWrite(handler http.Handler, index int) (int, []byte, time.Duration) {
	body := strings.NewReader(fmt.Sprintf(`{"name":"性能写入地区 %d","disambiguationLabel":null}`, index))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/regions", body)
	request.RemoteAddr = "198.51.100.20:1234"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://example.com")
	request.Header.Set("X-CSRF-Token", "performance-csrf-token")
	request.Header.Set("Idempotency-Key", fmt.Sprintf("performance-region-%d", index))
	request.AddCookie(&http.Cookie{Name: "__Host-history_wiki_session", Value: "performance-session-token"})
	request.AddCookie(&http.Cookie{Name: "__Host-history_wiki_csrf", Value: "performance-csrf-token"})
	response := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(response, request)
	return response.Code, response.Body.Bytes(), time.Since(started)
}

func perform(handler http.Handler, path string, client int) (int, []byte, time.Duration) {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", 1+client%250)
	response := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(response, request)
	return response.Code, response.Body.Bytes(), time.Since(started)
}

func runPeak(handler http.Handler) peakResult {
	const requests = 100
	started := time.Now()
	durations := make([]time.Duration, requests)
	statuses := make([]int, requests)
	var wait sync.WaitGroup
	for index := 0; index < requests; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			target := started.Add(time.Duration(index) * 10 * time.Millisecond)
			if delay := time.Until(target); delay > 0 {
				time.Sleep(delay)
			}
			statuses[index], _, durations[index] = perform(handler, "/api/v1/events/10000000-0000-7000-8000-000000000001", 20_000+index)
		}(index)
	}
	wait.Wait()
	errors := 0
	for _, status := range statuses {
		if status != http.StatusOK {
			errors++
		}
	}
	p95 := milliseconds(percentile95(durations))
	elapsed := milliseconds(time.Since(started))
	return peakResult{Requests: requests, ElapsedMS: elapsed, P95MS: p95, Errors: errors, Passed: errors == 0 && p95 < 100 && elapsed < 1500}
}

func percentile95(values []time.Duration) time.Duration {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[int(math.Ceil(float64(len(ordered))*0.95))-1]
}

func milliseconds(value time.Duration) float64 {
	return math.Round(float64(value.Microseconds())/10) / 100
}

func intPointer(value int) *int { return &value }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
