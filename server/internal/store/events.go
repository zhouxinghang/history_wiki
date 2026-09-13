package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
)

type eventQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type eventAssociationSpec struct {
	joinTable    string
	entityColumn string
	entityTable  string
}

var eventAssociationSpecs = []eventAssociationSpec{
	{joinTable: "event_draft_regions", entityColumn: "region_id", entityTable: "regions"},
	{joinTable: "event_draft_places", entityColumn: "place_id", entityTable: "places"},
	{joinTable: "event_draft_periods", entityColumn: "historical_period_id", entityTable: "historical_periods"},
	{joinTable: "event_draft_figures", entityColumn: "historical_figure_id", entityTable: "historical_figures"},
	{joinTable: "event_draft_topic_tags", entityColumn: "topic_tag_id", entityTable: "topic_tags"},
}

func (store *Postgres) ListManagedEvents(ctx context.Context) ([]historyevent.Event, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id
		FROM events
		ORDER BY updated_at DESC, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list managed events: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan managed event id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate managed events: %w", err)
	}
	rows.Close()

	events := make([]historyevent.Event, 0, len(ids))
	for _, id := range ids {
		managedEvent, err := managedEventByID(ctx, store.pool, id, false)
		if err != nil {
			return nil, err
		}
		events = append(events, managedEvent)
	}
	return events, nil
}

func (store *Postgres) ManagedEventByID(ctx context.Context, eventID string) (historyevent.Event, error) {
	return managedEventByID(ctx, store.pool, eventID, false)
}

func (store *Postgres) CreateManagedEvent(
	ctx context.Context,
	managedEvent historyevent.Event,
	idempotencyKey string,
	requestHash []byte,
	entry audit.Entry,
) (historyevent.Event, bool, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return historyevent.Event{}, false, fmt.Errorf("begin event creation transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	draft := managedEvent.Draft
	if draft == nil {
		return historyevent.Event{}, false, historyevent.ErrInvalidEvent
	}
	command, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records (actor_user_id, scope, idempotency_key, request_hash, target_id)
		VALUES ($1, 'events.create', $2, $3, $4)
		ON CONFLICT (actor_user_id, scope, idempotency_key) DO NOTHING
	`, draft.CreatedBy, idempotencyKey, requestHash, managedEvent.ID)
	if err != nil {
		return historyevent.Event{}, false, fmt.Errorf("reserve event idempotency key: %w", err)
	}
	if command.RowsAffected() == 0 {
		var existingHash []byte
		var targetID string
		if err := tx.QueryRow(ctx, `
			SELECT request_hash, target_id FROM idempotency_records
			WHERE actor_user_id = $1 AND scope = 'events.create' AND idempotency_key = $2
		`, draft.CreatedBy, idempotencyKey).Scan(&existingHash, &targetID); err != nil {
			return historyevent.Event{}, false, fmt.Errorf("read event idempotency result: %w", err)
		}
		if !bytes.Equal(existingHash, requestHash) {
			return historyevent.Event{}, false, historyevent.ErrIdempotencyConflict
		}
		existing, err := managedEventByID(ctx, tx, targetID, false)
		if err != nil {
			return historyevent.Event{}, false, err
		}
		entry.ActorUserID = &draft.CreatedBy
		entry.TargetID = &existing.ID
		entry.Details = cloneAuditDetails(entry.Details)
		entry.Details["replayed"] = true
		if err := appendAudit(ctx, tx, entry); err != nil {
			return historyevent.Event{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return historyevent.Event{}, false, fmt.Errorf("commit event replay transaction: %w", err)
		}
		return existing, true, nil
	}

	if err := validateEventAssociations(ctx, tx, *draft); err != nil {
		return historyevent.Event{}, false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (
			id, slug, publication_status, lock_version, created_by, updated_by, created_at, updated_at
		) VALUES ($1, $2, 'unpublished', 1, $3, $3, $4, $4)
	`, managedEvent.ID, managedEvent.Slug, draft.CreatedBy, managedEvent.CreatedAt)
	if isEventSlugConflict(err) {
		return historyevent.Event{}, false, historyevent.ErrSlugConflict
	}
	if err != nil {
		return historyevent.Event{}, false, fmt.Errorf("insert event: %w", err)
	}
	if err := insertEventDraft(ctx, tx, *draft); err != nil {
		return historyevent.Event{}, false, err
	}
	if err := replaceEventAssociations(ctx, tx, *draft); err != nil {
		return historyevent.Event{}, false, err
	}
	entry.ActorUserID = &draft.CreatedBy
	entry.TargetID = &managedEvent.ID
	if err := appendAudit(ctx, tx, entry); err != nil {
		return historyevent.Event{}, false, err
	}
	created, err := managedEventByID(ctx, tx, managedEvent.ID, false)
	if err != nil {
		return historyevent.Event{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return historyevent.Event{}, false, fmt.Errorf("commit event creation transaction: %w", err)
	}
	return created, false, nil
}

func (store *Postgres) UpdateManagedEventSlug(
	ctx context.Context,
	eventID string,
	update historyevent.SlugUpdate,
	entry audit.Entry,
) (historyevent.Event, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("begin event metadata update transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentVersion int64
	if err := tx.QueryRow(ctx, `SELECT lock_version FROM events WHERE id = $1 FOR UPDATE`, eventID).Scan(&currentVersion); errors.Is(err, pgx.ErrNoRows) {
		return historyevent.Event{}, historyevent.ErrEventNotFound
	} else if err != nil {
		return historyevent.Event{}, fmt.Errorf("lock event metadata: %w", err)
	}
	if currentVersion != update.ExpectedVersion {
		return historyevent.Event{}, historyevent.ErrEventVersionConflict
	}
	_, err = tx.Exec(ctx, `
		UPDATE events
		SET slug = $2, lock_version = lock_version + 1, updated_by = $3, updated_at = $4
		WHERE id = $1
	`, eventID, update.Slug, update.UpdatedBy, update.UpdatedAt)
	if isEventSlugConflict(err) {
		return historyevent.Event{}, historyevent.ErrSlugConflict
	}
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("update event slug: %w", err)
	}
	entry.ActorUserID = &update.UpdatedBy
	entry.TargetID = &eventID
	entry.Details = cloneAuditDetails(entry.Details)
	entry.Details["previousLockVersion"] = currentVersion
	entry.Details["lockVersion"] = currentVersion + 1
	if err := appendAudit(ctx, tx, entry); err != nil {
		return historyevent.Event{}, err
	}
	updated, err := managedEventByID(ctx, tx, eventID, false)
	if err != nil {
		return historyevent.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return historyevent.Event{}, fmt.Errorf("commit event metadata update transaction: %w", err)
	}
	return updated, nil
}

func (store *Postgres) UpdateEventDraft(
	ctx context.Context,
	eventID string,
	update historyevent.DraftUpdate,
	entry audit.Entry,
) (historyevent.Event, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("begin draft update transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var eventExists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM events WHERE id = $1 FOR UPDATE`, eventID).Scan(&eventExists); errors.Is(err, pgx.ErrNoRows) {
		return historyevent.Event{}, historyevent.ErrEventNotFound
	} else if err != nil {
		return historyevent.Event{}, fmt.Errorf("lock event for draft update: %w", err)
	}
	var currentVersion int64
	if err := tx.QueryRow(ctx, `SELECT lock_version FROM event_drafts WHERE event_id = $1 FOR UPDATE`, eventID).Scan(&currentVersion); errors.Is(err, pgx.ErrNoRows) {
		return historyevent.Event{}, historyevent.ErrEventNotFound
	} else if err != nil {
		return historyevent.Event{}, fmt.Errorf("lock event draft: %w", err)
	}
	if currentVersion != update.ExpectedVersion {
		return historyevent.Event{}, historyevent.ErrDraftVersionConflict
	}
	draft := historyevent.Draft{
		EventID: eventID, Title: update.Title, Summary: update.Summary, Narrative: update.Narrative,
		Time: update.Time, PrimaryCategory: update.PrimaryCategory, Prominence: update.Prominence,
		DisplayOrder: update.DisplayOrder, RegionIDs: update.RegionIDs, PlaceIDs: update.PlaceIDs,
		PeriodIDs: update.PeriodIDs, FigureIDs: update.FigureIDs, TopicTagIDs: update.TopicTagIDs,
		UpdatedBy: update.UpdatedBy, UpdatedAt: update.UpdatedAt,
	}
	if err := validateEventAssociationsForUpdate(ctx, tx, draft); err != nil {
		return historyevent.Event{}, err
	}
	timeValues, err := draftTimeValues(draft.Time)
	if err != nil {
		return historyevent.Event{}, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE event_drafts SET
			title = $2, summary = $3, narrative = $4,
			time_kind = $5, start_era = $6, start_year = $7, start_month = $8, start_day = $9,
			start_circa = $10, end_era = $11, end_year = $12, end_circa = $13,
			start_coordinate = $14, end_coordinate = $15,
			primary_category = $16, prominence = $17, display_order = $18,
			lock_version = lock_version + 1, updated_by = $19, updated_at = $20
		WHERE event_id = $1
	`, eventID, draft.Title, draft.Summary, draft.Narrative,
		timeValues.kind, timeValues.startEra, timeValues.startYear, timeValues.startMonth, timeValues.startDay,
		timeValues.startCirca, timeValues.endEra, timeValues.endYear, timeValues.endCirca,
		timeValues.startCoordinate, timeValues.endCoordinate,
		draft.PrimaryCategory, draft.Prominence, draft.DisplayOrder, draft.UpdatedBy, draft.UpdatedAt)
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("update event draft: %w", err)
	}
	if err := replaceEventAssociations(ctx, tx, draft); err != nil {
		return historyevent.Event{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE events SET updated_by = $2, updated_at = $3 WHERE id = $1`, eventID, update.UpdatedBy, update.UpdatedAt); err != nil {
		return historyevent.Event{}, fmt.Errorf("touch event: %w", err)
	}
	entry.ActorUserID = &update.UpdatedBy
	entry.TargetID = &eventID
	entry.Details = cloneAuditDetails(entry.Details)
	entry.Details["previousLockVersion"] = currentVersion
	entry.Details["lockVersion"] = currentVersion + 1
	if err := appendAudit(ctx, tx, entry); err != nil {
		return historyevent.Event{}, err
	}
	updated, err := managedEventByID(ctx, tx, eventID, false)
	if err != nil {
		return historyevent.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return historyevent.Event{}, fmt.Errorf("commit draft update transaction: %w", err)
	}
	return updated, nil
}

func managedEventByID(ctx context.Context, querier eventQuerier, eventID string, forUpdate bool) (historyevent.Event, error) {
	query := `
		SELECT
			e.id, e.slug, e.publication_status, e.current_revision_id, e.lock_version, e.created_at, e.updated_at,
			d.event_id, d.title, d.summary, d.narrative,
			d.time_kind::text, d.start_era::text, d.start_year, d.start_month, d.start_day,
			d.start_circa, d.end_era::text, d.end_year, d.end_circa,
			d.primary_category::text, d.prominence, d.display_order, d.lock_version, d.based_on_revision_no,
			d.created_by, d.updated_by, d.created_at, d.updated_at
		FROM events e
		LEFT JOIN event_drafts d ON d.event_id = e.id
		WHERE e.id = $1`
	if forUpdate {
		query += ` FOR UPDATE OF e, d`
	}
	var result historyevent.Event
	var draftEventID, title, summary, narrative *string
	var timeKind, startEra, endEra, category *string
	var startYear, startMonth, startDay, endYear *int
	var startCirca, endCirca *bool
	var prominence, displayOrder *int
	var draftLockVersion *int64
	var basedOnRevisionNo *int
	var createdBy, updatedBy *string
	var draftCreatedAt, draftUpdatedAt *time.Time
	err := querier.QueryRow(ctx, query, eventID).Scan(
		&result.ID, &result.Slug, &result.PublicationStatus, &result.CurrentRevisionID,
		&result.LockVersion, &result.CreatedAt, &result.UpdatedAt,
		&draftEventID, &title, &summary, &narrative,
		&timeKind, &startEra, &startYear, &startMonth, &startDay,
		&startCirca, &endEra, &endYear, &endCirca,
		&category, &prominence, &displayOrder, &draftLockVersion, &basedOnRevisionNo,
		&createdBy, &updatedBy, &draftCreatedAt, &draftUpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return historyevent.Event{}, historyevent.ErrEventNotFound
	}
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("read managed event: %w", err)
	}
	if draftEventID == nil {
		return result, nil
	}
	draft := historyevent.Draft{
		EventID: *draftEventID, Title: valueOrEmpty(title), Summary: valueOrEmpty(summary), Narrative: valueOrEmpty(narrative),
		DisplayOrder: valueOrZero(displayOrder), LockVersion: valueOrZero64(draftLockVersion),
		BasedOnRevisionNo: basedOnRevisionNo,
		CreatedBy:         valueOrEmpty(createdBy), UpdatedBy: valueOrEmpty(updatedBy),
		CreatedAt: valueOrTime(draftCreatedAt), UpdatedAt: valueOrTime(draftUpdatedAt),
	}
	if category != nil {
		value := historyevent.PrimaryCategory(*category)
		draft.PrimaryCategory = &value
	}
	if prominence != nil {
		value := *prominence
		draft.Prominence = &value
	}
	draft.Time, err = timeExpressionFromColumns(timeKind, startEra, startYear, startMonth, startDay, startCirca, endEra, endYear, endCirca)
	if err != nil {
		return historyevent.Event{}, err
	}
	if err := loadEventAssociations(ctx, querier, &draft); err != nil {
		return historyevent.Event{}, err
	}
	result.Draft = &draft
	return result, nil
}

type storedTimeValues struct {
	kind                                     *string
	startEra, endEra                         *string
	startYear, startMonth, startDay, endYear *int
	startCirca, endCirca                     bool
	startCoordinate, endCoordinate           *float64
}

func draftTimeValues(value *historyevent.TimeExpression) (storedTimeValues, error) {
	if value == nil {
		return storedTimeValues{}, nil
	}
	startCoordinate, endCoordinate, err := value.Coordinates()
	if err != nil {
		return storedTimeValues{}, err
	}
	kind := string(value.Kind)
	if kind == "exact-date" {
		kind = "exact_date"
	}
	result := storedTimeValues{kind: &kind, startCoordinate: &startCoordinate, endCoordinate: &endCoordinate}
	switch value.Kind {
	case historyevent.TimeExactDate:
		startEra := string(value.Date.Era)
		result.startEra, result.startYear, result.startMonth, result.startDay = &startEra, &value.Date.Year, &value.Date.Month, &value.Date.Day
	case historyevent.TimeYear, historyevent.TimeCirca:
		startEra := string(value.Year.Era)
		result.startEra, result.startYear = &startEra, &value.Year.Year
		result.startCirca = value.Kind == historyevent.TimeCirca
	case historyevent.TimeInterval:
		startEra, endEra := string(value.Start.Era), string(value.End.Era)
		result.startEra, result.startYear, result.startCirca = &startEra, &value.Start.Year, value.Start.Circa
		result.endEra, result.endYear, result.endCirca = &endEra, &value.End.Year, value.End.Circa
	}
	return result, nil
}

func timeExpressionFromColumns(
	kind, startEra *string,
	startYear, startMonth, startDay *int,
	startCirca *bool,
	endEra *string,
	endYear *int,
	endCirca *bool,
) (*historyevent.TimeExpression, error) {
	if kind == nil {
		return nil, nil
	}
	apiKind := *kind
	if apiKind == "exact_date" {
		apiKind = "exact-date"
	}
	value := historyevent.TimeExpression{Kind: historyevent.TimeKind(apiKind)}
	switch value.Kind {
	case historyevent.TimeExactDate:
		if startEra == nil || startYear == nil || startMonth == nil || startDay == nil {
			return nil, fmt.Errorf("read exact event date: %w", historyevent.ErrInvalidTimeExpression)
		}
		value.Date = &historyevent.ExactDate{Era: historyevent.Era(*startEra), Year: *startYear, Month: *startMonth, Day: *startDay}
	case historyevent.TimeYear, historyevent.TimeCirca:
		if startEra == nil || startYear == nil {
			return nil, fmt.Errorf("read event year: %w", historyevent.ErrInvalidTimeExpression)
		}
		value.Year = &historyevent.HistoricalYear{Era: historyevent.Era(*startEra), Year: *startYear}
	case historyevent.TimeInterval:
		if startEra == nil || startYear == nil || endEra == nil || endYear == nil {
			return nil, fmt.Errorf("read event interval: %w", historyevent.ErrInvalidTimeExpression)
		}
		value.Start = &historyevent.IntervalEndpoint{Era: historyevent.Era(*startEra), Year: *startYear, Circa: valueOrFalse(startCirca)}
		value.End = &historyevent.IntervalEndpoint{Era: historyevent.Era(*endEra), Year: *endYear, Circa: valueOrFalse(endCirca)}
	}
	normalized, err := historyevent.NormalizeTimeExpression(value)
	if err != nil {
		return nil, fmt.Errorf("read event time expression: %w", err)
	}
	return &normalized, nil
}

func insertEventDraft(ctx context.Context, tx pgx.Tx, draft historyevent.Draft) error {
	timeValues, err := draftTimeValues(draft.Time)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO event_drafts (
			event_id, title, summary, narrative,
			time_kind, start_era, start_year, start_month, start_day, start_circa,
			end_era, end_year, end_circa, start_coordinate, end_coordinate,
			primary_category, prominence, display_order, lock_version, based_on_revision_no,
			created_by, updated_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18, 1, $19,
			$20, $20, $21, $21
		)
	`, draft.EventID, draft.Title, draft.Summary, draft.Narrative,
		timeValues.kind, timeValues.startEra, timeValues.startYear, timeValues.startMonth, timeValues.startDay, timeValues.startCirca,
		timeValues.endEra, timeValues.endYear, timeValues.endCirca, timeValues.startCoordinate, timeValues.endCoordinate,
		draft.PrimaryCategory, draft.Prominence, draft.DisplayOrder, draft.BasedOnRevisionNo, draft.CreatedBy, draft.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert event draft: %w", err)
	}
	return nil
}

func validateEventAssociations(ctx context.Context, tx pgx.Tx, draft historyevent.Draft) error {
	values := [][]string{draft.RegionIDs, draft.PlaceIDs, draft.PeriodIDs, draft.FigureIDs, draft.TopicTagIDs}
	for index, spec := range eventAssociationSpecs {
		if err := validateActiveEventEntityIDs(ctx, tx, spec.entityTable, values[index]); err != nil {
			return err
		}
	}
	return nil
}

func validateEventAssociationsForUpdate(ctx context.Context, tx pgx.Tx, draft historyevent.Draft) error {
	values := [][]string{draft.RegionIDs, draft.PlaceIDs, draft.PeriodIDs, draft.FigureIDs, draft.TopicTagIDs}
	for index, spec := range eventAssociationSpecs {
		if err := validateWritableEventEntityIDs(ctx, tx, spec, draft.EventID, values[index]); err != nil {
			return err
		}
	}
	return nil
}

func validateEventAssociationsForPublication(ctx context.Context, tx pgx.Tx, draft historyevent.Draft) error {
	values := [][]string{draft.RegionIDs, draft.PlaceIDs, draft.PeriodIDs, draft.FigureIDs, draft.TopicTagIDs}
	for index, spec := range eventAssociationSpecs {
		if err := validateActiveEventEntityIDsForPublication(ctx, tx, spec.entityTable, values[index]); err != nil {
			return err
		}
	}
	return nil
}

func validateActiveEventEntityIDs(ctx context.Context, tx pgx.Tx, table string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM `+table+` WHERE id = ANY($1::uuid[]) AND status = 'active' FOR KEY SHARE`, ids)
	if err != nil {
		return fmt.Errorf("validate event %s associations: %w", table, err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate event %s associations: %w", table, err)
	}
	if count != len(ids) {
		return historyevent.ErrInvalidAssociation
	}
	return nil
}

func validateActiveEventEntityIDsForPublication(ctx context.Context, tx pgx.Tx, table string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM `+table+` WHERE id = ANY($1::uuid[]) AND status = 'active' FOR SHARE`, ids)
	if err != nil {
		return fmt.Errorf("validate publishable event %s associations: %w", table, err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate publishable event %s associations: %w", table, err)
	}
	if count != len(ids) {
		return historyevent.ErrInvalidAssociation
	}
	return nil
}

func validateWritableEventEntityIDs(ctx context.Context, tx pgx.Tx, spec eventAssociationSpec, eventID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT entity.id
		FROM `+spec.entityTable+` entity
		WHERE entity.id = ANY($1::uuid[])
		  AND (
			entity.status = 'active'
			OR EXISTS (
				SELECT 1 FROM `+spec.joinTable+` relationship
				WHERE relationship.event_id = $2 AND relationship.`+spec.entityColumn+` = entity.id
			)
		  )
		FOR KEY SHARE
	`, ids, eventID)
	if err != nil {
		return fmt.Errorf("validate writable event %s associations: %w", spec.entityTable, err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate writable event %s associations: %w", spec.entityTable, err)
	}
	if count != len(ids) {
		return historyevent.ErrInvalidAssociation
	}
	return nil
}

func replaceEventAssociations(ctx context.Context, tx pgx.Tx, draft historyevent.Draft) error {
	values := [][]string{draft.RegionIDs, draft.PlaceIDs, draft.PeriodIDs, draft.FigureIDs, draft.TopicTagIDs}
	for index, spec := range eventAssociationSpecs {
		if _, err := tx.Exec(ctx, `DELETE FROM `+spec.joinTable+` WHERE event_id = $1`, draft.EventID); err != nil {
			return fmt.Errorf("clear %s: %w", spec.joinTable, err)
		}
		for _, entityID := range values[index] {
			if _, err := tx.Exec(ctx, `INSERT INTO `+spec.joinTable+` (event_id, `+spec.entityColumn+`) VALUES ($1, $2)`, draft.EventID, entityID); err != nil {
				return fmt.Errorf("insert %s: %w", spec.joinTable, err)
			}
		}
	}
	return nil
}

func loadEventAssociations(ctx context.Context, querier eventQuerier, draft *historyevent.Draft) error {
	idDestinations := []*[]string{&draft.RegionIDs, &draft.PlaceIDs, &draft.PeriodIDs, &draft.FigureIDs, &draft.TopicTagIDs}
	refDestinations := []*[]historyevent.EntityReference{&draft.Regions, &draft.Places, &draft.Periods, &draft.Figures, &draft.TopicTags}
	for index, spec := range eventAssociationSpecs {
		rows, err := querier.Query(ctx, `
			SELECT resolved.id, resolved.name, resolved.disambiguation_label
			FROM `+spec.joinTable+` relationship
			JOIN `+spec.entityTable+` stored ON stored.id = relationship.`+spec.entityColumn+`
			JOIN `+spec.entityTable+` resolved ON resolved.id = COALESCE(stored.merged_into_id, stored.id)
			WHERE relationship.event_id = $1
			GROUP BY resolved.id, resolved.name, resolved.disambiguation_label
			ORDER BY lower(resolved.name), lower(COALESCE(resolved.disambiguation_label, '')), resolved.id
		`, draft.EventID)
		if err != nil {
			return fmt.Errorf("load %s: %w", spec.joinTable, err)
		}
		ids := make([]string, 0)
		references := make([]historyevent.EntityReference, 0)
		for rows.Next() {
			var reference historyevent.EntityReference
			if err := rows.Scan(&reference.ID, &reference.Name, &reference.DisambiguationLabel); err != nil {
				rows.Close()
				return fmt.Errorf("scan %s: %w", spec.joinTable, err)
			}
			ids = append(ids, reference.ID)
			references = append(references, reference)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate %s: %w", spec.joinTable, err)
		}
		rows.Close()
		*idDestinations[index] = ids
		*refDestinations[index] = references
	}
	return nil
}

func isEventSlugConflict(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505" && postgresError.ConstraintName == "events_slug_key"
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func valueOrZero64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func valueOrFalse(value *bool) bool {
	return value != nil && *value
}

func valueOrTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
