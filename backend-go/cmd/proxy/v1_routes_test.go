package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
)

// noPatternKey matches none of the redaction patterns: only the Redactor's list of loaded secrets
// can mask it.
const noPatternKey = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

// TestV1RelaysUpstreamErrorsWithoutTheInjectedKey: an upstream that refuses the gateway's own key
// and quotes it back reaches the /v1 client with the key masked, on a gated route and on a
// passthrough route, through the server's redactor (D42, GW-6); the key is not logged either.
func TestV1RelaysUpstreamErrorsWithoutTheInjectedKey(t *testing.T) {
	if config.NewRedactor().Redact(noPatternKey) != noPatternKey {
		t.Fatal("the test key must match no redaction pattern")
	}
	for _, key := range []string{noPatternKey, "sk-test-upstream-3f2a9c1b"} {
		t.Run(key[:4], func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+
					strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+`","code":"invalid_api_key"}}`)
			}))
			t.Cleanup(up.Close)
			// The key is loaded from the environment, as main loads it, so the redactor knows it.
			t.Setenv("TRUTHTABLE_UPSTREAM_API_KEY", key)
			var logs bytes.Buffer
			env := newTestEnv(t, envOpts{logs: &logs, mutate: func(c *config.Config) {
				c.UpstreamURL, c.UpstreamAPIKey = up.URL, key
			}})

			body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
			for _, call := range []struct{ method, path string }{{http.MethodPost, "/v1/chat/completions"}, {http.MethodGet, "/v1/models"}} {
				var b []byte
				if call.method == http.MethodPost {
					b = body
				}
				code, got := env.do(call.method, call.path, testKey, b)
				if code != http.StatusUnauthorized {
					t.Errorf("%s: status %d, want the upstream's 401", call.path, code)
				}
				if bytes.Contains(got, []byte(key)) {
					t.Errorf("%s: the injected key was relayed", call.path)
				}
				if !bytes.Contains(got, []byte("Incorrect API key provided: "+config.Masked)) {
					t.Errorf("%s: body %s, want the upstream's error with the key masked", call.path, got)
				}
			}
			if strings.Contains(env.logText(), key) {
				t.Error("the injected key was logged")
			}
		})
	}
}
