package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
	"github.com/zhouxinghang/history_wiki/server/internal/identifier"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestTransactionalEventImportHTTPFlowAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "import-admin@example.com", auth.RoleAdministrator, passwordHash, true, database)
	editor := createIntegrationUser(t, ctx, queryPool, "import-editor@example.com", auth.RoleEditor, passwordHash, false, database)
	now := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	dummyHash, err := auth.HashPassword("invalid-password-placeholder", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL: "https://history.test", SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour, Now: func() time.Time { return now }, DummyPasswordHash: dummyHash,
	})
	ids := createEventAssociationFixtures(t, ctx, queryPool, administrator.ID, now)
	editorSession, editorCSRF := loginIntegrationUser(t, handler, editor.Email)
	adminSession, adminCSRF := loginIntegrationUser(t, handler, administrator.Email)

	firstID := newImportEventID(t)
	secondID := newImportEventID(t)
	validDocument := marshalImportDocument(t, eventImportDocument{Events: []eventImportRecordRequest{
		{
			ID: firstID, Slug: "han-founded", eventDraftRequest: eventDraftRequest{
				Title: "汉朝建立", Summary: "刘邦建立汉朝。", Narrative: "历史事件草稿正文。",
				Time:            &historyevent.TimeExpression{Kind: historyevent.TimeYear, Year: &historyevent.HistoricalYear{Era: historyevent.EraBCE, Year: 202}},
				PrimaryCategory: categoryPointer(historyevent.CategoryPolitics), Prominence: importIntPointer(1),
				RegionIDs: []string{ids.region}, FigureIDs: []string{ids.figure},
			},
		},
		{
			ID: secondID, Slug: "paper-making", eventDraftRequest: eventDraftRequest{
				Title: "造纸术改进", DisplayOrder: importIntPointer(25), TopicTagIDs: []string{ids.topicTag},
			},
		},
	}})

	t.Run("editor can preflight without database writes but cannot import", func(t *testing.T) {
		before := importPersistenceCounts(t, ctx, queryPool)
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports/preflight", validDocument, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("preflight status = %d, body = %s", response.Code, response.Body.String())
		}
		var result eventImportValidationResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Valid || result.Total != 2 || len(result.Errors) != 0 {
			t.Fatalf("preflight result = %#v", result)
		}
		after := importPersistenceCounts(t, ctx, queryPool)
		if before != after {
			t.Fatalf("preflight changed persistent import data: before=%#v after=%#v", before, after)
		}

		denied := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports", validDocument, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"X-CSRF-Token": editorCSRF.Value, "Idempotency-Key": "editor-cannot-import",
		})
		if denied.Code != http.StatusForbidden || readProblem(t, denied).Code != "permission_denied" {
			t.Fatalf("editor import status = %d, body = %s", denied.Code, denied.Body.String())
		}
	})

	t.Run("preflight reports row field and business conflicts", func(t *testing.T) {
		invalidDocument := marshalImportDocument(t, eventImportDocument{Events: []eventImportRecordRequest{
			{ID: firstID, Slug: "duplicate", eventDraftRequest: eventDraftRequest{RegionIDs: []string{ids.inactiveRegion}}},
			{ID: firstID, Slug: "duplicate", eventDraftRequest: eventDraftRequest{Time: &historyevent.TimeExpression{
				Kind: historyevent.TimeExactDate, Date: &historyevent.ExactDate{Era: historyevent.EraCE, Year: 1900, Month: 2, Day: 29},
			}}},
		}})
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports/preflight", invalidDocument, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("invalid preflight status = %d, body = %s", response.Code, response.Body.String())
		}
		var result eventImportValidationResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Valid || !hasImportIssue(result.Errors, 0, "regionIds", "invalid_association") ||
			!hasImportIssue(result.Errors, 1, "id", "duplicate_uuid") ||
			!hasImportIssue(result.Errors, 1, "slug", "duplicate_slug") ||
			!hasImportIssue(result.Errors, 1, "time", "invalid_time_expression") {
			t.Fatalf("invalid preflight result = %#v", result)
		}
	})

	t.Run("preflight rejects PostgreSQL-incompatible null characters", func(t *testing.T) {
		invalidDocument := marshalImportDocument(t, eventImportDocument{Events: []eventImportRecordRequest{
			{
				ID:   firstID,
				Slug: "nul-text",
				eventDraftRequest: eventDraftRequest{
					Title:     "invalid\x00title",
					Summary:   "invalid\x00summary",
					Narrative: "invalid\x00narrative",
				},
			},
		}})
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports/preflight", invalidDocument, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("null character preflight status = %d, body=%s", response.Code, response.Body.String())
		}
		var result eventImportValidationResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Valid ||
			!hasImportIssue(result.Errors, 0, "title", "invalid_text") ||
			!hasImportIssue(result.Errors, 0, "summary", "invalid_text") ||
			!hasImportIssue(result.Errors, 0, "narrative", "invalid_text") {
			t.Fatalf("null character preflight result = %#v err=%v", result, err)
		}
	})

	t.Run("preflight rejects oversized association lists without writing", func(t *testing.T) {
		regionIDs := make([]string, historyevent.MaxEventRegions+1)
		for index := range regionIDs {
			regionIDs[index] = fmt.Sprintf("00000000-0000-7000-8000-%012d", index+1)
		}
		before := importPersistenceCounts(t, ctx, queryPool)
		invalidDocument := marshalImportDocument(t, eventImportDocument{Events: []eventImportRecordRequest{
			{ID: firstID, Slug: "too-many-regions", eventDraftRequest: eventDraftRequest{RegionIDs: regionIDs}},
		}})
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports/preflight", invalidDocument, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("association limit preflight status = %d, body=%s", response.Code, response.Body.String())
		}
		var result eventImportValidationResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Valid ||
			!hasImportIssue(result.Errors, 0, "regionIds", "too_many_items") {
			t.Fatalf("association limit preflight result = %#v err=%v", result, err)
		}
		if after := importPersistenceCounts(t, ctx, queryPool); before != after {
			t.Fatalf("association limit preflight changed persistence: before=%#v after=%#v", before, after)
		}
	})

	var original eventImportBatchResponse
	t.Run("administrator imports every draft in one unpublished batch", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports", validDocument, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value, "Idempotency-Key": "import-foundational-events",
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("import status = %d, body = %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &original); err != nil {
			t.Fatal(err)
		}
		if original.ImportedCount != 2 || len(original.EventIDs) != 2 || original.EventIDs[0] != firstID || original.EventIDs[1] != secondID {
			t.Fatalf("import result = %#v", original)
		}
		var events, drafts, visible int
		if err := queryPool.QueryRow(ctx, `
			SELECT count(*), count(d.event_id), count(*) FILTER (WHERE e.publication_status <> 'unpublished' OR e.current_revision_id IS NOT NULL)
			FROM events e LEFT JOIN event_drafts d ON d.event_id = e.id
		`).Scan(&events, &drafts, &visible); err != nil {
			t.Fatal(err)
		}
		if events != 2 || drafts != 2 || visible != 0 {
			t.Fatalf("events=%d drafts=%d visible=%d", events, drafts, visible)
		}
	})

	t.Run("same idempotency key and request returns the original result", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports", validDocument, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value, "Idempotency-Key": "import-foundational-events",
		})
		if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("replay status = %d, headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
		}
		var replayed eventImportBatchResponse
		if err := json.Unmarshal(response.Body.Bytes(), &replayed); err != nil {
			t.Fatal(err)
		}
		if replayed.BatchID != original.BatchID || replayed.CreatedAt != original.CreatedAt || strings.Join(replayed.EventIDs, ",") != strings.Join(original.EventIDs, ",") {
			t.Fatalf("replayed=%#v original=%#v", replayed, original)
		}
		if counts := importPersistenceCounts(t, ctx, queryPool); counts.events != 2 || counts.batches != 1 || counts.idempotency != 1 {
			t.Fatalf("counts after replay = %#v", counts)
		}
	})

	t.Run("changed request with same key conflicts", func(t *testing.T) {
		changed := strings.Replace(validDocument, "paper-making", "changed-paper-making", 1)
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports", changed, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value, "Idempotency-Key": "import-foundational-events",
		})
		if response.Code != http.StatusConflict || readProblem(t, response).Code != "idempotency_conflict" {
			t.Fatalf("changed replay status = %d, body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("database collisions and invalid associations roll back the whole batch", func(t *testing.T) {
		thirdID := newImportEventID(t)
		slugConflictID := newImportEventID(t)
		collision := marshalImportDocument(t, eventImportDocument{Events: []eventImportRecordRequest{
			{ID: thirdID, Slug: "new-before-conflict"},
			{ID: firstID, Slug: "another-new-slug"},
			{ID: slugConflictID, Slug: "han-founded"},
		}})
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports", collision, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value, "Idempotency-Key": "collision-batch",
		})
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("collision status = %d, body=%s", response.Code, response.Body.String())
		}
		var result eventImportValidationResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil ||
			!hasImportIssue(result.Errors, 1, "id", "uuid_conflict") ||
			!hasImportIssue(result.Errors, 2, "slug", "slug_conflict") {
			t.Fatalf("collision result = %#v err=%v", result, err)
		}
		var newCount int
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = ANY($1::uuid[])`, []string{thirdID, slugConflictID}).Scan(&newCount); err != nil || newCount != 0 {
			t.Fatalf("partial collision insert count=%d err=%v", newCount, err)
		}

		fourthID, fifthID := newImportEventID(t), newImportEventID(t)
		associationFailure := marshalImportDocument(t, eventImportDocument{Events: []eventImportRecordRequest{
			{ID: fourthID, Slug: "valid-looking-import"},
			{ID: fifthID, Slug: "bad-association-import", eventDraftRequest: eventDraftRequest{RegionIDs: []string{ids.inactiveRegion}}},
		}})
		response = performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/event-imports", associationFailure, []*http.Cookie{adminSession, adminCSRF}, map[string]string{
			"X-CSRF-Token": adminCSRF.Value, "Idempotency-Key": "association-failure-batch",
		})
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("association failure status = %d, body=%s", response.Code, response.Body.String())
		}
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = ANY($1::uuid[])`, []string{fourthID, fifthID}).Scan(&newCount); err != nil || newCount != 0 {
			t.Fatalf("partial association insert count=%d err=%v", newCount, err)
		}
	})

	var successfulAudits int
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'event.import' AND outcome = 'success'`).Scan(&successfulAudits); err != nil {
		t.Fatal(err)
	}
	if successfulAudits != 2 {
		t.Fatalf("successful import audits = %d, want original and replay", successfulAudits)
	}
}

func TestEventImportHardLimits(t *testing.T) {
	t.Run("record count", func(t *testing.T) {
		document := eventImportDocument{Events: make([]eventImportRecordRequest, historyevent.MaxImportRecords+1)}
		body := marshalImportDocument(t, document)
		request, response := importDecodeRequest(body)
		_, err := decodeEventImportDocument(response, request)
		if !errors.Is(err, errEventImportRecordLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("body bytes", func(t *testing.T) {
		body := `{"events":[{"id":"` + strings.Repeat("a", historyevent.MaxImportBytes) + `"}]}`
		request, response := importDecodeRequest(body)
		_, err := decodeEventImportDocument(response, request)
		var maxBytesError *http.MaxBytesError
		if !errors.As(err, &maxBytesError) {
			t.Fatalf("error = %v", err)
		}
	})
}

type importCounts struct{ events, batches, idempotency, audits int }

func importPersistenceCounts(t *testing.T, ctx context.Context, queryPool *pgxpool.Pool) importCounts {
	t.Helper()
	var result importCounts
	if err := queryPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM events),
		(SELECT count(*) FROM event_import_batches),
		(SELECT count(*) FROM idempotency_records WHERE scope = 'events.import'),
		(SELECT count(*) FROM audit_logs WHERE action = 'event.import')
	`).Scan(&result.events, &result.batches, &result.idempotency, &result.audits); err != nil {
		t.Fatal(err)
	}
	return result
}

func hasImportIssue(issues []historyevent.ImportIssue, index int, field, code string) bool {
	for _, issue := range issues {
		if issue.Index == index && issue.Field == field && issue.Code == code {
			return true
		}
	}
	return false
}

func marshalImportDocument(t *testing.T, document eventImportDocument) string {
	t.Helper()
	value, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func newImportEventID(t *testing.T) string {
	t.Helper()
	value, err := identifier.New()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func importDecodeRequest(body string) (*http.Request, *httptest.ResponseRecorder) {
	return httptest.NewRequest(http.MethodPost, "/api/v1/admin/event-imports/preflight", strings.NewReader(body)), httptest.NewRecorder()
}

func categoryPointer(value historyevent.PrimaryCategory) *historyevent.PrimaryCategory { return &value }
func importIntPointer(value int) *int                                                  { return &value }
