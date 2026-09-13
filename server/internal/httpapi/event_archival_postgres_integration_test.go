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

func TestEventArchivalAndRepublicationAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "archive-admin@example.com", auth.RoleAdministrator, passwordHash, true, database)
	editor := createIntegrationUser(t, ctx, queryPool, "archive-editor@example.com", auth.RoleEditor, passwordHash, false, database)
	now := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
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

	created := createPublicationDraft(t, handler, revisionDraftBody("archive-republish", "下线前版本", ids), "archive-republish", editorSession, editorCSRF)
	firstPublication := publishDraft(t, handler, created.ID, adminSession, adminCSRF)
	if firstPublication.Code != http.StatusOK {
		t.Fatalf("publish V1 status = %d, body = %s", firstPublication.Code, firstPublication.Body.String())
	}
	var firstRevisionID string
	if err := queryPool.QueryRow(ctx, `SELECT current_revision_id FROM events WHERE id = $1`, created.ID).Scan(&firstRevisionID); err != nil {
		t.Fatal(err)
	}

	t.Run("only administrators may archive", func(t *testing.T) {
		response := archiveEvent(t, handler, created.ID, 2, editorSession, editorCSRF)
		if response.Code != http.StatusForbidden {
			t.Fatalf("editor archive status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("archival atomically removes every public view and preserves management history", func(t *testing.T) {
		now = now.Add(time.Minute)
		response := archiveEvent(t, handler, created.ID, 2, adminSession, adminCSRF)
		if response.Code != http.StatusOK || response.Header().Get("ETag") != `"event-3"` {
			t.Fatalf("archive status = %d, headers = %#v, body = %s", response.Code, response.Header(), response.Body.String())
		}
		var archived managedEventResponse
		if err := json.Unmarshal(response.Body.Bytes(), &archived); err != nil {
			t.Fatal(err)
		}
		if archived.PublicationStatus != "archived" || archived.Draft != nil || archived.LockVersion != 3 {
			t.Fatalf("archived response = %#v", archived)
		}

		var status, currentRevisionID string
		var revisionCount, searchCount, archiveAuditCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT event.publication_status::text, event.current_revision_id,
				(SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM published_event_search WHERE event_id = event.id),
				(SELECT count(*) FROM audit_logs
				 WHERE action = 'event.archive' AND target_id = event.id::text AND outcome = 'success'
				   AND details->>'previousStatus' = 'published'
				   AND details->>'newStatus' = 'archived'
				   AND details->>'currentRevisionId' = event.current_revision_id::text)
			FROM events event WHERE event.id = $1
		`, created.ID).Scan(&status, &currentRevisionID, &revisionCount, &searchCount, &archiveAuditCount); err != nil {
			t.Fatal(err)
		}
		if status != "archived" || currentRevisionID != firstRevisionID || revisionCount != 1 || searchCount != 0 || archiveAuditCount != 1 {
			t.Fatalf("archived state = status %s revision %s revisions=%d search=%d audit=%d",
				status, currentRevisionID, revisionCount, searchCount, archiveAuditCount)
		}
		publishedCount, err := database.PublishedEventCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if publishedCount != 0 {
			t.Fatalf("published count after archive = %d", publishedCount)
		}

		list := performJSON(t, handler, http.MethodGet, "/api/v1/events?from=-300&to=-100&q=%E4%B8%8B%E7%BA%BF%E5%89%8D", "", nil)
		if list.Code != http.StatusOK {
			t.Fatalf("archived list status = %d, body = %s", list.Code, list.Body.String())
		}
		var publicList publishedEventQueryResponse
		if err := json.Unmarshal(list.Body.Bytes(), &publicList); err != nil {
			t.Fatal(err)
		}
		if publicList.SourceTotal != 0 || publicList.TotalMatching != 0 || len(publicList.Events) != 0 {
			t.Fatalf("archived public list = %#v", publicList)
		}
		if detail := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+created.ID, "", nil); detail.Code != http.StatusNotFound {
			t.Fatalf("archived detail status = %d, body = %s", detail.Code, detail.Body.String())
		}
		if bounds := performJSON(t, handler, http.MethodGet, "/api/v1/event-bounds", "", nil); bounds.Code != http.StatusOK || !strings.Contains(bounds.Body.String(), `"hasEvents":false`) {
			t.Fatalf("archived bounds status = %d, body = %s", bounds.Code, bounds.Body.String())
		}
		if metadata := performJSON(t, handler, http.MethodGet, "/api/v1/event-metadata", "", nil); metadata.Code != http.StatusOK || strings.Contains(metadata.Body.String(), "东亚") {
			t.Fatalf("archived metadata status = %d, body = %s", metadata.Code, metadata.Body.String())
		}

		managed := performJSON(t, handler, http.MethodGet, "/api/v1/admin/events/"+created.ID, "", []*http.Cookie{editorSession})
		if managed.Code != http.StatusOK || !strings.Contains(managed.Body.String(), `"publicationStatus":"archived"`) {
			t.Fatalf("managed archived event status = %d, body = %s", managed.Code, managed.Body.String())
		}
		revisions := performJSON(t, handler, http.MethodGet, "/api/v1/admin/events/"+created.ID+"/revisions", "", []*http.Cookie{editorSession})
		if revisions.Code != http.StatusOK || !strings.Contains(revisions.Body.String(), "下线前版本") {
			t.Fatalf("managed revisions status = %d, body = %s", revisions.Code, revisions.Body.String())
		}
	})

	t.Run("administrator drafts from the last revision and republishes a new immutable revision", func(t *testing.T) {
		now = now.Add(time.Minute)
		draftResponse := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/draft", "", []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value,
		})
		if draftResponse.Code != http.StatusCreated || draftResponse.Header().Get("ETag") != `"draft-1"` {
			t.Fatalf("archived draft status = %d, headers = %#v, body = %s", draftResponse.Code, draftResponse.Header(), draftResponse.Body.String())
		}
		var draft managedEventResponse
		if err := json.Unmarshal(draftResponse.Body.Bytes(), &draft); err != nil {
			t.Fatal(err)
		}
		if draft.Draft == nil || draft.Draft.BasedOnRevisionNo == nil || *draft.Draft.BasedOnRevisionNo != 1 || draft.Draft.Title != "下线前版本" {
			t.Fatalf("draft from archived event = %#v", draft.Draft)
		}

		now = now.Add(time.Minute)
		update := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/events/"+created.ID+"/draft",
			revisionDraftUpdateBody("重新发布版本", ids), []*http.Cookie{adminSession, adminCSRF}, map[string]string{
				"If-Match": `"draft-1"`, "X-CSRF-Token": adminCSRF.Value,
			})
		if update.Code != http.StatusOK || update.Header().Get("ETag") != `"draft-2"` {
			t.Fatalf("archived draft update status = %d, headers = %#v, body = %s", update.Code, update.Header(), update.Body.String())
		}

		if _, err := queryPool.Exec(ctx, `
			CREATE FUNCTION reject_test_republication_projection() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'forced republication projection failure'; END;
			$$;
			CREATE TRIGGER reject_test_republication_projection BEFORE INSERT ON published_event_search
			FOR EACH ROW EXECUTE FUNCTION reject_test_republication_projection();
		`); err != nil {
			t.Fatal(err)
		}
		failedRepublication := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/publish", "", []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"If-Match": `"draft-2"`, "X-CSRF-Token": adminCSRF.Value,
		})
		if _, err := queryPool.Exec(ctx, `DROP TRIGGER reject_test_republication_projection ON published_event_search; DROP FUNCTION reject_test_republication_projection()`); err != nil {
			t.Fatal(err)
		}
		if failedRepublication.Code != http.StatusServiceUnavailable {
			t.Fatalf("forced republication failure status = %d, body = %s", failedRepublication.Code, failedRepublication.Body.String())
		}
		var rolledBackStatus string
		var rolledBackDrafts, rolledBackRevisions, rolledBackSearch, successfulPublications int
		if err := queryPool.QueryRow(ctx, `
			SELECT event.publication_status::text,
				(SELECT count(*) FROM event_drafts WHERE event_id = event.id),
				(SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM published_event_search WHERE event_id = event.id),
				(SELECT count(*) FROM audit_logs WHERE action = 'event.publish' AND target_id = event.id::text AND outcome = 'success')
			FROM events event WHERE event.id = $1
		`, created.ID).Scan(&rolledBackStatus, &rolledBackDrafts, &rolledBackRevisions, &rolledBackSearch, &successfulPublications); err != nil {
			t.Fatal(err)
		}
		if rolledBackStatus != "archived" || rolledBackDrafts != 1 || rolledBackRevisions != 1 || rolledBackSearch != 0 || successfulPublications != 1 {
			t.Fatalf("rolled back republication = status %s drafts=%d revisions=%d search=%d successfulPublications=%d",
				rolledBackStatus, rolledBackDrafts, rolledBackRevisions, rolledBackSearch, successfulPublications)
		}
		publishedCount, err := database.PublishedEventCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if publishedCount != 0 {
			t.Fatalf("published count after rolled back republication = %d", publishedCount)
		}

		now = now.Add(time.Minute)
		republished := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/publish", "", []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"If-Match": `"draft-2"`, "X-CSRF-Token": adminCSRF.Value,
		})
		if republished.Code != http.StatusOK || !strings.Contains(republished.Body.String(), "重新发布版本") {
			t.Fatalf("republish status = %d, body = %s", republished.Code, republished.Body.String())
		}

		var status, currentRevisionID, firstTitle string
		var revisionCount, searchCount, archiveAuditCount, publishAuditCount, republicationAuditCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT event.publication_status::text, event.current_revision_id,
				(SELECT title FROM event_revisions WHERE id = $2),
				(SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM published_event_search WHERE event_id = event.id AND searchable_text LIKE '%重新发布版本%'),
				(SELECT count(*) FROM audit_logs WHERE action = 'event.archive' AND target_id = event.id::text AND outcome = 'success'),
				(SELECT count(*) FROM audit_logs WHERE action = 'event.publish' AND target_id = event.id::text AND outcome = 'success'),
				(SELECT count(*) FROM audit_logs WHERE action = 'event.publish' AND target_id = event.id::text AND outcome = 'success'
				 AND details->>'previousStatus' = 'archived' AND details->>'newStatus' = 'published')
			FROM events event WHERE event.id = $1
		`, created.ID, firstRevisionID).Scan(&status, &currentRevisionID, &firstTitle, &revisionCount, &searchCount, &archiveAuditCount, &publishAuditCount, &republicationAuditCount); err != nil {
			t.Fatal(err)
		}
		if status != "published" || currentRevisionID == firstRevisionID || firstTitle != "下线前版本" || revisionCount != 2 || searchCount != 1 || archiveAuditCount != 1 || publishAuditCount != 2 || republicationAuditCount != 1 {
			t.Fatalf("republished state = status %s current %s firstTitle %q revisions=%d search=%d archiveAudit=%d publishAudit=%d republicationAudit=%d",
				status, currentRevisionID, firstTitle, revisionCount, searchCount, archiveAuditCount, publishAuditCount, republicationAuditCount)
		}
		publishedCount, err = database.PublishedEventCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if publishedCount != 1 {
			t.Fatalf("published count after republication = %d", publishedCount)
		}
		public := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+created.ID, "", nil)
		if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "重新发布版本") {
			t.Fatalf("republished detail status = %d, body = %s", public.Code, public.Body.String())
		}
	})

	t.Run("search projection failure rolls archival and public count back", func(t *testing.T) {
		second := createPublicationDraft(t, handler, revisionDraftBody("archive-rollback", "下线回滚", ids), "archive-rollback", editorSession, editorCSRF)
		if response := publishDraft(t, handler, second.ID, adminSession, adminCSRF); response.Code != http.StatusOK {
			t.Fatalf("publish rollback fixture status = %d, body = %s", response.Code, response.Body.String())
		}
		if _, err := queryPool.Exec(ctx, `
			CREATE FUNCTION reject_test_search_projection_delete() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'forced search projection delete failure'; END;
			$$;
			CREATE TRIGGER reject_test_search_projection_delete BEFORE DELETE ON published_event_search
			FOR EACH ROW EXECUTE FUNCTION reject_test_search_projection_delete();
		`); err != nil {
			t.Fatal(err)
		}
		response := archiveEvent(t, handler, second.ID, 2, adminSession, adminCSRF)
		if _, err := queryPool.Exec(ctx, `DROP TRIGGER reject_test_search_projection_delete ON published_event_search; DROP FUNCTION reject_test_search_projection_delete()`); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("forced archive failure status = %d, body = %s", response.Code, response.Body.String())
		}
		var status string
		var searchCount, archiveAuditCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT publication_status::text,
				(SELECT count(*) FROM published_event_search WHERE event_id = events.id),
				(SELECT count(*) FROM audit_logs WHERE action = 'event.archive' AND target_id = events.id::text AND outcome = 'success')
			FROM events WHERE id = $1
		`, second.ID).Scan(&status, &searchCount, &archiveAuditCount); err != nil {
			t.Fatal(err)
		}
		if status != "published" || searchCount != 1 || archiveAuditCount != 0 {
			t.Fatalf("rolled back archive state = status %s search=%d audit=%d", status, searchCount, archiveAuditCount)
		}
		publishedCount, err := database.PublishedEventCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if publishedCount != 2 {
			t.Fatalf("published count after rolled back archive = %d", publishedCount)
		}
	})
}

func archiveEvent(t *testing.T, handler http.Handler, eventID string, version int64, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+eventID+"/archive", "", []*http.Cookie{session, csrf}, map[string]string{
		"If-Match": fmt.Sprintf(`"event-%d"`, version), "X-CSRF-Token": csrf.Value,
	})
}
