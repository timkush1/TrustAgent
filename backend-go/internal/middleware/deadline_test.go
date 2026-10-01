package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestWriteDeadline: without a server WriteTimeout (TRUTHTABLE_WRITE_TIMEOUT=0), a response
// written after its route's deadline never reaches the client, a quick one does, and a route
// whose deadline is 0 has none (SPEC M12).
func TestWriteDeadline(t *testing.T) {
	deadline := func(c *gin.Context) time.Duration {
		if c.Request.URL.Path == "/none" {
			return 0
		}
		return 50 * time.Millisecond
	}
	answer := func(after time.Duration) gin.HandlerFunc {
		return func(c *gin.Context) {
			time.Sleep(after)
			c.String(http.StatusOK, "ok")
		}
	}
	r := gin.New()
	r.Use(WriteDeadline(deadline))
	r.GET("/quick", answer(0))
	r.GET("/slow", answer(200*time.Millisecond))
	r.GET("/none", answer(200*time.Millisecond))
	srv := httptest.NewServer(r) // no WriteTimeout
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		path   string
		answer bool
	}{{"/quick", true}, {"/slow", false}, {"/none", true}} {
		resp, err := http.Get(srv.URL + tc.path)
		if err == nil {
			_ = resp.Body.Close()
		}
		switch {
		case tc.answer && (err != nil || resp.StatusCode != http.StatusOK):
			t.Errorf("%s: want an answer, got %v", tc.path, err)
		case !tc.answer && err == nil:
			t.Errorf("%s: a response written after the 50 ms deadline was delivered (HTTP %d)", tc.path, resp.StatusCode)
		}
	}

	// A recorder cannot set deadlines: the request still runs.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/quick", nil))
	if w.Code != http.StatusOK {
		t.Errorf("recorder: HTTP %d", w.Code)
	}
}
