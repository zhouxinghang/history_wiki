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

func TestPlaceAndHistoricalPeriodManagementHTTPFlowAgainstPostgres(t *testing.T) {
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
	administrator := createIntegrationUser(t, ctx, queryPool, "context-admin@example.com", auth.RoleAdministrator, passwordHash, true, database)
	createIntegrationUser(t, ctx, queryPool, "context-editor@example.com", auth.RoleEditor, passwordHash, false, database)

	now := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)
	dummyHash, err := auth.HashPassword("invalid-password-placeholder", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL: "https://history.test", SessionIdleTimeout: 8 * time.Hour,
		SessionMaxLifetime: 7 * 24 * time.Hour, Now: func() time.Time { return now }, DummyPasswordHash: dummyHash,
	})

	t.Run("ordinary readers cannot use place or historical period APIs", func(t *testing.T) {
		for _, path := range []string{"/api/v1/admin/places", "/api/v1/admin/periods"} {
			response := performJSON(t, handler, http.MethodGet, path, "", nil)
			if response.Code != http.StatusUnauthorized || readProblem(t, response).Code != "authentication_required" {
				t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body.String())
			}
		}
	})

	editorSession, editorCSRF := loginIntegrationUser(t, handler, "context-editor@example.com")
	administratorSession, administratorCSRF := loginIntegrationUser(t, handler, administrator.Email)

	createRegionForContext := func(key, name string) regionResponse {
		t.Helper()
		response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/regions", `{"name":"`+name+`","disambiguationLabel":null}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": key, "X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("create region status = %d, body = %s", response.Code, response.Body.String())
		}
		var region regionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &region); err != nil {
			t.Fatal(err)
		}
		return region
	}
	eastAsia := createRegionForContext("create-east-asia", "东亚")
	centralAsia := createRegionForContext("create-central-asia", "中亚")

	createEntity := func(path, key, body string) contextEntityResponse {
		t.Helper()
		response := performJSONWithHeaders(t, handler, http.MethodPost, path, body, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": key, "X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
		var entity contextEntityResponse
		if err := json.Unmarshal(response.Body.Bytes(), &entity); err != nil {
			t.Fatal(err)
		}
		return entity
	}

	firstPlace := createEntity("/api/v1/admin/places", "create-han-changan", `{"name":"长安","disambiguationLabel":"汉代都城","regionIds":["`+eastAsia.ID+`","`+centralAsia.ID+`"]}`)
	secondPlace := createEntity("/api/v1/admin/places", "create-tang-changan", `{"name":"长安","disambiguationLabel":"唐代都城","regionIds":["`+eastAsia.ID+`"]}`)
	if firstPlace.ID == secondPlace.ID || firstPlace.ID[14] != '7' || len(firstPlace.Regions) != 2 {
		t.Fatalf("same-name places = %#v and %#v", firstPlace, secondPlace)
	}

	firstPeriod := createEntity("/api/v1/admin/periods", "create-warring-states-china", `{"name":"战国","disambiguationLabel":"中国史分期","regionIds":["`+eastAsia.ID+`","`+centralAsia.ID+`"]}`)
	secondPeriod := createEntity("/api/v1/admin/periods", "create-warring-states-other", `{"name":"战国","disambiguationLabel":"同名分期示例","regionIds":["`+centralAsia.ID+`"]}`)
	if firstPeriod.ID == secondPeriod.ID || len(firstPeriod.Regions) != 2 {
		t.Fatalf("same-name historical periods = %#v and %#v", firstPeriod, secondPeriod)
	}

	t.Run("idempotent replays are audited in their committed transaction", func(t *testing.T) {
		tests := []struct {
			path       string
			key        string
			body       string
			id         string
			targetType string
		}{
			{path: "/api/v1/admin/places", key: "create-han-changan", body: `{"name":"长安","disambiguationLabel":"汉代都城","regionIds":["` + centralAsia.ID + `","` + eastAsia.ID + `"]}`, id: firstPlace.ID, targetType: "place"},
			{path: "/api/v1/admin/periods", key: "create-warring-states-china", body: `{"name":"战国","disambiguationLabel":"中国史分期","regionIds":["` + centralAsia.ID + `","` + eastAsia.ID + `"]}`, id: firstPeriod.ID, targetType: "historical_period"},
		}
		for _, test := range tests {
			response := performJSONWithHeaders(t, handler, http.MethodPost, test.path, test.body, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
				"Idempotency-Key": test.key, "X-CSRF-Token": editorCSRF.Value,
			})
			if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "true" {
				t.Fatalf("%s replay status = %d, headers = %#v, body = %s", test.path, response.Code, response.Header(), response.Body.String())
			}
			var replayed contextEntityResponse
			if err := json.Unmarshal(response.Body.Bytes(), &replayed); err != nil {
				t.Fatal(err)
			}
			if replayed.ID != test.id {
				t.Fatalf("%s replayed id = %s, want %s", test.path, replayed.ID, test.id)
			}
			var replayAudits int
			if err := queryPool.QueryRow(ctx, `
				SELECT count(*) FROM audit_logs
				WHERE target_type = $1 AND target_id = $2 AND outcome = 'success'
				  AND details @> '{"replayed": true}'::jsonb
			`, test.targetType, test.id).Scan(&replayAudits); err != nil {
				t.Fatal(err)
			}
			if replayAudits != 1 {
				t.Fatalf("%s replay audit count = %d, want 1", test.path, replayAudits)
			}
		}
	})

	t.Run("editor can query and select entities with their contexts", func(t *testing.T) {
		places := performJSON(t, handler, http.MethodGet, "/api/v1/admin/places?q=%E5%94%90%E4%BB%A3", "", []*http.Cookie{editorSession})
		if places.Code != http.StatusOK {
			t.Fatalf("places status = %d, body = %s", places.Code, places.Body.String())
		}
		var placeBody placeListResponse
		if err := json.Unmarshal(places.Body.Bytes(), &placeBody); err != nil {
			t.Fatal(err)
		}
		if len(placeBody.Places) != 1 || placeBody.Places[0].ID != secondPlace.ID {
			t.Fatalf("places = %#v", placeBody.Places)
		}

		periods := performJSON(t, handler, http.MethodGet, "/api/v1/admin/periods?q=%E4%B8%AD%E5%9B%BD", "", []*http.Cookie{administratorSession})
		if periods.Code != http.StatusOK {
			t.Fatalf("historical periods status = %d, body = %s", periods.Code, periods.Body.String())
		}
		var periodBody historicalPeriodListResponse
		if err := json.Unmarshal(periods.Body.Bytes(), &periodBody); err != nil {
			t.Fatal(err)
		}
		if len(periodBody.Periods) != 1 || periodBody.Periods[0].ID != firstPeriod.ID {
			t.Fatalf("historical periods = %#v", periodBody.Periods)
		}
	})

	t.Run("invalid and missing region relationships are rejected", func(t *testing.T) {
		missingPeriod := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/periods", `{"name":"无语境时期","disambiguationLabel":null,"regionIds":[]}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": "missing-period-context", "X-CSRF-Token": editorCSRF.Value,
		})
		if missingPeriod.Code != http.StatusUnprocessableEntity || readProblem(t, missingPeriod).Code != "invalid_historical_period" {
			t.Fatalf("missing period context status = %d, body = %s", missingPeriod.Code, missingPeriod.Body.String())
		}

		missingRegion := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/places", `{"name":"不存在地区地点","disambiguationLabel":null,"regionIds":["00000000-0000-7000-8000-000000000099"]}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"Idempotency-Key": "missing-region-place", "X-CSRF-Token": editorCSRF.Value,
		})
		if missingRegion.Code != http.StatusUnprocessableEntity || readProblem(t, missingRegion).Code != "invalid_region_association" {
			t.Fatalf("missing region status = %d, body = %s", missingRegion.Code, missingRegion.Body.String())
		}
	})

	t.Run("create and update payloads enforce the OpenAPI field contract", func(t *testing.T) {
		createBodies := []string{
			`{"name":"缺少消歧字段","regionIds":[]}`,
			`{"name":"缺少地区字段","disambiguationLabel":null}`,
			`{"name":"不应接受状态","disambiguationLabel":null,"regionIds":[],"status":"active"}`,
		}
		for index, body := range createBodies {
			response := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/admin/places", body, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
				"Idempotency-Key": "strict-place-payload-" + string(rune('a'+index)), "X-CSRF-Token": editorCSRF.Value,
			})
			if response.Code != http.StatusBadRequest || readProblem(t, response).Code != "invalid_request" {
				t.Fatalf("strict create body %s: status = %d, body = %s", body, response.Code, response.Body.String())
			}
		}

		updateBodies := []string{
			`{"name":"长安","status":"active","regionIds":["` + eastAsia.ID + `"]}`,
			`{"name":"长安","disambiguationLabel":"汉代都城","status":"active"}`,
		}
		for _, body := range updateBodies {
			response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/places/"+firstPlace.ID, body, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
				"If-Match": `"1"`, "X-CSRF-Token": administratorCSRF.Value,
			})
			if response.Code != http.StatusBadRequest || readProblem(t, response).Code != "invalid_request" {
				t.Fatalf("strict update body %s: status = %d, body = %s", body, response.Code, response.Body.String())
			}
		}
	})

	t.Run("editor cannot replace relationships", func(t *testing.T) {
		response := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/places/"+firstPlace.ID, `{"name":"长安","disambiguationLabel":"汉代都城","status":"active","regionIds":["`+eastAsia.ID+`"]}`, []*http.Cookie{editorSession, editorCSRF}, map[string]string{
			"If-Match": `"1"`, "X-CSRF-Token": editorCSRF.Value,
		})
		if response.Code != http.StatusForbidden || readProblem(t, response).Code != "permission_denied" {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("administrator updates relationships with optimistic locking", func(t *testing.T) {
		placeBody := `{"name":"长安","disambiguationLabel":"汉代都城","status":"active","regionIds":["` + eastAsia.ID + `"]}`
		updatedPlace := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/places/"+firstPlace.ID, placeBody, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match": `"1"`, "X-CSRF-Token": administratorCSRF.Value,
		})
		if updatedPlace.Code != http.StatusOK || updatedPlace.Header().Get("ETag") != `"2"` {
			t.Fatalf("place update status = %d, body = %s", updatedPlace.Code, updatedPlace.Body.String())
		}
		stalePlace := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/places/"+firstPlace.ID, placeBody, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match": `"1"`, "X-CSRF-Token": administratorCSRF.Value,
		})
		if stalePlace.Code != http.StatusConflict || readProblem(t, stalePlace).Code != "place_version_conflict" {
			t.Fatalf("stale place status = %d, body = %s", stalePlace.Code, stalePlace.Body.String())
		}

		periodBody := `{"name":"战国时期","disambiguationLabel":"中国史分期","status":"active","regionIds":["` + eastAsia.ID + `"]}`
		updatedPeriod := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/periods/"+firstPeriod.ID, periodBody, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match": `"1"`, "X-CSRF-Token": administratorCSRF.Value,
		})
		if updatedPeriod.Code != http.StatusOK || updatedPeriod.Header().Get("ETag") != `"2"` {
			t.Fatalf("period update status = %d, body = %s", updatedPeriod.Code, updatedPeriod.Body.String())
		}
		stalePeriod := performJSONWithHeaders(t, handler, http.MethodPatch, "/api/v1/admin/periods/"+firstPeriod.ID, periodBody, []*http.Cookie{administratorSession, administratorCSRF}, map[string]string{
			"If-Match": `"1"`, "X-CSRF-Token": administratorCSRF.Value,
		})
		if stalePeriod.Code != http.StatusConflict || readProblem(t, stalePeriod).Code != "historical_period_version_conflict" {
			t.Fatalf("stale period status = %d, body = %s", stalePeriod.Code, stalePeriod.Body.String())
		}

		var placeRegionCount, periodRegionCount int
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM place_regions WHERE place_id = $1`, firstPlace.ID).Scan(&placeRegionCount); err != nil {
			t.Fatal(err)
		}
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM period_regions WHERE historical_period_id = $1`, firstPeriod.ID).Scan(&periodRegionCount); err != nil {
			t.Fatal(err)
		}
		if placeRegionCount != 1 || periodRegionCount != 1 {
			t.Fatalf("stored relationship counts = place %d, historical period %d", placeRegionCount, periodRegionCount)
		}
	})

	t.Run("PostgreSQL rejects an active historical period without a region", func(t *testing.T) {
		periodID, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		tx, err := queryPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, `
			INSERT INTO historical_periods (id, name, status, created_by, updated_by)
			VALUES ($1, '无地区时期', 'active', $2, $2)
		`, periodID, administrator.ID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err == nil {
			t.Fatal("active historical period without a region unexpectedly committed")
		}
	})

	t.Run("place and historical period identities are immutable", func(t *testing.T) {
		replacementID, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := queryPool.Exec(ctx, `UPDATE places SET id = $2 WHERE id = $1`, firstPlace.ID, replacementID); err == nil {
			t.Fatal("place UUID update unexpectedly succeeded")
		}
		if _, err := queryPool.Exec(ctx, `UPDATE historical_periods SET id = $2 WHERE id = $1`, firstPeriod.ID, replacementID); err == nil {
			t.Fatal("historical period UUID update unexpectedly succeeded")
		}
	})

	t.Run("relationship identities cannot be reassigned with UPDATE", func(t *testing.T) {
		if _, err := queryPool.Exec(ctx, `
			UPDATE place_regions SET region_id = $2
			WHERE place_id = $1 AND region_id = $3
		`, firstPlace.ID, centralAsia.ID, eastAsia.ID); err == nil {
			t.Fatal("place relationship primary key update unexpectedly succeeded")
		}
		if _, err := queryPool.Exec(ctx, `
			UPDATE period_regions SET historical_period_id = $2
			WHERE historical_period_id = $1 AND region_id = $3
		`, firstPeriod.ID, secondPeriod.ID, eastAsia.ID); err == nil {
			t.Fatal("historical period relationship primary key update unexpectedly succeeded")
		}

		var firstPeriodRegions int
		if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM period_regions WHERE historical_period_id = $1`, firstPeriod.ID).Scan(&firstPeriodRegions); err != nil {
			t.Fatal(err)
		}
		if firstPeriodRegions != 1 {
			t.Fatalf("first active period regions = %d, want 1", firstPeriodRegions)
		}
	})

	var successfulPlaceWrites, successfulPeriodWrites int
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_type = 'place' AND outcome = 'success'`).Scan(&successfulPlaceWrites); err != nil {
		t.Fatal(err)
	}
	if err := queryPool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_type = 'historical_period' AND outcome = 'success'`).Scan(&successfulPeriodWrites); err != nil {
		t.Fatal(err)
	}
	if successfulPlaceWrites != 4 || successfulPeriodWrites != 4 {
		t.Fatalf("successful context entity audits = places %d, periods %d", successfulPlaceWrites, successfulPeriodWrites)
	}
}
