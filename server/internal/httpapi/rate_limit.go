package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type tokenBucketLimiter struct {
	mu       sync.Mutex
	capacity float64
	period   time.Duration
	buckets  map[string]tokenBucket
}

type tokenBucket struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

func newTokenBucketLimiter(capacity int, period time.Duration) *tokenBucketLimiter {
	return &tokenBucketLimiter{capacity: float64(capacity), period: period, buckets: make(map[string]tokenBucket)}
}

func (limiter *tokenBucketLimiter) available(key string, now time.Time) (bool, time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	key = limiter.boundedKey(key, now)
	bucket := limiter.refill(key, now)
	if bucket.tokens >= 1 {
		return true, 0
	}
	return false, time.Duration(math.Ceil((1 - bucket.tokens) * float64(limiter.period) / limiter.capacity))
}

func (limiter *tokenBucketLimiter) take(key string, now time.Time) (bool, time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	key = limiter.boundedKey(key, now)
	bucket := limiter.refill(key, now)
	if bucket.tokens < 1 {
		return false, time.Duration(math.Ceil((1 - bucket.tokens) * float64(limiter.period) / limiter.capacity))
	}
	bucket.tokens--
	limiter.buckets[key] = bucket
	return true, 0
}

func (limiter *tokenBucketLimiter) refund(key string, now time.Time) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	key = limiter.boundedKey(key, now)
	bucket := limiter.refill(key, now)
	bucket.tokens = math.Min(limiter.capacity, bucket.tokens+1)
	limiter.buckets[key] = bucket
}

func (limiter *tokenBucketLimiter) boundedKey(key string, now time.Time) string {
	if _, exists := limiter.buckets[key]; exists {
		return key
	}
	if len(limiter.buckets) < 10_000 {
		return key
	}
	cutoff := now.Add(-2 * limiter.period)
	for candidate, value := range limiter.buckets {
		if value.lastSeen.Before(cutoff) {
			delete(limiter.buckets, candidate)
		}
	}
	if len(limiter.buckets) >= 10_000 {
		return "__overflow__"
	}
	return key
}

func (limiter *tokenBucketLimiter) refill(key string, now time.Time) tokenBucket {
	bucket, exists := limiter.buckets[key]
	if !exists {
		bucket = tokenBucket{tokens: limiter.capacity, updated: now}
	}
	if elapsed := now.Sub(bucket.updated); elapsed > 0 {
		bucket.tokens = math.Min(limiter.capacity, bucket.tokens+elapsed.Seconds()*limiter.capacity/limiter.period.Seconds())
		bucket.updated = now
	}
	bucket.lastSeen = now
	limiter.buckets[key] = bucket
	return bucket
}

func (server *Server) limitPublicQueries(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		allowed, retryAfter := server.publicQueryLimiter.take(server.clientIP(request), server.config.Now())
		if !allowed {
			writeRateLimitProblem(writer, request, retryAfter, "public_query_rate_limited", "公开查询过于频繁")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (server *Server) limitManagementWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions ||
			(request.Method == http.MethodPost && (request.URL.Path == "/api/v1/admin/event-imports" || request.URL.Path == "/api/v1/admin/event-imports/preflight")) {
			next.ServeHTTP(writer, request)
			return
		}
		current := principalFromContext(request.Context())
		allowed, retryAfter := server.managementWriteLimiter.take(current.Session.User.ID, server.config.Now())
		if !allowed {
			writeRateLimitProblem(writer, request, retryAfter, "management_write_rate_limited", "管理写入过于频繁")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (server *Server) limitFormalImports(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		current := principalFromContext(request.Context())
		allowed, retryAfter := server.formalImportLimiter.take(current.Session.User.ID, server.config.Now())
		if !allowed {
			writeRateLimitProblem(writer, request, retryAfter, "event_import_rate_limited", "正式导入过于频繁")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func writeRateLimitProblem(writer http.ResponseWriter, request *http.Request, retryAfter time.Duration, code, title string) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	writer.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeProblem(writer, request, http.StatusTooManyRequests, code, title, "请求超过单实例服务的安全速率，请稍后重试。")
}
