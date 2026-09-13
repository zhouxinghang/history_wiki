-- +goose Up
CREATE TYPE canonical_entity_status AS ENUM ('active', 'inactive', 'merged');

CREATE TABLE regions (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 120),
    disambiguation_label TEXT CHECK (
        disambiguation_label IS NULL OR
        (btrim(disambiguation_label) <> '' AND char_length(disambiguation_label) <= 120)
    ),
    status canonical_entity_status NOT NULL DEFAULT 'active',
    merged_into_id UUID REFERENCES regions (id) ON DELETE RESTRICT,
    lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1),
    created_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((status = 'merged') = (merged_into_id IS NOT NULL)),
    CHECK (merged_into_id IS NULL OR merged_into_id <> id)
);

CREATE INDEX regions_name_idx ON regions (lower(name), lower(COALESCE(disambiguation_label, '')), id);
CREATE INDEX regions_status_idx ON regions (status, lower(name), id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_id_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id THEN
        RAISE EXCEPTION '% identity is immutable', TG_TABLE_NAME;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER regions_immutable_id
BEFORE UPDATE OF id ON regions
FOR EACH ROW EXECUTE FUNCTION reject_id_change();

CREATE TABLE idempotency_records (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_user_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    scope TEXT NOT NULL CHECK (scope <> ''),
    idempotency_key TEXT NOT NULL CHECK (idempotency_key <> '' AND char_length(idempotency_key) <= 200),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    target_id TEXT NOT NULL CHECK (target_id <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (actor_user_id, scope, idempotency_key)
);

-- +goose Down
DROP TABLE idempotency_records;
DROP TRIGGER regions_immutable_id ON regions;
DROP FUNCTION reject_id_change();
DROP TABLE regions;
DROP TYPE canonical_entity_status;
