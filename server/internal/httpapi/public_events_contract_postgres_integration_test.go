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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhouxinghang/history_wiki/server/internal/auth"
	"github.com/zhouxinghang/history_wiki/server/internal/config"
	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

func TestPublishedEventQueryContractAgainstPostgres(t *testing.T) {
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
	passwordHash, err := auth.HashPassword("unused integration password", passwordParams)
	if err != nil {
		t.Fatal(err)
	}
	administrator := createIntegrationUser(t, ctx, queryPool, "query-contract@example.com", auth.RoleAdministrator, passwordHash, true, database)
	handler := New(database, config.LatestMigrationVersion, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PublicBaseURL: "https://history.test", DummyPasswordHash: passwordHash,
	})

	const (
		crossEraID     = "00000000-0000-7000-8000-000000000101"
		beijingAID     = "00000000-0000-7000-8000-000000000102"
		beijingBID     = "00000000-0000-7000-8000-000000000103"
		zhongyuanID    = "00000000-0000-7000-8000-000000000104"
		alphaID        = "00000000-0000-7000-8000-000000000105"
		regionEastID   = "00000000-0000-7000-8000-000000000201"
		regionEuropeID = "00000000-0000-7000-8000-000000000202"
		placeID        = "00000000-0000-7000-8000-000000000301"
		periodID       = "00000000-0000-7000-8000-000000000401"
		figureID       = "00000000-0000-7000-8000-000000000501"
		topicID        = "00000000-0000-7000-8000-000000000601"
	)

	insertPublicQueryReferences(t, ctx, queryPool, administrator.ID, publicQueryReferenceIDs{
		regionEast: regionEastID, regionEurope: regionEuropeID, place: placeID,
		period: periodID, figure: figureID, topic: topicID,
	})
	fixtures := []publishedQueryFixture{
		{
			id: crossEraID, slug: "cross-era", title: "跨纪元事件", summary: "摘要唯一词",
			narrative: "正文含有 100%_literal 以及完整叙述。", timeKind: "interval",
			startEra: "BCE", startYear: 5, endEra: stringPointer("CE"), endYear: intPointer(5),
			startCoordinate: -4, endCoordinate: 5, category: "政治", prominence: 3, displayOrder: 10,
			regionIDs: []string{regionEastID}, placeIDs: []string{placeID}, periodIDs: []string{periodID},
			figureIDs: []string{figureID}, topicIDs: []string{topicID},
			searchableText: "跨纪元事件\n摘要唯一词\n正文含有 100%_literal 以及完整叙述。\nliteral%_place\n古称\ncaesar人物\n主题唯一词",
		},
		newYearQueryFixture(beijingAID, "beijing-a", "北京事件", 20, 5, 2),
		newYearQueryFixture(beijingBID, "beijing-b", "北京事件", 20, 5, 1),
		newYearQueryFixture(zhongyuanID, "zhongyuan", "中原事件", 20, 5, 3),
		{
			id: alphaID, slug: "alpha", title: "阿尔法事件", summary: "精确日期", narrative: "精确日期事件。",
			timeKind: "exact_date", startEra: "CE", startYear: 20, startMonth: intPointer(1), startDay: intPointer(1),
			startCoordinate: 20, endCoordinate: 20, category: "军事", prominence: 3, displayOrder: 5,
			searchableText: "阿尔法事件\n精确日期\n精确日期事件。",
		},
	}
	for _, fixture := range fixtures {
		insertPublishedQueryFixture(t, ctx, queryPool, administrator.ID, fixture)
	}

	t.Run("closed intervals cover BCE CE point and interval boundaries", func(t *testing.T) {
		atEnd := readPublishedQuery(t, handler, "/api/v1/events?from=5&to=6")
		if !containsPublishedEvent(atEnd.Events, crossEraID) {
			t.Fatalf("closed interval endpoint missing: %#v", atEnd.Events)
		}
		outside := readPublishedQuery(t, handler, "/api/v1/events?from=5.000001&to=6")
		if containsPublishedEvent(outside.Events, crossEraID) {
			t.Fatalf("event outside interval returned: %#v", outside.Events)
		}
		point := readPublishedQuery(t, handler, "/api/v1/events?from=20&to=20.5")
		if !containsPublishedEvent(point.Events, alphaID) {
			t.Fatalf("exact-date point missing: %#v", point.Events)
		}
	})

	t.Run("search is literal Latin-case-insensitive and limited to contracted fields", func(t *testing.T) {
		for _, query := range []string{
			"q=%25_", "q=LiTeRaL%25_PLaCe", "q=CAESAR", "q=%E4%B8%BB%E9%A2%98%E5%94%AF%E4%B8%80%E8%AF%8D",
			"q=%E6%91%98%E8%A6%81%E5%94%AF%E4%B8%80%E8%AF%8D", "q=%E5%AE%8C%E6%95%B4%E5%8F%99%E8%BF%B0",
		} {
			result := readPublishedQuery(t, handler, "/api/v1/events?from=-10&to=10&"+query)
			if len(result.Events) != 1 || result.Events[0].ID != crossEraID {
				t.Fatalf("search %q = %#v", query, result)
			}
		}
		for _, query := range []string{
			"q=%E4%B8%9C%E4%BA%9A%E7%AD%9B%E9%80%89%E5%94%AF%E4%B8%80%E8%AF%8D",
			"q=%E6%97%B6%E6%9C%9F%E4%B8%8D%E5%8F%AF%E6%90%9C%E7%B4%A2%E8%AF%8D", "q=%E6%94%BF%E6%B2%BB",
		} {
			result := readPublishedQuery(t, handler, "/api/v1/events?from=-10&to=10&"+query)
			if len(result.Events) != 0 {
				t.Fatalf("excluded-field search %q = %#v", query, result.Events)
			}
		}
	})

	t.Run("same-dimension OR and cross-dimension plus keyword AND use stable IDs", func(t *testing.T) {
		path := "/api/v1/events?from=-10&to=10&q=literal" +
			"&region=00000000-0000-7000-8000-000000000299&region=" + regionEastID +
			"&period=" + periodID + "&figure=" + figureID + "&category=%E6%94%BF%E6%B2%BB"
		result := readPublishedQuery(t, handler, path)
		if len(result.Events) != 1 || result.Events[0].ID != crossEraID {
			t.Fatalf("combined filters = %#v", result)
		}
		mismatch := readPublishedQuery(t, handler, path+"&category=%E5%86%9B%E4%BA%8B")
		if len(mismatch.Events) != 1 {
			t.Fatalf("same category dimension must remain OR: %#v", mismatch)
		}
		wrongFigure := readPublishedQuery(t, handler, "/api/v1/events?from=-10&to=10&period="+periodID+"&figure=00000000-0000-7000-8000-000000000599")
		if len(wrongFigure.Events) != 0 {
			t.Fatalf("cross-dimension AND = %#v", wrongFigure.Events)
		}
	})

	t.Run("counts prominence thresholds and fixed Chinese ordering are stable", func(t *testing.T) {
		at4000 := readPublishedQuery(t, handler, "/api/v1/events?from=-1000&to=3000")
		if at4000.SourceTotal != 5 || at4000.TotalMatching != 5 || at4000.ReturnedProminence != 1 || len(at4000.Events) != 1 {
			t.Fatalf("4000-year result = %#v", at4000)
		}
		at1200 := readPublishedQuery(t, handler, "/api/v1/events?from=-100&to=1100")
		if at1200.ReturnedProminence != 2 || len(at1200.Events) != 2 {
			t.Fatalf("1200-year result = %#v", at1200)
		}
		focused := readPublishedQuery(t, handler, "/api/v1/events?from=19&to=21")
		gotIDs := make([]string, len(focused.Events))
		for index := range focused.Events {
			gotIDs[index] = focused.Events[index].ID
		}
		wantIDs := []string{alphaID, beijingAID, beijingBID, zhongyuanID}
		if !equalStrings(gotIDs, wantIDs) {
			t.Fatalf("stable order = %v, want %v", gotIDs, wantIDs)
		}
	})

	t.Run("metadata uses period regions while detail and bounds are independent", func(t *testing.T) {
		metadata := performJSON(t, handler, http.MethodGet, "/api/v1/event-metadata", "", nil)
		if metadata.Code != http.StatusOK {
			t.Fatalf("metadata status = %d, body = %s", metadata.Code, metadata.Body.String())
		}
		var body publishedEventMetadataResponse
		if err := json.Unmarshal(metadata.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.PeriodGroups) != 1 || body.PeriodGroups[0].Context.ID != regionEuropeID ||
			len(body.PeriodGroups[0].Periods) != 1 || body.PeriodGroups[0].Periods[0].ID != periodID {
			t.Fatalf("period context metadata = %#v", body.PeriodGroups)
		}
		detail := performJSON(t, handler, http.MethodGet, "/api/v1/events/"+crossEraID, "", nil)
		if detail.Code != http.StatusOK {
			t.Fatalf("detail status = %d, body = %s", detail.Code, detail.Body.String())
		}
		bounds := performJSON(t, handler, http.MethodGet, "/api/v1/event-bounds", "", nil)
		var boundsBody struct {
			HasEvents bool    `json:"hasEvents"`
			Start     float64 `json:"start"`
			End       float64 `json:"end"`
		}
		if bounds.Code != http.StatusOK {
			t.Fatalf("bounds status = %d, body = %s", bounds.Code, bounds.Body.String())
		}
		if err := json.Unmarshal(bounds.Body.Bytes(), &boundsBody); err != nil {
			t.Fatal(err)
		}
		if !boundsBody.HasEvents || boundsBody.Start != -4 || boundsBody.End != 20 {
			t.Fatalf("bounds = %#v", boundsBody)
		}
	})

	t.Run("more than 5000 post-prominence results fail without truncation", func(t *testing.T) {
		insertDensePublishedQueryFixtures(t, ctx, queryPool, administrator.ID, 5_001)
		response := performJSON(t, handler, http.MethodGet, "/api/v1/events?from=99&to=101", "", nil)
		if response.Code != http.StatusUnprocessableEntity || readProblem(t, response).Code != "result_set_too_large" {
			t.Fatalf("large result status = %d, body = %s", response.Code, response.Body.String())
		}
		filtered, err := database.QueryPublishedEvents(ctx, historyevent.PublishedEventQuery{
			From: 99, To: 101, MaximumProminence: 2,
		})
		if err != nil || filtered.TotalMatching != 5_001 || len(filtered.Events) != 0 {
			t.Fatalf("post-prominence limit = result %#v, error %v", filtered, err)
		}
		if _, err := queryPool.Exec(ctx, `
			UPDATE events SET publication_status = 'archived'
			WHERE id = md5('dense-event-5001')::uuid
		`); err != nil {
			t.Fatal(err)
		}
		startedAt := time.Now()
		allowed, err := database.QueryPublishedEvents(ctx, historyevent.PublishedEventQuery{
			From: 99, To: 101, MaximumProminence: 3,
		})
		if err != nil {
			t.Fatal(err)
		}
		if allowed.TotalMatching != 5_000 || len(allowed.Events) != 5_000 {
			t.Fatalf("5000 result = matching %d, events %d", allowed.TotalMatching, len(allowed.Events))
		}
		t.Logf("5,000-row PostgreSQL public query baseline: %s", time.Since(startedAt))
	})

	var indexCount int
	if err := queryPool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'published_event_search_trgm_idx'
	`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatal("published event trigram search index is missing")
	}
}

type publicQueryReferenceIDs struct {
	regionEast, regionEurope, place, period, figure, topic string
}

type publishedQueryFixture struct {
	id, slug, title, summary, narrative, timeKind, startEra string
	startYear, prominence, displayOrder                     int
	startMonth, startDay, endYear                           *int
	endEra                                                  *string
	startCoordinate, endCoordinate                          float64
	category, searchableText                                string
	regionIDs, placeIDs, periodIDs, figureIDs, topicIDs     []string
}

func newYearQueryFixture(id, slug, title string, year, displayOrder, prominence int) publishedQueryFixture {
	return publishedQueryFixture{
		id: id, slug: slug, title: title, summary: title + "摘要", narrative: title + "正文",
		timeKind: "year", startEra: "CE", startYear: year,
		startCoordinate: float64(year), endCoordinate: float64(year), category: "军事",
		prominence: prominence, displayOrder: displayOrder,
		searchableText: title + "\n" + title + "摘要\n" + title + "正文",
	}
}

func insertPublicQueryReferences(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, ids publicQueryReferenceIDs) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO regions (id, name, status, created_by, updated_by) VALUES
		($1, '东亚筛选唯一词', 'active', $3, $3), ($2, '欧洲', 'active', $3, $3)`, ids.regionEast, ids.regionEurope, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO places (id, name, disambiguation_label, status, created_by, updated_by)
		VALUES ($1, 'Literal%_Place', '古称', 'active', $2, $2)`, ids.place, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO historical_periods (id, name, status, created_by, updated_by)
		VALUES ($1, '时期不可搜索词', 'active', $2, $2)`, ids.period, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO period_regions (historical_period_id, region_id) VALUES ($1, $2)`, ids.period, ids.regionEurope); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO historical_figures (id, name, status, created_by, updated_by)
		VALUES ($1, 'CaEsAr人物', 'active', $2, $2)`, ids.figure, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO topic_tags (id, name, status, created_by, updated_by)
		VALUES ($1, '主题唯一词', 'active', $2, $2)`, ids.topic, userID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertPublishedQueryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, fixture publishedQueryFixture) {
	t.Helper()
	revisionID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO events (id, slug, publication_status, created_by, updated_by)
		VALUES ($1, $2, 'unpublished', $3, $3)`, fixture.id, fixture.slug, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO event_revisions (
			id, event_id, revision_no, title, summary, narrative,
			time_kind, start_era, start_year, start_month, start_day, start_circa,
			end_era, end_year, end_circa, start_coordinate, end_coordinate,
			primary_category, prominence, display_order, published_by
		) VALUES (
			$1, $2, 1, $3, $4, $5,
			$6, $7, $8, $9, $10, FALSE,
			$11, $12, FALSE, $13, $14,
			$15, $16, $17, $18
		)`, revisionID, fixture.id, fixture.title, fixture.summary, fixture.narrative,
		fixture.timeKind, fixture.startEra, fixture.startYear, fixture.startMonth, fixture.startDay,
		fixture.endEra, fixture.endYear, fixture.startCoordinate, fixture.endCoordinate,
		fixture.category, fixture.prominence, fixture.displayOrder, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE events SET publication_status = 'published', current_revision_id = $2 WHERE id = $1`, fixture.id, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO published_event_search (event_id, event_revision_id, searchable_text)
		VALUES ($1, $2, lower($3))`, fixture.id, revisionID, fixture.searchableText); err != nil {
		t.Fatal(err)
	}
	insertRevisionIDs := func(table, column string, values []string) {
		for _, value := range values {
			if _, err := tx.Exec(ctx, `INSERT INTO `+table+` (event_revision_id, `+column+`) VALUES ($1, $2)`, revisionID, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	insertRevisionIDs("event_revision_regions", "region_id", fixture.regionIDs)
	insertRevisionIDs("event_revision_places", "place_id", fixture.placeIDs)
	insertRevisionIDs("event_revision_periods", "historical_period_id", fixture.periodIDs)
	insertRevisionIDs("event_revision_figures", "historical_figure_id", fixture.figureIDs)
	insertRevisionIDs("event_revision_topic_tags", "topic_tag_id", fixture.topicIDs)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertDensePublishedQueryFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, count int) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO events (id, slug, publication_status, created_by, updated_by)
		SELECT md5('dense-event-' || value)::uuid, 'dense-' || value, 'unpublished', $2, $2
		FROM generate_series(1, $1) value`, count, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO event_revisions (
			id, event_id, revision_no, title, summary, narrative,
			time_kind, start_era, start_year, start_circa, end_circa,
			start_coordinate, end_coordinate, primary_category, prominence,
			display_order, published_by
		)
		SELECT md5('dense-revision-' || value)::uuid, md5('dense-event-' || value)::uuid,
			1, '高密度历史事件 ' || value, '高密度摘要', '高密度正文',
			'year', 'CE', 100, FALSE, FALSE, 100, 100, '社会', 3, value, $2
		FROM generate_series(1, $1) value`, count, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE events event SET publication_status = 'published', current_revision_id = revision.id
		FROM event_revisions revision
		WHERE event.id = revision.event_id AND event.slug LIKE 'dense-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO published_event_search (event_id, event_revision_id, searchable_text)
		SELECT md5('dense-event-' || value)::uuid, md5('dense-revision-' || value)::uuid,
			'高密度历史事件\n高密度摘要\n高密度正文'
		FROM generate_series(1, $1) value`, count); err != nil {
		t.Fatal(err)
	}
}

func readPublishedQuery(t *testing.T, handler http.Handler, path string) publishedEventQueryResponse {
	t.Helper()
	response := performJSON(t, handler, http.MethodGet, path, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("query %s status = %d, body = %s", path, response.Code, response.Body.String())
	}
	var result publishedEventQueryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func containsPublishedEvent(events []publishedEventResponse, eventID string) bool {
	for _, event := range events {
		if event.ID == eventID {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }
