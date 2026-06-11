package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseKBListParamsDefaults(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/kb/claims", nil)

	limit, offset, status, err := parseKBListParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limit != 50 || offset != 0 || status != "" {
		t.Errorf("unexpected defaults: limit=%d offset=%d status=%q", limit, offset, status)
	}
}

func TestParseKBListParamsValid(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/kb/claims?limit=10&offset=5&status=quarantined", nil)

	limit, offset, status, err := parseKBListParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limit != 10 || offset != 5 || status != "quarantined" {
		t.Errorf("unexpected values: limit=%d offset=%d status=%q", limit, offset, status)
	}
}

func TestParseKBListParamsRejectsInvalid(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=999", "offset=-1", "status=banana"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/api/kb/claims?"+query, nil)

		if _, _, _, err := parseKBListParams(c); err == nil {
			t.Errorf("query %q should be rejected", query)
		}
	}
}

func TestKBEndpointsReturn503WithoutEngine(t *testing.T) {
	router := gin.New()
	router.GET("/api/kb/claims", handleListKBClaims(nil))
	router.GET("/api/kb/conflicts", handleListConflicts(nil))
	router.GET("/api/kb/stats", handleKBStats(nil))

	for _, path := range []string{"/api/kb/claims", "/api/kb/conflicts", "/api/kb/stats"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without engine: expected 503, got %d", path, w.Code)
		}
	}
}
