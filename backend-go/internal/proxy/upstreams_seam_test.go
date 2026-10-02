package proxy

import (
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/truthtable/backend-go/internal/upstream"
)

// Options.Upstreams is read per request: a new set applies from the next request
// on, and the passthrough reverse proxies follow it.
func TestUpstreamsAreReadPerRequest(t *testing.T) {
	client := func(url string) *upstream.Client {
		c, err := upstream.New(upstream.Config{BaseURL: url}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := client("http://a.invalid"), client("http://b.invalid")
	var cur atomic.Pointer[upstream.Set]
	cur.Store(&upstream.Set{OpenAI: a, Anthropic: a})
	h := New(Options{Upstreams: cur.Load, Logger: slog.New(slog.DiscardHandler)})

	first := h.upstreams()
	if first.OpenAI != a || h.reverseProxies(first)[a] == nil {
		t.Fatalf("initial set not served: %+v", first)
	}
	cur.Store(&upstream.Set{OpenAI: b})
	next := h.upstreams()
	rps := h.reverseProxies(next)
	if next.OpenAI != b || rps[b] == nil || rps[a] != nil {
		t.Errorf("a new set must apply to the next request: set %+v, proxies %v", next, rps)
	}

	none := New(Options{Logger: slog.New(slog.DiscardHandler)})
	if s := none.upstreams(); s == nil || s.OpenAI != nil {
		t.Errorf("nil Upstreams serves no upstream: %+v", s)
	}
	if none.redact("sk-x") != "sk-x" {
		t.Error("nil Redact is the identity")
	}
}
