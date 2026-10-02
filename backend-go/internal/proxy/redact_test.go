package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
)

// noPatternSecret matches none of the redaction patterns: only the Redactor's list of known
// secrets can mask it (P4-PLAN §0 rule 8).
const noPatternSecret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

// echoingError is an upstream 401 that quotes the key it was sent, as OpenAI's does.
func echoingError(key string) string {
	return `{"error":{"message":"Incorrect API key provided: ` + key + `. You can find your API key at https://platform.openai.com/account/api-keys.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`
}

// TestUpstreamErrorsNeverQuoteTheInjectedKey: in inject mode an upstream error that echoes the
// gateway's own key reaches the client masked, on a gated route and on a passthrough route, for a
// key the patterns know and for one only the Redactor's list of secrets knows (D42).
func TestUpstreamErrorsNeverQuoteTheInjectedKey(t *testing.T) {
	if config.NewRedactor().Redact(noPatternSecret) != noPatternSecret {
		t.Fatal("the test secret must match no redaction pattern")
	}
	for _, key := range []string{noPatternSecret, "sk-test-injected-3f2a9c1b"} {
		t.Run(key[:4], func(t *testing.T) {
			e := newEnv(t, envOpts{injectKey: key, redact: config.NewRedactor(key).Redact})
			e.up.status, e.up.body = http.StatusUnauthorized, echoingError(key)
			gated := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
			e.sink.result(t)
			passthrough := e.send(t, http.MethodGet, "/v1/models", "", nil)
			for name, w := range map[string]*httptest.ResponseRecorder{"gated": gated, "passthrough": passthrough} {
				if w.Code != http.StatusUnauthorized {
					t.Errorf("%s: status %d, want the upstream's 401", name, w.Code)
				}
				body := w.Body.String()
				if strings.Contains(body, key) {
					t.Errorf("%s: the injected key was relayed", name)
				}
				if !strings.Contains(body, "Incorrect API key provided: "+config.Masked) || !strings.Contains(body, `"code":"invalid_api_key"`) {
					t.Errorf("%s: body %s, want the upstream's error with the key masked", name, body)
				}
			}
			if cl := passthrough.Header().Get("Content-Length"); cl != "" && cl != strconv.Itoa(passthrough.Body.Len()) {
				t.Errorf("passthrough Content-Length %s for a %d-byte body", cl, passthrough.Body.Len())
			}
		})
	}
}

// TestLongUpstreamErrorsAreCut: inject mode relays at most 64 KiB of an upstream error body, so a
// key quoted past it never leaves; a compressed error body is decoded before it is masked.
func TestLongUpstreamErrorsAreCut(t *testing.T) {
	e := newEnv(t, envOpts{injectKey: noPatternSecret, redact: config.NewRedactor(noPatternSecret).Redact})
	long := `{"error":{"message":"` + strings.Repeat("x", maxRedactedErrorBytes) + noPatternSecret + `"}}`
	e.up.status, e.up.body = http.StatusBadRequest, long
	for _, w := range []*httptest.ResponseRecorder{
		e.do(t, "/v1/chat/completions", defaultRouteBody, nil),
		e.send(t, http.MethodPost, "/v1/embeddings", `{"model":"m","input":"x"}`, nil),
	} {
		if w.Code != http.StatusBadRequest || w.Body.Len() != maxRedactedErrorBytes || strings.Contains(w.Body.String(), noPatternSecret) {
			t.Errorf("status %d, %d bytes, key relayed %v; want 400 cut at %d bytes", w.Code, w.Body.Len(),
				strings.Contains(w.Body.String(), noPatternSecret), maxRedactedErrorBytes)
		}
	}
	e.sink.result(t)

	// A key that straddles the cut is masked before the body is cut: none of it is relayed, not
	// even its first characters, for a key only the list knows and for one the patterns know.
	for _, key := range []string{noPatternSecret, "sk-test-injected-3f2a9c1b7d"} {
		e := newEnv(t, envOpts{injectKey: key, redact: config.NewRedactor(key).Redact})
		pad := maxRedactedErrorBytes - len(`{"error":{"message":"`) - 6
		e.up.status, e.up.body = http.StatusBadRequest, `{"error":{"message":"`+strings.Repeat("x", pad)+key+`"}}`
		for _, w := range []*httptest.ResponseRecorder{
			e.do(t, "/v1/chat/completions", defaultRouteBody, nil),
			e.send(t, http.MethodPost, "/v1/embeddings", `{"model":"m","input":"x"}`, nil),
		} {
			got := w.Body.String()
			if w.Code != http.StatusBadRequest || len(got) > maxRedactedErrorBytes || strings.Contains(got, key[:6]) {
				t.Errorf("%s straddling the cut: status %d, %d bytes, key start relayed %v", key[:4], w.Code, len(got),
					strings.Contains(got, key[:6]))
			}
		}
		e.sink.result(t)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = io.WriteString(zw, echoingError(noPatternSecret))
	_ = zw.Close()
	e.up.status, e.up.body, e.up.encoding = http.StatusUnauthorized, gz.String(), "gzip"
	for _, w := range []*httptest.ResponseRecorder{
		e.do(t, "/v1/chat/completions", defaultRouteBody, nil),
		e.send(t, http.MethodGet, "/v1/models", "", nil),
	} {
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), noPatternSecret) ||
			!strings.Contains(w.Body.String(), "invalid_api_key") || w.Header().Get("Content-Encoding") != "" {
			t.Errorf("gzip error body: %d %q (Content-Encoding %q)", w.Code, w.Body.String(), w.Header().Get("Content-Encoding"))
		}
	}
	e.sink.result(t)
}

// TestPassthroughModeRelaysErrorsAsTheyCame: without an injected key the credential is the
// client's own, and so is what the upstream quotes: the body passes through unchanged.
func TestPassthroughModeRelaysErrorsAsTheyCame(t *testing.T) {
	e := newEnv(t, envOpts{redact: config.NewRedactor(upstreamKey).Redact})
	e.up.status, e.up.body = http.StatusUnauthorized, echoingError(upstreamKey)
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	e.sink.result(t)
	if w.Code != http.StatusUnauthorized || w.Body.String() != echoingError(upstreamKey) {
		t.Errorf("passthrough mode: %d %s", w.Code, w.Body)
	}
}
