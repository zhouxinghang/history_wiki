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

func (store *Postgres) ListCanonicalEntities(
	ctx context.Context,
	kind catalog.CanonicalEntityKind,
	filter catalog.CanonicalEntityListFilter,
) ([]catalog.CanonicalEntity, error) {
	table, err := catalog.CanonicalEntityTable(kind)
	if err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, fmt.Sprintf(`
		SELECT
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		FROM %s
		WHERE
			($1 = '' OR strpos(lower(name), lower($1)) > 0 OR
			 strpos(lower(COALESCE(disambiguation_label, '')), lower($1)) > 0)
			AND ($2 = '' OR status::text = $2)
		ORDER BY lower(name), lower(COALESCE(disambiguation_label, '')), id
		LIMIT $3
	`, table), filter.Query, filter.Status, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()

	entities := make([]catalog.CanonicalEntity, 0)
	for rows.Next() {
		entity, err := scanCanonicalEntity(rows)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", table, err)
	}
	return entities, nil
}

func (store *Postgres) CreateCanonicalEntity(
	ctx context.Context,
	kind catalog.CanonicalEntityKind,
	entity catalog.CanonicalEntity,
	idempotencyKey string,
	requestHash []byte,
	entry audit.Entry,
) (catalog.CanonicalEntity, bool, error) {
	table, err := catalog.CanonicalEntityTable(kind)
	if err != nil {
		return catalog.CanonicalEntity{}, false, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return catalog.CanonicalEntity{}, false, fmt.Errorf("begin %s creation transaction: %w", table, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	scope := table + ".create"
	command, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records (
			actor_user_id, scope, idempotency_key, request_hash, target_id
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (actor_user_id, scope, idempotency_key) DO NOTHING
	`, entity.CreatedBy, scope, idempotencyKey, requestHash, entity.ID)
	if err != nil {
		return catalog.CanonicalEntity{}, false, fmt.Errorf("reserve %s idempotency key: %w", table, err)
	}
	if command.RowsAffected() == 0 {
		var existingHash []byte
		var targetID string
		if err := tx.QueryRow(ctx, `
			SELECT request_hash, target_id
			FROM idempotency_records
			WHERE actor_user_id = $1 AND scope = $2 AND idempotency_key = $3
		`, entity.CreatedBy, scope, idempotencyKey).Scan(&existingHash, &targetID); err != nil {
			return catalog.CanonicalEntity{}, false, fmt.Errorf("read %s idempotency result: %w", table, err)
		}
		if !bytes.Equal(existingHash, requestHash) {
			return catalog.CanonicalEntity{}, false, catalog.ErrIdempotencyConflict
		}
		existing, err := canonicalEntityByID(ctx, tx, table, targetID, false)
		if err != nil {
			return catalog.CanonicalEntity{}, false, err
		}
		entry.ActorUserID = &entity.CreatedBy
		entry.TargetID = &existing.ID
		entry.Details = cloneAuditDetails(entry.Details)
		entry.Details["replayed"] = true
		if err := appendAudit(ctx, tx, entry); err != nil {
			return catalog.CanonicalEntity{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return catalog.CanonicalEntity{}, false, fmt.Errorf("commit %s idempotent replay transaction: %w", table, err)
		}
		return existing, true, nil
	}

	created, err := insertCanonicalEntity(ctx, tx, table, entity)
	if err != nil {
		return catalog.CanonicalEntity{}, false, err
	}
	entry.ActorUserID = &entity.CreatedBy
	entry.TargetID = &created.ID
	if err := appendAudit(ctx, tx, entry); err != nil {
		return catalog.CanonicalEntity{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.CanonicalEntity{}, false, fmt.Errorf("commit %s creation transaction: %w", table, err)
	}
	return created, false, nil
}

func cloneAuditDetails(details map[string]any) map[string]any {
	cloned := make(map[string]any, len(details)+1)
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
}

func (store *Postgres) UpdateCanonicalEntity(
	ctx context.Context,
	kind catalog.CanonicalEntityKind,
	entityID string,
	update catalog.CanonicalEntityUpdate,
	entry audit.Entry,
) (catalog.CanonicalEntity, error) {
	table, err := catalog.CanonicalEntityTable(kind)
	if err != nil {
		return catalog.CanonicalEntity{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return catalog.CanonicalEntity{}, fmt.Errorf("begin %s update transaction: %w", table, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	current, err := canonicalEntityByID(ctx, tx, table, entityID, true)
	if err != nil {
		return catalog.CanonicalEntity{}, err
	}
	if current.LockVersion != update.ExpectedVersion {
		return catalog.CanonicalEntity{}, catalog.ErrVersionConflict
	}
	if current.Status == catalog.StatusMerged {
		return catalog.CanonicalEntity{}, catalog.ErrCanonicalEntityNotWritable
	}
	if err := ensureCanonicalEntityStatusChangeAllowed(ctx, tx, table, entityID, update.Status); err != nil {
		return catalog.CanonicalEntity{}, err
	}

	updated, err := scanCanonicalEntity(tx.QueryRow(ctx, fmt.Sprintf(`
		UPDATE %s
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
	`, table), entityID, update.Name, update.DisambiguationLabel, update.Status, update.UpdatedBy, update.UpdatedAt))
	if err != nil {
		return catalog.CanonicalEntity{}, fmt.Errorf("update %s: %w", table, err)
	}
	if err := refreshPublishedSearchForCanonicalIdentity(ctx, tx, governanceSpecs[kind], entityID); err != nil {
		return catalog.CanonicalEntity{}, err
	}

	entry.ActorUserID = &update.UpdatedBy
	entry.TargetID = &updated.ID
	if entry.Details == nil {
		entry.Details = map[string]any{}
	}
	entry.Details["previousLockVersion"] = current.LockVersion
	entry.Details["lockVersion"] = updated.LockVersion
	if err := appendAudit(ctx, tx, entry); err != nil {
		return catalog.CanonicalEntity{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.CanonicalEntity{}, fmt.Errorf("commit %s update transaction: %w", table, err)
	}
	return updated, nil
}

func insertCanonicalEntity(
	ctx context.Context,
	tx pgx.Tx,
	table string,
	entity catalog.CanonicalEntity,
) (catalog.CanonicalEntity, error) {
	created, err := scanCanonicalEntity(tx.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $6, $7, $7)
		RETURNING
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
	`, table),
		entity.ID,
		entity.Name,
		entity.DisambiguationLabel,
		entity.Status,
		entity.MergedIntoID,
		entity.CreatedBy,
		entity.CreatedAt,
	))
	if err != nil {
		return catalog.CanonicalEntity{}, fmt.Errorf("insert %s: %w", table, err)
	}
	return created, nil
}

type canonicalEntityScanner interface {
	Scan(...any) error
}

func scanCanonicalEntity(row canonicalEntityScanner) (catalog.CanonicalEntity, error) {
	var entity catalog.CanonicalEntity
	if err := row.Scan(
		&entity.ID,
		&entity.Name,
		&entity.DisambiguationLabel,
		&entity.Status,
		&entity.MergedIntoID,
		&entity.LockVersion,
		&entity.CreatedBy,
		&entity.UpdatedBy,
		&entity.CreatedAt,
		&entity.UpdatedAt,
	); err != nil {
		return catalog.CanonicalEntity{}, err
	}
	return entity, nil
}

func canonicalEntityByID(
	ctx context.Context,
	querier regionQuerier,
	table string,
	entityID string,
	forUpdate bool,
) (catalog.CanonicalEntity, error) {
	lockingClause := ""
	if forUpdate {
		lockingClause = " FOR UPDATE"
	}
	entity, err := scanCanonicalEntity(querier.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		FROM %s
		WHERE id = $1%s
	`, table, lockingClause), entityID))
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.CanonicalEntity{}, catalog.ErrCanonicalEntityNotFound
	}
	if err != nil {
		return catalog.CanonicalEntity{}, fmt.Errorf("find %s: %w", table, err)
	}
	return entity, nil
}
