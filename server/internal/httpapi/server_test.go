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
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	pingErr          error
	migrationVersion int64
	migrationApplied bool
	migrationErr     error
	publishedCount   int64
	countErr         error
}

func (store *fakeStore) Ping(context.Context) error { return store.pingErr }

func (store *fakeStore) MigrationVersion(context.Context) (int64, bool, error) {
	return store.migrationVersion, store.migrationApplied, store.migrationErr
}

func (store *fakeStore) PublishedEventCount(context.Context) (int64, error) {
	return store.publishedCount, store.countErr
}

func TestEmptyCatalogContract(t *testing.T) {
	server := newTestServer(&fakeStore{migrationVersion: 1, migrationApplied: true})
	tests := []struct {
		path string
		want map[string]any
	}{
		{
			path: "/api/v1/event-bounds",
			want: map[string]any{"hasEvents": false, "start": nil, "end": nil},
		},
		{
			path: "/api/v1/event-metadata",
			want: map[string]any{
				"periodGroups": []any{}, "regions": []any{}, "figures": []any{}, "primaryCategories": []any{},
			},
		},
		{
			path: "/api/v1/events?from=-100&to=100",
			want: map[string]any{
				"events": []any{}, "sourceTotal": float64(0), "totalMatching": float64(0), "returnedProminence": float64(3),
				"coveredFrom": float64(-100), "coveredTo": float64(100),
			},
		},
		{
			path: "/api/v1/events?from=-100&to=100&pad=25",
			want: map[string]any{
				"events": []any{}, "sourceTotal": float64(0), "totalMatching": float64(0), "returnedProminence": float64(3),
				"coveredFrom": float64(-125), "coveredTo": float64(125),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}

			var got map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !equalJSON(got, test.want) {
				t.Fatalf("response = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestEmptyCatalogEventDetailIsNotFound(t *testing.T) {
	server := newTestServer(&fakeStore{})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/events/00000000-0000-0000-0000-000000000000", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	assertProblemCode(t, response, "event_not_found")
}

func TestEventQueryValidatesRange(t *testing.T) {
	server := newTestServer(&fakeStore{})
	for _, path := range []string{
		"/api/v1/events",
		"/api/v1/events?from=1&to=1",
		"/api/v1/events?from=not-a-number&to=2",
		"/api/v1/events?from=0&to=2&pad=-1",
		"/api/v1/events?from=0&to=2&pad=not-a-number",
		"/api/v1/events?from=0&to=2&pad=NaN",
		"/api/v1/events?from=0&to=2&pad=Inf",
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want %d", path, response.Code, http.StatusBadRequest)
		}
		assertProblemCode(t, response, "invalid_query")
	}
}

func TestHealthChecksSeparateLivenessAndReadiness(t *testing.T) {
	store := &fakeStore{pingErr: errors.New("offline")}
	server := newTestServer(store)

	live := httptest.NewRecorder()
	server.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("live status = %d, want %d", live.Code, http.StatusOK)
	}

	ready := httptest.NewRecorder()
	server.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want %d", ready.Code, http.StatusServiceUnavailable)
	}
	assertProblemCode(t, ready, "service_not_ready")
}

func TestReadinessRequiresLatestMigration(t *testing.T) {
	server := newTestServer(&fakeStore{migrationVersion: 0, migrationApplied: true})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	assertProblemCode(t, response, "migration_not_ready")
}

func TestRequestIDAndMetricsUseBoundedRouteLabels(t *testing.T) {
	server := newTestServer(&fakeStore{migrationVersion: 1, migrationApplied: true})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/live?secret=not-logged", nil)
	server.ServeHTTP(response, request)
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("response does not expose X-Request-ID")
	}

	metrics := httptest.NewRecorder()
	server.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metrics.Body.String()
	if !strings.Contains(body, `history_wiki_http_requests_total{method="GET",route="/health/live",status="200"} 1`) {
		t.Fatalf("metrics do not contain health route: %s", body)
	}
	if strings.Contains(body, "secret=not-logged") {
		t.Fatal("metrics contain raw query string")
	}
	for _, method := range []string{"CUSTOM-ONE", "CUSTOM-TWO"} {
		server.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/unknown", nil))
	}
	metrics = httptest.NewRecorder()
	server.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `method="OTHER"`) || strings.Contains(metrics.Body.String(), "CUSTOM-ONE") || strings.Contains(metrics.Body.String(), "CUSTOM-TWO") {
		t.Fatalf("custom methods were not normalized: %s", metrics.Body.String())
	}
}

func TestPublicQueryRateLimit(t *testing.T) {
	server := newTestServer(&fakeStore{})
	for index := 0; index < 120; index++ {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/event-bounds", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", index+1, response.Code)
		}
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/event-bounds", nil))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("limited response = %d, retry-after %q", response.Code, response.Header().Get("Retry-After"))
	}
	assertProblemCode(t, response, "public_query_rate_limited")
}

func TestTokenBucketBoundsConcurrentWorkAndKeyMemory(t *testing.T) {
	limiter := newTokenBucketLimiter(10, 15*time.Minute)
	now := time.Unix(100, 0)
	var wait sync.WaitGroup
	allowed := make(chan bool, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ok, _ := limiter.take("one-client", now)
			allowed <- ok
		}()
	}
	wait.Wait()
	close(allowed)
	count := 0
	for ok := range allowed {
		if ok {
			count++
		}
	}
	if count != 10 {
		t.Fatalf("concurrent allowed = %d, want 10", count)
	}
	for index := 0; index < 11_000; index++ {
		_, _ = limiter.take(fmt.Sprintf("client-%d", index), now)
	}
	if len(limiter.buckets) > 10_001 {
		t.Fatalf("rate limiter retained %d keys", len(limiter.buckets))
	}
}

func TestTrustedProxyClientIPUsesRightmostConfiguredHop(t *testing.T) {
	server := &Server{config: Config{TrustedProxyCount: 2}}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.3:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.2")
	if got := server.clientIP(request); got != "203.0.113.10" {
		t.Fatalf("client IP = %q", got)
	}
	request.Header.Set("X-Forwarded-For", "spoofed, 10.0.0.2")
	if got := server.clientIP(request); got != "10.0.0.3" {
		t.Fatalf("invalid forwarding chain trusted: %q", got)
	}
}

func TestDecodeJSONBodyRejectsMoreThanTwoMiB(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":"`+strings.Repeat("x", maxJSONBodyBytes)+`"}`))
	err := decodeJSONBody(httptest.NewRecorder(), request, &struct {
		Value string `json:"value"`
	}{})
	var sizeError *http.MaxBytesError
	if !errors.As(err, &sizeError) {
		t.Fatalf("error = %v, want MaxBytesError", err)
	}
}

func newTestServer(store CatalogStore) http.Handler {
	return New(store, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func assertProblemCode(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != want {
		t.Fatalf("problem code = %q, want %q", body.Code, want)
	}
}

func equalJSON(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
