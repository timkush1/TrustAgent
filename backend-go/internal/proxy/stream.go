package proxy

import (
	"io"
	"net/http"
	"strings"
)

// serveStream relays an event stream to the client and gates it (SPEC §2.12 streaming):
// passthrough or buffer per route. resp is the upstream response, nil for a synthesized
// test_response stream; body is the stream to relay. For now it copies body as it is (GS-5, GS-6).
func (h *Handler) serveStream(f *flow, resp *http.Response, body io.Reader) {
	if _, err := io.Copy(f.c.Writer, body); err != nil {
		h.log.Debug("stream relay stopped", "trace_id", f.c.GetString(traceIDKey), "err", err)
	}
}

// synthStream is the event stream of a stream:true test_response in f's format. For now it is
// the text as it is (GS-4 synthesizes the format's events).
func (h *Handler) synthStream(f *flow, text string) io.Reader {
	return strings.NewReader(text)
}
