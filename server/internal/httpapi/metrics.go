package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhouxinghang/history_wiki/server/internal/store"
)

var requestDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2.5, 5}

type metricsRegistry struct {
	inFlight atomic.Int64
	mu       sync.Mutex
	http     map[httpMetricKey]*httpMetric
	results  map[operationMetricKey]uint64
}

type httpMetricKey struct{ method, route, status string }

type httpMetric struct {
	count   uint64
	sum     float64
	buckets []uint64
}

type operationMetricKey struct{ operation, outcome string }

func newMetricsRegistry() *metricsRegistry {
	return &metricsRegistry{http: make(map[httpMetricKey]*httpMetric), results: make(map[operationMetricKey]uint64)}
}

func (metrics *metricsRegistry) recordHTTP(method, route string, status int, duration time.Duration) {
	key := httpMetricKey{method: method, route: route, status: strconv.Itoa(status)}
	metrics.mu.Lock()
	value := metrics.http[key]
	if value == nil {
		value = &httpMetric{buckets: make([]uint64, len(requestDurationBuckets))}
		metrics.http[key] = value
	}
	value.count++
	value.sum += duration.Seconds()
	for index, boundary := range requestDurationBuckets {
		if duration.Seconds() <= boundary {
			value.buckets[index]++
		}
	}
	metrics.mu.Unlock()
}

func (metrics *metricsRegistry) recordResult(operation, outcome string) {
	metrics.mu.Lock()
	metrics.results[operationMetricKey{operation: operation, outcome: outcome}]++
	metrics.mu.Unlock()
}

func (server *Server) metricsHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	server.metrics.writePrometheus(writer, server.store)
}

func (metrics *metricsRegistry) writePrometheus(writer http.ResponseWriter, catalogStore CatalogStore) {
	metrics.mu.Lock()
	httpSnapshot := make(map[httpMetricKey]httpMetric, len(metrics.http))
	for key, value := range metrics.http {
		copyValue := *value
		copyValue.buckets = append([]uint64(nil), value.buckets...)
		httpSnapshot[key] = copyValue
	}
	resultSnapshot := make(map[operationMetricKey]uint64, len(metrics.results))
	for key, value := range metrics.results {
		resultSnapshot[key] = value
	}
	metrics.mu.Unlock()

	fmt.Fprintln(writer, "# HELP history_wiki_http_requests_total HTTP requests completed.")
	fmt.Fprintln(writer, "# TYPE history_wiki_http_requests_total counter")
	fmt.Fprintln(writer, "# HELP history_wiki_http_request_duration_seconds HTTP request duration.")
	fmt.Fprintln(writer, "# TYPE history_wiki_http_request_duration_seconds histogram")
	keys := make([]httpMetricKey, 0, len(httpSnapshot))
	for key := range httpSnapshot {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].method+keys[i].route+keys[i].status < keys[j].method+keys[j].route+keys[j].status
	})
	for _, key := range keys {
		value := httpSnapshot[key]
		labels := fmt.Sprintf(`method=%q,route=%q,status=%q`, escapeMetricLabel(key.method), escapeMetricLabel(key.route), key.status)
		fmt.Fprintf(writer, "history_wiki_http_requests_total{%s} %d\n", labels, value.count)
		for index, boundary := range requestDurationBuckets {
			fmt.Fprintf(writer, "history_wiki_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(boundary, 'f', -1, 64), value.buckets[index])
		}
		fmt.Fprintf(writer, "history_wiki_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, value.count)
		fmt.Fprintf(writer, "history_wiki_http_request_duration_seconds_sum{%s} %g\n", labels, value.sum)
		fmt.Fprintf(writer, "history_wiki_http_request_duration_seconds_count{%s} %d\n", labels, value.count)
	}
	fmt.Fprintln(writer, "# HELP history_wiki_operation_results_total Publication and import outcomes.")
	fmt.Fprintln(writer, "# TYPE history_wiki_operation_results_total counter")
	resultKeys := make([]operationMetricKey, 0, len(resultSnapshot))
	for key := range resultSnapshot {
		resultKeys = append(resultKeys, key)
	}
	sort.Slice(resultKeys, func(i, j int) bool {
		return resultKeys[i].operation+resultKeys[i].outcome < resultKeys[j].operation+resultKeys[j].outcome
	})
	for _, key := range resultKeys {
		fmt.Fprintf(writer, "history_wiki_operation_results_total{operation=%q,outcome=%q} %d\n", key.operation, key.outcome, resultSnapshot[key])
	}
	fmt.Fprintln(writer, "# HELP history_wiki_http_requests_in_flight HTTP requests currently being served.")
	fmt.Fprintln(writer, "# TYPE history_wiki_http_requests_in_flight gauge")
	fmt.Fprintf(writer, "history_wiki_http_requests_in_flight %d\n", metrics.inFlight.Load())

	provider, ok := catalogStore.(interface {
		DatabaseMetrics() (store.DatabasePoolMetrics, []store.DatabaseQueryMetric)
	})
	if !ok {
		return
	}
	pool, queries := provider.DatabaseMetrics()
	fmt.Fprintln(writer, "# HELP history_wiki_database_pool_connections PostgreSQL pool connections.")
	fmt.Fprintln(writer, "# TYPE history_wiki_database_pool_connections gauge")
	fmt.Fprintf(writer, "history_wiki_database_pool_connections{state=\"acquired\"} %d\n", pool.AcquiredConns)
	fmt.Fprintf(writer, "history_wiki_database_pool_connections{state=\"idle\"} %d\n", pool.IdleConns)
	fmt.Fprintf(writer, "history_wiki_database_pool_connections{state=\"total\"} %d\n", pool.TotalConns)
	fmt.Fprintf(writer, "history_wiki_database_pool_connections{state=\"maximum\"} %d\n", pool.MaxConns)
	fmt.Fprintln(writer, "# HELP history_wiki_database_pool_acquires_total PostgreSQL pool acquisitions.")
	fmt.Fprintln(writer, "# TYPE history_wiki_database_pool_acquires_total counter")
	fmt.Fprintf(writer, "history_wiki_database_pool_acquires_total %d\n", pool.AcquireCount)
	sort.Slice(queries, func(i, j int) bool { return queries[i].Operation < queries[j].Operation })
	for _, query := range queries {
		fmt.Fprintf(writer, "history_wiki_database_queries_total{operation=%q,outcome=\"success\"} %d\n", query.Operation, query.Count-query.Errors)
		fmt.Fprintf(writer, "history_wiki_database_queries_total{operation=%q,outcome=\"error\"} %d\n", query.Operation, query.Errors)
		fmt.Fprintf(writer, "history_wiki_database_query_duration_seconds_sum{operation=%q} %g\n", query.Operation, query.DurationSeconds)
		fmt.Fprintf(writer, "history_wiki_database_query_duration_seconds_count{operation=%q} %d\n", query.Operation, query.Count)
	}
}

func metricRoute(request *http.Request) string {
	route := chi.RouteContext(request.Context()).RoutePattern()
	if route == "" {
		return "unmatched"
	}
	return route
}

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func escapeMetricLabel(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}
