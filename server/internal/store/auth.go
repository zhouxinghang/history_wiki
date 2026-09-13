package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
)

func (store *Postgres) CreateFirstAdministrator(ctx context.Context, user auth.User, entry audit.Entry) error {
	if err := user.Validate(); err != nil {
		return err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin first administrator transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `LOCK TABLE users IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock users for bootstrap: %w", err)
	}
	var userCount int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if userCount != 0 {
		return auth.ErrAlreadyInitialized
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, email, normalized_email, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
	`, user.ID, user.Email, user.NormalizedEmail, user.PasswordHash, user.Role); err != nil {
		return fmt.Errorf("insert first administrator: %w", err)
	}
	entry.ActorUserID = &user.ID
	entry.TargetID = &user.ID
	if err := appendAudit(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit first administrator transaction: %w", err)
	}
	return nil
}

func (store *Postgres) UserByNormalizedEmail(ctx context.Context, normalizedEmail string) (auth.User, error) {
	user, err := scanUser(store.pool.QueryRow(ctx, `
		SELECT id, email, normalized_email, password_hash, role, disabled_at, lock_version, created_at, updated_at
		FROM users
		WHERE normalized_email = $1
	`, normalizedEmail))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("find user by email: %w", err)
	}
	return user, nil
}

func (store *Postgres) UserByID(ctx context.Context, userID string) (auth.User, error) {
	user, err := scanUser(store.pool.QueryRow(ctx, `
		SELECT id, email, normalized_email, password_hash, role, disabled_at, lock_version, created_at, updated_at
		FROM users
		WHERE id = $1
	`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("find user by id: %w", err)
	}
	return user, nil
}

func (store *Postgres) ListUsers(ctx context.Context) ([]auth.User, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id, email, normalized_email, password_hash, role, disabled_at, lock_version, created_at, updated_at
		FROM users
		ORDER BY disabled_at NULLS FIRST, lower(email), id
	`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]auth.User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

func (store *Postgres) CreateUser(ctx context.Context, user auth.User, entry audit.Entry) (auth.User, error) {
	if err := user.Validate(); err != nil {
		return auth.User{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, fmt.Errorf("begin user creation transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	created, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO users (
			id, email, normalized_email, password_hash, role, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		RETURNING id, email, normalized_email, password_hash, role, disabled_at, lock_version, created_at, updated_at
	`, user.ID, user.Email, user.NormalizedEmail, user.PasswordHash, user.Role, user.CreatedAt))
	if isUniqueViolation(err) {
		return auth.User{}, auth.ErrEmailAlreadyExists
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("insert user: %w", err)
	}
	entry.TargetID = &created.ID
	entry.Action = "account.create"
	entry.TargetType = "user"
	entry.Outcome = audit.OutcomeSuccess
	entry.Details = map[string]any{"email": created.Email, "role": created.Role}
	if err := appendAudit(ctx, tx, entry); err != nil {
		return auth.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.User{}, fmt.Errorf("commit user creation transaction: %w", err)
	}
	return created, nil
}

func (store *Postgres) UpdateUser(
	ctx context.Context,
	userID string,
	role auth.Role,
	disabled bool,
	expectedVersion int64,
	updatedAt time.Time,
	entry audit.Entry,
) (auth.User, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return auth.User{}, fmt.Errorf("begin user update transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// This lock serializes administrator membership changes, so two concurrent
	// requests cannot both conclude that another active administrator remains.
	if _, err := tx.Exec(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return auth.User{}, fmt.Errorf("lock users for account update: %w", err)
	}
	current, err := scanUser(tx.QueryRow(ctx, `
		SELECT id, email, normalized_email, password_hash, role, disabled_at, lock_version, created_at, updated_at
		FROM users WHERE id = $1 FOR UPDATE
	`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("find user for update: %w", err)
	}
	if current.LockVersion != expectedVersion {
		return auth.User{}, auth.ErrUserVersionConflict
	}

	willBeActiveAdministrator := role == auth.RoleAdministrator && !disabled
	if current.Role == auth.RoleAdministrator && current.DisabledAt == nil && !willBeActiveAdministrator {
		var administrators int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM users
			WHERE role = 'administrator' AND disabled_at IS NULL
		`).Scan(&administrators); err != nil {
			return auth.User{}, fmt.Errorf("count active administrators: %w", err)
		}
		if administrators <= 1 {
			return auth.User{}, auth.ErrLastAdministrator
		}
	}

	roleChanged := current.Role != role
	disabledChanged := (current.DisabledAt != nil) != disabled
	var disabledAt *time.Time
	if disabled {
		value := updatedAt
		if current.DisabledAt != nil {
			value = *current.DisabledAt
		}
		disabledAt = &value
	}
	updated, err := scanUser(tx.QueryRow(ctx, `
		UPDATE users
		SET role = $2, disabled_at = $3, updated_at = $4, lock_version = lock_version + 1
		WHERE id = $1 AND lock_version = $5
		RETURNING id, email, normalized_email, password_hash, role, disabled_at, lock_version, created_at, updated_at
	`, userID, role, disabledAt, updatedAt, expectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("update user: %w", err)
	}

	entry.TargetID = &updated.ID
	entry.TargetType = "user"
	entry.Outcome = audit.OutcomeSuccess
	if roleChanged {
		roleEntry := entry
		roleEntry.Action = "account.role_change"
		roleEntry.Details = map[string]any{"previousRole": current.Role, "role": updated.Role}
		if err := appendAudit(ctx, tx, roleEntry); err != nil {
			return auth.User{}, err
		}
	}
	if disabledChanged {
		statusEntry := entry
		statusEntry.Action = "account.enable"
		if disabled {
			statusEntry.Action = "account.disable"
		}
		statusEntry.Details = map[string]any{"disabled": disabled}
		if err := appendAudit(ctx, tx, statusEntry); err != nil {
			return auth.User{}, err
		}
	}
	if !roleChanged && !disabledChanged {
		unchangedEntry := entry
		unchangedEntry.Action = "account.update"
		unchangedEntry.Details = map[string]any{"changed": false}
		if err := appendAudit(ctx, tx, unchangedEntry); err != nil {
			return auth.User{}, err
		}
	}
	if roleChanged || (disabledChanged && disabled) {
		reason := "role_change"
		if disabled {
			reason = "account_disabled"
		}
		if err := revokeUserSessions(ctx, tx, updated.ID, updatedAt, reason, entry); err != nil {
			return auth.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.User{}, fmt.Errorf("commit user update transaction: %w", err)
	}
	return updated, nil
}

func (store *Postgres) ChangePassword(ctx context.Context, userID, passwordHash string, updatedAt time.Time, entry audit.Entry) error {
	return store.updatePassword(ctx, userID, passwordHash, updatedAt, "account.password_change", "password_change", entry)
}

func (store *Postgres) ResetPassword(ctx context.Context, userID, passwordHash string, updatedAt time.Time, entry audit.Entry) error {
	return store.updatePassword(ctx, userID, passwordHash, updatedAt, "account.password_reset", "password_reset", entry)
}

func (store *Postgres) updatePassword(
	ctx context.Context,
	userID, passwordHash string,
	updatedAt time.Time,
	action, reason string,
	entry audit.Entry,
) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin password update transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	command, err := tx.Exec(ctx, `
		UPDATE users
		SET password_hash = $2, updated_at = $3, lock_version = lock_version + 1
		WHERE id = $1
	`, userID, passwordHash, updatedAt)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if command.RowsAffected() != 1 {
		return auth.ErrUserNotFound
	}
	entry.TargetID = &userID
	entry.TargetType = "user"
	entry.Action = action
	entry.Outcome = audit.OutcomeSuccess
	entry.Details = map[string]any{"sessionsRevoked": true}
	if err := appendAudit(ctx, tx, entry); err != nil {
		return err
	}
	if err := revokeUserSessions(ctx, tx, userID, updatedAt, reason, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit password update transaction: %w", err)
	}
	return nil
}

func revokeUserSessions(ctx context.Context, tx pgx.Tx, userID string, revokedAt time.Time, reason string, base audit.Entry) error {
	command, err := tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2)
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID, revokedAt)
	if err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	base.Action = "authentication.sessions_revoke"
	base.TargetType = "user"
	base.TargetID = &userID
	base.Outcome = audit.OutcomeSuccess
	base.Details = map[string]any{"reason": reason, "revokedSessionCount": command.RowsAffected()}
	return appendAudit(ctx, tx, base)
}

type userScanner interface {
	Scan(...any) error
}

func scanUser(row userScanner) (auth.User, error) {
	var user auth.User
	err := row.Scan(
		&user.ID,
		&user.Email,
		&user.NormalizedEmail,
		&user.PasswordHash,
		&user.Role,
		&user.DisabledAt,
		&user.LockVersion,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	return user, err
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505" &&
		postgresError.ConstraintName == "users_normalized_email_key"
}

func (store *Postgres) CreateSession(ctx context.Context, session auth.Session, entry audit.Entry) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin session transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (
			id, token_digest, csrf_token_digest, user_id, created_at,
			last_seen_at, idle_expires_at, absolute_expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $5, $6, $7)
	`,
		session.ID,
		session.TokenDigest,
		session.CSRFTokenDigest,
		session.User.ID,
		session.CreatedAt,
		session.IdleExpiresAt,
		session.AbsoluteExpires,
	); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	if err := appendAudit(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit session transaction: %w", err)
	}
	return nil
}

func (store *Postgres) SessionByTokenDigest(ctx context.Context, digest []byte) (auth.Session, error) {
	var session auth.Session
	err := store.pool.QueryRow(ctx, `
		SELECT
			s.id, s.token_digest, s.csrf_token_digest,
			s.created_at, s.last_seen_at, s.idle_expires_at, s.absolute_expires_at, s.revoked_at,
			u.id, u.email, u.normalized_email, u.role, u.disabled_at, u.lock_version, u.created_at, u.updated_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_digest = $1
	`, digest).Scan(
		&session.ID,
		&session.TokenDigest,
		&session.CSRFTokenDigest,
		&session.CreatedAt,
		&session.LastSeenAt,
		&session.IdleExpiresAt,
		&session.AbsoluteExpires,
		&session.RevokedAt,
		&session.User.ID,
		&session.User.Email,
		&session.User.NormalizedEmail,
		&session.User.Role,
		&session.User.DisabledAt,
		&session.User.LockVersion,
		&session.User.CreatedAt,
		&session.User.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Session{}, auth.ErrSessionNotFound
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("find session: %w", err)
	}
	return session, nil
}

func (store *Postgres) TouchSession(ctx context.Context, sessionID string, lastSeenAt, idleExpiresAt time.Time) error {
	command, err := store.pool.Exec(ctx, `
		UPDATE sessions
		SET last_seen_at = $2, idle_expires_at = LEAST($3, absolute_expires_at)
		WHERE id = $1 AND revoked_at IS NULL
	`, sessionID, lastSeenAt, idleExpiresAt)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	if command.RowsAffected() != 1 {
		return auth.ErrSessionNotFound
	}
	return nil
}

func (store *Postgres) RevokeSession(ctx context.Context, sessionID string, revokedAt time.Time, entry audit.Entry) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin revoke session transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = COALESCE(revoked_at, $2)
		WHERE id = $1
	`, sessionID, revokedAt); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if err := appendAudit(ctx, tx, entry); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit revoke session transaction: %w", err)
	}
	return nil
}

func (store *Postgres) AppendAudit(ctx context.Context, entry audit.Entry) error {
	return appendAudit(ctx, store.pool, entry)
}

type auditExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func appendAudit(ctx context.Context, executor auditExecutor, entry audit.Entry) error {
	details, err := entry.DetailsJSON()
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	if _, err := executor.Exec(ctx, `
		INSERT INTO audit_logs (
			request_id, actor_user_id, action, target_type, target_id,
			source_ip, user_agent, outcome, details
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		entry.RequestID,
		entry.ActorUserID,
		entry.Action,
		entry.TargetType,
		entry.TargetID,
		entry.SourceIP,
		entry.UserAgent,
		entry.Outcome,
		string(details),
	); err != nil {
		return fmt.Errorf("append audit log: %w", err)
	}
	return nil
}
