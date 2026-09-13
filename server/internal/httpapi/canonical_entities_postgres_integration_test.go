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

func TestFigureAndTopicTagManagementHTTPFlowAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "entity-admin@example.com", auth.RoleAdministrator, passwordHash, true, database)
	editor := createIntegrationUser(t, ctx, queryPool, "entity-editor@example.com", auth.RoleEditor, passwordHash, false, database)

	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)
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

	for _, path := range []string{"/api/v1/admin/figures", "/api/v1/admin/topic-tags"} {
		response := performJSON(t, handler, http.MethodGet, path, "", nil)
		if response.Code != http.StatusUnauthorized || readProblem(t, response).Code != "authentication_required" {
			t.Fatalf("reader access to %s: status = %d, body = %s", path, response.Code, response.Body.String())
		}
	}

	editorSession, editorCSRF := loginIntegrationUser(t, handler, editor.Email)
	administratorSession, administratorCSRF := loginIntegrationUser(t, handler, administrator.Email)

	t.Run("create requests reject update-only and missing fields", func(t *testing.T) {
		for _, test := range []struct {
			key  string
			body string
		}{
			{key: "strict-create-status", body: `{"name":"不应创建","disambiguationLabel":null,"status":"inactive"}`},
			{key: "strict-create-missing-label", body: `{"name":"不应创建"}`},
		} {
			response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/figures", test.body, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
				"Idempotency-Key": test.key,
				"X-CSRF-Token":    editorCSRF.Value,
			})
			if response.Code != http.StatusBadRequest || readProblem(t, response).Code != "invalid_request" {
				t.Fatalf("body %s: status = %d, response = %s", test.body, response.Code, response.Body.String())
			}
		}
	})

	createEntity := func(path, key, body string, cookies []*http.Cookie) canonicalEntityResponse {
		t.Helper()
		csrf := cookies[1]
		response := performJSONWithHeaders(t, handler, http.MethodPost, path, body, cookies, map[string]string{
			"Idempotency-Key": key,
			"X-CSRF-Token":    csrf.Value,
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
		var entity canonicalEntityResponse
		if err := json.Unmarshal(response.Body.Bytes(), &entity); err != nil {
			t.Fatal(err)
		}
		if entity.ID[14] != '7' || entity.LockVersion != 1 || entity.Status != "active" {
			t.Fatalf("created entity = %#v", entity)
		}
		return entity
	}

	firstFigure := createEntity(
		"/api/v1/admin/figures",
		"create-liu-che-emperor",
		`{"name":"刘彻","disambiguationLabel":"汉武帝"}`,
		[]*http.Cookie{editorSession, editorCSRF},
	)
	t.Run("idempotent replay returns the original entity and records the replay request", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/figures", `{"name":"刘彻","disambiguationLabel":"汉武帝"}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": "create-liu-che-emperor",
			"X-CSRF-Token":    editorCSRF.Value,
		})
		if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("status = %d, headers = %#v, body = %s", response.Code, response.Header(), response.Body.String())
		}
		var replayed canonicalEntityResponse
		if err := json.Unmarshal(response.Body.Bytes(), &replayed); err != nil {
			t.Fatal(err)
		}
		if replayed.ID != firstFigure.ID {
			t.Fatalf("replayed id = %s, want %s", replayed.ID, firstFigure.ID)
		}
		var auditCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT count(*) FROM audit_logs
			WHERE action = 'historical_figure.create'
			  AND actor_user_id = $1
			  AND details @> '{"replayed": true}'::jsonb
		`, editor.ID).Scan(&auditCount); err != nil {
			t.Fatal(err)
		}
		if auditCount != 1 {
			t.Fatalf("idempotent replay audit count = %d", auditCount)
		}
	})
	t.Run("update requires every OpenAPI field", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/figures/"+firstFigure.ID, `{"name":"刘彻","status":"active"}`, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": administratorCSRF.Value,
		})
		if response.Code != http.StatusBadRequest || readProblem(t, response).Code != "invalid_request" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})
	secondFigure := createEntity(
		"/api/v1/admin/figures",
		"create-liu-che-modern",
		`{"name":"刘彻","disambiguationLabel":"近现代同名人物"}`,
		[]*http.Cookie{editorSession, editorCSRF},
	)
	if firstFigure.ID == secondFigure.ID || firstFigure.Name != secondFigure.Name {
		t.Fatalf("same-name historical figures = %#v and %#v", firstFigure, secondFigure)
	}

	topicTag := createEntity(
		"/api/v1/admin/topic-tags",
		"create-silk-road",
		`{"name":"丝绸之路","disambiguationLabel":null}`,
		[]*http.Cookie{administratorSession, administratorCSRF},
	)

	t.Run("editor searches and selects same-name figures by disambiguation", func(t *testing.T) {
		response := performJSON(t, handler, http.MethodGet, "/api/v1/admin/figures?q=%E8%BF%91%E7%8E%B0%E4%BB%A3&limit=100", "", []*http.Cookie{editorSession})
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var body struct {
			Figures []canonicalEntityResponse `json:"figures"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Figures) != 1 || body.Figures[0].ID != secondFigure.ID {
			t.Fatalf("figures = %#v", body.Figures)
		}
	})

	t.Run("administrator can query topic tags created by administrators", func(t *testing.T) {
		response := performJSON(t, handler, http.MethodGet, "/api/v1/admin/topic-tags?q=%E4%B8%9D%E7%BB%B8", "", []*http.Cookie{administratorSession})
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var body struct {
			TopicTags []canonicalEntityResponse `json:"topicTags"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.TopicTags) != 1 || body.TopicTags[0].ID != topicTag.ID {
			t.Fatalf("topic tags = %#v", body.TopicTags)
		}
	})

	t.Run("editor cannot modify a figure", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/figures/"+firstFigure.ID, `{"name":"刘彻","disambiguationLabel":"西汉皇帝","status":"active"}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusForbidden || readProblem(t, response).Code != "permission_denied" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("administrator update uses optimistic locking", func(t *testing.T) {
		body := `{"name":"刘彻","disambiguationLabel":"西汉汉武帝","status":"inactive"}`
		updated := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/figures/"+firstFigure.ID, body, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": administratorCSRF.Value,
		})
		if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"2"` {
			t.Fatalf("status = %d, headers = %#v, body = %s", updated.Code, updated.Header(), updated.Body.String())
		}

		stale := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/figures/"+firstFigure.ID, body, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match":     `"1"`,
			"X-CSRF-Token": administratorCSRF.Value,
		})
		if stale.Code != http.StatusConflict || readProblem(t, stale).Code != "historical_figure_version_conflict" {
			t.Fatalf("status = %d, body = %s", stale.Code, stale.Body.String())
		}
	})

	var figureCreates, topicTagCreates, figureUpdates int
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'historical_figure.create' AND outcome = 'success' AND actor_user_id = $1`, editor.ID).Scan(&figureCreates); err != nil {
		t.Fatal(err)
	}
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'topic_tag.create' AND outcome = 'success' AND actor_user_id = $1`, administrator.ID).Scan(&topicTagCreates); err != nil {
		t.Fatal(err)
	}
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'historical_figure.update' AND outcome = 'success' AND actor_user_id = $1`, administrator.ID).Scan(&figureUpdates); err != nil {
		t.Fatal(err)
	}
	if figureCreates != 3 || topicTagCreates != 1 || figureUpdates != 1 {
		t.Fatalf("successful entity audits = figure creates %d, topic tag creates %d, figure updates %d", figureCreates, topicTagCreates, figureUpdates)
	}

	t.Run("canonical entity identities are immutable in PostgreSQL", func(t *testing.T) {
		replacementID, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queryPool.Exec(ctx, `UPDATE historical_figures SET id = $2 WHERE id = $1`, firstFigure.ID, replacementID); err == nil {
			t.Fatal("historical figure UUID update unexpectedly succeeded")
		}
		if _, err := queryPool.Exec(ctx, `UPDATE topic_tags SET id = $2 WHERE id = $1`, topicTag.ID, replacementID); err == nil {
			t.Fatal("topic tag UUID update unexpectedly succeeded")
		}
	})
}
