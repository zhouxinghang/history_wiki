package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
)

func (store *Postgres) ListEventRevisions(ctx context.Context, eventID string) ([]historyevent.Revision, error) {
	var exists bool
	if err := store.pool.QueryRow(ctx, `SELECT true FROM events WHERE id = $1`, eventID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return nil, historyevent.ErrEventNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read event for revision list: %w", err)
	}
	rows, err := store.pool.Query(ctx, `
		SELECT revision.id, revision.event_id, revision.revision_no, revision.title,
			revision.published_by, publisher.email, revision.published_at,
			event.current_revision_id = revision.id
		FROM event_revisions revision
		JOIN users publisher ON publisher.id = revision.published_by
		JOIN events event ON event.id = revision.event_id
		WHERE revision.event_id = $1
		ORDER BY revision.revision_no DESC
	`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list event revision numbers: %w", err)
	}
	revisions := make([]historyevent.Revision, 0)
	for rows.Next() {
		var revision historyevent.Revision
		if err := rows.Scan(
			&revision.ID, &revision.EventID, &revision.RevisionNo, &revision.Title,
			&revision.PublishedBy, &revision.PublishedByEmail, &revision.PublishedAt, &revision.Current,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan event revision summary: %w", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate event revision numbers: %w", err)
	}
	rows.Close()

	return revisions, nil
}

func (store *Postgres) EventRevisionByNumber(ctx context.Context, eventID string, revisionNo int) (historyevent.Revision, error) {
	return eventRevisionByNumber(ctx, store.pool, eventID, revisionNo)
}

func (store *Postgres) CreateDraftFromCurrentRevision(
	ctx context.Context,
	eventID string,
	command historyevent.DraftFromRevisionCommand,
	entry audit.Entry,
) (historyevent.Event, error) {
	return store.createDraftFromRevision(ctx, eventID, nil, command, entry)
}

func (store *Postgres) RestoreEventRevision(
	ctx context.Context,
	eventID string,
	revisionNo int,
	command historyevent.DraftFromRevisionCommand,
	entry audit.Entry,
) (historyevent.Event, error) {
	return store.createDraftFromRevision(ctx, eventID, &revisionNo, command, entry)
}

func (store *Postgres) createDraftFromRevision(
	ctx context.Context,
	eventID string,
	requestedRevisionNo *int,
	command historyevent.DraftFromRevisionCommand,
	entry audit.Entry,
) (historyevent.Event, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("begin event draft creation transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentRevisionID *string
	if err := tx.QueryRow(ctx, `SELECT current_revision_id FROM events WHERE id = $1 FOR UPDATE`, eventID).Scan(&currentRevisionID); errors.Is(err, pgx.ErrNoRows) {
		return historyevent.Event{}, historyevent.ErrEventNotFound
	} else if err != nil {
		return historyevent.Event{}, fmt.Errorf("lock event for draft creation: %w", err)
	}
	var draftExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM event_drafts WHERE event_id = $1)`, eventID).Scan(&draftExists); err != nil {
		return historyevent.Event{}, fmt.Errorf("check active event draft: %w", err)
	}
	if draftExists {
		return historyevent.Event{}, historyevent.ErrActiveDraftExists
	}

	var revisionID string
	var revisionNo int
	if requestedRevisionNo == nil {
		if currentRevisionID == nil {
			return historyevent.Event{}, historyevent.ErrNoPublishedRevision
		}
		if err := tx.QueryRow(ctx, `
			SELECT id, revision_no FROM event_revisions WHERE event_id = $1 AND id = $2
		`, eventID, *currentRevisionID).Scan(&revisionID, &revisionNo); errors.Is(err, pgx.ErrNoRows) {
			return historyevent.Event{}, historyevent.ErrRevisionNotFound
		} else if err != nil {
			return historyevent.Event{}, fmt.Errorf("read current event revision: %w", err)
		}
	} else {
		if *requestedRevisionNo < 1 {
			return historyevent.Event{}, historyevent.ErrRevisionNotFound
		}
		if err := tx.QueryRow(ctx, `
			SELECT id, revision_no FROM event_revisions WHERE event_id = $1 AND revision_no = $2
		`, eventID, *requestedRevisionNo).Scan(&revisionID, &revisionNo); errors.Is(err, pgx.ErrNoRows) {
			return historyevent.Event{}, historyevent.ErrRevisionNotFound
		} else if err != nil {
			return historyevent.Event{}, fmt.Errorf("read event revision for restoration: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO event_drafts (
			event_id, title, summary, narrative,
			time_kind, start_era, start_year, start_month, start_day, start_circa,
			end_era, end_year, end_circa, start_coordinate, end_coordinate,
			primary_category, prominence, display_order, lock_version, based_on_revision_no,
			created_by, updated_by, created_at, updated_at
		)
		SELECT event_id, title, summary, narrative,
			time_kind, start_era, start_year, start_month, start_day, start_circa,
			end_era, end_year, end_circa, start_coordinate, end_coordinate,
			primary_category, prominence, display_order, 1, revision_no,
			$3, $3, $4, $4
		FROM event_revisions
		WHERE event_id = $1 AND id = $2
	`, eventID, revisionID, command.CreatedBy, command.CreatedAt)
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("create active draft from event revision: %w", err)
	}
	for _, spec := range revisionAssociationSpecs {
		draftTable := strings.Replace(spec.joinTable, "event_revision_", "event_draft_", 1)
		_, err := tx.Exec(ctx, `INSERT INTO `+draftTable+` (event_id, `+spec.entityColumn+`)
			SELECT $1, `+spec.entityColumn+` FROM `+spec.joinTable+` WHERE event_revision_id = $2`, eventID, revisionID)
		if err != nil {
			return historyevent.Event{}, fmt.Errorf("restore %s: %w", draftTable, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE events
		SET lock_version = lock_version + 1, updated_by = $2, updated_at = $3
		WHERE id = $1
	`, eventID, command.CreatedBy, command.CreatedAt); err != nil {
		return historyevent.Event{}, fmt.Errorf("touch event after draft creation: %w", err)
	}

	entry.ActorUserID = &command.CreatedBy
	entry.TargetID = &eventID
	entry.Details = cloneAuditDetails(entry.Details)
	entry.Details["basedOnRevisionNo"] = revisionNo
	entry.Details["basedOnRevisionId"] = revisionID
	if err := appendAudit(ctx, tx, entry); err != nil {
		return historyevent.Event{}, err
	}
	created, err := managedEventByID(ctx, tx, eventID, false)
	if err != nil {
		return historyevent.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return historyevent.Event{}, fmt.Errorf("commit event draft creation transaction: %w", err)
	}
	return created, nil
}

func eventRevisionByNumber(ctx context.Context, querier eventQuerier, eventID string, revisionNo int) (historyevent.Revision, error) {
	var result historyevent.Revision
	var timeKind, startEra string
	var endEra *string
	var startYear int
	var startMonth, startDay, endYear *int
	var startCirca, endCirca bool
	err := querier.QueryRow(ctx, `
		SELECT revision.id, revision.event_id, revision.revision_no,
			revision.title, revision.summary, revision.narrative,
			revision.time_kind::text, revision.start_era::text, revision.start_year,
			revision.start_month, revision.start_day, revision.start_circa,
			revision.end_era::text, revision.end_year, revision.end_circa,
			revision.primary_category::text, revision.prominence, revision.display_order,
			revision.published_by, publisher.email, revision.published_at,
			event.current_revision_id = revision.id
		FROM event_revisions revision
		JOIN users publisher ON publisher.id = revision.published_by
		JOIN events event ON event.id = revision.event_id
		WHERE revision.event_id = $1 AND revision.revision_no = $2
	`, eventID, revisionNo).Scan(
		&result.ID, &result.EventID, &result.RevisionNo,
		&result.Title, &result.Summary, &result.Narrative,
		&timeKind, &startEra, &startYear, &startMonth, &startDay, &startCirca,
		&endEra, &endYear, &endCirca,
		&result.PrimaryCategory, &result.Prominence, &result.DisplayOrder,
		&result.PublishedBy, &result.PublishedByEmail, &result.PublishedAt, &result.Current,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return historyevent.Revision{}, historyevent.ErrRevisionNotFound
	}
	if err != nil {
		return historyevent.Revision{}, fmt.Errorf("read event revision: %w", err)
	}
	result.Time, err = publishedTimeExpression(timeKind, startEra, startYear, startMonth, startDay, startCirca, endEra, endYear, endCirca)
	if err != nil {
		return historyevent.Revision{}, err
	}
	if err := loadRevisionAssociations(ctx, querier, &result); err != nil {
		return historyevent.Revision{}, err
	}
	return result, nil
}
