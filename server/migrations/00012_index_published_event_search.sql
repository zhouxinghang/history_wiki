-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE INDEX published_event_search_trgm_idx
ON published_event_search
USING GIN (searchable_text public.gin_trgm_ops);

-- +goose Down
DROP INDEX published_event_search_trgm_idx;
