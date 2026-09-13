package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhouxinghang/history_wiki/server/internal/audit"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestRegionManagementHTTPFlowAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "administrator@example.com", auth.RoleAdministrator, passwordHash, true, database)
	createIntegrationUser(t, ctx, queryPool, "editor@example.com", auth.RoleEditor, passwordHash, false, database)

	now := time.Date(2026, 9, 12, 14, 0, 0, 0, time.UTC)
	dummyHash, err := auth.HashPassword("invalid-password-placeholder", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL:      "https://history.test",
		SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour,
		Now:                func() time.Time { return now },
		DummyPasswordHash:  dummyHash,
	})

	t.Run("ordinary readers cannot use region management APIs", func(t *testing.T) {
		response := performJSON(t, handler, http.MethodGet, "/api/v1/admin/regions", "", nil)
		if response.Code != http.StatusUnauthorized || readProblem(t, response).Code != "authentication_required" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	editorSession, editorCSRF := loginIntegrationUser(t, handler, "editor@example.com")
	administratorSession, administratorCSRF := loginIntegrationUser(t, handler, administrator.Email)

	create := func(key, body string) *regionResponse {
		t.Helper()
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/regions", body, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": key,
			"X-CSRF-Token":    editorCSRF.Value,
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
		}
		var region regionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &region); err != nil {
			t.Fatal(err)
		}
		return &region
	}

	first := create("create-congo-republic", `{"name":"刚果","disambiguationLabel":"刚果共和国"}`)
	second := create("create-congo-democratic-republic", `{"name":"刚果","disambiguationLabel":"刚果民主共和国"}`)
	if first.ID == second.ID || first.Name != second.Name {
		t.Fatalf("same-name regions = %#v and %#v", first, second)
	}
	if first.ID[14] != '7' || first.LockVersion != 1 || first.Status != "active" {
		t.Fatalf("created region = %#v", first)
	}

	t.Run("same idempotency key replays the original result", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/regions", `{"name":"刚果","disambiguationLabel":"刚果共和国"}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": "create-congo-republic",
			"X-CSRF-Token":    editorCSRF.Value,
		})
		if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("status = %d, headers = %#v, body = %s", response.Code, response.Header(), response.Body.String())
		}
		var replayed regionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &replayed); err != nil {
			t.Fatal(err)
		}
		if replayed.ID != first.ID {
			t.Fatalf("replayed id = %q, want %q", replayed.ID, first.ID)
		}
	})

	t.Run("same idempotency key with another payload conflicts", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/regions", `{"name":"东亚","disambiguationLabel":null}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": "create-congo-republic",
			"X-CSRF-Token":    editorCSRF.Value,
		})
		if response.Code != http.StatusConflict || readProblem(t, response).Code != "idempotency_conflict" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("editor can search regions by disambiguation label", func(t *testing.T) {
		response := performJSON(t, handler, http.MethodGet, "/api/v1/admin/regions?q=%E6%B0%91%E4%B8%BB&limit=100", "", []*http.Cookie{editorSession})
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var body regionListResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Regions) != 1 || body.Regions[0].ID != second.ID {
			t.Fatalf("regions = %#v", body.Regions)
		}
	})

	t.Run("editor cannot modify a region", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/regions/"+first.ID, `{"name":"刚果共和国","disambiguationLabel":null,"status":"active"}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusForbidden || readProblem(t, response).Code != "permission_denied" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("administrator update uses optimistic locking", func(t *testing.T) {
		body := `{"name":"刚果（布）","disambiguationLabel":"刚果共和国","status":"inactive"}`
		updated := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/regions/"+first.ID, body, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": administratorCSRF.Value,
		})
		if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
			t.Fatalf("status = %d, headers = %#v, body = %s", updated.Code, updated.Header(), updated.Body.String())
		}

		stale := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/regions/"+first.ID, body, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": administratorCSRF.Value,
		})
		if stale.Code != http.StatusConflict || readProblem(t, stale).Code != "region_version_conflict" {
			t.Fatalf("status = %d, body = %s", stale.Code, stale.Body.String())
		}

		var name, status string
		var lockVersion int64
		if err := queryPool.QueryRow(ctx, `SELECT name, status, lock_version FROM regions WHERE id = $1`, first.ID).Scan(&name, &status, &lockVersion); err != nil {
			t.Fatal(err)
		}
		if name != "刚果（布）" || status != "inactive" || lockVersion != 2 {
			t.Fatalf("stored region = %q, %q, %d", name, status, lockVersion)
		}
	})

	var successfulCreates, successfulUpdates int
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'region.create' AND outcome = 'success'`).Scan(&successfulCreates); err != nil {
		t.Fatal(err)
	}
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'region.update' AND outcome = 'success'`).Scan(&successfulUpdates); err != nil {
		t.Fatal(err)
	}
	if successfulCreates != 2 || successfulUpdates != 1 {
		t.Fatalf("successful region audits = create %d, update %d", successfulCreates, successfulUpdates)
	}

	t.Run("region identity is immutable in PostgreSQL", func(t *testing.T) {
		replacementID, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queryPool.Exec(ctx, `UPDATE regions SET id = $2 WHERE id = $1`, first.ID, replacementID); err == nil {
			t.Fatal("region UUID update unexpectedly succeeded")
		}
	})
}

func createIntegrationUser(
	t *testing.T,
	ctx context.Context,
	queryPool *pgxpool.Pool,
	email string,
	role auth.Role,
	passwordHash string,
	first bool,
	database *store.Postgres,
) auth.User {
	t.Helper()
	userID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	user := auth.User{ID: userID, Email: email, NormalizedEmail: strings.ToLower(email), PasswordHash: passwordHash, Role: role}
	if first {
		if err := database.CreateFirstAdministrator(ctx, user, audit.Entry{
			RequestID: "region-integration-bootstrap", Action: "administrator.bootstrap", TargetType: "user", Outcome: audit.OutcomeSuccess,
		}); err != nil {
			t.Fatal(err)
		}
		return user
	}
	if _, err := queryPool.Exec(ctx, `
		INSERT INTO users (id, email, normalized_email, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
	`, user.ID, user.Email, user.NormalizedEmail, user.PasswordHash, user.Role); err != nil {
		t.Fatal(err)
	}
	return user
}

func loginIntegrationUser(t *testing.T, handler http.Handler, email string) (*http.Cookie, *http.Cookie) {
	t.Helper()
	response := performJSON(t, handler, http.MethodPost, "/api/v1/auth/login", `{"email":"`+email+`","password":"correct horse battery staple"}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
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
		t.Fatal("login did not return both session and CSRF cookies")
	}
	return session, csrf
}
