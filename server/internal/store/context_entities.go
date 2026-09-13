package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
)

type contextEntityRecord struct {
	ID                  string
	Name                string
	DisambiguationLabel *string
	Status              catalog.EntityStatus
	MergedIntoID        *string
	RegionIDs           []string
	Regions             []catalog.RegionReference
	LockVersion         int64
	CreatedBy           string
	UpdatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type contextEntitySpec struct {
	kind             catalog.CanonicalEntityKind
	table            string
	joinTable        string
	joinEntityColumn string
	idempotencyScope string
	notFound         error
	notWritable      error
	versionConflict  error
}

var placeSpec = contextEntitySpec{
	kind:  catalog.KindPlace,
	table: "places", joinTable: "place_regions", joinEntityColumn: "place_id",
	idempotencyScope: "places.create", notFound: catalog.ErrPlaceNotFound,
	notWritable: catalog.ErrPlaceNotWritable, versionConflict: catalog.ErrPlaceVersionConflict,
}

var historicalPeriodSpec = contextEntitySpec{
	kind:  catalog.KindHistoricalPeriod,
	table: "historical_periods", joinTable: "period_regions", joinEntityColumn: "historical_period_id",
	idempotencyScope: "periods.create", notFound: catalog.ErrHistoricalPeriodNotFound,
	notWritable: catalog.ErrHistoricalPeriodNotWritable, versionConflict: catalog.ErrHistoricalPeriodVersionConflict,
}

func (store *Postgres) ListPlaces(ctx context.Context, filter catalog.ContextEntityListFilter) ([]catalog.Place, error) {
	records, err := store.listContextEntities(ctx, placeSpec, filter)
	if err != nil {
		return nil, err
	}
	places := make([]catalog.Place, 0, len(records))
	for _, record := range records {
		places = append(places, placeFromRecord(record))
	}
	return places, nil
}

func (store *Postgres) ListHistoricalPeriods(ctx context.Context, filter catalog.ContextEntityListFilter) ([]catalog.HistoricalPeriod, error) {
	records, err := store.listContextEntities(ctx, historicalPeriodSpec, filter)
	if err != nil {
		return nil, err
	}
	periods := make([]catalog.HistoricalPeriod, 0, len(records))
	for _, record := range records {
		periods = append(periods, historicalPeriodFromRecord(record))
	}
	return periods, nil
}

func (store *Postgres) listContextEntities(ctx context.Context, spec contextEntitySpec, filter catalog.ContextEntityListFilter) ([]contextEntityRecord, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		FROM `+spec.table+`
		WHERE
			($1 = '' OR strpos(lower(name), lower($1)) > 0 OR
			 strpos(lower(COALESCE(disambiguation_label, '')), lower($1)) > 0)
			AND ($2 = '' OR status::text = $2)
		ORDER BY lower(name), lower(COALESCE(disambiguation_label, '')), id
		LIMIT $3
	`, filter.Query, filter.Status, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", spec.table, err)
	}
	defer rows.Close()

	records := make([]contextEntityRecord, 0)
	for rows.Next() {
		record, err := scanContextEntity(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", spec.table, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", spec.table, err)
	}
	if err := loadContextEntityRegions(ctx, store.pool, spec, records); err != nil {
		return nil, err
	}
	return records, nil
}

func (store *Postgres) CreatePlace(
	ctx context.Context,
	place catalog.Place,
	idempotencyKey string,
	requestHash []byte,
	entry audit.Entry,
) (catalog.Place, bool, error) {
	record, replayed, err := store.createContextEntity(ctx, placeSpec, recordFromPlace(place), idempotencyKey, requestHash, entry)
	return placeFromRecord(record), replayed, err
}

func (store *Postgres) CreateHistoricalPeriod(
	ctx context.Context,
	period catalog.HistoricalPeriod,
	idempotencyKey string,
	requestHash []byte,
	entry audit.Entry,
) (catalog.HistoricalPeriod, bool, error) {
	record, replayed, err := store.createContextEntity(ctx, historicalPeriodSpec, recordFromHistoricalPeriod(period), idempotencyKey, requestHash, entry)
	return historicalPeriodFromRecord(record), replayed, err
}

func (store *Postgres) createContextEntity(
	ctx context.Context,
	spec contextEntitySpec,
	record contextEntityRecord,
	idempotencyKey string,
	requestHash []byte,
	entry audit.Entry,
) (contextEntityRecord, bool, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return contextEntityRecord{}, false, fmt.Errorf("begin %s creation transaction: %w", spec.table, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	command, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records (
			actor_user_id, scope, idempotency_key, request_hash, target_id
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (actor_user_id, scope, idempotency_key) DO NOTHING
	`, record.CreatedBy, spec.idempotencyScope, idempotencyKey, requestHash, record.ID)
	if err != nil {
		return contextEntityRecord{}, false, fmt.Errorf("reserve %s idempotency key: %w", spec.table, err)
	}
	if command.RowsAffected() == 0 {
		var existingHash []byte
		var targetID string
		if err := tx.QueryRow(ctx, `
			SELECT request_hash, target_id
			FROM idempotency_records
			WHERE actor_user_id = $1 AND scope = $2 AND idempotency_key = $3
		`, record.CreatedBy, spec.idempotencyScope, idempotencyKey).Scan(&existingHash, &targetID); err != nil {
			return contextEntityRecord{}, false, fmt.Errorf("read %s idempotency result: %w", spec.table, err)
		}
		if !bytes.Equal(existingHash, requestHash) {
			return contextEntityRecord{}, false, catalog.ErrIdempotencyConflict
		}
		existing, err := contextEntityByID(ctx, tx, spec, targetID, false)
		if err != nil {
			return contextEntityRecord{}, false, err
		}
		entry.ActorUserID = &record.CreatedBy
		entry.TargetID = &existing.ID
		entry.Details = cloneAuditDetails(entry.Details)
		entry.Details["replayed"] = true
		if err := appendAudit(ctx, tx, entry); err != nil {
			return contextEntityRecord{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contextEntityRecord{}, false, fmt.Errorf("commit %s idempotent replay transaction: %w", spec.table, err)
		}
		return existing, true, nil
	}

	if err := validateActiveRegionIDs(ctx, tx, record.RegionIDs); err != nil {
		return contextEntityRecord{}, false, err
	}
	created, err := insertContextEntity(ctx, tx, spec, record)
	if err != nil {
		return contextEntityRecord{}, false, err
	}
	if err := replaceContextEntityRegions(ctx, tx, spec, created.ID, record.RegionIDs); err != nil {
		return contextEntityRecord{}, false, err
	}
	created, err = contextEntityByID(ctx, tx, spec, created.ID, false)
	if err != nil {
		return contextEntityRecord{}, false, err
	}
	entry.ActorUserID = &record.CreatedBy
	entry.TargetID = &created.ID
	if err := appendAudit(ctx, tx, entry); err != nil {
		return contextEntityRecord{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contextEntityRecord{}, false, fmt.Errorf("commit %s creation transaction: %w", spec.table, err)
	}
	return created, false, nil
}

func (store *Postgres) UpdatePlace(ctx context.Context, placeID string, update catalog.PlaceUpdate, entry audit.Entry) (catalog.Place, error) {
	record, err := store.updateContextEntity(ctx, placeSpec, placeID, contextEntityRecord{
		Name: update.Name, DisambiguationLabel: update.DisambiguationLabel, Status: update.Status,
		RegionIDs: update.RegionIDs, LockVersion: update.ExpectedVersion, UpdatedBy: update.UpdatedBy, UpdatedAt: update.UpdatedAt,
	}, entry)
	return placeFromRecord(record), err
}

func (store *Postgres) UpdateHistoricalPeriod(ctx context.Context, periodID string, update catalog.HistoricalPeriodUpdate, entry audit.Entry) (catalog.HistoricalPeriod, error) {
	record, err := store.updateContextEntity(ctx, historicalPeriodSpec, periodID, contextEntityRecord{
		Name: update.Name, DisambiguationLabel: update.DisambiguationLabel, Status: update.Status,
		RegionIDs: update.RegionIDs, LockVersion: update.ExpectedVersion, UpdatedBy: update.UpdatedBy, UpdatedAt: update.UpdatedAt,
	}, entry)
	return historicalPeriodFromRecord(record), err
}

func (store *Postgres) updateContextEntity(
	ctx context.Context,
	spec contextEntitySpec,
	entityID string,
	update contextEntityRecord,
	entry audit.Entry,
) (contextEntityRecord, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return contextEntityRecord{}, fmt.Errorf("begin %s update transaction: %w", spec.table, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	current, err := contextEntityByID(ctx, tx, spec, entityID, true)
	if err != nil {
		return contextEntityRecord{}, err
	}
	if current.LockVersion != update.LockVersion {
		return contextEntityRecord{}, spec.versionConflict
	}
	if current.Status == catalog.StatusMerged {
		return contextEntityRecord{}, spec.notWritable
	}
	if err := ensureCanonicalEntityStatusChangeAllowed(ctx, tx, spec.table, entityID, update.Status); err != nil {
		if errors.Is(err, catalog.ErrCanonicalEntityNotWritable) {
			return contextEntityRecord{}, spec.notWritable
		}
		return contextEntityRecord{}, err
	}
	if err := validateActiveRegionIDs(ctx, tx, update.RegionIDs); err != nil {
		return contextEntityRecord{}, err
	}

	updated, err := scanContextEntity(tx.QueryRow(ctx, `
		UPDATE `+spec.table+`
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
	`, entityID, update.Name, update.DisambiguationLabel, update.Status, update.UpdatedBy, update.UpdatedAt))
	if err != nil {
		return contextEntityRecord{}, fmt.Errorf("update %s: %w", spec.table, err)
	}
	if err := replaceContextEntityRegions(ctx, tx, spec, entityID, update.RegionIDs); err != nil {
		return contextEntityRecord{}, err
	}
	updated, err = contextEntityByID(ctx, tx, spec, entityID, false)
	if err != nil {
		return contextEntityRecord{}, err
	}
	if err := refreshPublishedSearchForCanonicalIdentity(ctx, tx, governanceSpecs[spec.kind], entityID); err != nil {
		return contextEntityRecord{}, err
	}

	entry.ActorUserID = &update.UpdatedBy
	entry.TargetID = &updated.ID
	if entry.Details == nil {
		entry.Details = map[string]any{}
	}
	entry.Details["previousLockVersion"] = current.LockVersion
	entry.Details["lockVersion"] = updated.LockVersion
	if err := appendAudit(ctx, tx, entry); err != nil {
		return contextEntityRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contextEntityRecord{}, fmt.Errorf("commit %s update transaction: %w", spec.table, err)
	}
	return updated, nil
}

func insertContextEntity(ctx context.Context, tx pgx.Tx, spec contextEntitySpec, record contextEntityRecord) (contextEntityRecord, error) {
	created, err := scanContextEntity(tx.QueryRow(ctx, `
		INSERT INTO `+spec.table+` (
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $6, $7, $7)
		RETURNING
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
	`, record.ID, record.Name, record.DisambiguationLabel, record.Status, record.MergedIntoID, record.CreatedBy, record.CreatedAt))
	if err != nil {
		return contextEntityRecord{}, fmt.Errorf("insert %s: %w", spec.table, err)
	}
	return created, nil
}

func replaceContextEntityRegions(ctx context.Context, tx pgx.Tx, spec contextEntitySpec, entityID string, regionIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM `+spec.joinTable+` WHERE `+spec.joinEntityColumn+` = $1`, entityID); err != nil {
		return fmt.Errorf("clear %s region relationships: %w", spec.table, err)
	}
	for _, regionID := range regionIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO `+spec.joinTable+` (`+spec.joinEntityColumn+`, region_id) VALUES ($1, $2)`, entityID, regionID); err != nil {
			return fmt.Errorf("insert %s region relationship: %w", spec.table, err)
		}
	}
	return nil
}

func validateActiveRegionIDs(ctx context.Context, querier contextEntityQuerier, regionIDs []string) error {
	if len(regionIDs) == 0 {
		return nil
	}
	var count int
	if err := querier.QueryRow(ctx, `
		SELECT count(*)
		FROM regions
		WHERE id = ANY($1::uuid[]) AND status = 'active'
	`, regionIDs).Scan(&count); err != nil {
		return fmt.Errorf("validate associated regions: %w", err)
	}
	if count != len(regionIDs) {
		return catalog.ErrInvalidRegionAssociation
	}
	return nil
}

type contextEntityScanner interface {
	Scan(...any) error
}

func scanContextEntity(row contextEntityScanner) (contextEntityRecord, error) {
	var record contextEntityRecord
	if err := row.Scan(
		&record.ID, &record.Name, &record.DisambiguationLabel, &record.Status, &record.MergedIntoID,
		&record.LockVersion, &record.CreatedBy, &record.UpdatedBy, &record.CreatedAt, &record.UpdatedAt,
	); err != nil {
		return contextEntityRecord{}, err
	}
	return record, nil
}

type contextEntityQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func contextEntityByID(ctx context.Context, querier contextEntityQuerier, spec contextEntitySpec, entityID string, forUpdate bool) (contextEntityRecord, error) {
	lockingClause := ""
	if forUpdate {
		lockingClause = " FOR UPDATE"
	}
	record, err := scanContextEntity(querier.QueryRow(ctx, `
		SELECT
			id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at
		FROM `+spec.table+`
		WHERE id = $1
	`+lockingClause, entityID))
	if errors.Is(err, pgx.ErrNoRows) {
		return contextEntityRecord{}, spec.notFound
	}
	if err != nil {
		return contextEntityRecord{}, fmt.Errorf("find %s: %w", spec.table, err)
	}
	records := []contextEntityRecord{record}
	if err := loadContextEntityRegions(ctx, querier, spec, records); err != nil {
		return contextEntityRecord{}, err
	}
	return records[0], nil
}

func loadContextEntityRegions(ctx context.Context, querier contextEntityQuerier, spec contextEntitySpec, records []contextEntityRecord) error {
	if len(records) == 0 {
		return nil
	}
	entityIDs := make([]string, 0, len(records))
	indexByID := make(map[string]int, len(records))
	for index := range records {
		records[index].RegionIDs = []string{}
		records[index].Regions = []catalog.RegionReference{}
		entityIDs = append(entityIDs, records[index].ID)
		indexByID[records[index].ID] = index
	}
	rows, err := querier.Query(ctx, `
		SELECT relationships.`+spec.joinEntityColumn+`, resolved.id, resolved.name, resolved.disambiguation_label
		FROM `+spec.joinTable+` relationships
		JOIN regions stored ON stored.id = relationships.region_id
		JOIN regions resolved ON resolved.id = COALESCE(stored.merged_into_id, stored.id)
		WHERE relationships.`+spec.joinEntityColumn+` = ANY($1::uuid[])
		GROUP BY relationships.`+spec.joinEntityColumn+`, resolved.id, resolved.name, resolved.disambiguation_label
		ORDER BY relationships.`+spec.joinEntityColumn+`, lower(resolved.name), lower(COALESCE(resolved.disambiguation_label, '')), resolved.id
	`, entityIDs)
	if err != nil {
		return fmt.Errorf("load %s regions: %w", spec.table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var entityID string
		var reference catalog.RegionReference
		if err := rows.Scan(&entityID, &reference.ID, &reference.Name, &reference.DisambiguationLabel); err != nil {
			return fmt.Errorf("scan %s region: %w", spec.table, err)
		}
		index := indexByID[entityID]
		records[index].RegionIDs = append(records[index].RegionIDs, reference.ID)
		records[index].Regions = append(records[index].Regions, reference)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s regions: %w", spec.table, err)
	}
	return nil
}

func recordFromPlace(place catalog.Place) contextEntityRecord {
	return contextEntityRecord{
		ID: place.ID, Name: place.Name, DisambiguationLabel: place.DisambiguationLabel, Status: place.Status,
		MergedIntoID: place.MergedIntoID, RegionIDs: place.RegionIDs, LockVersion: place.LockVersion,
		CreatedBy: place.CreatedBy, UpdatedBy: place.UpdatedBy, CreatedAt: place.CreatedAt, UpdatedAt: place.UpdatedAt,
	}
}

func placeFromRecord(record contextEntityRecord) catalog.Place {
	return catalog.Place{
		ID: record.ID, Name: record.Name, DisambiguationLabel: record.DisambiguationLabel, Status: record.Status,
		MergedIntoID: record.MergedIntoID, RegionIDs: record.RegionIDs, Regions: record.Regions, LockVersion: record.LockVersion,
		CreatedBy: record.CreatedBy, UpdatedBy: record.UpdatedBy, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func recordFromHistoricalPeriod(period catalog.HistoricalPeriod) contextEntityRecord {
	return contextEntityRecord{
		ID: period.ID, Name: period.Name, DisambiguationLabel: period.DisambiguationLabel, Status: period.Status,
		MergedIntoID: period.MergedIntoID, RegionIDs: period.RegionIDs, LockVersion: period.LockVersion,
		CreatedBy: period.CreatedBy, UpdatedBy: period.UpdatedBy, CreatedAt: period.CreatedAt, UpdatedAt: period.UpdatedAt,
	}
}

func historicalPeriodFromRecord(record contextEntityRecord) catalog.HistoricalPeriod {
	return catalog.HistoricalPeriod{
		ID: record.ID, Name: record.Name, DisambiguationLabel: record.DisambiguationLabel, Status: record.Status,
		MergedIntoID: record.MergedIntoID, RegionIDs: record.RegionIDs, Regions: record.Regions, LockVersion: record.LockVersion,
		CreatedBy: record.CreatedBy, UpdatedBy: record.UpdatedBy, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}
