package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
)

const createRegionIdempotencyScope = "regions.create"

func (store *Postgres) ListRegions(ctx context.Context, filter catalog.RegionListFilter) ([]catalog.Region, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		FROM regions
		WHERE
			($1 = '' OR strpos(lower(name), lower($1)) > 0 OR
			 strpos(lower(COALESCE(disambiguation_label, '')), lower($1)) > 0)
			AND ($2 = '' OR status::text = $2)
		ORDER BY lower(name), lower(COALESCE(disambiguation_label, '')), id
		LIMIT $3
	`, filter.Query, filter.Status, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list regions: %w", err)
	}
	defer rows.Close()

	regions := make([]catalog.Region, 0)
	for rows.Next() {
		region, err := scanRegion(rows)
		if err != nil {
			return nil, err
		}
		regions = append(regions, region)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate regions: %w", err)
	}
	return regions, nil
}

func (store *Postgres) CreateRegion(
	ctx context.Context,
	region catalog.Region,
	idempotencyKey string,
	requestHash []byte,
	entry audit.Entry,
) (catalog.Region, bool, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return catalog.Region{}, false, fmt.Errorf("begin region creation transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	command, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records (
			actor_user_id, scope, idempotency_key, request_hash, target_id
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (actor_user_id, scope, idempotency_key) DO NOTHING
	`, region.CreatedBy, createRegionIdempotencyScope, idempotencyKey, requestHash, region.ID)
	if err != nil {
		return catalog.Region{}, false, fmt.Errorf("reserve region idempotency key: %w", err)
	}
	if command.RowsAffected() == 0 {
		var existingHash []byte
		var targetID string
		if err := tx.QueryRow(ctx, `
			SELECT request_hash, target_id
			FROM idempotency_records
			WHERE actor_user_id = $1 AND scope = $2 AND idempotency_key = $3
		`, region.CreatedBy, createRegionIdempotencyScope, idempotencyKey).Scan(&existingHash, &targetID); err != nil {
			return catalog.Region{}, false, fmt.Errorf("read region idempotency result: %w", err)
		}
		if !bytes.Equal(existingHash, requestHash) {
			return catalog.Region{}, false, catalog.ErrIdempotencyConflict
		}
		existing, err := regionByID(ctx, tx, targetID, false)
		if err != nil {
			return catalog.Region{}, false, err
		}
		return existing, true, nil
	}

	created, err := insertRegion(ctx, tx, region)
	if err != nil {
		return catalog.Region{}, false, err
	}
	entry.ActorUserID = &region.CreatedBy
	entry.TargetID = &created.ID
	if err := appendAudit(ctx, tx, entry); err != nil {
		return catalog.Region{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Region{}, false, fmt.Errorf("commit region creation transaction: %w", err)
	}
	return created, false, nil
}

func (store *Postgres) UpdateRegion(
	ctx context.Context,
	regionID string,
	update catalog.RegionUpdate,
	entry audit.Entry,
) (catalog.Region, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return catalog.Region{}, fmt.Errorf("begin region update transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	current, err := regionByID(ctx, tx, regionID, true)
	if err != nil {
		return catalog.Region{}, err
	}
	if current.LockVersion != update.ExpectedVersion {
		return catalog.Region{}, catalog.ErrVersionConflict
	}
	if current.Status == catalog.StatusMerged {
		return catalog.Region{}, catalog.ErrRegionNotWritable
	}
	if err := ensureCanonicalEntityStatusChangeAllowed(ctx, tx, "regions", regionID, update.Status); err != nil {
		if errors.Is(err, catalog.ErrCanonicalEntityNotWritable) {
			return catalog.Region{}, catalog.ErrRegionNotWritable
		}
		return catalog.Region{}, err
	}

	updated, err := scanRegion(tx.QueryRow(ctx, `
		UPDATE regions
		SET
			name = $2,
			disambiguation_label = $3,
			status = $4,
			updated_by = $5,
			updated_at = $6,
			lock_version = lock_version + 1
		WHERE id = $1
		RETURNING
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
	`, regionID, update.Name, update.DisambiguationLabel, update.Status, update.UpdatedBy, update.UpdatedAt))
	if err != nil {
		return catalog.Region{}, fmt.Errorf("update region: %w", err)
	}
	if err := refreshPublishedSearchForCanonicalIdentity(ctx, tx, governanceSpecs[catalog.KindRegion], regionID); err != nil {
		return catalog.Region{}, err
	}

	entry.ActorUserID = &update.UpdatedBy
	entry.TargetID = &updated.ID
	if entry.Details == nil {
		entry.Details = map[string]any{}
	}
	entry.Details["previousLockVersion"] = current.LockVersion
	entry.Details["lockVersion"] = updated.LockVersion
	if err := appendAudit(ctx, tx, entry); err != nil {
		return catalog.Region{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Region{}, fmt.Errorf("commit region update transaction: %w", err)
	}
	return updated, nil
}

func insertRegion(ctx context.Context, tx pgx.Tx, region catalog.Region) (catalog.Region, error) {
	created, err := scanRegion(tx.QueryRow(ctx, `
		INSERT INTO regions (
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $6, $7, $7)
		RETURNING
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
	`,
		region.ID,
		region.Name,
		region.DisambiguationLabel,
		region.Status,
		region.MergedIntoID,
		region.CreatedBy,
		region.CreatedAt,
	))
	if err != nil {
		return catalog.Region{}, fmt.Errorf("insert region: %w", err)
	}
	return created, nil
}

type regionScanner interface {
	Scan(...any) error
}

func scanRegion(row regionScanner) (catalog.Region, error) {
	var region catalog.Region
	if err := row.Scan(
		&region.ID,
		&region.Name,
		&region.DisambiguationLabel,
		&region.Status,
		&region.MergedIntoID,
		&region.LockVersion,
		&region.CreatedBy,
		&region.UpdatedBy,
		&region.CreatedAt,
		&region.UpdatedAt,
	); err != nil {
		return catalog.Region{}, err
	}
	return region, nil
}

type regionQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func regionByID(ctx context.Context, querier regionQuerier, regionID string, forUpdate bool) (catalog.Region, error) {
	lockingClause := ""
	if forUpdate {
		lockingClause = " FOR UPDATE"
	}
	region, err := scanRegion(querier.QueryRow(ctx, `
		SELECT
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		FROM regions
		WHERE id = $1
	`+lockingClause, regionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Region{}, catalog.ErrRegionNotFound
	}
	if err != nil {
		return catalog.Region{}, fmt.Errorf("find region: %w", err)
	}
	return region, nil
}
