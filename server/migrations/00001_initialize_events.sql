-- +goose Up
CREATE TYPE publication_status AS ENUM ('unpublished', 'published', 'archived');

CREATE TABLE events (
    id UUID PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE CHECK (slug <> ''),
    publication_status publication_status NOT NULL DEFAULT 'unpublished',
    current_revision_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX events_publication_status_idx ON events (publication_status);

-- +goose Down
DROP TABLE events;
DROP TYPE publication_status;

