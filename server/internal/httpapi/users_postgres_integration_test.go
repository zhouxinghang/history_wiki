package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestUserManagementHTTPFlowAgainstPostgres(t *testing.T) {
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

	params := auth.DefaultArgon2idParams()
	params.Memory = 1024
	params.Iterations = 1
	passwordHash, err := auth.HashPassword("correct horse battery staple", params)
	if err != nil {
		t.Fatal(err)
	}
	administrator := createIntegrationUser(t, ctx, queryPool, "administrator@example.com", auth.RoleAdministrator, passwordHash, true, database)
	dummyHash, err := auth.HashPassword("invalid-password-placeholder", params)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL:     "https://history.test",
		Now:               func() time.Time { return now },
		DummyPasswordHash: dummyHash,
		PasswordParams:    params,
	})
	adminSession, adminCSRF := loginIntegrationUser(t, handler, administrator.Email)

	t.Run("editor cannot use administrator account endpoints", func(t *testing.T) {
		created := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/users", `{
			"email":"editor@example.com",
			"password":"correct horse battery staple",
			"role":"editor"
		}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{"X-CSRF-Token": adminCSRF.Value})
		if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` {
			t.Fatalf("create status = %d, headers = %#v, body = %s", created.Code, created.Header(), created.Body.String())
		}
		var body managedUserResponse
		if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.LockVersion != 1 {
			t.Fatalf("created user lock version = %d", body.LockVersion)
		}
		editorSession, editorCSRF := loginIntegrationUser(t, handler, "editor@example.com")
		for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/users/" + administrator.ID} {
			method := http.MethodGet
			if path != "/api/v1/admin/users" {
				method = http.MethodPatch
			}
			body := ""
			cookies := []*http.Cookie{editorSession}
			headers := map[string]string(nil)
			if method == http.MethodPatch {
				body = `{"role":"administrator","disabled":false}`
				cookies = append(cookies, editorCSRF)
				headers = map[string]string{"X-CSRF-Token": editorCSRF.Value}
			}
			response := performJSONWithHeaders(t, handler, method, path, body, cookies, headers)
			if response.Code != http.StatusForbidden || readProblem(t, response).Code != "permission_denied" {
				t.Fatalf("%s %s status = %d, body = %s", method, path, response.Code, response.Body.String())
			}
		}
	})

	var editor managedUserResponse
	t.Run("administrator lists created accounts", func(t *testing.T) {
		response := performJSON(t, handler, http.MethodGet, "/api/v1/admin/users", "", []*http.Cookie{adminSession})
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var body userListResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Users) != 2 {
			t.Fatalf("users = %#v", body.Users)
		}
		for _, user := range body.Users {
			if user.LockVersion != 1 {
				t.Fatalf("initial user lock version = %d for %s", user.LockVersion, user.Email)
			}
			if user.Email == "editor@example.com" {
				editor = user
			}
		}
		if editor.ID == "" {
			t.Fatal("created editor missing from account list")
		}
	})

	t.Run("last active administrator cannot be disabled or demoted", func(t *testing.T) {
		for _, body := range []string{
			`{"role":"administrator","disabled":true}`,
			`{"role":"editor","disabled":false}`,
		} {
			response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/users/"+administrator.ID, body, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
				"If-Match":     `"1"`,
				"X-CSRF-Token": adminCSRF.Value,
			})
			if response.Code != http.StatusConflict || readProblem(t, response).Code != "last_administrator_required" {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		}
	})

	t.Run("account updates require and enforce the latest version", func(t *testing.T) {
		created := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/users", `{
			"email":"concurrent@example.com",
			"password":"correct horse battery staple",
			"role":"editor"
		}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{"X-CSRF-Token": adminCSRF.Value})
		if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` {
			t.Fatalf("create status = %d, headers = %#v, body = %s", created.Code, created.Header(), created.Body.String())
		}
		var concurrentUser managedUserResponse
		if err := json.Unmarshal(created.Body.Bytes(), &concurrentUser); err != nil {
			t.Fatal(err)
		}

		missingVersion := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/users/"+concurrentUser.ID, `{"role":"editor","disabled":true}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value,
		})
		if missingVersion.Code != http.StatusPreconditionRequired || readProblem(t, missingVersion).Code != "precondition_required" {
			t.Fatalf("missing version status = %d, body = %s", missingVersion.Code, missingVersion.Body.String())
		}

		disabled := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/users/"+concurrentUser.ID, `{"role":"editor","disabled":true}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": adminCSRF.Value,
		})
		if disabled.Code != http.StatusOK || disabled.Header().Get("ETag") != `"2"` {
			t.Fatalf("disable status = %d, headers = %#v, body = %s", disabled.Code, disabled.Header(), disabled.Body.String())
		}
		var disabledUser managedUserResponse
		if err := json.Unmarshal(disabled.Body.Bytes(), &disabledUser); err != nil {
			t.Fatal(err)
		}
		if disabledUser.LockVersion != 2 || disabledUser.DisabledAt == nil {
			t.Fatalf("disabled user = %#v", disabledUser)
		}

		stale := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/users/"+concurrentUser.ID, `{"role":"administrator","disabled":false}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": adminCSRF.Value,
		})
		if stale.Code != http.StatusConflict || readProblem(t, stale).Code != "user_version_conflict" {
			t.Fatalf("stale status = %d, body = %s", stale.Code, stale.Body.String())
		}

		var disabledAt *time.Time
		var role auth.Role
		var lockVersion int64
		if err := queryPool.QueryRow(ctx, `SELECT role, disabled_at, lock_version FROM users WHERE id = $1`, concurrentUser.ID).Scan(&role, &disabledAt, &lockVersion); err != nil {
			t.Fatal(err)
		}
		if role != auth.RoleEditor || disabledAt == nil || lockVersion != 2 {
			t.Fatalf("stored stale-write target = role %q, disabled_at %v, lock_version %d", role, disabledAt, lockVersion)
		}

		var conflictAudits int
		if err := queryPool.QueryRow(ctx, `
			SELECT count(*) FROM audit_logs
			WHERE action = 'account.update'
			  AND outcome = 'failure'
			  AND target_id = $1
			  AND details->>'reason' = 'version_conflict'
		`, concurrentUser.ID).Scan(&conflictAudits); err != nil {
			t.Fatal(err)
		}
		if conflictAudits != 1 {
			t.Fatalf("version conflict audit count = %d", conflictAudits)
		}
	})

	t.Run("own password change revokes every session and requires the new password", func(t *testing.T) {
		firstSession, csrf := loginIntegrationUser(t, handler, editor.Email)
		secondSession, _ := loginIntegrationUser(t, handler, editor.Email)
		changed := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/auth/change-password", `{
			"currentPassword":"correct horse battery staple",
			"newPassword":"new correct horse battery staple"
		}`, []*http.Cookie{firstSession, csrf}, map[string]string{"X-CSRF-Token": csrf.Value})
		if changed.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", changed.Code, changed.Body.String())
		}
		editor.LockVersion++
		for _, session := range []*http.Cookie{firstSession, secondSession} {
			me := performJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", []*http.Cookie{session})
			if me.Code != http.StatusUnauthorized {
				t.Fatalf("revoked session status = %d", me.Code)
			}
		}
		oldLogin := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"editor@example.com","password":"correct horse battery staple"}`, nil)
		newLogin := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"editor@example.com","password":"new correct horse battery staple"}`, nil)
		if oldLogin.Code != http.StatusUnauthorized || newLogin.Code != http.StatusOK {
			t.Fatalf("old/new login statuses = %d/%d", oldLogin.Code, newLogin.Code)
		}
	})

	t.Run("administrator reset and disable immediately revoke target sessions", func(t *testing.T) {
		login := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"editor@example.com","password":"new correct horse battery staple"}`, nil)
		editorSession, editorCSRF := responseCookies(t, login)
		reset := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/users/"+editor.ID+"/reset-password", `{"password":"temporary horse battery staple"}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{"X-CSRF-Token": adminCSRF.Value})
		if reset.Code != http.StatusNoContent {
			t.Fatalf("reset status = %d, body = %s", reset.Code, reset.Body.String())
		}
		editor.LockVersion++
		if me := performJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", []*http.Cookie{editorSession}); me.Code != http.StatusUnauthorized {
			t.Fatalf("reset session status = %d", me.Code)
		}

		login = performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"editor@example.com","password":"temporary horse battery staple"}`, nil)
		editorSession, editorCSRF = responseCookies(t, login)
		promoted := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/users/"+editor.ID, `{"role":"administrator","disabled":false}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"If-Match":     canonicalEntityETag(editor.LockVersion),
			"X-CSRF-Token": adminCSRF.Value,
		})
		if promoted.Code != http.StatusOK || promoted.Header().Get("ETag") != canonicalEntityETag(editor.LockVersion+1) {
			t.Fatalf("promote status = %d, headers = %#v, body = %s", promoted.Code, promoted.Header(), promoted.Body.String())
		}
		editor.LockVersion++
		if me := performJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", []*http.Cookie{editorSession, editorCSRF}); me.Code != http.StatusUnauthorized {
			t.Fatalf("role-change session status = %d", me.Code)
		}
		login = performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"editor@example.com","password":"temporary horse battery staple"}`, nil)
		editorSession, editorCSRF = responseCookies(t, login)
		disabled := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/users/"+editor.ID, `{"role":"administrator","disabled":true}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"If-Match":     canonicalEntityETag(editor.LockVersion),
			"X-CSRF-Token": adminCSRF.Value,
		})
		if disabled.Code != http.StatusOK || disabled.Header().Get("ETag") != canonicalEntityETag(editor.LockVersion+1) {
			t.Fatalf("disable status = %d, headers = %#v, body = %s", disabled.Code, disabled.Header(), disabled.Body.String())
		}
		editor.LockVersion++
		if me := performJSON(t, handler, http.MethodGet, "/api/v1/auth/me", "", []*http.Cookie{editorSession, editorCSRF}); me.Code != http.StatusUnauthorized {
			t.Fatalf("disabled session status = %d", me.Code)
		}
		if login := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"editor@example.com","password":"temporary horse battery staple"}`, nil); login.Code != http.StatusUnauthorized {
			t.Fatalf("disabled login status = %d", login.Code)
		}
	})

	t.Run("password verification failures are audited without credentials", func(t *testing.T) {
		if _, err := queryPool.Exec(ctx, `UPDATE users SET password_hash = '$argon2id$malformed' WHERE id = $1`, administrator.ID); err != nil {
			t.Fatal(err)
		}
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/auth/change-password", `{
			"currentPassword":"do not audit this password",
			"newPassword":"nor this replacement password"
		}`, []*http.Cookie{adminSession, adminCSRF}, map[string]string{"X-CSRF-Token": adminCSRF.Value})
		if response.Code != http.StatusServiceUnavailable || readProblem(t, response).Code != "authentication_unavailable" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var reason string
		var containsUnexpectedFields bool
		if err := queryPool.QueryRow(ctx, `
			SELECT
				details->>'reason',
				details ?| ARRAY['currentPassword', 'newPassword', 'password', 'passwordHash', 'session', 'csrfToken']
			FROM audit_logs
			WHERE action = 'account.password_change'
			  AND outcome = 'failure'
			  AND target_id = $1
			ORDER BY id DESC LIMIT 1
		`, administrator.ID).Scan(&reason, &containsUnexpectedFields); err != nil {
			t.Fatal(err)
		}
		if reason != "password_verification_failed" || containsUnexpectedFields {
			t.Fatalf("password verification audit reason = %q, contains credentials = %t", reason, containsUnexpectedFields)
		}
	})

	t.Run("account, password, permission, and session revocation actions are audited", func(t *testing.T) {
		var count int
		if err := queryPool.QueryRow(ctx, `
			SELECT count(*) FROM audit_logs
			WHERE action IN (
				'account.create', 'account.disable', 'account.role_change', 'account.password_change',
				'account.password_reset', 'authentication.sessions_revoke'
			)
		`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count < 9 {
			t.Fatalf("account audit count = %d", count)
		}
	})
}

func responseCookies(t *testing.T, response interface {
	Result() *http.Response
}) (*http.Cookie, *http.Cookie) {
	t.Helper()
	if response.Result().StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.Result().StatusCode)
	}
	var session, csrf *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case "__Host-history_wiki_session":
			session = cookie
		case "__Host-history_wiki_csrf":
			csrf = cookie
		}
	}
	if session == nil || csrf == nil {
		t.Fatal("login response did not set session and CSRF cookies")
	}
	return session, csrf
}
