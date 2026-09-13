package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
)

type governanceSpec struct {
	table        string
	draftJoin    string
	revisionJoin string
	entityColumn string
}

var governanceSpecs = map[catalog.CanonicalEntityKind]governanceSpec{
	catalog.KindRegion: {
		table: "regions", draftJoin: "event_draft_regions", revisionJoin: "event_revision_regions", entityColumn: "region_id",
	},
	catalog.KindPlace: {
		table: "places", draftJoin: "event_draft_places", revisionJoin: "event_revision_places", entityColumn: "place_id",
	},
	catalog.KindHistoricalPeriod: {
		table: "historical_periods", draftJoin: "event_draft_periods", revisionJoin: "event_revision_periods", entityColumn: "historical_period_id",
	},
	catalog.KindHistoricalFigure: {
		table: "historical_figures", draftJoin: "event_draft_figures", revisionJoin: "event_revision_figures", entityColumn: "historical_figure_id",
	},
	catalog.KindTopicTag: {
		table: "topic_tags", draftJoin: "event_draft_topic_tags", revisionJoin: "event_revision_topic_tags", entityColumn: "topic_tag_id",
	},
}

func governanceSpecification(kind catalog.CanonicalEntityKind) (governanceSpec, error) {
	spec, ok := governanceSpecs[kind]
	if !ok {
		return governanceSpec{}, fmt.Errorf("%w: unsupported kind %q", catalog.ErrInvalidCanonicalEntity, kind)
	}
	return spec, nil
}

func (store *Postgres) CanonicalEntityMergeImpact(
	ctx context.Context,
	kind catalog.CanonicalEntityKind,
	entityID string,
) (catalog.EntityMergeImpact, error) {
	spec, err := governanceSpecification(kind)
	if err != nil {
		return catalog.EntityMergeImpact{}, err
	}
	entity, err := canonicalEntityByID(ctx, store.pool, spec.table, entityID, false)
	if err != nil {
		return catalog.EntityMergeImpact{}, err
	}
	if entity.Status == catalog.StatusMerged {
		return catalog.EntityMergeImpact{}, catalog.ErrCanonicalEntityNotWritable
	}
	return canonicalEntityMergeImpact(ctx, store.pool, spec, entityID)
}

func (store *Postgres) MergeCanonicalEntity(
	ctx context.Context,
	kind catalog.CanonicalEntityKind,
	entityID string,
	command catalog.CanonicalEntityMerge,
	entry audit.Entry,
) (catalog.CanonicalEntity, catalog.EntityMergeImpact, error) {
	spec, err := governanceSpecification(kind)
	if err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, fmt.Errorf("begin %s merge transaction: %w", spec.table, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockCanonicalMergePair(ctx, tx, spec.table, entityID, command.TargetID); err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, err
	}

	current, err := canonicalEntityByID(ctx, tx, spec.table, entityID, true)
	if err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, err
	}
	if current.LockVersion != command.ExpectedVersion {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, catalog.ErrVersionConflict
	}
	if current.Status == catalog.StatusMerged {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, catalog.ErrCanonicalEntityNotWritable
	}
	if command.TargetID == entityID {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, catalog.ErrInvalidMergeTarget
	}
	target, err := canonicalEntityByID(ctx, tx, spec.table, command.TargetID, true)
	if errors.Is(err, catalog.ErrCanonicalEntityNotFound) {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, catalog.ErrInvalidMergeTarget
	}
	if err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, err
	}
	if target.Status != catalog.StatusActive || target.MergedIntoID != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, catalog.ErrInvalidMergeTarget
	}

	impact, err := canonicalEntityMergeImpact(ctx, tx, spec, entityID)
	if err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, err
	}
	if !impact.Equal(command.ConfirmedImpact) {
		return catalog.CanonicalEntity{}, impact, catalog.ErrMergeImpactChanged
	}

	// Compress every existing alias before retiring the current target.  The
	// database trigger rejects a merge that would leave a chain behind.
	aliases, err := tx.Exec(ctx, `UPDATE `+spec.table+`
		SET merged_into_id = $2, updated_by = $3, updated_at = $4, lock_version = lock_version + 1
		WHERE merged_into_id = $1`, entityID, command.TargetID, command.UpdatedBy, command.UpdatedAt)
	if err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, fmt.Errorf("compress %s merge aliases: %w", spec.table, err)
	}

	merged, err := scanCanonicalEntity(tx.QueryRow(ctx, `UPDATE `+spec.table+`
		SET status = 'merged', merged_into_id = $2, updated_by = $3, updated_at = $4,
			lock_version = lock_version + 1
		WHERE id = $1
		RETURNING id, name, disambiguation_label, status, merged_into_id, lock_version,
			created_by, updated_by, created_at, updated_at`, entityID, command.TargetID, command.UpdatedBy, command.UpdatedAt))
	if err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, fmt.Errorf("merge %s: %w", spec.table, err)
	}
	if err := refreshPublishedSearchForCanonicalIdentity(ctx, tx, spec, command.TargetID); err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, err
	}

	entry.ActorUserID = &command.UpdatedBy
	entry.TargetID = &entityID
	entry.Details = cloneAuditDetails(entry.Details)
	entry.Details["targetId"] = command.TargetID
	entry.Details["targetName"] = target.Name
	entry.Details["compressedAliasCount"] = aliases.RowsAffected()
	entry.Details["impact"] = impact
	entry.Details["previousLockVersion"] = current.LockVersion
	entry.Details["lockVersion"] = merged.LockVersion
	if err := appendAudit(ctx, tx, entry); err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.CanonicalEntity{}, catalog.EntityMergeImpact{}, fmt.Errorf("commit %s merge transaction: %w", spec.table, err)
	}
	return merged, impact, nil
}

func lockCanonicalMergePair(ctx context.Context, tx pgx.Tx, table, sourceID, targetID string) error {
	ids := []string{sourceID, targetID}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, table+":"+id); err != nil {
			return fmt.Errorf("lock %s merge entity: %w", table, err)
		}
	}
	return nil
}

func ensureCanonicalEntityStatusChangeAllowed(ctx context.Context, querier regionQuerier, table, entityID string, status catalog.EntityStatus) error {
	if status == catalog.StatusActive {
		return nil
	}
	var hasAliases bool
	if err := querier.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE merged_into_id = $1)`, entityID).Scan(&hasAliases); err != nil {
		return fmt.Errorf("check %s aliases: %w", table, err)
	}
	if hasAliases {
		return catalog.ErrCanonicalEntityNotWritable
	}
	return nil
}

func canonicalEntityMergeImpact(
	ctx context.Context,
	querier eventQuerier,
	spec governanceSpec,
	entityID string,
) (catalog.EntityMergeImpact, error) {
	var impact catalog.EntityMergeImpact
	err := querier.QueryRow(ctx, `
		SELECT
			(SELECT count(DISTINCT relation.event_id)
			 FROM `+spec.draftJoin+` relation
			 JOIN `+spec.table+` entity ON entity.id = relation.`+spec.entityColumn+`
			 WHERE COALESCE(entity.merged_into_id, entity.id) = $1),
			(SELECT count(DISTINCT relation.event_revision_id)
			 FROM `+spec.revisionJoin+` relation
			 JOIN `+spec.table+` entity ON entity.id = relation.`+spec.entityColumn+`
			 WHERE COALESCE(entity.merged_into_id, entity.id) = $1),
			(SELECT count(DISTINCT event.id)
			 FROM events event
			 JOIN `+spec.revisionJoin+` relation ON relation.event_revision_id = event.current_revision_id
			 JOIN `+spec.table+` entity ON entity.id = relation.`+spec.entityColumn+`
			 WHERE event.publication_status = 'published'
			   AND COALESCE(entity.merged_into_id, entity.id) = $1)
	`, entityID).Scan(&impact.DraftCount, &impact.RevisionCount, &impact.PublishedEventCount)
	if err != nil {
		return catalog.EntityMergeImpact{}, fmt.Errorf("read %s merge impact: %w", spec.table, err)
	}
	return impact, nil
}

func refreshPublishedSearchForCanonicalIdentity(ctx context.Context, tx pgx.Tx, spec governanceSpec, canonicalID string) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT event.id
		FROM events event
		JOIN `+spec.revisionJoin+` relation ON relation.event_revision_id = event.current_revision_id
		JOIN `+spec.table+` entity ON entity.id = relation.`+spec.entityColumn+`
		WHERE event.publication_status = 'published'
		  AND COALESCE(entity.merged_into_id, entity.id) = $1
	`, canonicalID)
	if err != nil {
		return fmt.Errorf("find affected published events for %s: %w", spec.table, err)
	}
	eventIDs := make([]string, 0)
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			rows.Close()
			return fmt.Errorf("scan affected published event for %s: %w", spec.table, err)
		}
		eventIDs = append(eventIDs, eventID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate affected published events for %s: %w", spec.table, err)
	}
	rows.Close()
	for _, eventID := range eventIDs {
		if err := rebuildPublishedEventSearch(ctx, tx, eventID); err != nil {
			return err
		}
	}
	return nil
}

func rebuildPublishedEventSearch(ctx context.Context, tx pgx.Tx, eventID string) error {
	var revisionID, title, summary, narrative string
	if err := tx.QueryRow(ctx, `
		SELECT revision.id, revision.title, revision.summary, revision.narrative
		FROM events event
		JOIN event_revisions revision ON revision.id = event.current_revision_id
		WHERE event.id = $1 AND event.publication_status = 'published'
	`, eventID).Scan(&revisionID, &title, &summary, &narrative); err != nil {
		return fmt.Errorf("read published event for search rebuild: %w", err)
	}
	parts := []string{title, summary, narrative}
	for _, spec := range []revisionAssociationSpec{revisionAssociationSpecs[1], revisionAssociationSpecs[3], revisionAssociationSpecs[4]} {
		rows, err := tx.Query(ctx, `
			SELECT resolved.name, resolved.disambiguation_label
			FROM `+spec.joinTable+` relation
			JOIN `+spec.entityTable+` stored ON stored.id = relation.`+spec.entityColumn+`
			JOIN `+spec.entityTable+` resolved ON resolved.id = COALESCE(stored.merged_into_id, stored.id)
			WHERE relation.event_revision_id = $1
			ORDER BY lower(resolved.name), lower(COALESCE(resolved.disambiguation_label, '')), resolved.id
		`, revisionID)
		if err != nil {
			return fmt.Errorf("read %s for search rebuild: %w", spec.joinTable, err)
		}
		for rows.Next() {
			var name string
			var label *string
			if err := rows.Scan(&name, &label); err != nil {
				rows.Close()
				return fmt.Errorf("scan %s for search rebuild: %w", spec.joinTable, err)
			}
			parts = append(parts, name)
			if label != nil {
				parts = append(parts, *label)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate %s for search rebuild: %w", spec.joinTable, err)
		}
		rows.Close()
	}
	if _, err := tx.Exec(ctx, `UPDATE published_event_search SET searchable_text = $2 WHERE event_id = $1`, eventID, strings.ToLower(strings.Join(parts, "\n"))); err != nil {
		return fmt.Errorf("rebuild published event search: %w", err)
	}
	return nil
}
