package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestAuthenticationHTTPFlowAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	testDatabaseURL, queryPool := prepareTestDatabase(t, ctx, databaseURL)

	database, err := store.Open(ctx, testDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	passwordParams := auth.DefaultArgon2idParams()
	passwordParams.Memory = 1024
	passwordParams.Iterations = 1
	passwordHash, err := auth.HashPassword("correct horse battery staple", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	administratorID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	administrator := auth.User{
		ID:              administratorID,
		Email:           "admin@example.com",
		NormalizedEmail: "admin@example.com",
		PasswordHash:    passwordHash,
		Role:            auth.RoleAdministrator,
	}
	if err := database.CreateFirstAdministrator(ctx, administrator, audit.Entry{
		RequestID:  "integration-bootstrap",
		Action:     "administrator.bootstrap",
		TargetType: "user",
		Outcome:    audit.OutcomeSuccess,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateFirstAdministrator(ctx, administrator, audit.Entry{
		RequestID:  "integration-bootstrap-again",
		Action:     "administrator.bootstrap",
		TargetType: "user",
		Outcome:    audit.OutcomeSuccess,
	}); !errors.Is(err, auth.ErrAlreadyInitialized) {
		t.Fatalf("second bootstrap error = %v", err)
	}

	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	dummyHash, err := auth.HashPassword("invalid-password-placeholder", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	httpConfig := Config{
		PublicBaseURL:      "https://history.test",
		SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour,
		Now:                func() time.Time { return now },
		DummyPasswordHash:  dummyHash,
	}
	handler := New(database, config.LatestMigrationVersion, logger, httpConfig)

	t.Run("login rejects a cross-origin request", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "https://history.test/api/v1/auth/login", strings.NewReader(`{"email":"admin@example.com","password":"correct horse battery staple"}`))
		request.Header.Set("Origin", "https://evil.example")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || readProblem(t, response).Code != "origin_validation_failed" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("login failure does not disclose whether the email exists", func(t *testing.T) {
		unknown := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"missing@example.com","password":"wrong password"}`, nil)
		wrongPassword := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"admin@example.com","password":"wrong password"}`, nil)
		if unknown.Code != http.StatusUnauthorized || wrongPassword.Code != http.StatusUnauthorized {
			t.Fatalf("statuses = %d and %d", unknown.Code, wrongPassword.Code)
		}
		if readProblem(t, unknown).Code != "invalid_credentials" || readProblem(t, wrongPassword).Code != "invalid_credentials" {
			t.Fatal("login failures did not use the common invalid_credentials response")
		}
	})

	var sessionCookie, csrfCookie *http.Cookie
	t.Run("login creates secure server-side session and current identity", func(t *testing.T) {
		login := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"ADMIN@example.com","password":"correct horse battery staple"}`, nil)
		if login.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", login.Code, login.Body.String())
		}
		for _, cookie := range login.Result().Cookies() {
			switch cookie.Name {
			case "__Host-history_wiki_session":
				sessionCookie = cookie
			case "__Host-history_wiki_csrf":
				csrfCookie = cookie
			}
		}
		if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("invalid session cookie: %#v", sessionCookie)
		}
		if csrfCookie == nil || csrfCookie.HttpOnly || !csrfCookie.Secure || csrfCookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("invalid CSRF cookie: %#v", csrfCookie)
		}

		var storedDigest []byte
		if err := queryPool.QueryRow(ctx, `SELECT token_digest FROM sessions ORDER BY created_at DESC LIMIT 1`).Scan(&storedDigest); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(storedDigest), sessionCookie.Value) || len(storedDigest) != 32 {
			t.Fatalf("session token was not stored as a SHA-256 digest")
		}

		me := performJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", []*http.Cookie{sessionCookie})
		if me.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", me.Code, me.Body.String())
		}
		var body struct {
			User struct {
				Email string    `json:"email"`
				Role  auth.Role `json:"role"`
			} `json:"user"`
		}
		if err := json.Unmarshal(me.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.User.Email != administrator.Email || body.User.Role != auth.RoleAdministrator {
			t.Fatalf("current user = %#v", body.User)
		}
	})

	t.Run("management boundary rejects unauthenticated access", func(t *testing.T) {
		response := performJSON(t, handler, http.MethodGet, "/api/v1/admin", "", nil)
		if response.Code != http.StatusUnauthorized || readProblem(t, response).Code != "authentication_required" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("administrator authorization rejects an editor", func(t *testing.T) {
		editorID, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queryPool.Exec(ctx, `
			INSERT INTO users (id, email, normalized_email, password_hash, role)
			VALUES ($1, 'editor@example.com', 'editor@example.com', $2, 'editor')
		`, editorID, passwordHash); err != nil {
			t.Fatal(err)
		}
		login := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"editor@example.com","password":"correct horse battery staple"}`, nil)
		var editorCookie *http.Cookie
		for _, cookie := range login.Result().Cookies() {
			if cookie.Name == "__Host-history_wiki_session" {
				editorCookie = cookie
			}
		}
		if editorCookie == nil {
			t.Fatalf("editor login status = %d, body = %s", login.Code, login.Body.String())
		}

		securityServer := &Server{authStore: database, logger: logger, config: withConfigDefaults(httpConfig)}
		protected := chi.NewRouter()
		protected.Use(middleware.RequestID)
		protected.Use(securityServer.withRequestActor)
		protected.With(
			securityServer.authenticate,
			securityServer.requireRole(auth.RoleAdministrator),
		).Get("/administrator-only", func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNoContent)
		})
		response := performJSON(t, protected, http.MethodGet, "/administrator-only", "", []*http.Cookie{editorCookie})
		if response.Code != http.StatusForbidden || readProblem(t, response).Code != "permission_denied" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var count int
		if err := queryPool.QueryRow(ctx, `
			SELECT count(*) FROM audit_logs
			WHERE action = 'authorization.denied' AND actor_user_id = $1
		`, editorID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("authorization denial audit count = %d", count)
		}
	})

	t.Run("logout rejects missing and invalid CSRF tokens", func(t *testing.T) {
		missing := performJSON(t, handler, http.MethodPost, "/api/v1/auth/logout", "", []*http.Cookie{sessionCookie, csrfCookie})
		if missing.Code != http.StatusForbidden || readProblem(t, missing).Code != "csrf_validation_failed" {
			t.Fatalf("missing token status = %d, body = %s", missing.Code, missing.Body.String())
		}
		invalid := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/auth/logout", "", []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"X-CSRF-Token": "not-the-cookie-token",
		})
		if invalid.Code != http.StatusForbidden || readProblem(t, invalid).Code != "csrf_validation_failed" {
			t.Fatalf("invalid token status = %d, body = %s", invalid.Code, invalid.Body.String())
		}
	})

	t.Run("logout revokes the session", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/auth/logout", "", []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"X-CSRF-Token": csrfCookie.Value,
		})
		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		me := performJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", []*http.Cookie{sessionCookie})
		if me.Code != http.StatusUnauthorized {
			t.Fatalf("revoked session status = %d", me.Code)
		}
	})

	t.Run("expired session is rejected and audited", func(t *testing.T) {
		login := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"admin@example.com","password":"correct horse battery staple"}`, nil)
		var expiringCookie *http.Cookie
		for _, cookie := range login.Result().Cookies() {
			if cookie.Name == "__Host-history_wiki_session" {
				expiringCookie = cookie
			}
		}
		if expiringCookie == nil {
			t.Fatal("missing session cookie")
		}
		now = now.Add(8*time.Hour + time.Second)
		me := performJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", []*http.Cookie{expiringCookie})
		if me.Code != http.StatusUnauthorized || readProblem(t, me).Code != "session_expired" {
			t.Fatalf("status = %d, body = %s", me.Code, me.Body.String())
		}
		var count int
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'authentication.session_expired'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("session expiration audit count = %d", count)
		}
	})

	t.Run("audit log is append only", func(t *testing.T) {
		if _, err := queryPool.Exec(ctx, `UPDATE audit_logs SET action = 'tampered' WHERE id = (SELECT min(id) FROM audit_logs)`); err == nil {
			t.Fatal("audit log update unexpectedly succeeded")
		}
	})
}

type problemBody struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func readProblem(t *testing.T, response *httptest.ResponseRecorder) problemBody {
	t.Helper()
	var body problemBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func performJSON(t *testing.T, handler http.Handler, method, path, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return performJSONWithHeaders(t, handler, method, path, body, cookies, nil)
}

func performJSONWithHeaders(t *testing.T, handler http.Handler, method, path, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://history.test"+path, strings.NewReader(body))
	request.Header.Set("Origin", "https://history.test")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func prepareTestDatabase(t *testing.T, ctx context.Context, databaseURL string) (string, *pgxpool.Pool) {
	t.Helper()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	id, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "history_wiki_test_" + strings.ReplaceAll(id, "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	testDatabaseURL := parsed.String()

	sqlDatabase, err := sql.Open("pgx", testDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDatabase.Close()
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(sqlDatabase, "../../migrations"); err != nil {
		t.Fatal(err)
	}

	queryPool, err := pgxpool.New(ctx, testDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queryPool.Close)
	if err := queryPool.Ping(ctx); err != nil {
		t.Fatal(fmt.Errorf("ping isolated PostgreSQL schema: %w", err))
	}
	return testDatabaseURL, queryPool
}
