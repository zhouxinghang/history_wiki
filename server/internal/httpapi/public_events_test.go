package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
)

func TestProminenceForSpanBoundaries(t *testing.T) {
	tests := []struct {
		span float64
		want int
	}{
		{span: 1199.999999, want: 3},
		{span: 1200, want: 2},
		{span: 3999.999999, want: 2},
		{span: 4000, want: 1},
	}
	for _, test := range tests {
		if got := prominenceForSpan(test.span); got != test.want {
			t.Fatalf("prominenceForSpan(%v) = %d, want %d", test.span, got, test.want)
		}
	}
}

type fakePublicEventStore struct {
	captured *historyevent.PublishedEventQuery
	result   historyevent.PublishedEventQueryResult
}

func (store *fakePublicEventStore) Ping(context.Context) error { return nil }

func (store *fakePublicEventStore) MigrationVersion(context.Context) (int64, bool, error) {
	return 1, true, nil
}

func (store *fakePublicEventStore) PublishedEventCount(context.Context) (int64, error) {
	return 0, nil
}

func (store *fakePublicEventStore) PublishedEventBounds(context.Context) (*historyevent.PublishedEventBounds, error) {
	return nil, nil
}

func (store *fakePublicEventStore) PublishedEventMetadata(context.Context) (historyevent.PublishedEventMetadata, error) {
	return historyevent.PublishedEventMetadata{}, nil
}

func (store *fakePublicEventStore) QueryPublishedEvents(_ context.Context, query historyevent.PublishedEventQuery) (historyevent.PublishedEventQueryResult, error) {
	if store.captured != nil {
		*store.captured = query
	}
	return store.result, nil
}

func (store *fakePublicEventStore) PublishedEventByID(context.Context, string) (historyevent.PublishedEvent, error) {
	return historyevent.PublishedEvent{}, nil
}

func TestEventQueryPaddingSeparatesCountsFromEventWindow(t *testing.T) {
	var captured historyevent.PublishedEventQuery
	store := &fakePublicEventStore{
		captured: &captured,
		result: historyevent.PublishedEventQueryResult{
			SourceTotal: 12, TotalMatching: 4,
		},
	}
	server := New(store, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/events?from=100&to=300&pad=50", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if captured.From != 100 || captured.To != 300 {
		t.Fatalf("count window = [%v, %v], want [100, 300]", captured.From, captured.To)
	}
	if captured.EventFrom != 50 || captured.EventTo != 350 {
		t.Fatalf("event window = [%v, %v], want [50, 350]", captured.EventFrom, captured.EventTo)
	}
	if captured.MaximumProminence != prominenceForSpan(200) {
		t.Fatalf("maximum prominence = %d, want %d", captured.MaximumProminence, prominenceForSpan(200))
	}

	var body publishedEventQueryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CoveredFrom != 50 || body.CoveredTo != 350 || body.TotalMatching != 4 || body.SourceTotal != 12 {
		t.Fatalf("response = %#v", body)
	}
}
