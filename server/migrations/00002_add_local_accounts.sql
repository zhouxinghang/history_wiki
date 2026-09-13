-- +goose Up
CREATE TYPE user_role AS ENUM ('editor', 'administrator');
CREATE TYPE audit_outcome AS ENUM ('success', 'failure');

CREATE TABLE users (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL CHECK (email <> ''),
    normalized_email TEXT NOT NULL UNIQUE CHECK (normalized_email = lower(btrim(normalized_email))),
    password_hash TEXT NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    role user_role NOT NULL,
    disabled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    id UUID PRIMARY KEY,
    token_digest BYTEA NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    csrf_token_digest BYTEA NOT NULL CHECK (octet_length(csrf_token_digest) = 32),
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    idle_expires_at TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    CHECK (idle_expires_at <= absolute_expires_at)
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expiry_idx ON sessions (idle_expires_at, absolute_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE audit_logs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    request_id TEXT NOT NULL CHECK (request_id <> ''),
    actor_user_id UUID REFERENCES users (id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action <> ''),
    target_type TEXT NOT NULL CHECK (target_type <> ''),
    target_id TEXT,
    source_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    outcome audit_outcome NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details) = 'object')
);

CREATE INDEX audit_logs_actor_time_idx ON audit_logs (actor_user_id, occurred_at DESC);
CREATE INDEX audit_logs_action_time_idx ON audit_logs (action, occurred_at DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_append_only_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_logs_append_only
BEFORE UPDATE OR DELETE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION reject_append_only_change();

-- +goose Down
DROP TRIGGER audit_logs_append_only ON audit_logs;
DROP FUNCTION reject_append_only_change();
DROP TABLE audit_logs;
DROP TABLE sessions;
DROP TABLE users;
DROP TYPE audit_outcome;
DROP TYPE user_role;
