package jev

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
)

// TestForwardTestHeaders (D119): the test-mode headers go only to a local endpoint, and only when
// forwarding is on; a real vendor, or a private address, never gets them.
func TestForwardTestHeaders(t *testing.T) {
	opt := CallOptions{Scenario: "fault:429@1", TraceID: "trace-1"}
	cases := []struct {
		endpoint string
		enabled  bool
		want     bool
	}{
		{"http://127.0.0.1:8090/api/alpha/decisions", true, true},
		{"http://[::1]:8090/v1/systemone", true, true},
		{"http://localhost:8090/v1/systemone", true, true},
		{"http://mock-upstream:8090/api/alpha/decisions", true, true},
		{"http://127.0.0.1:8090/api/alpha/decisions", false, false},
		{"https://openrouter.ai/api/alpha/decisions", true, false},
		{"https://api.typesafe.ai/v1/systemone", true, false},
		{"http://10.0.0.5:8090/v1/systemone", true, false},
		{"http://192.168.1.20:8090/v1/systemone", true, false},
		{"http://172.17.0.2:8090/v1/systemone", true, false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		h := http.Header{}
		forwardTestHeaders(h, u, opt, tc.enabled)
		got := h.Get("X-TrustAgent-Scenario") == opt.Scenario && h.Get("X-Mock-Key") == opt.TraceID
		if got != tc.want || (!tc.want && (h.Get("X-TrustAgent-Scenario") != "" || h.Get("X-Mock-Key") != "")) {
			t.Errorf("%s (enabled %v): headers %v, want sent: %v", tc.endpoint, tc.enabled, h, tc.want)
		}
	}
	// Without a trace id there is no key to count by; without a scenario no scenario header.
	h := http.Header{}
	forwardTestHeaders(h, &url.URL{Scheme: "http", Host: "localhost"}, CallOptions{}, true)
	if len(h) != 0 {
		t.Errorf("empty options sent %v", h)
	}
	forwardTestHeaders(h, nil, opt, true)
	if len(h) != 0 {
		t.Errorf("no endpoint sent %v", h)
	}
}

// TestForwardTestHeadersHedgeSharesTheKey: the attempt and its hedge carry the same X-Mock-Key,
// so mock-upstream counts a fault spec per call, not per request.
func TestForwardTestHeadersHedgeSharesTheKey(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) {
		c.HTTP.ForwardScenario = true
		h.withFault("hang@1")(c)
	})
	resp, err := decide(t, cl, testRequest("trace-shared"), CallOptions{HedgeAfter: 50 * time.Millisecond, TraceID: "trace-shared"})
	if err != nil {
		t.Fatal(err)
	}
	reqs := h.decisionRequests()
	if !resp.Hedged || len(reqs) != 2 {
		t.Fatalf("hedged %v, %d requests", resp.Hedged, len(reqs))
	}
	for i, r := range reqs {
		if got := r.Header.Get("X-Mock-Key"); got != "trace-shared" {
			t.Errorf("request %d: X-Mock-Key %q", i, got)
		}
	}
}
