package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newRouter(handlers ...gin.HandlerFunc) *gin.Engine {
	router := gin.New()
	all := append(handlers, func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	router.POST("/test", all...)
	router.GET("/test", all...)
	return router
}

// ---------------------------------------------------------------------------
// APIKeyAuth
// ---------------------------------------------------------------------------

func TestAuthDisabledWhenNoKeys(t *testing.T) {
	router := newRouter(APIKeyAuth(nil))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with auth disabled, got %d", w.Code)
	}
}

func TestAuthRejectsMissingKey(t *testing.T) {
	router := newRouter(APIKeyAuth([]string{"secret"}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without key, got %d", w.Code)
	}
}

func TestAuthRejectsWrongKey(t *testing.T) {
	router := newRouter(APIKeyAuth([]string{"secret"}))
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-API-Key", "wrong")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", w.Code)
	}
}

func TestAuthAcceptsBearerToken(t *testing.T) {
	router := newRouter(APIKeyAuth([]string{"secret"}))
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with bearer token, got %d", w.Code)
	}
}

func TestAuthAcceptsAPIKeyHeader(t *testing.T) {
	router := newRouter(APIKeyAuth([]string{"first", "second"}))
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-API-Key", "second")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with valid X-API-Key, got %d", w.Code)
	}
}

func TestKeyAllowedEmptyCandidate(t *testing.T) {
	if KeyAllowed([]string{"secret"}, "") {
		t.Error("empty candidate must never be allowed")
	}
}

// ---------------------------------------------------------------------------
// CORS
// ---------------------------------------------------------------------------

func TestCORSAllowsListedOrigin(t *testing.T) {
	router := newRouter(CORS([]string{"http://localhost:5173"}))
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("expected reflected origin, got %q", got)
	}
	exposed := w.Header().Get("Access-Control-Expose-Headers")
	for _, h := range []string{"X-TrustAgent-Trace-Id", "X-TrustAgent-Action", "X-TrustAgent-Audit", "X-Request-ID", "ETag"} {
		if !strings.Contains(exposed, h) {
			t.Errorf("Access-Control-Expose-Headers %q lacks %s", exposed, h)
		}
	}
	if strings.Contains(exposed, "*") {
		t.Error("exposed headers are listed explicitly, never *")
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-TrustAgent-Key") {
		t.Errorf("allowed headers %q lack X-TrustAgent-Key", got)
	}
}

// TestCORSHeadersExact pins the cross-origin dashboard's CORS headers to the SPEC §2.4 lists
// (D43): no Authorization or X-API-Key, no wildcard, Max-Age 600.
func TestCORSHeadersExact(t *testing.T) {
	// Global middleware, as in production: it answers the preflight without an OPTIONS route.
	router := gin.New()
	router.Use(CORS([]string{"http://localhost:5173"}))
	router.PATCH("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	req.Header.Set("Access-Control-Request-Headers", "content-type, if-match, x-trustagent-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	want := map[string]string{
		"Access-Control-Allow-Origin":  "http://localhost:5173",
		"Access-Control-Allow-Methods": "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type, If-Match, X-TrustAgent-Key, X-TrustAgent-CSRF, X-TrustAgent-Route, X-TrustAgent-Mode, X-Request-ID",
		"Access-Control-Expose-Headers": "ETag, Retry-After, X-Request-ID, X-TrustAgent-Trace-Id, X-TrustAgent-Route, X-TrustAgent-Mode, " +
			"X-TrustAgent-Action, X-TrustAgent-Would-Action, X-TrustAgent-Status, X-TrustAgent-Verdict, X-TrustAgent-Risk, " +
			"X-TrustAgent-Calibrated, X-TrustAgent-Degraded, X-TrustAgent-Reasons, X-TrustAgent-Checked-Against, " +
			"X-TrustAgent-Decision-Model, X-TrustAgent-Gate-Ms, X-TrustAgent-Overhead-Ms, X-TrustAgent-Audit, X-TrustAgent-Source",
		"Access-Control-Max-Age": "600",
		"Vary":                   "Origin",
	}
	for h, v := range want {
		if got := w.Header().Get(h); got != v {
			t.Errorf("%s = %q\nwant %q", h, got, v)
		}
	}
	if w.Code != http.StatusNoContent {
		t.Errorf("preflight status %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q: the cross-origin dashboard sends no cookies", got)
	}
	allowed := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
	for _, dropped := range []string{"authorization", "x-api-key"} {
		if strings.Contains(allowed, dropped) {
			t.Errorf("Access-Control-Allow-Headers still allows %s (D43)", dropped)
		}
	}
}

func TestCORSIgnoresUnlistedOrigin(t *testing.T) {
	router := newRouter(CORS([]string{"http://localhost:5173"}))
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no CORS header for unlisted origin, got %q", got)
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	// CORS is global middleware in production (router.Use), which gin runs
	// even when no OPTIONS route is registered — mirror that here.
	router := gin.New()
	router.Use(CORS([]string{"http://localhost:5173"}))
	router.GET("/test", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	req := httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for preflight, got %d", w.Code)
	}
}

func TestOriginAllowed(t *testing.T) {
	allowed := []string{"http://localhost:5173"}

	if !OriginAllowed(allowed, "") {
		t.Error("empty origin (non-browser client) should be allowed")
	}
	if !OriginAllowed(allowed, "http://localhost:5173") {
		t.Error("listed origin should be allowed")
	}
	if OriginAllowed(allowed, "https://evil.example.com") {
		t.Error("unlisted origin must be rejected")
	}
}

// ---------------------------------------------------------------------------
// RateLimit
// ---------------------------------------------------------------------------

func TestMemoryLimiterEnforcesLimit(t *testing.T) {
	limiter := NewMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if !limiter.Allow(ctx, "client", 3, time.Minute) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if limiter.Allow(ctx, "client", 3, time.Minute) {
		t.Error("4th request should be denied")
	}
	if !limiter.Allow(ctx, "other-client", 3, time.Minute) {
		t.Error("different client must have its own bucket")
	}
}

func TestRateLimitMiddlewareReturns429(t *testing.T) {
	router := newRouter(RateLimit(NewMemoryLimiter(), 2, time.Minute, nil))

	statuses := []int{}
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))
		statuses = append(statuses, w.Code)
	}

	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK {
		t.Errorf("first two requests should pass, got %v", statuses)
	}
	if statuses[2] != http.StatusTooManyRequests {
		t.Errorf("third request should be 429, got %d", statuses[2])
	}
}

func TestRateLimitKeyedByAPIKey(t *testing.T) {
	router := newRouter(RateLimit(NewMemoryLimiter(), 1, time.Minute, nil))

	first := httptest.NewRequest("GET", "/test", nil)
	first.Header.Set("X-API-Key", "alice")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, first)
	if w.Code != http.StatusOK {
		t.Fatalf("alice's first request should pass, got %d", w.Code)
	}

	second := httptest.NewRequest("GET", "/test", nil)
	second.Header.Set("X-API-Key", "bob")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, second)
	if w.Code != http.StatusOK {
		t.Errorf("bob has his own bucket and should pass, got %d", w.Code)
	}
}

// recordingLimiter allows everything and records the bucket names it is asked about.
type recordingLimiter struct{ keys []string }

func (l *recordingLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) bool {
	l.keys = append(l.keys, key)
	return true
}

func TestRateLimitNeverNamesABucketAfterACredential(t *testing.T) {
	limiter := &recordingLimiter{}
	router := newRouter(RateLimit(limiter, 10, time.Minute, nil))
	headers := []map[string]string{
		{"Authorization": "Bearer sk-upstream-credential"},
		{"X-API-Key": "sk-upstream-credential"},
		{"X-TrustAgent-Key": "ta-ingest-key", "Authorization": "Bearer sk-upstream-credential"},
		{},
	}
	for _, h := range headers {
		req := httptest.NewRequest("GET", "/test", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
	if len(limiter.keys) != len(headers) {
		t.Fatalf("buckets = %v", limiter.keys)
	}
	for _, key := range limiter.keys {
		if strings.Contains(key, "sk-upstream-credential") || strings.Contains(key, "ta-ingest-key") {
			t.Errorf("bucket %q carries a raw credential", key)
		}
	}
	if limiter.keys[0] != limiter.keys[1] {
		t.Errorf("the same credential in Authorization and X-API-Key should share a bucket: %v", limiter.keys[:2])
	}
	if limiter.keys[2] == limiter.keys[0] {
		t.Errorf("X-TrustAgent-Key should name the bucket before Authorization: %v", limiter.keys)
	}
	if !strings.HasSuffix(limiter.keys[3], ":ip:192.0.2.1") {
		t.Errorf("a keyless caller should be limited by IP, got %q", limiter.keys[3])
	}
}

func TestRateLimitWritesTheRouteErrorShape(t *testing.T) {
	var denied []string
	deny := func(c *gin.Context, status int, code, message string) {
		denied = append(denied, code)
		c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "trustagent_error", "code": code}})
	}
	router := newRouter(RateLimit(NewMemoryLimiter(), 1, time.Minute, deny))
	var w *httptest.ResponseRecorder
	for i := 0; i < 2; i++ {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))
	}
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if len(denied) != 1 || denied[0] != CodeRateLimited {
		t.Errorf("deny calls = %v", denied)
	}
	if !strings.Contains(w.Body.String(), `"code":"rate_limited"`) || strings.Contains(w.Body.String(), "ok") {
		t.Errorf("body = %s (the handler must not run)", w.Body.String())
	}
}

func TestRateLimitDefaultErrorBody(t *testing.T) {
	router := newRouter(RateLimit(NewMemoryLimiter(), 1, time.Minute, nil))
	var w *httptest.ResponseRecorder
	for i := 0; i < 2; i++ {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))
	}
	if !strings.HasPrefix(w.Body.String(), `{"error":{"code":"rate_limited","message":`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// BodyLimit
// ---------------------------------------------------------------------------

func TestBodyLimitRejectsOversizedBody(t *testing.T) {
	router := gin.New()
	router.POST("/test", BodyLimit(16), func(c *gin.Context) {
		if _, err := c.GetRawData(); err != nil {
			c.String(http.StatusRequestEntityTooLarge, "too large")
			return
		}
		c.String(http.StatusOK, "ok")
	})

	small := httptest.NewRequest("POST", "/test", strings.NewReader("tiny"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, small)
	if w.Code != http.StatusOK {
		t.Errorf("small body should pass, got %d", w.Code)
	}

	big := httptest.NewRequest("POST", "/test", strings.NewReader(strings.Repeat("x", 64)))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body should be rejected, got %d", w.Code)
	}
}

// TestRateLimitByIdentity: cookie sessions behind one IP get one bucket each (SPEC §2.4, M8), a
// caller without an identity falls back to the IP, and the identity never names a bucket raw.
func TestRateLimitByIdentity(t *testing.T) {
	limiter := NewMemoryLimiter()
	identity := func(c *gin.Context) string { return c.GetHeader("X-Test-Identity") }
	router := newRouter(RateLimitBy(limiter, 1, time.Minute, nil, identity))
	do := func(id string) int {
		req := httptest.NewRequest("GET", "/test", nil)
		if id != "" {
			req.Header.Set("X-Test-Identity", id)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	if a, b := do("session:a"), do("session:b"); a != http.StatusOK || b != http.StatusOK {
		t.Errorf("two sessions from one IP each get a bucket: %d %d", a, b)
	}
	if again := do("session:a"); again != http.StatusTooManyRequests {
		t.Errorf("a session's second request in the window is refused, got %d", again)
	}
	if first, second := do(""), do(""); first != http.StatusOK || second != http.StatusTooManyRequests {
		t.Errorf("without an identity the IP bucket applies: %d %d", first, second)
	}

	rec := &recordingLimiter{}
	newRouter(RateLimitBy(rec, 10, time.Minute, nil, identity)).ServeHTTP(httptest.NewRecorder(), func() *http.Request {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Test-Identity", "session:secret-session-id")
		return req
	}())
	if len(rec.keys) != 1 || strings.Contains(rec.keys[0], "secret-session-id") || !strings.Contains(rec.keys[0], ":id:") {
		t.Errorf("identity bucket %v", rec.keys)
	}
}
