package llmapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestStreamWriterThroughGin runs the passthrough flow on gin's ResponseWriter, the one the proxy
// handler hands over: events are flushed, the write deadline can be extended (gin's writer
// unwraps to the connection's), and the declared and undeclared trailers arrive.
func TestStreamWriterThroughGin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Header("X-TrustAgent-Trace-Id", "t1")
		sw := NewStreamWriter(c.Writer)
		sw.DeclareTrailers("X-TrustAgent-Action", "X-TrustAgent-Mode")
		asm, _ := NewStreamAssembler(FormatForPath(c.Request.URL.Path))
		res := Relay(NewReader(bytes.NewReader(fixture(t, "openai_chat_basic.sse"))), asm, sw)
		if !res.Held || res.ClientGone(c.Request.Context()) {
			t.Errorf("relay %+v", res)
		}
		if err := sw.ExtendWriteDeadline(time.Minute); err != nil {
			t.Errorf("extend the write deadline through gin: %v", err)
		}
		if err := sw.Finish(&testAnnotation, map[string]string{
			"X-TrustAgent-Action": "flag", "X-TrustAgent-Mode": "flag", "X-TrustAgent-Status": "ok",
		}); err != nil {
			t.Errorf("finish: %v", err)
		}
	})
	srv := httptest.NewServer(engine)
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-TrustAgent-Trace-Id") != "t1" {
		t.Errorf("headers %v", resp.Header)
	}
	for name, want := range map[string]string{"X-TrustAgent-Action": "flag", "X-TrustAgent-Mode": "flag", "X-TrustAgent-Status": "ok"} {
		if got := resp.Trailer.Get(name); got != want {
			t.Errorf("trailer %s = %q, want %q", name, got, want)
		}
	}
	evs := clientEvents(t, body)
	if n := len(evs); n < 2 || !IsTerminal(FormatOpenAIChat, evs[n-1]) || len(evs[n-2].Comments) != 1 {
		t.Errorf("stream tail %q", tail(string(body), 300))
	}
}
