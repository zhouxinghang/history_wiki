package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestEventDraftManagementHTTPFlowAgainstPostgres(t *testing.T) {
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
	editor := createIntegrationUser(t, ctx, queryPool, "event-editor@example.com", auth.RoleEditor, passwordHash, true, database)
	now := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	dummyHash, err := auth.HashPassword("invalid-password-placeholder", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL: "https://history.test", SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour, Now: func() time.Time { return now }, DummyPasswordHash: dummyHash,
	})

	unauthenticated := performJSON(t, handler, http.MethodGet, "/api/v1/admin/events", "", nil)
	if unauthenticated.Code != http.StatusUnauthorized || readProblem(t, unauthenticated).Code != "authentication_required" {
		t.Fatalf("unauthenticated status = %d, body = %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	sessionCookie, csrfCookie := loginIntegrationUser(t, handler, editor.Email)
	ids := createEventAssociationFixtures(t, ctx, queryPool, editor.ID, now)
	body := fmt.Sprintf(`{
		"slug":"qin-unification","title":"秦统一六国","summary":"秦结束战国割据局面。","narrative":"完整叙述。",
		"time":{"kind":"exact-date","date":{"era":"BCE","year":221,"month":10,"day":1}},
		"primaryCategory":"政治","prominence":1,"displayOrder":20,
		"regionIds":[%q],"placeIds":[%q],"periodIds":[%q],"figureIds":[%q],"topicTagIds":[%q]
	}`, ids.region, ids.place, ids.period, ids.figure, ids.topicTag)
	createdResponse := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events", body, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
		"Idempotency-Key": "create-qin-unification", "X-CSRF-Token": csrfCookie.Value,
	})
	if createdResponse.Code != http.StatusCreated || createdResponse.Header().Get("ETag") != `"draft-1"` {
		t.Fatalf("create status = %d, headers = %#v, body = %s", createdResponse.Code, createdResponse.Header(), createdResponse.Body.String())
	}
	var created managedEventResponse
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID[14] != '7' || created.Slug != "qin-unification" || created.Draft == nil ||
		len(created.Draft.Regions) != 1 || len(created.Draft.Places) != 1 || len(created.Draft.Periods) != 1 ||
		len(created.Draft.Figures) != 1 || len(created.Draft.TopicTags) != 1 {
		t.Fatalf("created event = %#v", created)
	}

	var coordinate float64
	if err := queryPool.QueryRow(ctx, `SELECT start_coordinate FROM event_drafts WHERE event_id = $1`, created.ID).Scan(&coordinate); err != nil {
		t.Fatal(err)
	}
	wantCoordinate := -220.0 + 274.0/366.0
	if math.Abs(coordinate-wantCoordinate) > 1e-12 {
		t.Fatalf("stored coordinate = %.15f, want %.15f", coordinate, wantCoordinate)
	}

	t.Run("idempotent retry returns the same immutable UUIDv7", func(t *testing.T) {
		replayed := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events", body, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"Idempotency-Key": "create-qin-unification", "X-CSRF-Token": csrfCookie.Value,
		})
		if replayed.Code != http.StatusCreated || replayed.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("replay status = %d, headers = %#v, body = %s", replayed.Code, replayed.Header(), replayed.Body.String())
		}
		var replayedEvent managedEventResponse
		if err := json.Unmarshal(replayed.Body.Bytes(), &replayedEvent); err != nil {
			t.Fatal(err)
		}
		if replayedEvent.ID != created.ID {
			t.Fatalf("replayed id = %s, want %s", replayedEvent.ID, created.ID)
		}
	})

	t.Run("incomplete drafts are allowed", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events", `{"slug":"unfinished-event"}`, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"Idempotency-Key": "create-unfinished", "X-CSRF-Token": csrfCookie.Value,
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("incomplete draft status = %d, body = %s", response.Code, response.Body.String())
		}
		var unfinished managedEventResponse
		if err := json.Unmarshal(response.Body.Bytes(), &unfinished); err != nil {
			t.Fatal(err)
		}
		if unfinished.Draft == nil || unfinished.Draft.Time != nil || unfinished.Draft.PrimaryCategory != nil || len(unfinished.Draft.Regions) != 0 {
			t.Fatalf("incomplete draft = %#v", unfinished.Draft)
		}
	})

	t.Run("duplicate slug is rejected", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events", `{"slug":"qin-unification"}`, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"Idempotency-Key": "duplicate-slug", "X-CSRF-Token": csrfCookie.Value,
		})
		if response.Code != http.StatusConflict || readProblem(t, response).Code != "event_slug_conflict" {
			t.Fatalf("duplicate slug status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("draft updates use optimistic locking without overwriting", func(t *testing.T) {
		updatedBody := fmt.Sprintf(`{
			"title":"秦统一六国（修订）","summary":"新摘要","narrative":"新正文",
			"time":{"kind":"interval","start":{"era":"BCE","year":230,"circa":true},"end":{"era":"BCE","year":221}},
			"primaryCategory":"政治","prominence":2,"displayOrder":10,
			"regionIds":[%q],"placeIds":[],"periodIds":[%q],"figureIds":[%q],"topicTagIds":[%q]
		}`, ids.region, ids.period, ids.figure, ids.topicTag)
		updatedResponse := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/events/"+created.ID+"/draft", updatedBody, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"If-Match": `"draft-1"`, "X-CSRF-Token": csrfCookie.Value,
		})
		if updatedResponse.Code != http.StatusOK || updatedResponse.Header().Get("ETag") != `"draft-2"` {
			t.Fatalf("update status = %d, headers = %#v, body = %s", updatedResponse.Code, updatedResponse.Header(), updatedResponse.Body.String())
		}

		stale := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/events/"+created.ID+"/draft", `{"title":"陈旧覆盖"}`, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"If-Match": `"draft-1"`, "X-CSRF-Token": csrfCookie.Value,
		})
		if stale.Code != http.StatusConflict || stale.Header().Get("ETag") != `"draft-2"` || readProblem(t, stale).Code != "event_draft_version_conflict" {
			t.Fatalf("stale update status = %d, headers = %#v, body = %s", stale.Code, stale.Header(), stale.Body.String())
		}
		var storedTitle string
		if err := queryPool.QueryRow(ctx, `SELECT title FROM event_drafts WHERE event_id = $1`, created.ID).Scan(&storedTitle); err != nil {
			t.Fatal(err)
		}
		if storedTitle != "秦统一六国（修订）" {
			t.Fatalf("stored title = %q", storedTitle)
		}
	})

	t.Run("invalid dates, reversed intervals, and inactive associations are rejected", func(t *testing.T) {
		invalidBodies := []string{
			`{"time":{"kind":"exact-date","date":{"era":"CE","year":1900,"month":2,"day":29}}}`,
			`{"time":{"kind":"year","year":{"era":"CE","year":0}}}`,
			`{"time":{"kind":"interval","start":{"era":"CE","year":2},"end":{"era":"CE","year":1}}}`,
			fmt.Sprintf(`{"regionIds":[%q]}`, ids.inactiveRegion),
		}
		for _, invalidBody := range invalidBodies {
			response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/events/"+created.ID+"/draft", invalidBody, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
				"If-Match": `"draft-2"`, "X-CSRF-Token": csrfCookie.Value,
			})
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid body %s: status = %d, body = %s", invalidBody, response.Code, response.Body.String())
			}
		}
	})

	t.Run("slug updates use an independent optimistic lock", func(t *testing.T) {
		updated := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/events/"+created.ID, `{"slug":"qin-empire-unification"}`, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"If-Match": `"event-1"`, "X-CSRF-Token": csrfCookie.Value,
		})
		if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"event-2"` {
			t.Fatalf("slug update status = %d, headers = %#v, body = %s", updated.Code, updated.Header(), updated.Body.String())
		}
		stale := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/events/"+created.ID, `{"slug":"stale-slug"}`, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{
			"If-Match": `"event-1"`, "X-CSRF-Token": csrfCookie.Value,
		})
		if stale.Code != http.StatusConflict || stale.Header().Get("ETag") != `"event-2"` || readProblem(t, stale).Code != "event_version_conflict" {
			t.Fatalf("stale slug status = %d, headers = %#v, body = %s", stale.Code, stale.Header(), stale.Body.String())
		}
	})

	t.Run("database protects event and relationship identity and one active draft", func(t *testing.T) {
		replacementID, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queryPool.Exec(ctx, `UPDATE events SET id = $2 WHERE id = $1`, created.ID, replacementID); err == nil {
			t.Fatal("event UUID update unexpectedly succeeded")
		}
		if _, err := queryPool.Exec(ctx, `
			UPDATE event_draft_regions SET region_id = $2 WHERE event_id = $1 AND region_id = $3
		`, created.ID, ids.inactiveRegion, ids.region); err == nil {
			t.Fatal("event relationship identity update unexpectedly succeeded")
		}
		if _, err := queryPool.Exec(ctx, `
			INSERT INTO event_drafts (event_id, created_by, updated_by) VALUES ($1, $2, $2)
		`, created.ID, editor.ID); err == nil {
			t.Fatal("second active draft unexpectedly succeeded")
		}
	})

	list := performJSON(t, handler, http.MethodGet, "/api/v1/admin/events", "", []*http.Cookie{sessionCookie})
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listed managedEventListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Events) != 2 {
		t.Fatalf("listed events = %d, want 2", len(listed.Events))
	}

	var successfulAudits int
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_type = 'event' AND outcome = 'success'`).Scan(&successfulAudits); err != nil {
		t.Fatal(err)
	}
	if successfulAudits != 5 {
		t.Fatalf("successful event audits = %d, want 5", successfulAudits)
	}
}

type eventFixtureIDs struct {
	region, inactiveRegion, place, period, figure, topicTag string
}

func createEventAssociationFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, now time.Time) eventFixtureIDs {
	t.Helper()
	ids := eventFixtureIDs{}
	for _, target := range []*string{&ids.region, &ids.inactiveRegion, &ids.place, &ids.period, &ids.figure, &ids.topicTag} {
		id, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		*target = id
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO regions (id, name, status, created_by, updated_by, created_at, updated_at)
		VALUES ($1, '东亚', 'active', $3, $3, $4, $4), ($2, '停用地区', 'inactive', $3, $3, $4, $4)
	`, ids.region, ids.inactiveRegion, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO places (id, name, status, created_by, updated_by, created_at, updated_at) VALUES ($1, '咸阳', 'active', $2, $2, $3, $3)`, ids.place, userID, now); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO historical_periods (id, name, status, created_by, updated_by, created_at, updated_at) VALUES ($1, '战国', 'active', $2, $2, $3, $3)`, ids.period, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO period_regions (historical_period_id, region_id) VALUES ($1, $2)`, ids.period, ids.region); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO historical_figures (id, name, status, created_by, updated_by, created_at, updated_at) VALUES ($1, '秦始皇', 'active', $2, $2, $3, $3)`, ids.figure, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO topic_tags (id, name, status, created_by, updated_by, created_at, updated_at) VALUES ($1, '统一', 'active', $2, $2, $3, $3)`, ids.topicTag, userID, now); err != nil {
		t.Fatal(err)
	}
	return ids
}
