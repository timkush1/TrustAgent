package proxy

import (
	"net/http"
	"testing"

	"github.com/truthtable/backend-go/internal/llmapi"
)

// TestModelsFollowTheClientsFamily: /v1/models goes to the Anthropic upstream for a request that
// carries anthropic-version, or an x-api-key that is not a proxy key; to the OpenAI upstream
// otherwise (D67).
func TestModelsFollowTheClientsFamily(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		headers   map[string]string
		anthropic bool
	}{
		{name: "openai sdk", path: "/v1/models"},
		{name: "anthropic sdk", path: "/v1/models", headers: map[string]string{"Anthropic-Version": "2023-06-01", "Authorization": ""}, anthropic: true},
		{name: "one model, anthropic sdk", path: "/v1/models/claude-haiku-4-5", headers: map[string]string{"Anthropic-Version": "2023-06-01"}, anthropic: true},
		{name: "anthropic key", path: "/v1/models", headers: map[string]string{"X-Api-Key": "sk-ant-client-key", "Authorization": ""}, anthropic: true},
		{name: "proxy key on x-api-key", path: "/v1/models", headers: map[string]string{"X-Api-Key": ingestKey}},
		{name: "minted proxy key on x-api-key", path: "/v1/models", headers: map[string]string{"X-Api-Key": "ta_" + "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"}},
		{name: "trailing slash", path: "/v1/models/", headers: map[string]string{"Anthropic-Version": "2023-06-01"}, anthropic: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, envOpts{})
			w := e.send(t, http.MethodGet, tc.path, "", tc.headers)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			want, other := e.up, e.anth
			if tc.anthropic {
				want, other = e.anth, e.up
			}
			if want.calls() != 1 || other.calls() != 0 {
				t.Fatalf("openai %d calls, anthropic %d calls; want the %s upstream", e.up.calls(), e.anth.calls(), map[bool]string{true: "anthropic", false: "openai"}[tc.anthropic])
			}
			r, _ := want.last(t)
			if r.URL.Path != tc.path {
				t.Errorf("upstream path %q, want %q", r.URL.Path, tc.path)
			}
			// A proxy key never travels upstream, whichever upstream answers (B1).
			if v := r.Header.Get("X-Api-Key"); v == ingestKey || len(v) > 3 && v[:3] == "ta_" {
				t.Error("a proxy key on x-api-key went upstream")
			}
		})
	}
}

// TestModelsErrorsTakeTheFamilysShape: a failed /v1/models call is answered in the error shape of
// the family it went to.
func TestModelsErrorsTakeTheFamilysShape(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.anth.Close()
	e.up.Close()
	checkAnthropicError(t, e.send(t, http.MethodGet, "/v1/models", "", map[string]string{"Anthropic-Version": "2023-06-01"}),
		http.StatusBadGateway, llmapi.AnthropicAPIError)
	checkOpenAIError(t, e.send(t, http.MethodGet, "/v1/models", "", nil), http.StatusBadGateway, CodeUpstreamError)
}
