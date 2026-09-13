package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestFirstEventPublicationAndPublicReadingAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "publisher@example.com", auth.RoleAdministrator, passwordHash, true, database)
	editor := createIntegrationUser(t, ctx, queryPool, "publication-editor@example.com", auth.RoleEditor, passwordHash, false, database)
	now := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	dummyHash, err := auth.HashPassword("invalid-password-placeholder", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL: "https://history.test", SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour, Now: func() time.Time { return now }, DummyPasswordHash: dummyHash,
	})
	adminSession, adminCSRF := loginIntegrationUser(t, handler, administrator.Email)
	editorSession, editorCSRF := loginIntegrationUser(t, handler, editor.Email)
	ids := createEventAssociationFixtures(t, ctx, queryPool, editor.ID, now)
	secondPlaceID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queryPool.Exec(ctx, `
		INSERT INTO places (id, name, disambiguation_label, status, created_by, updated_by, created_at, updated_at)
		VALUES ($1, '雍城', '秦都', 'active', $2, $2, $3, $3)
	`, secondPlaceID, editor.ID, now); err != nil {
		t.Fatal(err)
	}

	completeBody := fmt.Sprintf(`{
		"slug":"qin-unification-public","title":"秦统一六国","summary":"秦结束战国割据局面。","narrative":"完整叙述。",
		"time":{"kind":"year","year":{"era":"BCE","year":221}},
		"primaryCategory":"政治","prominence":1,"displayOrder":20,
		"regionIds":[%q],"placeIds":[%q,%q],"periodIds":[%q],"figureIds":[%q],"topicTagIds":[%q]
	}`, ids.region, ids.place, secondPlaceID, ids.period, ids.figure, ids.topicTag)
	created := createPublicationDraft(t, handler, completeBody, "publication-complete", editorSession, editorCSRF)
	incomplete := createPublicationDraft(t, handler, `{"slug":"publication-incomplete"}`, "publication-incomplete", editorSession, editorCSRF)

	t.Run("unpublished events are absent from public list and detail", func(t *testing.T) {
		list := performJSON(t, handler, http.MethodGet, "/api/v1/events?from=-300&to=-100", "", nil)
		if list.Code != http.StatusOK {
			t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
		}
		var body publishedEventQueryResponse
		if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.SourceTotal != 0 || len(body.Events) != 0 {
			t.Fatalf("unpublished public result = %#v", body)
		}
		detail := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+created.ID, "", nil)
		if detail.Code != http.StatusNotFound || readProblem(t, detail).Code != "event_not_found" {
			t.Fatalf("unpublished detail status = %d, body = %s", detail.Code, detail.Body.String())
		}
	})

	t.Run("only administrators may publish complete drafts", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/publish", "", []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"If-Match": `"draft-1"`, "X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusForbidden {
			t.Fatalf("editor publish status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("publication rejects incomplete drafts without changing them", func(t *testing.T) {
		response := publishDraft(t, handler, incomplete.ID, adminSession, adminCSRF)
		if response.Code != http.StatusUnprocessableEntity || readProblem(t, response).Code != "incomplete_event_draft" {
			t.Fatalf("incomplete publish status = %d, body = %s", response.Code, response.Body.String())
		}
		var status string
		var draftCount, revisionCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT publication_status::text,
				(SELECT count(*) FROM event_drafts WHERE event_id = events.id),
				(SELECT count(*) FROM event_revisions WHERE event_id = events.id)
			FROM events WHERE id = $1
		`, incomplete.ID).Scan(&status, &draftCount, &revisionCount); err != nil {
			t.Fatal(err)
		}
		if status != "unpublished" || draftCount != 1 || revisionCount != 0 {
			t.Fatalf("incomplete state = status %s, drafts %d, revisions %d", status, draftCount, revisionCount)
		}
	})

	var published publishedEventResponse
	t.Run("administrator publishes immutable V1 atomically", func(t *testing.T) {
		response := publishDraft(t, handler, created.ID, adminSession, adminCSRF)
		if response.Code != http.StatusOK || response.Header().Get("Location") != "/api/v1/events/"+created.ID {
			t.Fatalf("publish status = %d, headers = %#v, body = %s", response.Code, response.Header(), response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &published); err != nil {
			t.Fatal(err)
		}
		if published.ID != created.ID || len(published.Places) != 2 || published.Places[1].DisambiguationLabel == nil {
			t.Fatalf("published response = %#v", published)
		}

		var status string
		var currentRevisionID string
		var draftCount, revisionCount, regionCount, placeCount, searchCount, auditCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT event.publication_status::text, event.current_revision_id,
				(SELECT count(*) FROM event_drafts WHERE event_id = event.id),
				(SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM event_revision_regions WHERE event_revision_id = event.current_revision_id),
				(SELECT count(*) FROM event_revision_places WHERE event_revision_id = event.current_revision_id),
				(SELECT count(*) FROM published_event_search WHERE event_id = event.id AND searchable_text LIKE '%咸阳%'),
				(SELECT count(*) FROM audit_logs WHERE action = 'event.publish' AND target_id = event.id::text AND outcome = 'success')
			FROM events event WHERE event.id = $1
		`, created.ID).Scan(&status, &currentRevisionID, &draftCount, &revisionCount, &regionCount, &placeCount, &searchCount, &auditCount); err != nil {
			t.Fatal(err)
		}
		if status != "published" || currentRevisionID == "" || draftCount != 0 || revisionCount != 1 ||
			regionCount != 1 || placeCount != 2 || searchCount != 1 || auditCount != 1 {
			t.Fatalf("published state = %s %s drafts=%d revisions=%d regions=%d places=%d search=%d audit=%d",
				status, currentRevisionID, draftCount, revisionCount, regionCount, placeCount, searchCount, auditCount)
		}
	})

	t.Run("timeline, bounds, metadata and independent detail expose current names", func(t *testing.T) {
		if _, err := queryPool.Exec(ctx, `UPDATE places SET name = '咸阳宫城', updated_at = $2 WHERE id = $1`, ids.place, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		list := performJSON(t, handler, http.MethodGet, "/api/v1/events?from=-221&to=-219", "", nil)
		if list.Code != http.StatusOK {
			t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
		}
		var result publishedEventQueryResponse
		if err := json.Unmarshal(list.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.SourceTotal != 1 || result.TotalMatching != 1 || len(result.Events) != 1 ||
			len(result.Events[0].Places) != 2 || result.Events[0].Places[0].Name != "咸阳宫城" {
			t.Fatalf("public timeline result = %#v", result)
		}
		detail := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+created.ID, "", nil)
		if detail.Code != http.StatusOK {
			t.Fatalf("detail status = %d, body = %s", detail.Code, detail.Body.String())
		}
		var detailEvent publishedEventResponse
		if err := json.Unmarshal(detail.Body.Bytes(), &detailEvent); err != nil {
			t.Fatal(err)
		}
		if len(detailEvent.Places) != 2 || detailEvent.Places[0].Name != "咸阳宫城" {
			t.Fatalf("public detail = %#v", detailEvent)
		}
		bounds := performJSON(t, handler, http.MethodGet, "/api/v1/event-bounds", "", nil)
		if bounds.Code != http.StatusOK || !strings.Contains(bounds.Body.String(), `"hasEvents":true`) {
			t.Fatalf("bounds status = %d, body = %s", bounds.Code, bounds.Body.String())
		}
		metadata := performJSON(t, handler, http.MethodGet, "/api/v1/event-metadata", "", nil)
		if metadata.Code != http.StatusOK || !strings.Contains(metadata.Body.String(), "秦始皇") {
			t.Fatalf("metadata status = %d, body = %s", metadata.Code, metadata.Body.String())
		}
	})

	t.Run("published revisions and associations are append only", func(t *testing.T) {
		if _, err := queryPool.Exec(ctx, `UPDATE event_revisions SET title = '篡改' WHERE event_id = $1`, created.ID); err == nil {
			t.Fatal("published revision update unexpectedly succeeded")
		}
		if _, err := queryPool.Exec(ctx, `DELETE FROM event_revision_places WHERE event_revision_id = (SELECT current_revision_id FROM events WHERE id = $1)`, created.ID); err == nil {
			t.Fatal("published revision association delete unexpectedly succeeded")
		}
	})

	t.Run("search projection failure rolls the whole publication back", func(t *testing.T) {
		rollbackBody := strings.Replace(completeBody, "qin-unification-public", "rollback-publication", 1)
		rollbackDraft := createPublicationDraft(t, handler, rollbackBody, "publication-rollback", editorSession, editorCSRF)
		if _, err := queryPool.Exec(ctx, `
			CREATE FUNCTION reject_test_search_projection() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'forced search projection failure'; END;
			$$;
			CREATE TRIGGER reject_test_search_projection BEFORE INSERT ON published_event_search
			FOR EACH ROW EXECUTE FUNCTION reject_test_search_projection();
		`); err != nil {
			t.Fatal(err)
		}
		response := publishDraft(t, handler, rollbackDraft.ID, adminSession, adminCSRF)
		if _, err := queryPool.Exec(ctx, `DROP TRIGGER reject_test_search_projection ON published_event_search; DROP FUNCTION reject_test_search_projection()`); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("forced failure status = %d, body = %s", response.Code, response.Body.String())
		}
		var status string
		var draftCount, revisionCount, searchCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT event.publication_status::text,
				(SELECT count(*) FROM event_drafts WHERE event_id = event.id),
				(SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM published_event_search WHERE event_id = event.id)
			FROM events event WHERE event.id = $1
		`, rollbackDraft.ID).Scan(&status, &draftCount, &revisionCount, &searchCount); err != nil {
			t.Fatal(err)
		}
		if status != "unpublished" || draftCount != 1 || revisionCount != 0 || searchCount != 0 {
			t.Fatalf("rollback state = status %s, drafts %d, revisions %d, search %d", status, draftCount, revisionCount, searchCount)
		}
	})

	t.Run("published revision accepts publication field boundaries", func(t *testing.T) {
		boundaryBody := fmt.Sprintf(`{
			"slug":"draft-boundaries","title":"%s","summary":"%s","narrative":"%s",
			"time":{"kind":"year","year":{"era":"CE","year":2026}},
			"primaryCategory":"文化","prominence":3,"displayOrder":0,"regionIds":[%q]
		}`, strings.Repeat("题", 200), strings.Repeat("摘", 500), strings.Repeat("文", 20000), ids.region)
		boundaryDraft := createPublicationDraft(t, handler, boundaryBody, "publication-boundaries", editorSession, editorCSRF)
		response := publishDraft(t, handler, boundaryDraft.ID, adminSession, adminCSRF)
		if response.Code != http.StatusOK {
			t.Fatalf("boundary publish status = %d, body = %s", response.Code, response.Body.String())
		}
	})
}

func createPublicationDraft(t *testing.T, handler http.Handler, body, idempotencyKey string, session, csrf *http.Cookie) managedEventResponse {
	t.Helper()
	response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events", body, []*http.Cookie{session, csrf}, map[string]string{
		"Idempotency-Key": idempotencyKey, "X-CSRF-Token": csrf.Value,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("create publication draft status = %d, body = %s", response.Code, response.Body.String())
	}
	var created managedEventResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created
}

func publishDraft(t *testing.T, handler http.Handler, eventID string, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+eventID+"/publish", "", []*http.Cookie{session, csrf}, map[string]string{
		"If-Match": `"draft-1"`, "X-CSRF-Token": csrf.Value,
	})
}
