package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
)

var importAssociationFields = []string{"regionIds", "placeIds", "periodIds", "figureIds", "topicTagIds"}

// ValidateEventImport performs the database-backed part of preflight. It is
// deliberately read-only: no idempotency reservation, batch row, audit row or
// historical event is written by this method.
func (store *Postgres) ValidateEventImport(ctx context.Context, records []historyevent.ImportRecord) ([]historyevent.ImportIssue, error) {
	return validateEventImport(ctx, store.pool, records, false)
}

func (store *Postgres) ImportEvents(
	ctx context.Context,
	batch historyevent.ImportBatch,
	records []historyevent.ImportRecord,
	actorUserID, idempotencyKey string,
	requestHash []byte,
	entry audit.Entry,
) (historyevent.ImportBatch, bool, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return historyevent.ImportBatch{}, false, fmt.Errorf("begin event import transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	command, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records (actor_user_id, scope, idempotency_key, request_hash, target_id)
		VALUES ($1, 'events.import', $2, $3, $4)
		ON CONFLICT (actor_user_id, scope, idempotency_key) DO NOTHING
	`, actorUserID, idempotencyKey, requestHash, batch.ID)
	if err != nil {
		return historyevent.ImportBatch{}, false, fmt.Errorf("reserve event import idempotency key: %w", err)
	}
	if command.RowsAffected() == 0 {
		var existingHash []byte
		var batchID string
		if err := tx.QueryRow(ctx, `
			SELECT request_hash, target_id FROM idempotency_records
			WHERE actor_user_id = $1 AND scope = 'events.import' AND idempotency_key = $2
		`, actorUserID, idempotencyKey).Scan(&existingHash, &batchID); err != nil {
			return historyevent.ImportBatch{}, false, fmt.Errorf("read event import idempotency result: %w", err)
		}
		if !bytes.Equal(existingHash, requestHash) {
			return historyevent.ImportBatch{}, false, historyevent.ErrIdempotencyConflict
		}
		existing, err := eventImportBatchByID(ctx, tx, batchID)
		if err != nil {
			return historyevent.ImportBatch{}, false, err
		}
		entry.ActorUserID = &actorUserID
		entry.TargetID = &existing.ID
		entry.Details = cloneAuditDetails(entry.Details)
		entry.Details["eventCount"] = len(existing.EventIDs)
		entry.Details["eventIds"] = existing.EventIDs
		entry.Details["replayed"] = true
		if err := appendAudit(ctx, tx, entry); err != nil {
			return historyevent.ImportBatch{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return historyevent.ImportBatch{}, false, fmt.Errorf("commit event import replay transaction: %w", err)
		}
		return existing, true, nil
	}

	// Serialize event identity checks with every INSERT into events. This makes
	// the collision check and all following inserts one atomic decision.
	if _, err := tx.Exec(ctx, `LOCK TABLE events IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return historyevent.ImportBatch{}, false, fmt.Errorf("lock events for import: %w", err)
	}
	issues, err := validateEventImport(ctx, tx, records, true)
	if err != nil {
		return historyevent.ImportBatch{}, false, err
	}
	if len(issues) > 0 {
		return historyevent.ImportBatch{}, false, &historyevent.ImportValidationError{Issues: issues}
	}

	eventIDsJSON, err := json.Marshal(batch.EventIDs)
	if err != nil {
		return historyevent.ImportBatch{}, false, fmt.Errorf("encode event import result: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event_import_batches (id, actor_user_id, event_ids, created_at)
		VALUES ($1, $2, $3, $4)
	`, batch.ID, actorUserID, eventIDsJSON, batch.CreatedAt); err != nil {
		return historyevent.ImportBatch{}, false, fmt.Errorf("insert event import batch: %w", err)
	}

	if err := insertImportedEvents(ctx, tx, records, actorUserID, batch.CreatedAt); err != nil {
		return historyevent.ImportBatch{}, false, err
	}

	entry.ActorUserID = &actorUserID
	entry.TargetID = &batch.ID
	entry.Details = cloneAuditDetails(entry.Details)
	entry.Details["eventCount"] = len(records)
	entry.Details["eventIds"] = batch.EventIDs
	if err := appendAudit(ctx, tx, entry); err != nil {
		return historyevent.ImportBatch{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return historyevent.ImportBatch{}, false, fmt.Errorf("commit event import transaction: %w", err)
	}
	return batch, false, nil
}

// insertImportedEvents pipelines the batch instead of making tens of
// thousands of request/response round trips at the documented 10k limit.
func insertImportedEvents(ctx context.Context, tx pgx.Tx, records []historyevent.ImportRecord, actorUserID string, createdAt time.Time) error {
	var statements pgx.Batch
	for _, record := range records {
		managedEvent := record.Event
		draft := managedEvent.Draft
		if draft == nil {
			return historyevent.ErrInvalidEvent
		}
		timeValues, err := draftTimeValues(draft.Time)
		if err != nil {
			return err
		}
		statements.Queue(`
			INSERT INTO events (
				id, slug, publication_status, lock_version, created_by, updated_by, created_at, updated_at
			) VALUES ($1, $2, 'unpublished', 1, $3, $3, $4, $4)
		`, managedEvent.ID, managedEvent.Slug, actorUserID, createdAt)
		statements.Queue(`
			INSERT INTO event_drafts (
				event_id, title, summary, narrative,
				time_kind, start_era, start_year, start_month, start_day, start_circa,
				end_era, end_year, end_circa, start_coordinate, end_coordinate,
				primary_category, prominence, display_order, lock_version,
				created_by, updated_by, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15,
				$16, $17, $18, 1,
				$19, $19, $20, $20
			)
		`, draft.EventID, draft.Title, draft.Summary, draft.Narrative,
			timeValues.kind, timeValues.startEra, timeValues.startYear, timeValues.startMonth, timeValues.startDay, timeValues.startCirca,
			timeValues.endEra, timeValues.endYear, timeValues.endCirca, timeValues.startCoordinate, timeValues.endCoordinate,
			draft.PrimaryCategory, draft.Prominence, draft.DisplayOrder, actorUserID, createdAt)
		associationValues := [][]string{draft.RegionIDs, draft.PlaceIDs, draft.PeriodIDs, draft.FigureIDs, draft.TopicTagIDs}
		for index, spec := range eventAssociationSpecs {
			if len(associationValues[index]) == 0 {
				continue
			}
			statements.Queue(`INSERT INTO `+spec.joinTable+` (event_id, `+spec.entityColumn+`) SELECT $1, unnest($2::uuid[])`, draft.EventID, associationValues[index])
		}
	}
	results := tx.SendBatch(ctx, &statements)
	for range statements.Len() {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("insert imported event batch: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("close imported event batch: %w", err)
	}
	return nil
}

func eventImportBatchByID(ctx context.Context, querier eventQuerier, batchID string) (historyevent.ImportBatch, error) {
	var eventIDsJSON []byte
	var result historyevent.ImportBatch
	if err := querier.QueryRow(ctx, `
		SELECT id, event_ids, created_at FROM event_import_batches WHERE id = $1
	`, batchID).Scan(&result.ID, &eventIDsJSON, &result.CreatedAt); err != nil {
		return historyevent.ImportBatch{}, fmt.Errorf("read event import batch: %w", err)
	}
	if err := json.Unmarshal(eventIDsJSON, &result.EventIDs); err != nil {
		return historyevent.ImportBatch{}, fmt.Errorf("decode event import batch: %w", err)
	}
	return result, nil
}

func validateEventImport(ctx context.Context, querier eventQuerier, records []historyevent.ImportRecord, lockAssociations bool) ([]historyevent.ImportIssue, error) {
	issues := make([]historyevent.ImportIssue, 0)
	if len(records) == 0 {
		return issues, nil
	}
	ids := make([]string, 0, len(records))
	slugs := make([]string, 0, len(records))
	for _, record := range records {
		if record.Event.ID != "" {
			ids = append(ids, record.Event.ID)
		}
		if record.Event.Slug != "" {
			slugs = append(slugs, record.Event.Slug)
		}
	}
	existingIDs := make(map[string]struct{})
	existingSlugs := make(map[string]struct{})
	if len(ids) > 0 || len(slugs) > 0 {
		rows, err := querier.Query(ctx, `
			SELECT id::text, slug FROM events
			WHERE id = ANY($1::uuid[]) OR slug = ANY($2::text[])
		`, ids, slugs)
		if err != nil {
			return nil, fmt.Errorf("validate imported event identities: %w", err)
		}
		for rows.Next() {
			var id, slug string
			if err := rows.Scan(&id, &slug); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan imported event identity: %w", err)
			}
			existingIDs[id] = struct{}{}
			existingSlugs[slug] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate imported event identities: %w", err)
		}
		rows.Close()
	}

	for _, record := range records {
		index := record.Index
		if _, ok := existingIDs[record.Event.ID]; ok {
			issues = append(issues, historyevent.ImportIssue{Index: index, Field: "id", Code: "uuid_conflict", Detail: "UUID 已属于既有历史事件。"})
		}
		if _, ok := existingSlugs[record.Event.Slug]; ok {
			issues = append(issues, historyevent.ImportIssue{Index: index, Field: "slug", Code: "slug_conflict", Detail: "slug 已属于既有历史事件。"})
		}
	}

	for associationIndex, spec := range eventAssociationSpecs {
		allIDs := make([]string, 0)
		for _, record := range records {
			draft := record.Event.Draft
			if draft == nil {
				continue
			}
			lists := [][]string{draft.RegionIDs, draft.PlaceIDs, draft.PeriodIDs, draft.FigureIDs, draft.TopicTagIDs}
			allIDs = append(allIDs, lists[associationIndex]...)
		}
		active, err := activeImportEntityIDs(ctx, querier, spec.entityTable, allIDs, lockAssociations)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			index := record.Index
			draft := record.Event.Draft
			if draft == nil {
				continue
			}
			lists := [][]string{draft.RegionIDs, draft.PlaceIDs, draft.PeriodIDs, draft.FigureIDs, draft.TopicTagIDs}
			for _, entityID := range lists[associationIndex] {
				if _, ok := active[entityID]; !ok {
					issues = append(issues, historyevent.ImportIssue{
						Index: index, Field: importAssociationFields[associationIndex], Code: "invalid_association",
						Detail: "规范实体不存在或当前不是有效状态：" + entityID,
					})
				}
			}
		}
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Index != issues[j].Index {
			return issues[i].Index < issues[j].Index
		}
		if issues[i].Field != issues[j].Field {
			return issues[i].Field < issues[j].Field
		}
		return issues[i].Code < issues[j].Code
	})
	return issues, nil
}

func activeImportEntityIDs(ctx context.Context, querier eventQuerier, table string, ids []string, lock bool) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(ids) == 0 {
		return result, nil
	}
	query := `SELECT id::text FROM ` + table + ` WHERE id = ANY($1::uuid[]) AND status = 'active'`
	if lock {
		query += ` FOR KEY SHARE`
	}
	rows, err := querier.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("validate imported event %s associations: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan imported event %s association: %w", table, err)
		}
		result[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate imported event %s associations: %w", table, err)
	}
	return result, nil
}
