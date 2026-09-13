package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

type Role string

const (
	RoleEditor        Role = "editor"
	RoleAdministrator Role = "administrator"
)

var (
	ErrInvalidEmail        = errors.New("invalid email address")
	ErrUserNotFound        = errors.New("user not found")
	ErrEmailAlreadyExists  = errors.New("email address already exists")
	ErrLastAdministrator   = errors.New("at least one active administrator is required")
	ErrUserVersionConflict = errors.New("user version conflict")
	ErrSessionNotFound     = errors.New("session not found")
	ErrAlreadyInitialized  = errors.New("an administrator or editor already exists")
)

type User struct {
	ID              string
	Email           string
	NormalizedEmail string
	PasswordHash    string
	Role            Role
	DisabledAt      *time.Time
	LockVersion     int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Session struct {
	ID              string
	TokenDigest     []byte
	CSRFTokenDigest []byte
	User            User
	CreatedAt       time.Time
	LastSeenAt      time.Time
	IdleExpiresAt   time.Time
	AbsoluteExpires time.Time
	RevokedAt       *time.Time
}

func NormalizeEmail(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 320 {
		return "", ErrInvalidEmail
	}
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil || parsed.Address != trimmed {
		return "", ErrInvalidEmail
	}
	return strings.ToLower(trimmed), nil
}

func (session Session) ActiveAt(now time.Time) bool {
	return session.RevokedAt == nil &&
		session.User.DisabledAt == nil &&
		now.Before(session.IdleExpiresAt) &&
		now.Before(session.AbsoluteExpires)
}

func (role Role) Valid() bool {
	return role == RoleEditor || role == RoleAdministrator
}

func (role Role) String() string {
	return string(role)
}

func (user User) Validate() error {
	if user.ID == "" || user.Email == "" || user.NormalizedEmail == "" || user.PasswordHash == "" {
		return errors.New("user is incomplete")
	}
	if !user.Role.Valid() {
		return fmt.Errorf("invalid role %q", user.Role)
	}
	return nil
}
