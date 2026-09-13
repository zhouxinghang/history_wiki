-- +goose Up
CREATE TABLE event_revisions (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES events (id) ON DELETE RESTRICT,
    revision_no INTEGER NOT NULL CHECK (revision_no >= 1),
    title TEXT NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    summary TEXT NOT NULL CHECK (char_length(btrim(summary)) BETWEEN 1 AND 500),
    narrative TEXT NOT NULL CHECK (char_length(btrim(narrative)) BETWEEN 1 AND 20000),
    time_kind event_time_kind NOT NULL,
    start_era historical_era NOT NULL,
    start_year INTEGER NOT NULL CHECK (start_year BETWEEN 1 AND 999999),
    start_month SMALLINT CHECK (start_month BETWEEN 1 AND 12),
    start_day SMALLINT CHECK (start_day BETWEEN 1 AND 31),
    start_circa BOOLEAN NOT NULL DEFAULT FALSE,
    end_era historical_era,
    end_year INTEGER CHECK (end_year BETWEEN 1 AND 999999),
    end_circa BOOLEAN NOT NULL DEFAULT FALSE,
    start_coordinate DOUBLE PRECISION NOT NULL,
    end_coordinate DOUBLE PRECISION NOT NULL,
    primary_category primary_category NOT NULL,
    prominence SMALLINT NOT NULL CHECK (prominence BETWEEN 1 AND 3),
    display_order INTEGER NOT NULL CHECK (display_order BETWEEN 0 AND 1000000),
    published_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    published_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (event_id, revision_no),
    UNIQUE (event_id, id),
    CHECK (
        (time_kind = 'exact_date' AND start_month IS NOT NULL AND start_day IS NOT NULL
            AND end_era IS NULL AND end_year IS NULL AND end_coordinate = start_coordinate
            AND start_circa = FALSE AND end_circa = FALSE)
        OR
        (time_kind = 'year' AND start_month IS NULL AND start_day IS NULL
            AND end_era IS NULL AND end_year IS NULL AND end_coordinate = start_coordinate
            AND start_circa = FALSE AND end_circa = FALSE)
        OR
        (time_kind = 'circa' AND start_month IS NULL AND start_day IS NULL
            AND end_era IS NULL AND end_year IS NULL AND end_coordinate = start_coordinate
            AND start_circa = TRUE AND end_circa = FALSE)
        OR
        (time_kind = 'interval' AND start_month IS NULL AND start_day IS NULL
            AND end_era IS NOT NULL AND end_year IS NOT NULL
            AND start_coordinate <= end_coordinate)
    )
);

CREATE INDEX event_revisions_event_idx ON event_revisions (event_id, revision_no DESC);
CREATE INDEX event_revisions_time_idx ON event_revisions (start_coordinate, end_coordinate);

ALTER TABLE events
    ADD CONSTRAINT events_current_revision_fk
    FOREIGN KEY (id, current_revision_id)
    REFERENCES event_revisions (event_id, id)
    ON DELETE RESTRICT;
CREATE INDEX events_publication_revision_idx ON events (publication_status, current_revision_id);

CREATE TABLE event_revision_regions (
    event_revision_id UUID NOT NULL REFERENCES event_revisions (id) ON DELETE RESTRICT,
    region_id UUID NOT NULL REFERENCES regions (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_revision_id, region_id)
);
CREATE INDEX event_revision_regions_region_idx ON event_revision_regions (region_id, event_revision_id);

CREATE TABLE event_revision_places (
    event_revision_id UUID NOT NULL REFERENCES event_revisions (id) ON DELETE RESTRICT,
    place_id UUID NOT NULL REFERENCES places (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_revision_id, place_id)
);
CREATE INDEX event_revision_places_place_idx ON event_revision_places (place_id, event_revision_id);

CREATE TABLE event_revision_periods (
    event_revision_id UUID NOT NULL REFERENCES event_revisions (id) ON DELETE RESTRICT,
    historical_period_id UUID NOT NULL REFERENCES historical_periods (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_revision_id, historical_period_id)
);
CREATE INDEX event_revision_periods_period_idx ON event_revision_periods (historical_period_id, event_revision_id);

CREATE TABLE event_revision_figures (
    event_revision_id UUID NOT NULL REFERENCES event_revisions (id) ON DELETE RESTRICT,
    historical_figure_id UUID NOT NULL REFERENCES historical_figures (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_revision_id, historical_figure_id)
);
CREATE INDEX event_revision_figures_figure_idx ON event_revision_figures (historical_figure_id, event_revision_id);

CREATE TABLE event_revision_topic_tags (
    event_revision_id UUID NOT NULL REFERENCES event_revisions (id) ON DELETE RESTRICT,
    topic_tag_id UUID NOT NULL REFERENCES topic_tags (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_revision_id, topic_tag_id)
);
CREATE INDEX event_revision_topic_tags_tag_idx ON event_revision_topic_tags (topic_tag_id, event_revision_id);

CREATE TABLE published_event_search (
    event_id UUID PRIMARY KEY REFERENCES events (id) ON DELETE CASCADE,
    event_revision_id UUID NOT NULL UNIQUE REFERENCES event_revisions (id) ON DELETE RESTRICT,
    searchable_text TEXT NOT NULL
);

CREATE TRIGGER event_revisions_append_only
BEFORE UPDATE OR DELETE ON event_revisions
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();
CREATE TRIGGER event_revision_regions_append_only
BEFORE UPDATE OR DELETE ON event_revision_regions
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();
CREATE TRIGGER event_revision_places_append_only
BEFORE UPDATE OR DELETE ON event_revision_places
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();
CREATE TRIGGER event_revision_periods_append_only
BEFORE UPDATE OR DELETE ON event_revision_periods
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();
CREATE TRIGGER event_revision_figures_append_only
BEFORE UPDATE OR DELETE ON event_revision_figures
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();
CREATE TRIGGER event_revision_topic_tags_append_only
BEFORE UPDATE OR DELETE ON event_revision_topic_tags
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();

REVOKE UPDATE, DELETE ON event_revisions FROM PUBLIC;
REVOKE UPDATE, DELETE ON event_revision_regions FROM PUBLIC;
REVOKE UPDATE, DELETE ON event_revision_places FROM PUBLIC;
REVOKE UPDATE, DELETE ON event_revision_periods FROM PUBLIC;
REVOKE UPDATE, DELETE ON event_revision_figures FROM PUBLIC;
REVOKE UPDATE, DELETE ON event_revision_topic_tags FROM PUBLIC;

-- +goose Down
DROP TRIGGER event_revision_topic_tags_append_only ON event_revision_topic_tags;
DROP TRIGGER event_revision_figures_append_only ON event_revision_figures;
DROP TRIGGER event_revision_periods_append_only ON event_revision_periods;
DROP TRIGGER event_revision_places_append_only ON event_revision_places;
DROP TRIGGER event_revision_regions_append_only ON event_revision_regions;
DROP TRIGGER event_revisions_append_only ON event_revisions;
DROP TABLE published_event_search;
DROP TABLE event_revision_topic_tags;
DROP TABLE event_revision_figures;
DROP TABLE event_revision_periods;
DROP TABLE event_revision_places;
DROP TABLE event_revision_regions;
DROP INDEX events_publication_revision_idx;
ALTER TABLE events DROP CONSTRAINT events_current_revision_fk;
DROP TABLE event_revisions;
