package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const maximumPublishedEventQueryResults = 5_000

type revisionAssociationSpec struct {
	joinTable    string
	entityColumn string
	entityTable  string
}

var revisionAssociationSpecs = []revisionAssociationSpec{
	{joinTable: "event_revision_regions", entityColumn: "region_id", entityTable: "regions"},
	{joinTable: "event_revision_places", entityColumn: "place_id", entityTable: "places"},
	{joinTable: "event_revision_periods", entityColumn: "historical_period_id", entityTable: "historical_periods"},
	{joinTable: "event_revision_figures", entityColumn: "historical_figure_id", entityTable: "historical_figures"},
	{joinTable: "event_revision_topic_tags", entityColumn: "topic_tag_id", entityTable: "topic_tags"},
}

func (store *Postgres) PublishEvent(
	ctx context.Context,
	eventID string,
	command historyevent.PublishCommand,
	entry audit.Entry,
) (historyevent.PublishedEvent, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("begin event publication transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status historyevent.PublicationStatus
	var currentRevisionID *string
	if err := tx.QueryRow(ctx, `SELECT publication_status, current_revision_id FROM events WHERE id = $1 FOR UPDATE`, eventID).Scan(&status, &currentRevisionID); errors.Is(err, pgx.ErrNoRows) {
		return historyevent.PublishedEvent{}, historyevent.ErrEventNotFound
	} else if err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("lock event for publication: %w", err)
	}
	var draftVersion int64
	if err := tx.QueryRow(ctx, `SELECT lock_version FROM event_drafts WHERE event_id = $1 FOR UPDATE`, eventID).Scan(&draftVersion); errors.Is(err, pgx.ErrNoRows) {
		if currentRevisionID != nil {
			return historyevent.PublishedEvent{}, historyevent.ErrDraftVersionConflict
		}
		return historyevent.PublishedEvent{}, historyevent.ErrEventNotFound
	} else if err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("lock event draft for publication: %w", err)
	}
	if draftVersion != command.ExpectedDraftVersion {
		return historyevent.PublishedEvent{}, historyevent.ErrDraftVersionConflict
	}

	managedEvent, err := managedEventByID(ctx, tx, eventID, false)
	if err != nil {
		return historyevent.PublishedEvent{}, err
	}
	if managedEvent.Draft == nil {
		return historyevent.PublishedEvent{}, historyevent.ErrEventNotFound
	}
	if err := historyevent.ValidateSlugForPublication(managedEvent.Slug); err != nil {
		return historyevent.PublishedEvent{}, err
	}
	if err := historyevent.ValidateForPublication(*managedEvent.Draft); err != nil {
		return historyevent.PublishedEvent{}, err
	}
	if err := validateEventAssociationsForPublication(ctx, tx, *managedEvent.Draft); err != nil {
		return historyevent.PublishedEvent{}, err
	}

	var revisionNo int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(revision_no), 0) + 1 FROM event_revisions WHERE event_id = $1`, eventID).Scan(&revisionNo); err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("select next event revision number: %w", err)
	}
	timeValues, err := draftTimeValues(managedEvent.Draft.Time)
	if err != nil {
		return historyevent.PublishedEvent{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO event_revisions (
			id, event_id, revision_no, title, summary, narrative,
			time_kind, start_era, start_year, start_month, start_day, start_circa,
			end_era, end_year, end_circa, start_coordinate, end_coordinate,
			primary_category, prominence, display_order, published_by, published_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22
		)
	`, command.RevisionID, eventID, revisionNo,
		managedEvent.Draft.Title, managedEvent.Draft.Summary, managedEvent.Draft.Narrative,
		timeValues.kind, timeValues.startEra, timeValues.startYear, timeValues.startMonth, timeValues.startDay, timeValues.startCirca,
		timeValues.endEra, timeValues.endYear, timeValues.endCirca, timeValues.startCoordinate, timeValues.endCoordinate,
		managedEvent.Draft.PrimaryCategory, managedEvent.Draft.Prominence, managedEvent.Draft.DisplayOrder,
		command.PublishedBy, command.PublishedAt)
	if err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("insert event revision: %w", err)
	}
	for _, spec := range revisionAssociationSpecs {
		draftTable := strings.Replace(spec.joinTable, "event_revision_", "event_draft_", 1)
		_, err := tx.Exec(ctx, `INSERT INTO `+spec.joinTable+` (event_revision_id, `+spec.entityColumn+`)
			SELECT $2, `+spec.entityColumn+` FROM `+draftTable+` WHERE event_id = $1`, eventID, command.RevisionID)
		if err != nil {
			return historyevent.PublishedEvent{}, fmt.Errorf("copy %s: %w", spec.joinTable, err)
		}
	}

	searchText := publicationSearchText(*managedEvent.Draft)
	if _, err := tx.Exec(ctx, `
		INSERT INTO published_event_search (event_id, event_revision_id, searchable_text)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO UPDATE SET
			event_revision_id = EXCLUDED.event_revision_id,
			searchable_text = EXCLUDED.searchable_text
	`, eventID, command.RevisionID, searchText); err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("update published event search projection: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE events
		SET publication_status = 'published', current_revision_id = $2,
			lock_version = lock_version + 1, updated_by = $3, updated_at = $4
		WHERE id = $1
	`, eventID, command.RevisionID, command.PublishedBy, command.PublishedAt); err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("activate event revision: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM event_drafts WHERE event_id = $1`, eventID); err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("remove published event draft: %w", err)
	}

	entry.ActorUserID = &command.PublishedBy
	entry.TargetID = &eventID
	entry.Details = cloneAuditDetails(entry.Details)
	entry.Details["revisionId"] = command.RevisionID
	entry.Details["revisionNo"] = revisionNo
	entry.Details["previousStatus"] = status
	entry.Details["newStatus"] = historyevent.StatusPublished
	if err := appendAudit(ctx, tx, entry); err != nil {
		return historyevent.PublishedEvent{}, err
	}
	published, err := publishedEventByID(ctx, tx, eventID)
	if err != nil {
		return historyevent.PublishedEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("commit event publication transaction: %w", err)
	}
	return published, nil
}

func (store *Postgres) ArchiveEvent(
	ctx context.Context,
	eventID string,
	command historyevent.ArchiveCommand,
	entry audit.Entry,
) (historyevent.Event, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return historyevent.Event{}, fmt.Errorf("begin event archival transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status historyevent.PublicationStatus
	var currentRevisionID *string
	var currentVersion int64
	if err := tx.QueryRow(ctx, `
		SELECT publication_status, current_revision_id, lock_version
		FROM events
		WHERE id = $1
		FOR UPDATE
	`, eventID).Scan(&status, &currentRevisionID, &currentVersion); errors.Is(err, pgx.ErrNoRows) {
		return historyevent.Event{}, historyevent.ErrEventNotFound
	} else if err != nil {
		return historyevent.Event{}, fmt.Errorf("lock event for archival: %w", err)
	}
	if currentVersion != command.ExpectedEventVersion {
		return historyevent.Event{}, historyevent.ErrEventVersionConflict
	}
	if status != historyevent.StatusPublished || currentRevisionID == nil {
		return historyevent.Event{}, historyevent.ErrEventNotPublished
	}

	var revisionNo int
	if err := tx.QueryRow(ctx, `
		SELECT revision_no
		FROM event_revisions
		WHERE event_id = $1 AND id = $2
	`, eventID, *currentRevisionID).Scan(&revisionNo); err != nil {
		return historyevent.Event{}, fmt.Errorf("read event revision for archival: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM published_event_search WHERE event_id = $1`, eventID); err != nil {
		return historyevent.Event{}, fmt.Errorf("remove published event search projection: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE events
		SET publication_status = 'archived', lock_version = lock_version + 1,
			updated_by = $2, updated_at = $3
		WHERE id = $1
	`, eventID, command.ArchivedBy, command.ArchivedAt); err != nil {
		return historyevent.Event{}, fmt.Errorf("archive event: %w", err)
	}

	entry.ActorUserID = &command.ArchivedBy
	entry.TargetID = &eventID
	entry.Details = cloneAuditDetails(entry.Details)
	entry.Details["previousStatus"] = status
	entry.Details["newStatus"] = historyevent.StatusArchived
	entry.Details["currentRevisionId"] = *currentRevisionID
	entry.Details["currentRevisionNo"] = revisionNo
	if err := appendAudit(ctx, tx, entry); err != nil {
		return historyevent.Event{}, err
	}
	archived, err := managedEventByID(ctx, tx, eventID, false)
	if err != nil {
		return historyevent.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return historyevent.Event{}, fmt.Errorf("commit event archival transaction: %w", err)
	}
	return archived, nil
}

func (store *Postgres) PublishedEventByID(ctx context.Context, eventID string) (historyevent.PublishedEvent, error) {
	return publishedEventByID(ctx, store.pool, eventID)
}

func (store *Postgres) PublishedEventBounds(ctx context.Context) (*historyevent.PublishedEventBounds, error) {
	var start, end *float64
	err := store.pool.QueryRow(ctx, `
		SELECT min(revision.start_coordinate), max(revision.end_coordinate)
		FROM events event
		JOIN event_revisions revision ON revision.id = event.current_revision_id
		WHERE event.publication_status = 'published'
	`).Scan(&start, &end)
	if err != nil {
		return nil, fmt.Errorf("read published event bounds: %w", err)
	}
	if start == nil || end == nil {
		return nil, nil
	}
	return &historyevent.PublishedEventBounds{Start: *start, End: *end}, nil
}

func (store *Postgres) QueryPublishedEvents(ctx context.Context, query historyevent.PublishedEventQuery) (historyevent.PublishedEventQueryResult, error) {
	var result historyevent.PublishedEventQueryResult
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return result, fmt.Errorf("begin published event query transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	periodIDs, err := expandCanonicalFilterIDs(ctx, tx, "historical_periods", query.PeriodIDs)
	if err != nil {
		return result, err
	}
	regionIDs, err := expandCanonicalFilterIDs(ctx, tx, "regions", query.RegionIDs)
	if err != nil {
		return result, err
	}
	figureIDs, err := expandCanonicalFilterIDs(ctx, tx, "historical_figures", query.FigureIDs)
	if err != nil {
		return result, err
	}

	categories := make([]string, len(query.PrimaryCategories))
	for index, category := range query.PrimaryCategories {
		categories[index] = string(category)
	}
	searchJoin := ""
	if strings.TrimSpace(query.SearchTerm) != "" {
		searchJoin = " JOIN published_event_search search ON search.event_id = event.id AND search.event_revision_id = revision.id"
	}
	candidatePredicate := func(coordinateFrom, coordinateTo, searchParam, periodParam, regionParam, figureParam, categoryParam string) string {
		searchPredicate := " AND " + searchParam + " = ''"
		if strings.TrimSpace(query.SearchTerm) != "" {
			searchPredicate = " AND search.searchable_text ILIKE " + searchParam + " ESCAPE '\\'"
		}
		return `
		FROM events event
		JOIN event_revisions revision ON revision.id = event.current_revision_id` + searchJoin + `
		WHERE event.publication_status = 'published'
		  AND revision.start_coordinate <= ` + coordinateTo + ` AND revision.end_coordinate >= ` + coordinateFrom + `
		  ` + searchPredicate + `
		  AND (COALESCE(cardinality(` + periodParam + `::uuid[]), 0) = 0 OR EXISTS (
			SELECT 1 FROM event_revision_periods relation
			WHERE relation.event_revision_id = revision.id
			  AND relation.historical_period_id = ANY(` + periodParam + `::uuid[])
		  ))
		  AND (COALESCE(cardinality(` + regionParam + `::uuid[]), 0) = 0 OR EXISTS (
			SELECT 1 FROM event_revision_regions relation
			WHERE relation.event_revision_id = revision.id
			  AND relation.region_id = ANY(` + regionParam + `::uuid[])
		  ))
		  AND (COALESCE(cardinality(` + figureParam + `::uuid[]), 0) = 0 OR EXISTS (
			SELECT 1 FROM event_revision_figures relation
			WHERE relation.event_revision_id = revision.id
			  AND relation.historical_figure_id = ANY(` + figureParam + `::uuid[])
		  ))
		  AND (COALESCE(cardinality(` + categoryParam + `::text[]), 0) = 0 OR revision.primary_category::text = ANY(` + categoryParam + `::text[]))`
	}
	filterArgs := func(from, to float64) []any {
		return []any{from, to, literalSubstringPattern(query.SearchTerm), periodIDs, regionIDs, figureIDs, categories}
	}
	if err := tx.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM events WHERE publication_status = 'published'),
			count(*) `+candidatePredicate("$1", "$2", "$3", "$4", "$5", "$6", "$7"), filterArgs(query.From, query.To)...).Scan(&result.SourceTotal, &result.TotalMatching); err != nil {
		return result, fmt.Errorf("count published event query: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT event.id, event.slug,
			revision.id, revision.event_id, revision.revision_no,
			revision.title, revision.summary, revision.narrative,
			revision.time_kind::text, revision.start_era::text, revision.start_year,
			revision.start_month, revision.start_day, revision.start_circa,
			revision.end_era::text, revision.end_year, revision.end_circa,
			revision.primary_category::text, revision.prominence, revision.display_order,
			revision.published_by, revision.published_at, revision.start_coordinate `+candidatePredicate("$1", "$2", "$3", "$4", "$5", "$6", "$7")+`
		  AND revision.prominence <= $8
		LIMIT $9
	`, append(filterArgs(query.EventFrom, query.EventTo), query.MaximumProminence, maximumPublishedEventQueryResults+1)...)
	if err != nil {
		return result, fmt.Errorf("query published events: %w", err)
	}
	candidatesToReturn := make([]publishedEventQueryCandidate, 0)
	for rows.Next() {
		candidate, err := scanPublishedEventQueryCandidate(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		candidatesToReturn = append(candidatesToReturn, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("iterate published event ids: %w", err)
	}
	rows.Close()
	if len(candidatesToReturn) > maximumPublishedEventQueryResults {
		return result, historyevent.ErrResultSetTooLarge
	}
	if err := loadQueryCandidateAssociations(ctx, tx, candidatesToReturn); err != nil {
		return result, err
	}
	sortPublishedEventQueryCandidates(candidatesToReturn)
	result.Events = make([]historyevent.PublishedEvent, len(candidatesToReturn))
	for index := range candidatesToReturn {
		result.Events[index] = candidatesToReturn[index].Event
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit published event query transaction: %w", err)
	}
	return result, nil
}

func expandCanonicalFilterIDs(ctx context.Context, querier eventQuerier, table string, requestedIDs []string) ([]string, error) {
	if len(requestedIDs) == 0 {
		return nil, nil
	}
	rows, err := querier.Query(ctx, `
		SELECT candidate.id
		FROM `+table+` candidate
		WHERE COALESCE(candidate.merged_into_id, candidate.id) = ANY (
			SELECT COALESCE(requested.merged_into_id, requested.id)
			FROM `+table+` requested
			WHERE requested.id = ANY($1::uuid[])
		)
		ORDER BY candidate.id
	`, requestedIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve %s filter identifiers: %w", table, err)
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s filter identifier: %w", table, err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s filter identifiers: %w", table, err)
	}
	if len(result) == 0 {
		// Preserve the semantic difference between no filter and a syntactically
		// valid identifier that is unknown to this catalog.
		return requestedIDs, nil
	}
	return result, nil
}

func literalSubstringPattern(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	).Replace(value)
	return "%" + escaped + "%"
}

type publishedEventQueryCandidate struct {
	Event           historyevent.PublishedEvent
	StartCoordinate float64
}

type publishedEventScanner interface {
	Scan(...any) error
}

func scanPublishedEventQueryCandidate(row publishedEventScanner) (publishedEventQueryCandidate, error) {
	var candidate publishedEventQueryCandidate
	var timeKind, startEra string
	var endEra *string
	var startYear int
	var startMonth, startDay, endYear *int
	var startCirca, endCirca bool
	result := &candidate.Event
	err := row.Scan(
		&result.ID, &result.Slug,
		&result.Revision.ID, &result.Revision.EventID, &result.Revision.RevisionNo,
		&result.Revision.Title, &result.Revision.Summary, &result.Revision.Narrative,
		&timeKind, &startEra, &startYear, &startMonth, &startDay, &startCirca,
		&endEra, &endYear, &endCirca,
		&result.Revision.PrimaryCategory, &result.Revision.Prominence, &result.Revision.DisplayOrder,
		&result.Revision.PublishedBy, &result.Revision.PublishedAt, &candidate.StartCoordinate,
	)
	if err != nil {
		return candidate, fmt.Errorf("scan published event query result: %w", err)
	}
	result.Revision.Time, err = publishedTimeExpression(
		timeKind, startEra, startYear, startMonth, startDay, startCirca,
		endEra, endYear, endCirca,
	)
	if err != nil {
		return candidate, err
	}
	return candidate, nil
}

func loadQueryCandidateAssociations(ctx context.Context, querier eventQuerier, candidates []publishedEventQueryCandidate) error {
	if len(candidates) == 0 {
		return nil
	}
	revisionIDs := make([]string, len(candidates))
	indexByRevisionID := make(map[string]int, len(candidates))
	for index := range candidates {
		revisionID := candidates[index].Event.Revision.ID
		revision := &candidates[index].Event.Revision
		revision.Regions = []historyevent.EntityReference{}
		revision.Places = []historyevent.EntityReference{}
		revision.Periods = []historyevent.EntityReference{}
		revision.Figures = []historyevent.EntityReference{}
		revision.TopicTags = []historyevent.EntityReference{}
		revisionIDs[index] = revisionID
		indexByRevisionID[revisionID] = index
	}
	for associationIndex, spec := range revisionAssociationSpecs {
		rows, err := querier.Query(ctx, `
			SELECT relation.event_revision_id, resolved.id, resolved.name, resolved.disambiguation_label
			FROM `+spec.joinTable+` relation
			JOIN `+spec.entityTable+` stored ON stored.id = relation.`+spec.entityColumn+`
			JOIN `+spec.entityTable+` resolved ON resolved.id = COALESCE(stored.merged_into_id, stored.id)
			WHERE relation.event_revision_id = ANY($1::uuid[])
			GROUP BY relation.event_revision_id, resolved.id, resolved.name, resolved.disambiguation_label
			ORDER BY relation.event_revision_id, lower(resolved.name),
				lower(COALESCE(resolved.disambiguation_label, '')), resolved.id
		`, revisionIDs)
		if err != nil {
			return fmt.Errorf("load published event query %s: %w", spec.joinTable, err)
		}
		for rows.Next() {
			var revisionID string
			var reference historyevent.EntityReference
			if err := rows.Scan(&revisionID, &reference.ID, &reference.Name, &reference.DisambiguationLabel); err != nil {
				rows.Close()
				return fmt.Errorf("scan published event query %s: %w", spec.joinTable, err)
			}
			candidateIndex, ok := indexByRevisionID[revisionID]
			if !ok {
				rows.Close()
				return fmt.Errorf("load published event query %s: unknown revision %s", spec.joinTable, revisionID)
			}
			appendRevisionAssociation(&candidates[candidateIndex].Event.Revision, associationIndex, reference)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate published event query %s: %w", spec.joinTable, err)
		}
		rows.Close()
	}
	return nil
}

func appendRevisionAssociation(revision *historyevent.Revision, associationIndex int, reference historyevent.EntityReference) {
	switch associationIndex {
	case 0:
		revision.Regions = append(revision.Regions, reference)
	case 1:
		revision.Places = append(revision.Places, reference)
	case 2:
		revision.Periods = append(revision.Periods, reference)
	case 3:
		revision.Figures = append(revision.Figures, reference)
	case 4:
		revision.TopicTags = append(revision.TopicTags, reference)
	}
}

func sortPublishedEventQueryCandidates(candidates []publishedEventQueryCandidate) {
	titleCollator := collate.New(language.MustParse("zh-Hans-u-co-pinyin"))
	sort.Slice(candidates, func(leftIndex, rightIndex int) bool {
		left, right := candidates[leftIndex], candidates[rightIndex]
		if left.StartCoordinate != right.StartCoordinate {
			return left.StartCoordinate < right.StartCoordinate
		}
		if left.Event.Revision.DisplayOrder != right.Event.Revision.DisplayOrder {
			return left.Event.Revision.DisplayOrder < right.Event.Revision.DisplayOrder
		}
		if comparison := titleCollator.CompareString(left.Event.Revision.Title, right.Event.Revision.Title); comparison != 0 {
			return comparison < 0
		}
		return left.Event.ID < right.Event.ID
	})
}

func (store *Postgres) PublishedEventMetadata(ctx context.Context) (historyevent.PublishedEventMetadata, error) {
	var result historyevent.PublishedEventMetadata
	var err error
	result.Regions, err = loadPublishedMetadataReferences(ctx, store.pool, "event_revision_regions", "region_id", "regions")
	if err != nil {
		return result, err
	}
	result.Figures, err = loadPublishedMetadataReferences(ctx, store.pool, "event_revision_figures", "historical_figure_id", "historical_figures")
	if err != nil {
		return result, err
	}
	rows, err := store.pool.Query(ctx, `
		SELECT DISTINCT revision.primary_category::text
		FROM events event
		JOIN event_revisions revision ON revision.id = event.current_revision_id
		WHERE event.publication_status = 'published'
		ORDER BY revision.primary_category::text
	`)
	if err != nil {
		return result, fmt.Errorf("load published primary categories: %w", err)
	}
	for rows.Next() {
		var category historyevent.PrimaryCategory
		if err := rows.Scan(&category); err != nil {
			rows.Close()
			return result, fmt.Errorf("scan published primary category: %w", err)
		}
		result.PrimaryCategories = append(result.PrimaryCategories, category)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("iterate published primary categories: %w", err)
	}
	rows.Close()
	result.PeriodGroups, err = loadPublishedPeriodGroups(ctx, store.pool)
	return result, err
}

func publishedEventByID(ctx context.Context, querier eventQuerier, eventID string) (historyevent.PublishedEvent, error) {
	var result historyevent.PublishedEvent
	var timeKind, startEra string
	var endEra *string
	var startYear int
	var startMonth, startDay, endYear *int
	var startCirca, endCirca bool
	err := querier.QueryRow(ctx, `
		SELECT event.id, event.slug,
			revision.id, revision.event_id, revision.revision_no,
			revision.title, revision.summary, revision.narrative,
			revision.time_kind::text, revision.start_era::text, revision.start_year,
			revision.start_month, revision.start_day, revision.start_circa,
			revision.end_era::text, revision.end_year, revision.end_circa,
			revision.primary_category::text, revision.prominence, revision.display_order,
			revision.published_by, revision.published_at
		FROM events event
		JOIN event_revisions revision ON revision.id = event.current_revision_id
		WHERE event.id = $1 AND event.publication_status = 'published'
	`, eventID).Scan(
		&result.ID, &result.Slug,
		&result.Revision.ID, &result.Revision.EventID, &result.Revision.RevisionNo,
		&result.Revision.Title, &result.Revision.Summary, &result.Revision.Narrative,
		&timeKind, &startEra, &startYear, &startMonth, &startDay, &startCirca,
		&endEra, &endYear, &endCirca,
		&result.Revision.PrimaryCategory, &result.Revision.Prominence, &result.Revision.DisplayOrder,
		&result.Revision.PublishedBy, &result.Revision.PublishedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return historyevent.PublishedEvent{}, historyevent.ErrEventNotFound
	}
	if err != nil {
		return historyevent.PublishedEvent{}, fmt.Errorf("read published event: %w", err)
	}
	result.Revision.Time, err = publishedTimeExpression(timeKind, startEra, startYear, startMonth, startDay, startCirca, endEra, endYear, endCirca)
	if err != nil {
		return historyevent.PublishedEvent{}, err
	}
	if err := loadRevisionAssociations(ctx, querier, &result.Revision); err != nil {
		return historyevent.PublishedEvent{}, err
	}
	return result, nil
}

func publishedTimeExpression(kind, startEra string, startYear int, startMonth, startDay *int, startCirca bool, endEra *string, endYear *int, endCirca bool) (historyevent.TimeExpression, error) {
	returnValue, err := timeExpressionFromColumns(&kind, &startEra, &startYear, startMonth, startDay, &startCirca, endEra, endYear, &endCirca)
	if err != nil {
		return historyevent.TimeExpression{}, fmt.Errorf("read published event time: %w", err)
	}
	if returnValue == nil {
		return historyevent.TimeExpression{}, fmt.Errorf("read published event time: %w", historyevent.ErrInvalidTimeExpression)
	}
	return *returnValue, nil
}

func loadRevisionAssociations(ctx context.Context, querier eventQuerier, revision *historyevent.Revision) error {
	destinations := []*[]historyevent.EntityReference{&revision.Regions, &revision.Places, &revision.Periods, &revision.Figures, &revision.TopicTags}
	for index, spec := range revisionAssociationSpecs {
		rows, err := querier.Query(ctx, `
			SELECT resolved.id, resolved.name, resolved.disambiguation_label
			FROM `+spec.joinTable+` relation
			JOIN `+spec.entityTable+` stored ON stored.id = relation.`+spec.entityColumn+`
			JOIN `+spec.entityTable+` resolved ON resolved.id = COALESCE(stored.merged_into_id, stored.id)
			WHERE relation.event_revision_id = $1
			GROUP BY resolved.id, resolved.name, resolved.disambiguation_label
			ORDER BY lower(resolved.name), lower(COALESCE(resolved.disambiguation_label, '')), resolved.id
		`, revision.ID)
		if err != nil {
			return fmt.Errorf("load %s: %w", spec.joinTable, err)
		}
		references := make([]historyevent.EntityReference, 0)
		for rows.Next() {
			var reference historyevent.EntityReference
			if err := rows.Scan(&reference.ID, &reference.Name, &reference.DisambiguationLabel); err != nil {
				rows.Close()
				return fmt.Errorf("scan %s: %w", spec.joinTable, err)
			}
			references = append(references, reference)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate %s: %w", spec.joinTable, err)
		}
		rows.Close()
		*destinations[index] = references
	}
	return nil
}

func publicationSearchText(draft historyevent.Draft) string {
	parts := []string{draft.Title, draft.Summary, draft.Narrative}
	for _, references := range [][]historyevent.EntityReference{draft.Places, draft.Figures, draft.TopicTags} {
		for _, reference := range references {
			parts = append(parts, reference.Name)
			if reference.DisambiguationLabel != nil {
				parts = append(parts, *reference.DisambiguationLabel)
			}
		}
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

func loadPublishedMetadataReferences(ctx context.Context, querier eventQuerier, joinTable, entityColumn, entityTable string) ([]historyevent.EntityReference, error) {
	rows, err := querier.Query(ctx, `
		SELECT DISTINCT resolved.id, resolved.name, resolved.disambiguation_label
		FROM events event
		JOIN `+joinTable+` relation ON relation.event_revision_id = event.current_revision_id
		JOIN `+entityTable+` stored ON stored.id = relation.`+entityColumn+`
		JOIN `+entityTable+` resolved ON resolved.id = COALESCE(stored.merged_into_id, stored.id)
		WHERE event.publication_status = 'published'
		ORDER BY resolved.name, resolved.disambiguation_label, resolved.id
	`)
	if err != nil {
		return nil, fmt.Errorf("load published %s metadata: %w", entityTable, err)
	}
	defer rows.Close()
	result := make([]historyevent.EntityReference, 0)
	for rows.Next() {
		var reference historyevent.EntityReference
		if err := rows.Scan(&reference.ID, &reference.Name, &reference.DisambiguationLabel); err != nil {
			return nil, fmt.Errorf("scan published %s metadata: %w", entityTable, err)
		}
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published %s metadata: %w", entityTable, err)
	}
	return result, nil
}

func loadPublishedPeriodGroups(ctx context.Context, querier eventQuerier) ([]historyevent.PeriodFilterGroup, error) {
	rows, err := querier.Query(ctx, `
		SELECT DISTINCT resolved_region.id, resolved_region.name, resolved_region.disambiguation_label,
			resolved_period.id, resolved_period.name, resolved_period.disambiguation_label
		FROM events event
		JOIN event_revision_periods relation ON relation.event_revision_id = event.current_revision_id
		JOIN historical_periods stored_period ON stored_period.id = relation.historical_period_id
		JOIN historical_periods resolved_period ON resolved_period.id = COALESCE(stored_period.merged_into_id, stored_period.id)
		JOIN period_regions period_region ON period_region.historical_period_id = resolved_period.id
		JOIN regions stored_region ON stored_region.id = period_region.region_id
		JOIN regions resolved_region ON resolved_region.id = COALESCE(stored_region.merged_into_id, stored_region.id)
		WHERE event.publication_status = 'published'
		ORDER BY resolved_region.name, resolved_region.disambiguation_label, resolved_region.id,
			resolved_period.name, resolved_period.disambiguation_label, resolved_period.id
	`)
	if err != nil {
		return nil, fmt.Errorf("load published period metadata: %w", err)
	}
	defer rows.Close()
	groups := make([]historyevent.PeriodFilterGroup, 0)
	for rows.Next() {
		var contextReference, periodReference historyevent.EntityReference
		if err := rows.Scan(
			&contextReference.ID, &contextReference.Name, &contextReference.DisambiguationLabel,
			&periodReference.ID, &periodReference.Name, &periodReference.DisambiguationLabel,
		); err != nil {
			return nil, fmt.Errorf("scan published period metadata: %w", err)
		}
		if len(groups) == 0 || groups[len(groups)-1].Context.ID != contextReference.ID {
			groups = append(groups, historyevent.PeriodFilterGroup{Context: contextReference})
		}
		groups[len(groups)-1].Periods = append(groups[len(groups)-1].Periods, periodReference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published period metadata: %w", err)
	}
	return groups, nil
}
