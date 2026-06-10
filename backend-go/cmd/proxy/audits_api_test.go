package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestParseListFilterDefaults(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/audits", nil)

	filter, err := parseListFilter(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filter.Limit != 50 || filter.Offset != 0 || filter.Grade != "" || filter.Flagged != nil {
		t.Errorf("unexpected defaults: %+v", filter)
	}
}

func TestParseListFilterValidValues(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/audits?limit=10&offset=20&grade=B&flagged=true", nil)

	filter, err := parseListFilter(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filter.Limit != 10 || filter.Offset != 20 || filter.Grade != "B" {
		t.Errorf("unexpected filter: %+v", filter)
	}
	if filter.Flagged == nil || *filter.Flagged != true {
		t.Errorf("flagged not parsed: %+v", filter.Flagged)
	}
}

func TestParseListFilterRejectsInvalid(t *testing.T) {
	for _, query := range []string{
		"limit=0", "limit=201", "limit=abc",
		"offset=-1", "offset=x",
		"grade=Z", "grade=a",
		"flagged=maybe",
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/api/audits?"+query, nil)

		if _, err := parseListFilter(c); err == nil {
			t.Errorf("query %q should be rejected", query)
		}
	}
}

func TestAuditsEndpointsReturn503WithoutStore(t *testing.T) {
	router := gin.New()
	router.GET("/api/audits", handleListAudits(nil))
	router.GET("/api/audits/:id", handleGetAudit(nil))

	for _, path := range []string{"/api/audits", "/api/audits/some-id"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without store: expected 503, got %d", path, w.Code)
		}
	}
}
