-- +goose Up
CREATE TABLE event_import_batches (
    id UUID PRIMARY KEY,
    actor_user_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    event_ids JSONB NOT NULL CHECK (
        jsonb_typeof(event_ids) = 'array'
        AND jsonb_array_length(event_ids) BETWEEN 1 AND 10000
    ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX event_import_batches_actor_time_idx
    ON event_import_batches (actor_user_id, created_at DESC);

CREATE TRIGGER event_import_batches_immutable
BEFORE UPDATE OR DELETE ON event_import_batches
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();

-- +goose Down
DROP TRIGGER event_import_batches_immutable ON event_import_batches;
DROP TABLE event_import_batches;
