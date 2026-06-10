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
	router := newRouter(RateLimit(NewMemoryLimiter(), 2, time.Minute))

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
	router := newRouter(RateLimit(NewMemoryLimiter(), 1, time.Minute))

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
