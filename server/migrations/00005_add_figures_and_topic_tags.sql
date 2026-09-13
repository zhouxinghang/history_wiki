-- +goose Up
CREATE TABLE historical_figures (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 120),
    disambiguation_label TEXT CHECK (
        disambiguation_label IS NULL OR
        (btrim(disambiguation_label) <> '' AND char_length(disambiguation_label) <= 120)
    ),
    status canonical_entity_status NOT NULL DEFAULT 'active',
    merged_into_id UUID REFERENCES historical_figures (id) ON DELETE RESTRICT,
    lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1),
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((status = 'merged') = (merged_into_id IS NOT NULL)),
    CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX historical_figures_name_idx
    ON historical_figures (lower(name), lower(COALESCE(disambiguation_label, '')), id);
CREATE INDEX historical_figures_status_idx
    ON historical_figures (status, lower(name), id);

CREATE TRIGGER historical_figures_immutable_id
BEFORE UPDATE OF id ON historical_figures
FOR EACH ROW EXECUTE FUNCTION reject_id_change();

CREATE TABLE topic_tags (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 120),
    disambiguation_label TEXT CHECK (
        disambiguation_label IS NULL OR
        (btrim(disambiguation_label) <> '' AND char_length(disambiguation_label) <= 120)
    ),
    status canonical_entity_status NOT NULL DEFAULT 'active',
    merged_into_id UUID REFERENCES topic_tags (id) ON DELETE RESTRICT,
    lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1),
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((status = 'merged') = (merged_into_id IS NOT NULL)),
    CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX topic_tags_name_idx
    ON topic_tags (lower(name), lower(COALESCE(disambiguation_label, '')), id);
CREATE INDEX topic_tags_status_idx
    ON topic_tags (status, lower(name), id);

CREATE TRIGGER topic_tags_immutable_id
BEFORE UPDATE OF id ON topic_tags
FOR EACH ROW EXECUTE FUNCTION reject_id_change();

-- +goose Down
DROP TRIGGER topic_tags_immutable_id ON topic_tags;
DROP TABLE topic_tags;
DROP TRIGGER historical_figures_immutable_id ON historical_figures;
DROP TABLE historical_figures;
