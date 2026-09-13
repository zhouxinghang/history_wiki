package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/catalog"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestCanonicalEntityGovernanceAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "governance-admin@example.com", auth.RoleAdministrator, passwordHash, true, database)
	editor := createIntegrationUser(t, ctx, queryPool, "governance-editor@example.com", auth.RoleEditor, passwordHash, false, database)
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL: "https://history.test", SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour, Now: func() time.Time { return now }, DummyPasswordHash: passwordHash,
	})
	adminSession, adminCSRF := loginIntegrationUser(t, handler, administrator.Email)
	editorSession, editorCSRF := loginIntegrationUser(t, handler, editor.Email)
	ids := createEventAssociationFixtures(t, ctx, queryPool, editor.ID, now)
	targetFigureID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queryPool.Exec(ctx, `
		INSERT INTO historical_figures (
			id, name, disambiguation_label, status, created_by, updated_by, created_at, updated_at
		) VALUES ($1, '秦始皇', '统一后的称号', 'active', $2, $2, $3, $3)
	`, targetFigureID, administrator.ID, now); err != nil {
		t.Fatal(err)
	}
	var sameNameCount int
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM historical_figures WHERE name = '秦始皇'`).Scan(&sameNameCount); err != nil || sameNameCount != 2 {
		t.Fatalf("same-name figures count = %d, error = %v", sameNameCount, err)
	}

	created := createPublicationDraft(t, handler, revisionDraftBody("canonical-governance", "规范实体治理事件", ids), "canonical-governance", editorSession, editorCSRF)
	if response := publishDraft(t, handler, created.ID, adminSession, adminCSRF); response.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", response.Code, response.Body.String())
	}

	t.Run("renaming refreshes public display and search in the same transaction", func(t *testing.T) {
		now = now.Add(time.Minute)
		response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/figures/"+ids.figure,
			`{"name":"秦王政","disambiguationLabel":"秦统一六国前后","status":"active"}`,
			[]*http.Cookie{adminSession, adminCSRF}, map[string]string{"If-Match": `"1"`, "X-CSRF-Token": adminCSRF.Value})
		if response.Code != http.StatusOK {
			t.Fatalf("rename status = %d, body = %s", response.Code, response.Body.String())
		}
		assertPublishedFigure(t, handler, created.ID, ids.figure, "秦王政")
		result := readPublishedQuery(t, handler, "/api/v1/events?from=-300&to=-200&q="+url.QueryEscape("秦王政"))
		if len(result.Events) != 1 || result.Events[0].ID != created.ID {
			t.Fatalf("renamed search result = %#v", result.Events)
		}
	})

	t.Run("inactive entities remain readable but cannot be added to a new draft", func(t *testing.T) {
		now = now.Add(time.Minute)
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events/"+created.ID+"/draft", "",
			[]*http.Cookie{editorSession, editorCSRF}, map[string]string{"X-CSRF-Token": editorCSRF.Value})
		if response.Code != http.StatusCreated {
			t.Fatalf("restore current revision status = %d, body = %s", response.Code, response.Body.String())
		}
		response = performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/figures/"+ids.figure,
			`{"name":"秦王政","disambiguationLabel":"秦统一六国前后","status":"inactive"}`,
			[]*http.Cookie{adminSession, adminCSRF}, map[string]string{"If-Match": `"2"`, "X-CSRF-Token": adminCSRF.Value})
		if response.Code != http.StatusOK {
			t.Fatalf("deactivate status = %d, body = %s", response.Code, response.Body.String())
		}
		assertPublishedFigure(t, handler, created.ID, ids.figure, "秦王政")

		newBody := revisionDraftBody("inactive-figure-new-draft", "不应创建", ids)
		response = performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/events", newBody,
			[]*http.Cookie{editorSession, editorCSRF}, map[string]string{
				"Idempotency-Key": "inactive-figure-new-draft", "X-CSRF-Token": editorCSRF.Value,
			})
		if response.Code != http.StatusUnprocessableEntity || readProblem(t, response).Code != "invalid_event_association" {
			t.Fatalf("inactive association status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("merge requires administrator impact confirmation and preserves old identifiers", func(t *testing.T) {
		forbidden := performJSON(t, handler, http.MethodGet, "/api/v1/admin/figures/"+ids.figure+"/merge-impact", "", []*http.Cookie{editorSession})
		if forbidden.Code != http.StatusForbidden {
			t.Fatalf("editor impact status = %d, body = %s", forbidden.Code, forbidden.Body.String())
		}

		impactResponse := performJSON(t, handler, http.MethodGet, "/api/v1/admin/figures/"+ids.figure+"/merge-impact", "", []*http.Cookie{adminSession})
		if impactResponse.Code != http.StatusOK {
			t.Fatalf("impact status = %d, body = %s", impactResponse.Code, impactResponse.Body.String())
		}
		var impact catalog.EntityMergeImpact
		if err := json.Unmarshal(impactResponse.Body.Bytes(), &impact); err != nil {
			t.Fatal(err)
		}
		if impact != (catalog.EntityMergeImpact{DraftCount: 1, RevisionCount: 1, PublishedEventCount: 1}) {
			t.Fatalf("impact = %#v", impact)
		}

		staleBody := fmt.Sprintf(`{"targetId":%q,"confirmedImpact":{"draftCount":0,"revisionCount":0,"publishedEventCount":0}}`, targetFigureID)
		stale := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/figures/"+ids.figure+"/merge", staleBody,
			[]*http.Cookie{adminSession, adminCSRF}, map[string]string{"If-Match": `"3"`, "X-CSRF-Token": adminCSRF.Value})
		if stale.Code != http.StatusConflict || readProblem(t, stale).Code != "merge_impact_changed" {
			t.Fatalf("stale impact status = %d, body = %s", stale.Code, stale.Body.String())
		}

		mergeBodyBytes, err := json.Marshal(mergeCanonicalEntityRequest{TargetID: targetFigureID, ConfirmedImpact: &impact})
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
		merged := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/figures/"+ids.figure+"/merge", string(mergeBodyBytes),
			[]*http.Cookie{adminSession, adminCSRF}, map[string]string{"If-Match": `"3"`, "X-CSRF-Token": adminCSRF.Value})
		if merged.Code != http.StatusOK || merged.Header().Get("ETag") != `"4"` {
			t.Fatalf("merge status = %d, headers = %#v, body = %s", merged.Code, merged.Header(), merged.Body.String())
		}

		var status string
		var mergedIntoID, revisionFigureID, draftFigureID string
		if err := queryPool.QueryRow(ctx, `SELECT status::text, merged_into_id FROM historical_figures WHERE id = $1`, ids.figure).Scan(&status, &mergedIntoID); err != nil {
			t.Fatal(err)
		}
		if err := queryPool.QueryRow(ctx, `SELECT historical_figure_id FROM event_revision_figures LIMIT 1`).Scan(&revisionFigureID); err != nil {
			t.Fatal(err)
		}
		if err := queryPool.QueryRow(ctx, `SELECT historical_figure_id FROM event_draft_figures LIMIT 1`).Scan(&draftFigureID); err != nil {
			t.Fatal(err)
		}
		if status != "merged" || mergedIntoID != targetFigureID || revisionFigureID != ids.figure || draftFigureID != ids.figure {
			t.Fatalf("stored merge state = status %s target %s revision %s draft %s", status, mergedIntoID, revisionFigureID, draftFigureID)
		}

		assertPublishedFigure(t, handler, created.ID, targetFigureID, "秦始皇")
		managed := performJSON(t, handler, http.MethodGet, "/api/v1/admin/events/"+created.ID, "", []*http.Cookie{editorSession})
		var managedBody managedEventResponse
		if managed.Code != http.StatusOK || json.Unmarshal(managed.Body.Bytes(), &managedBody) != nil || managedBody.Draft == nil ||
			len(managedBody.Draft.Figures) != 1 || managedBody.Draft.Figures[0].ID != targetFigureID {
			t.Fatalf("resolved draft = status %d, body %s", managed.Code, managed.Body.String())
		}

		for _, filterID := range []string{ids.figure, targetFigureID} {
			result := readPublishedQuery(t, handler, "/api/v1/events?from=-300&to=-200&figure="+filterID)
			if len(result.Events) != 1 || result.Events[0].ID != created.ID {
				t.Fatalf("figure filter %s = %#v", filterID, result.Events)
			}
		}
		if result := readPublishedQuery(t, handler, "/api/v1/events?from=-300&to=-200&q="+url.QueryEscape("秦王政")); len(result.Events) != 0 {
			t.Fatalf("old-name search = %#v", result.Events)
		}
		if result := readPublishedQuery(t, handler, "/api/v1/events?from=-300&to=-200&q="+url.QueryEscape("秦始皇")); len(result.Events) != 1 {
			t.Fatalf("target-name search = %#v", result.Events)
		}

		var successfulMergeAudits, failedMergeAudits int
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'historical_figure.merge' AND outcome = 'success' AND target_id = $1`, ids.figure).Scan(&successfulMergeAudits); err != nil {
			t.Fatal(err)
		}
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'historical_figure.merge' AND outcome = 'failure' AND target_id = $1`, ids.figure).Scan(&failedMergeAudits); err != nil {
			t.Fatal(err)
		}
		if successfulMergeAudits != 1 || failedMergeAudits != 1 {
			t.Fatalf("merge audits = success %d, failure %d", successfulMergeAudits, failedMergeAudits)
		}

		deactivateTarget := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/figures/"+targetFigureID,
			`{"name":"秦始皇","disambiguationLabel":"统一后的称号","status":"inactive"}`,
			[]*http.Cookie{adminSession, adminCSRF}, map[string]string{"If-Match": `"1"`, "X-CSRF-Token": adminCSRF.Value})
		if deactivateTarget.Code != http.StatusConflict || readProblem(t, deactivateTarget).Code != "historical_figure_not_writable" {
			t.Fatalf("deactivate merge target status = %d, body = %s", deactivateTarget.Code, deactivateTarget.Body.String())
		}
	})

	t.Run("database rejects chains and invalid targets", func(t *testing.T) {
		thirdID, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queryPool.Exec(ctx, `INSERT INTO historical_figures (id, name, status, created_by, updated_by) VALUES ($1, '秦代人物', 'active', $2, $2)`, thirdID, administrator.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := queryPool.Exec(ctx, `UPDATE historical_figures SET status = 'merged', merged_into_id = $2 WHERE id = $1`, targetFigureID, thirdID); err == nil {
			t.Fatal("database accepted an uncontrolled merge chain")
		}
		if _, err := queryPool.Exec(ctx, `UPDATE historical_figures SET status = 'inactive' WHERE id = $1`, targetFigureID); err == nil {
			t.Fatal("database allowed a final merge target with aliases to become inactive")
		}
		if _, err := queryPool.Exec(ctx, `UPDATE historical_figures SET status = 'merged', merged_into_id = $1 WHERE id = $1`, thirdID); err == nil {
			t.Fatal("database accepted a self-cycle")
		}
	})
}

func assertPublishedFigure(t *testing.T, handler http.Handler, eventID, figureID, name string) {
	t.Helper()
	response := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+eventID, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("published detail status = %d, body = %s", response.Code, response.Body.String())
	}
	var body publishedEventResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Figures) != 1 || body.Figures[0].ID != figureID || body.Figures[0].Name != name {
		t.Fatalf("published figures = %#v", body.Figures)
	}
}
