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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestPublishedEventEditingAndRevisionManagementAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "revision-publisher@example.com", auth.RoleAdministrator, passwordHash, true, database)
	editor := createIntegrationUser(t, ctx, queryPool, "revision-editor@example.com", auth.RoleEditor, passwordHash, false, database)
	now := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
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

	completeBody := revisionDraftBody("qin-revision-history", "秦统一六国 V1", ids)
	created := createPublicationDraft(t, handler, completeBody, "revision-history", editorSession, editorCSRF)
	firstPublication := publishDraft(t, handler, created.ID, adminSession, adminCSRF)
	if firstPublication.Code != http.StatusOK {
		t.Fatalf("publish V1 status = %d, body = %s", firstPublication.Code, firstPublication.Body.String())
	}

	t.Run("create current draft while readers remain on V1", func(t *testing.T) {
		now = now.Add(time.Minute)
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/draft", "", []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"draft-1"` {
			t.Fatalf("create current draft status = %d, headers = %#v, body = %s", response.Code, response.Header(), response.Body.String())
		}
		var managed managedEventResponse
		if err := json.Unmarshal(response.Body.Bytes(), &managed); err != nil {
			t.Fatal(err)
		}
		if managed.Draft == nil || managed.Draft.BasedOnRevisionNo == nil || *managed.Draft.BasedOnRevisionNo != 1 || managed.Draft.Title != "秦统一六国 V1" {
			t.Fatalf("current draft = %#v", managed.Draft)
		}
		public := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+created.ID, "", nil)
		if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "秦统一六国 V1") {
			t.Fatalf("public event changed while draft exists: status = %d, body = %s", public.Code, public.Body.String())
		}
	})

	var winningTitle string
	t.Run("concurrent draft updates cannot overwrite one another", func(t *testing.T) {
		now = now.Add(time.Minute)
		bodies := []string{
			revisionDraftUpdateBody("秦统一六国 V2-A", ids),
			revisionDraftUpdateBody("秦统一六国 V2-B", ids),
		}
		responses := concurrentRequests(handler, http.MethodPatch, "/api/v1/admin/events/"+created.ID+"/draft", bodies,
			[]*http.Cookie{editorSession, editorCSRF}, map[string]string{"If-Match": `"draft-1"`, "X-CSRF-Token": editorCSRF.Value})
		statuses := []int{responses[0].Code, responses[1].Code}
		sort.Ints(statuses)
		if statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
			t.Fatalf("concurrent update statuses = %v, bodies = %q / %q", statuses, responses[0].Body.String(), responses[1].Body.String())
		}
		for _, response := range responses {
			if response.Code != http.StatusOK {
				if readProblem(t, response).Code != "event_draft_version_conflict" {
					t.Fatalf("concurrent update conflict = %s", response.Body.String())
				}
				continue
			}
			var managed managedEventResponse
			if err := json.Unmarshal(response.Body.Bytes(), &managed); err != nil {
				t.Fatal(err)
			}
			winningTitle = managed.Draft.Title
		}
		var title string
		var version int64
		if err := queryPool.QueryRow(ctx, `SELECT title, lock_version FROM event_drafts WHERE event_id = $1`, created.ID).Scan(&title, &version); err != nil {
			t.Fatal(err)
		}
		if title != winningTitle || version != 2 {
			t.Fatalf("stored draft = title %q version %d, winner = %q", title, version, winningTitle)
		}
	})

	t.Run("republish creates V2 and switches readers atomically", func(t *testing.T) {
		now = now.Add(time.Minute)
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/publish", "", []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"If-Match": `"draft-2"`, "X-CSRF-Token": adminCSRF.Value,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("publish V2 status = %d, body = %s", response.Code, response.Body.String())
		}
		var currentRevisionNo, revisionCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT revision.revision_no, (SELECT count(*) FROM event_revisions WHERE event_id = event.id)
			FROM events event JOIN event_revisions revision ON revision.id = event.current_revision_id
			WHERE event.id = $1
		`, created.ID).Scan(&currentRevisionNo, &revisionCount); err != nil {
			t.Fatal(err)
		}
		if currentRevisionNo != 2 || revisionCount != 2 {
			t.Fatalf("current revision = %d, count = %d", currentRevisionNo, revisionCount)
		}
		public := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+created.ID, "", nil)
		if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), winningTitle) {
			t.Fatalf("public V2 = status %d, body %s", public.Code, public.Body.String())
		}
	})

	t.Run("management lists and views every immutable revision with current entity names", func(t *testing.T) {
		if _, err := queryPool.Exec(ctx, `UPDATE regions SET name = '东亚（当前名称）', updated_at = $2 WHERE id = $1`, ids.region, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		list := performJSON(t, handler, http.MethodGet, "/api/v1/admin/events/"+created.ID+"/revisions", "", []*http.Cookie{editorSession, editorCSRF})
		if list.Code != http.StatusOK {
			t.Fatalf("revision list status = %d, body = %s", list.Code, list.Body.String())
		}
		var listed managedEventRevisionListResponse
		if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed.Revisions) != 2 || listed.Revisions[0].RevisionNo != 2 || !listed.Revisions[0].Current ||
			listed.Revisions[0].PublishedBy.Email != administrator.Email || listed.Revisions[1].Current {
			t.Fatalf("revision list = %#v", listed.Revisions)
		}
		detail := performJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/v1/admin/events/%s/revisions/1", created.ID), "", []*http.Cookie{editorSession, editorCSRF})
		if detail.Code != http.StatusOK {
			t.Fatalf("revision detail status = %d, body = %s", detail.Code, detail.Body.String())
		}
		var revision managedEventRevisionResponse
		if err := json.Unmarshal(detail.Body.Bytes(), &revision); err != nil {
			t.Fatal(err)
		}
		if revision.RevisionNo != 1 || revision.Current || revision.PublishedAt == "" || revision.PublishedBy.Email != administrator.Email ||
			len(revision.Regions) != 1 || revision.Regions[0].ID != ids.region || revision.Regions[0].Name != "东亚（当前名称）" {
			t.Fatalf("revision detail = %#v", revision)
		}
	})

	t.Run("restore copies V1 to a draft without activating or mutating it", func(t *testing.T) {
		now = now.Add(time.Minute)
		response := performJSONWithHeaders(t, handler, http.MethodPost, fmt.Sprintf("/api/v1/admin/events/%s/revisions/1/restore", created.ID), "", []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"draft-1"` {
			t.Fatalf("restore V1 status = %d, headers = %#v, body = %s", response.Code, response.Header(), response.Body.String())
		}
		var managed managedEventResponse
		if err := json.Unmarshal(response.Body.Bytes(), &managed); err != nil {
			t.Fatal(err)
		}
		if managed.Draft == nil || managed.Draft.Title != "秦统一六国 V1" || managed.Draft.BasedOnRevisionNo == nil || *managed.Draft.BasedOnRevisionNo != 1 {
			t.Fatalf("restored draft = %#v", managed.Draft)
		}
		public := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+created.ID, "", nil)
		if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), winningTitle) {
			t.Fatalf("restore activated old version: status = %d, body = %s", public.Code, public.Body.String())
		}
		var v1Title string
		var currentRevisionNo int
		if err := queryPool.QueryRow(ctx, `
			SELECT old.title, current.revision_no
			FROM event_revisions old
			JOIN events event ON event.id = old.event_id
			JOIN event_revisions current ON current.id = event.current_revision_id
			WHERE old.event_id = $1 AND old.revision_no = 1
		`, created.ID).Scan(&v1Title, &currentRevisionNo); err != nil {
			t.Fatal(err)
		}
		if v1Title != "秦统一六国 V1" || currentRevisionNo != 2 {
			t.Fatalf("revision state after restore = V1 %q, current V%d", v1Title, currentRevisionNo)
		}

		conflict := performJSONWithHeaders(t, handler, http.MethodPost, fmt.Sprintf("/api/v1/admin/events/%s/revisions/2/restore", created.ID), "", []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"X-CSRF-Token": editorCSRF.Value,
		})
		if conflict.Code != http.StatusConflict || readProblem(t, conflict).Code != "event_draft_already_exists" || conflict.Header().Get("ETag") != `"draft-1"` {
			t.Fatalf("restore conflict status = %d, headers = %#v, body = %s", conflict.Code, conflict.Header(), conflict.Body.String())
		}
	})

	t.Run("concurrent publishes create only one next revision", func(t *testing.T) {
		now = now.Add(time.Minute)
		responses := concurrentRequests(handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/publish", []string{"", ""},
			[]*http.Cookie{adminSession, adminCSRF}, map[string]string{"If-Match": `"draft-1"`, "X-CSRF-Token": adminCSRF.Value})
		statuses := []int{responses[0].Code, responses[1].Code}
		sort.Ints(statuses)
		if statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
			t.Fatalf("concurrent publish statuses = %v, bodies = %q / %q", statuses, responses[0].Body.String(), responses[1].Body.String())
		}
		for _, response := range responses {
			if response.Code == http.StatusConflict && readProblem(t, response).Code != "event_draft_version_conflict" {
				t.Fatalf("publish conflict = %s", response.Body.String())
			}
		}
		var currentRevisionNo, revisionCount, draftCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT revision.revision_no,
				(SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM event_drafts WHERE event_id = event.id)
			FROM events event JOIN event_revisions revision ON revision.id = event.current_revision_id
			WHERE event.id = $1
		`, created.ID).Scan(&currentRevisionNo, &revisionCount, &draftCount); err != nil {
			t.Fatal(err)
		}
		if currentRevisionNo != 3 || revisionCount != 3 || draftCount != 0 {
			t.Fatalf("concurrent publish state = current V%d, revisions %d, drafts %d", currentRevisionNo, revisionCount, draftCount)
		}
	})

	t.Run("draft update and publication use one lock order without deadlock", func(t *testing.T) {
		raceBody := revisionDraftBody("publish-update-race", "并发锁顺序 V1", ids)
		raceEvent := createPublicationDraft(t, handler, raceBody, "publish-update-race", editorSession, editorCSRF)
		if response := publishDraft(t, handler, raceEvent.ID, adminSession, adminCSRF); response.Code != http.StatusOK {
			t.Fatalf("publish race V1 status = %d, body = %s", response.Code, response.Body.String())
		}
		if response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+raceEvent.ID+"/draft", "", []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"X-CSRF-Token": editorCSRF.Value,
		}); response.Code != http.StatusCreated {
			t.Fatalf("create race draft status = %d, body = %s", response.Code, response.Body.String())
		}
		if _, err := queryPool.Exec(ctx, `
			CREATE FUNCTION delay_test_event_draft_update() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_sleep(0.5); RETURN NEW; END;
			$$;
			CREATE TRIGGER delay_test_event_draft_update BEFORE UPDATE ON event_drafts
			FOR EACH ROW EXECUTE FUNCTION delay_test_event_draft_update();
		`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = queryPool.Exec(context.Background(), `
				DROP TRIGGER IF EXISTS delay_test_event_draft_update ON event_drafts;
				DROP FUNCTION IF EXISTS delay_test_event_draft_update();
			`)
		}()

		responses := make(chan *httptest.ResponseRecorder, 2)
		go func() {
			responses <- rawJSONRequest(handler, http.MethodPatch, "/api/v1/admin/events/"+raceEvent.ID+"/draft",
				revisionDraftUpdateBody("并发锁顺序 V2", ids), []*http.Cookie{editorSession, editorCSRF},
				map[string]string{"If-Match": `"draft-1"`, "X-CSRF-Token": editorCSRF.Value})
		}()
		time.Sleep(100 * time.Millisecond)
		go func() {
			responses <- rawJSONRequest(handler, http.MethodPost, "/api/v1/admin/events/"+raceEvent.ID+"/publish", "",
				[]*http.Cookie{adminSession, adminCSRF}, map[string]string{"If-Match": `"draft-1"`, "X-CSRF-Token": adminCSRF.Value})
		}()
		first, second := <-responses, <-responses
		statuses := []int{first.Code, second.Code}
		sort.Ints(statuses)
		if statuses[0] != http.StatusOK || (statuses[1] != http.StatusConflict && statuses[1] != http.StatusNotFound) {
			t.Fatalf("publish/update race statuses = %v, bodies = %q / %q", statuses, first.Body.String(), second.Body.String())
		}
		if first.Code == http.StatusServiceUnavailable || second.Code == http.StatusServiceUnavailable {
			t.Fatalf("publish/update race deadlocked: %d / %d", first.Code, second.Code)
		}
		var revisionCount, draftCount int
		var draftVersion *int64
		if err := queryPool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM event_drafts WHERE event_id = event.id),
				(SELECT lock_version FROM event_drafts WHERE event_id = event.id)
			FROM events event WHERE event.id = $1
		`, raceEvent.ID).Scan(&revisionCount, &draftCount, &draftVersion); err != nil {
			t.Fatal(err)
		}
		validUpdatedDraft := revisionCount == 1 && draftCount == 1 && draftVersion != nil && *draftVersion == 2
		validPublishedDraft := revisionCount == 2 && draftCount == 0 && draftVersion == nil
		if !validUpdatedDraft && !validPublishedDraft {
			t.Fatalf("publish/update race state = revisions %d, drafts %d, draft version %v", revisionCount, draftCount, draftVersion)
		}
	})

	t.Run("publication revalidates canonical entity status", func(t *testing.T) {
		inactiveDraft := createPublicationDraft(t, handler,
			revisionDraftBody("inactive-region-publication", "失效地区不可发布", ids),
			"inactive-region-publication", editorSession, editorCSRF)
		if _, err := queryPool.Exec(ctx, `UPDATE regions SET status = 'inactive', updated_at = $2 WHERE id = $1`, ids.region, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		response := publishDraft(t, handler, inactiveDraft.ID, adminSession, adminCSRF)
		if response.Code != http.StatusUnprocessableEntity || readProblem(t, response).Code != "invalid_event_association" {
			t.Fatalf("inactive association publish status = %d, body = %s", response.Code, response.Body.String())
		}
		var status string
		var revisionCount, draftCount int
		if err := queryPool.QueryRow(ctx, `
			SELECT publication_status::text,
				(SELECT count(*) FROM event_revisions WHERE event_id = event.id),
				(SELECT count(*) FROM event_drafts WHERE event_id = event.id)
			FROM events event WHERE event.id = $1
		`, inactiveDraft.ID).Scan(&status, &revisionCount, &draftCount); err != nil {
			t.Fatal(err)
		}
		if status != "unpublished" || revisionCount != 0 || draftCount != 1 {
			t.Fatalf("inactive association publication changed state = %s, revisions %d, drafts %d", status, revisionCount, draftCount)
		}
	})
}

func revisionDraftBody(slug, title string, ids eventFixtureIDs) string {
	return fmt.Sprintf(`{
		"slug":%q,"title":%q,"summary":"秦结束战国割据局面。","narrative":"完整叙述。",
		"time":{"kind":"year","year":{"era":"BCE","year":221}},
		"primaryCategory":"政治","prominence":1,"displayOrder":20,
		"regionIds":[%q],"placeIds":[%q],"periodIds":[%q],"figureIds":[%q],"topicTagIds":[%q]
	}`, slug, title, ids.region, ids.place, ids.period, ids.figure, ids.topicTag)
}

func revisionDraftUpdateBody(title string, ids eventFixtureIDs) string {
	return fmt.Sprintf(`{
		"title":%q,"summary":"秦结束战国割据局面。","narrative":"完整叙述。",
		"time":{"kind":"year","year":{"era":"BCE","year":221}},
		"primaryCategory":"政治","prominence":1,"displayOrder":20,
		"regionIds":[%q],"placeIds":[%q],"periodIds":[%q],"figureIds":[%q],"topicTagIds":[%q]
	}`, title, ids.region, ids.place, ids.period, ids.figure, ids.topicTag)
}

func concurrentRequests(handler http.Handler, method, path string, bodies []string, cookies []*http.Cookie, headers map[string]string) []*httptest.ResponseRecorder {
	responses := make([]*httptest.ResponseRecorder, len(bodies))
	var wait sync.WaitGroup
	wait.Add(len(bodies))
	for index, body := range bodies {
		go func(index int, body string) {
			defer wait.Done()
			responses[index] = rawJSONRequest(handler, method, path, body, cookies, headers)
		}(index, body)
	}
	wait.Wait()
	return responses
}

func rawJSONRequest(handler http.Handler, method, path, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
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
