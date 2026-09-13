-- +goose Up
CREATE TYPE event_time_kind AS ENUM ('exact_date', 'year', 'circa', 'interval');
CREATE TYPE historical_era AS ENUM ('BCE', 'CE');
CREATE TYPE primary_category AS ENUM ('政治', '军事', '文化', '科技', '社会', '交流');

ALTER TABLE events
    ADD COLUMN lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1),
    ADD COLUMN created_by UUID REFERENCES users (id) ON DELETE RESTRICT,
    ADD COLUMN updated_by UUID REFERENCES users (id) ON DELETE RESTRICT,
    ADD CONSTRAINT events_slug_format CHECK (
        char_length(slug) <= 120 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
    );

CREATE TRIGGER events_immutable_id
BEFORE UPDATE OF id ON events
FOR EACH ROW EXECUTE FUNCTION reject_id_change();

CREATE TABLE event_drafts (
    event_id UUID PRIMARY KEY REFERENCES events (id) ON DELETE RESTRICT,
    title TEXT NOT NULL DEFAULT '' CHECK (char_length(title) <= 200),
    summary TEXT NOT NULL DEFAULT '' CHECK (char_length(summary) <= 1000),
    narrative TEXT NOT NULL DEFAULT '' CHECK (char_length(narrative) <= 100000),
    time_kind event_time_kind,
    start_era historical_era,
    start_year INTEGER CHECK (start_year BETWEEN 1 AND 999999),
    start_month SMALLINT CHECK (start_month BETWEEN 1 AND 12),
    start_day SMALLINT CHECK (start_day BETWEEN 1 AND 31),
    start_circa BOOLEAN NOT NULL DEFAULT FALSE,
    end_era historical_era,
    end_year INTEGER CHECK (end_year BETWEEN 1 AND 999999),
    end_circa BOOLEAN NOT NULL DEFAULT FALSE,
    start_coordinate DOUBLE PRECISION,
    end_coordinate DOUBLE PRECISION,
    primary_category primary_category,
    prominence SMALLINT CHECK (prominence BETWEEN 1 AND 3),
    display_order INTEGER NOT NULL DEFAULT 1000 CHECK (display_order BETWEEN -1000000 AND 1000000),
    lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1),
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (
        (time_kind IS NULL AND start_era IS NULL AND start_year IS NULL AND start_month IS NULL AND start_day IS NULL
            AND end_era IS NULL AND end_year IS NULL AND start_coordinate IS NULL AND end_coordinate IS NULL
            AND start_circa = FALSE AND end_circa = FALSE)
        OR
        (time_kind = 'exact_date' AND start_era IS NOT NULL AND start_year IS NOT NULL
            AND start_month IS NOT NULL AND start_day IS NOT NULL AND end_era IS NULL AND end_year IS NULL
            AND start_coordinate IS NOT NULL AND end_coordinate = start_coordinate
            AND start_circa = FALSE AND end_circa = FALSE)
        OR
        (time_kind = 'year' AND start_era IS NOT NULL AND start_year IS NOT NULL
            AND start_month IS NULL AND start_day IS NULL AND end_era IS NULL AND end_year IS NULL
            AND start_coordinate IS NOT NULL AND end_coordinate = start_coordinate
            AND start_circa = FALSE AND end_circa = FALSE)
        OR
        (time_kind = 'circa' AND start_era IS NOT NULL AND start_year IS NOT NULL
            AND start_month IS NULL AND start_day IS NULL AND end_era IS NULL AND end_year IS NULL
            AND start_coordinate IS NOT NULL AND end_coordinate = start_coordinate
            AND start_circa = TRUE AND end_circa = FALSE)
        OR
        (time_kind = 'interval' AND start_era IS NOT NULL AND start_year IS NOT NULL
            AND start_month IS NULL AND start_day IS NULL AND end_era IS NOT NULL AND end_year IS NOT NULL
            AND start_coordinate IS NOT NULL AND end_coordinate IS NOT NULL AND start_coordinate <= end_coordinate)
    )
);

CREATE INDEX event_drafts_time_idx ON event_drafts (start_coordinate, end_coordinate);

CREATE TABLE event_draft_regions (
    event_id UUID NOT NULL REFERENCES event_drafts (event_id) ON DELETE CASCADE,
    region_id UUID NOT NULL REFERENCES regions (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_id, region_id)
);
CREATE INDEX event_draft_regions_region_idx ON event_draft_regions (region_id, event_id);

CREATE TABLE event_draft_places (
    event_id UUID NOT NULL REFERENCES event_drafts (event_id) ON DELETE CASCADE,
    place_id UUID NOT NULL REFERENCES places (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_id, place_id)
);
CREATE INDEX event_draft_places_place_idx ON event_draft_places (place_id, event_id);

CREATE TABLE event_draft_periods (
    event_id UUID NOT NULL REFERENCES event_drafts (event_id) ON DELETE CASCADE,
    historical_period_id UUID NOT NULL REFERENCES historical_periods (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_id, historical_period_id)
);
CREATE INDEX event_draft_periods_period_idx ON event_draft_periods (historical_period_id, event_id);

CREATE TABLE event_draft_figures (
    event_id UUID NOT NULL REFERENCES event_drafts (event_id) ON DELETE CASCADE,
    historical_figure_id UUID NOT NULL REFERENCES historical_figures (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_id, historical_figure_id)
);
CREATE INDEX event_draft_figures_figure_idx ON event_draft_figures (historical_figure_id, event_id);

CREATE TABLE event_draft_topic_tags (
    event_id UUID NOT NULL REFERENCES event_drafts (event_id) ON DELETE CASCADE,
    topic_tag_id UUID NOT NULL REFERENCES topic_tags (id) ON DELETE RESTRICT,
    PRIMARY KEY (event_id, topic_tag_id)
);
CREATE INDEX event_draft_topic_tags_tag_idx ON event_draft_topic_tags (topic_tag_id, event_id);

-- Relationship rows have no mutable attributes; replacements use DELETE plus INSERT.
CREATE TRIGGER event_draft_regions_immutable_identity BEFORE UPDATE ON event_draft_regions
FOR EACH ROW EXECUTE FUNCTION reject_context_relationship_update();
CREATE TRIGGER event_draft_places_immutable_identity BEFORE UPDATE ON event_draft_places
FOR EACH ROW EXECUTE FUNCTION reject_context_relationship_update();
CREATE TRIGGER event_draft_periods_immutable_identity BEFORE UPDATE ON event_draft_periods
FOR EACH ROW EXECUTE FUNCTION reject_context_relationship_update();
CREATE TRIGGER event_draft_figures_immutable_identity BEFORE UPDATE ON event_draft_figures
FOR EACH ROW EXECUTE FUNCTION reject_context_relationship_update();
CREATE TRIGGER event_draft_topic_tags_immutable_identity BEFORE UPDATE ON event_draft_topic_tags
FOR EACH ROW EXECUTE FUNCTION reject_context_relationship_update();

-- +goose Down
DROP TRIGGER event_draft_topic_tags_immutable_identity ON event_draft_topic_tags;
DROP TRIGGER event_draft_figures_immutable_identity ON event_draft_figures;
DROP TRIGGER event_draft_periods_immutable_identity ON event_draft_periods;
DROP TRIGGER event_draft_places_immutable_identity ON event_draft_places;
DROP TRIGGER event_draft_regions_immutable_identity ON event_draft_regions;
DROP TABLE event_draft_topic_tags;
DROP TABLE event_draft_figures;
DROP TABLE event_draft_periods;
DROP TABLE event_draft_places;
DROP TABLE event_draft_regions;
DROP TABLE event_drafts;
DROP TRIGGER events_immutable_id ON events;
ALTER TABLE events
    DROP CONSTRAINT events_slug_format,
    DROP COLUMN updated_by,
    DROP COLUMN created_by,
    DROP COLUMN lock_version;
DROP TYPE primary_category;
DROP TYPE historical_era;
DROP TYPE event_time_kind;
