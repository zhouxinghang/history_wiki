-- +goose Up
ALTER TABLE users
    ADD COLUMN lock_version BIGINT NOT NULL DEFAULT 1 CHECK (lock_version >= 1);

-- +goose Down
ALTER TABLE users DROP COLUMN lock_version;
