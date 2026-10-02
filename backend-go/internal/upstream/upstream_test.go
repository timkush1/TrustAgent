package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/netguard"
)

func newClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: base, Timeout: 5 * time.Second}, func(s string) bool { return s == "ta-proxy-key" })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsBadBase(t *testing.T) {
	for _, base := range []string{"ftp://x", "api.openai.com", "https://", "://"} {
		if _, err := New(Config{BaseURL: base}, nil); err == nil {
			t.Errorf("%q: want an error", base)
		}
	}
	c, err := New(Config{}, nil)
	if err != nil || c.Base().String() != DefaultBaseURL {
		t.Errorf("default base: %v %v", c, err)
	}
}

func TestURLJoin(t *testing.T) {
	cases := []struct{ base, path, query, want string }{
		{"https://api.openai.com", "/v1/chat/completions", "", "https://api.openai.com/v1/chat/completions"},
		{"https://openrouter.ai/api", "/v1/chat/completions", "", "https://openrouter.ai/api/v1/chat/completions"},
		{"https://openrouter.ai/api/", "/v1/models", "a=1", "https://openrouter.ai/api/v1/models?a=1"},
		{"https://gw.example/base?api-version=2024-10-01", "/v1/chat/completions", "", "https://gw.example/base/v1/chat/completions?api-version=2024-10-01"},
		{"https://gw.example/base?api-version=2024-10-01", "/v1/chat/completions", "x=%2F", "https://gw.example/base/v1/chat/completions?api-version=2024-10-01&x=%2F"},
		{"http://localhost:11434", "/v1/embeddings", "", "http://localhost:11434/v1/embeddings"},
	}
	for _, c := range cases {
		if got := newClient(t, c.base).URL(c.path, c.query).String(); got != c.want {
			t.Errorf("%s + %s?%s = %s, want %s", c.base, c.path, c.query, got, c.want)
		}
	}
}

func TestOutboundHeaders(t *testing.T) {
	c := newClient(t, "https://api.openai.com")
	in := http.Header{
		"Authorization":       {"Bearer sk-upstream-key"},
		"X-Trustagent-Key":    {"ta-proxy-key"},
		"X-Trustagent-Route":  {"support-bot"},
		"X-TrustAgent-Mode":   {"flag"}, // non-canonical spelling still goes
		"Cookie":              {"ta_session=abc"},
		"Accept-Encoding":     {"br, gzip"},
		"Content-Length":      {"123"},
		"Host":                {"localhost:8080"},
		"Connection":          {"keep-alive, X-Custom-Hop"},
		"X-Custom-Hop":        {"1"},
		"Keep-Alive":          {"timeout=5"},
		"Content-Type":        {"application/json"},
		"Openai-Organization": {"org-1"},
		"X-Request-Id":        {"req-1"},
	}
	out := c.OutboundHeaders(in)
	want := http.Header{
		"Authorization":       {"Bearer sk-upstream-key"},
		"Content-Type":        {"application/json"},
		"Openai-Organization": {"org-1"},
		"X-Request-Id":        {"req-1"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("outbound headers\n%v\nwant\n%v", out, want)
	}
	if in.Get("Cookie") == "" {
		t.Error("the input headers must not be modified")
	}

	// B1: a TrustAgent key is never forwarded, wherever the client put it.
	for _, h := range []http.Header{
		{"Authorization": {"Bearer ta-proxy-key"}},
		{"Authorization": {"bearer  ta-proxy-key "}},
		{"X-Api-Key": {"ta-proxy-key"}},
		{"Api-Key": {"ta-proxy-key"}},
	} {
		if out := c.OutboundHeaders(h); len(out) != 0 {
			t.Errorf("%v forwarded as %v", h, out)
		}
	}
	if out := c.OutboundHeaders(http.Header{"X-Api-Key": {"sk-ant-upstream"}}); out.Get("X-Api-Key") != "sk-ant-upstream" {
		t.Error("an upstream x-api-key passes through")
	}
}

func TestDoForwardsOnClientContextAndDecodesGzip(t *testing.T) {
	var got *http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = zw.Write([]byte(`{"ok":true}`))
			_ = zw.Close()
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := newClient(t, srv.URL+"/api")
	hdr := http.Header{"Authorization": {"Bearer sk-up"}, "Accept-Encoding": {"br"}, "X-Trustagent-Key": {"ta-proxy-key"}}
	resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "q=1", hdr, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer resp.Body.Close()
	body, tooLarge, err := ReadBody(resp, 1024)
	if err != nil || tooLarge || string(body) != `{"ok":true}` {
		t.Fatalf("body %q tooLarge %v err %v", body, tooLarge, err)
	}
	if got.URL.Path != "/api/v1/chat/completions" || got.URL.RawQuery != "q=1" || string(gotBody) != `{"a":1}` {
		t.Errorf("upstream saw %s?%s %s", got.URL.Path, got.URL.RawQuery, gotBody)
	}
	if got.Header.Get("Authorization") != "Bearer sk-up" || got.Header.Get("X-Trustagent-Key") != "" {
		t.Errorf("upstream headers %v", got.Header)
	}
	// The client's Accept-Encoding is dropped; the transport negotiates gzip itself and decodes it.
	if ae := got.Header.Get("Accept-Encoding"); strings.Contains(ae, "br") {
		t.Errorf("Accept-Encoding %q was forwarded", ae)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Error("a transparently decoded body keeps no Content-Encoding")
	}
}

func TestDoHonoursCanceledClientContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err := newClient(t, srv.URL).Do(ctx, http.MethodPost, "/v1/chat/completions", "", http.Header{}, nil)
	if err == nil || !IsTimeout(err) {
		t.Errorf("err %v, want a timeout from the client's context", err)
	}
}

func TestReadBodyLimitAndStrayGzip(t *testing.T) {
	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader("0123456789"))}
	b, tooLarge, err := ReadBody(resp, 4)
	if err != nil || !tooLarge || string(b) != "01234" {
		t.Errorf("limit: %q %v %v", b, tooLarge, err)
	}
	rest, _ := io.ReadAll(resp.Body)
	if string(b)+string(rest) != "0123456789" {
		t.Error("the unread rest must remain readable for passthrough")
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(`{"x":1}`))
	_ = zw.Close()
	resp = &http.Response{Header: http.Header{"Content-Encoding": {"gzip"}, "Content-Length": {"99"}}, Body: io.NopCloser(&buf)}
	b, _, err = ReadBody(resp, 100)
	if err != nil || string(b) != `{"x":1}` || resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Content-Length") != "" {
		t.Errorf("stray gzip: %q %v %v", b, err, resp.Header)
	}
	resp = &http.Response{Header: http.Header{"Content-Encoding": {"gzip"}}, Body: io.NopCloser(strings.NewReader("not gzip"))}
	if _, _, err := ReadBody(resp, 100); err == nil {
		t.Error("an undecodable gzip body is an error")
	}
}

func TestCopyResponseHeaders(t *testing.T) {
	src := http.Header{
		"Content-Type":                {"application/json"},
		"Content-Length":              {"10"},
		"Content-Encoding":            {"gzip"},
		"Set-Cookie":                  {"a=1", "b=2"},
		"Connection":                  {"close, X-Hop"},
		"X-Hop":                       {"x"},
		"Transfer-Encoding":           {"chunked"},
		"X-Trustagent-Action":         {"block"},
		"X-Request-Id":                {"upstream-id"},
		"Access-Control-Allow-Origin": {"*"},
		"Openai-Processing-Ms":        {"42"},
	}
	dst := http.Header{"X-Request-Id": {"ours"}}
	CopyResponseHeaders(dst, src)
	want := http.Header{
		"X-Request-Id":         {"ours"},
		"Content-Type":         {"application/json"},
		"Set-Cookie":           {"a=1", "b=2"},
		"Openai-Processing-Ms": {"42"},
	}
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("copied\n%v\nwant\n%v", dst, want)
	}
}

func TestReverseProxy(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("X-Trustagent-Status", "spoofed")
		w.Header().Set("X-Request-Id", "upstream")
		_, _ = w.Write([]byte(`{"data":[]}` + r.URL.Path))
	}))
	defer srv.Close()
	c := newClient(t, srv.URL+"/api")
	var proxyErr error
	rp := c.ReverseProxy(func(w http.ResponseWriter, r *http.Request, err error) {
		proxyErr = err
		w.WriteHeader(http.StatusBadGateway)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer ta-proxy-key")
	req.Header.Set("X-TrustAgent-Key", "ta-proxy-key")
	req.Header.Set("Cookie", "x=1")
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-Id", "ours")
	rp.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != `{"data":[]}/api/v1/models` || proxyErr != nil {
		t.Errorf("response %d %q %v", w.Code, w.Body.String(), proxyErr)
	}
	if seen.Get("Authorization") != "" || seen.Get("X-Trustagent-Key") != "" || seen.Get("Cookie") != "" {
		t.Errorf("upstream saw %v", seen)
	}
	if w.Header().Get("X-Trustagent-Status") != "" || !reflect.DeepEqual(w.Header().Values("X-Request-Id"), []string{"ours"}) {
		t.Errorf("client got %v", w.Header())
	}

	srv.Close()
	w = httptest.NewRecorder()
	rp.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if w.Code != http.StatusBadGateway || proxyErr == nil {
		t.Errorf("a dead upstream goes to onError: %d %v", w.Code, proxyErr)
	}
}

func injectClient(t *testing.T, base, header string) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: base, APIKey: "sk-injected-upstream", AuthHeader: header, Timeout: 5 * time.Second},
		func(s string) bool { return s == "ta-proxy-key" })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestOutboundHeadersInjectMode (B1, D15): in inject mode every client credential goes, in any
// spelling, and the upstream's own is set: Bearer on authorization, the raw key on any other
// header. anthropic-version and the other client headers stay; no TrustAgent key is forwarded.
func TestOutboundHeadersInjectMode(t *testing.T) {
	in := http.Header{
		"Authorization":     {"Bearer sk-client-own"},
		"X-Api-Key":         {"sk-ant-client-own"},
		"api-key":           {"azure-client-own"}, // a hand-built, non-canonical spelling
		"X-Trustagent-Key":  {"ta-proxy-key"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"tools-2024-04-04"},
		"Content-Type":      {"application/json"},
		"Cookie":            {"ta_session=abc"},
	}
	kept := http.Header{"Anthropic-Version": {"2023-06-01"}, "Anthropic-Beta": {"tools-2024-04-04"}, "Content-Type": {"application/json"}}
	for _, c := range []struct {
		header string
		want   http.Header
	}{
		{"", http.Header{"Authorization": {"Bearer sk-injected-upstream"}}},
		{"authorization", http.Header{"Authorization": {"Bearer sk-injected-upstream"}}},
		{"x-api-key", http.Header{"X-Api-Key": {"sk-injected-upstream"}}},
		{"api-key", http.Header{"Api-Key": {"sk-injected-upstream"}}},
	} {
		cl := injectClient(t, "https://api.example", c.header)
		if !cl.Injecting() {
			t.Errorf("%q: not injecting", c.header)
		}
		want := kept.Clone()
		for k, v := range c.want {
			want[k] = v
		}
		if got := cl.OutboundHeaders(in); !reflect.DeepEqual(got, want) {
			t.Errorf("auth header %q:\n%v\nwant\n%v", c.header, got, want)
		}
	}
	// The proxy key a client sent as its SDK credential is replaced, never forwarded.
	cl := injectClient(t, "https://api.example", "x-api-key")
	for _, h := range []http.Header{
		{"Authorization": {"Bearer ta-proxy-key"}},
		{"X-Api-Key": {"ta-proxy-key"}},
		{"x-api-key": {"ta-proxy-key"}, "Authorization": {"Bearer ta-proxy-key"}},
	} {
		got := cl.OutboundHeaders(h)
		if !reflect.DeepEqual(got, http.Header{"X-Api-Key": {"sk-injected-upstream"}}) {
			t.Errorf("%v forwarded as %v", h, got)
		}
	}
	if in.Get("Authorization") != "Bearer sk-client-own" || len(in["api-key"]) != 1 {
		t.Error("the input headers must not be modified")
	}
	if _, err := New(Config{APIKey: "k", AuthHeader: "x api key"}, nil); err == nil {
		t.Error("an auth header that is not a header name is refused")
	}
}

// TestOutboundHeadersPassthroughMode: without a key of its own the upstream gets the client's
// credential untouched (anthropic-version too), and a TrustAgent key in any spelling or position
// is dropped.
func TestOutboundHeadersPassthroughMode(t *testing.T) {
	c := newClient(t, "https://api.example")
	if c.Injecting() {
		t.Fatal("no key: passthrough")
	}
	in := http.Header{"X-Api-Key": {"sk-ant-client-own"}, "Anthropic-Version": {"2023-06-01"}, "Authorization": {"Bearer sk-client-own"}}
	if got := c.OutboundHeaders(in); !reflect.DeepEqual(got, in) {
		t.Errorf("passthrough changed the credentials: %v", got)
	}
	for _, h := range []http.Header{
		{"authorization": {"Bearer ta-proxy-key"}},
		{"Authorization": {"ta-proxy-key"}},        // no scheme
		{"Authorization": {"Token  ta-proxy-key"}}, // another scheme
		{"x-api-key": {" ta-proxy-key"}},
		{"X-Api-Key": {"Bearer ta-proxy-key"}},
		{"Api-Key": {"sk-other", "ta-proxy-key"}},
	} {
		if out := c.OutboundHeaders(h); len(out) != 0 {
			t.Errorf("%v forwarded as %v", h, out)
		}
	}
}

// TestInjectedCredentialReachesTheUpstream: Do and the ReverseProxy of an injecting upstream send
// its key and none of the client's.
func TestInjectedCredentialReachesTheUpstream(t *testing.T) {
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := injectClient(t, srv.URL, "")
	hdr := http.Header{"Authorization": {"Bearer ta-proxy-key"}, "Anthropic-Version": {"2023-06-01"}}
	resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/messages", "", hdr, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-Api-Key", "sk-client-own")
	c.ReverseProxy(func(http.ResponseWriter, *http.Request, error) {}).ServeHTTP(httptest.NewRecorder(), req)
	if len(seen) != 2 {
		t.Fatalf("%d upstream requests", len(seen))
	}
	for i, h := range seen {
		if h.Get("Authorization") != "Bearer sk-injected-upstream" || h.Get("X-Api-Key") != "" {
			t.Errorf("request %d credentials %v", i, h)
		}
	}
	if seen[0].Get("Anthropic-Version") != "2023-06-01" {
		t.Error("anthropic-version passes through in inject mode")
	}
}

func TestSetFor(t *testing.T) {
	openai, anthropic := newClient(t, "https://openai.example"), newClient(t, "https://anthropic.example")
	s := &Set{OpenAI: openai, Anthropic: anthropic}
	if s.For(llmapi.FamilyAnthropic) != anthropic || s.For(llmapi.FamilyOpenAI) != openai || s.For("") != openai {
		t.Error("For routes by family")
	}
	s.Anthropic = nil
	if s.For(llmapi.FamilyAnthropic) != openai {
		t.Error("without an Anthropic upstream the default one answers")
	}
}

// An upstream at a link-local or cloud-metadata address is refused at dial time (D78), so the
// gateway never sends a key there, however the upstream URL spells the host.
func TestUpstreamRefusesMetadataAtDialTime(t *testing.T) {
	c, err := New(Config{BaseURL: "http://169.254.169.254/v1", APIKey: "sk-test-upstream-0123456789", Timeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", http.Header{}, []byte(`{}`))
	if cancel != nil {
		cancel()
	}
	if !errors.Is(err, netguard.ErrForbiddenTarget) {
		t.Fatalf("Do against the metadata address = %v, want netguard.ErrForbiddenTarget", err)
	}
}
