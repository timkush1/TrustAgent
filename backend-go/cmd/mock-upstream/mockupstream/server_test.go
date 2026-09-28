package mockupstream

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, cfg Config) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

type call struct {
	method, path, body string
	header             map[string]string
}

func do(t *testing.T, srv *httptest.Server, c call) (*http.Response, []byte) {
	t.Helper()
	method := c.method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, srv.URL+c.path, strings.NewReader(c.body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func decodeJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

// groundedPassChat is the grounded-pass scenario request (matched by its last user message).
const groundedPassChat = `{"model":"gpt-4o-mini","messages":[{"role":"system","content":"You are the Acme support assistant."},` +
	`{"role":"user","content":"How long do I have to get a refund on an annual plan?"}]}`

const groundedPassAnswer = "You can get a refund on an annual plan within 30 days of purchase. Monthly plans are non-refundable."

func TestOpenAIChatJSON(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	resp, b := do(t, srv, call{path: "/v1/chat/completions", body: groundedPassChat})
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	m := decodeJSON(t, b)
	choice := m["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if msg["content"] != groundedPassAnswer || choice["finish_reason"] != "stop" || m["object"] != "chat.completion" || m["model"] != "gpt-4o-mini" {
		t.Errorf("chat completion %s", b)
	}
	if _, ok := m["provider"]; ok {
		t.Error("provider is OpenRouter-only")
	}
	// An unknown conversation gets the default answer; a scenario header wins over matching.
	_, b = do(t, srv, call{path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`})
	if !strings.Contains(string(b), "mock answer from mock-upstream") {
		t.Errorf("default answer %s", b)
	}
	_, b = do(t, srv, call{path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		header: map[string]string{"X-Mock-Scenario": "grounded-pass"}})
	if !strings.Contains(string(b), groundedPassAnswer) {
		t.Errorf("scenario header %s", b)
	}
}

// sseEvents reads an event stream into (event, data) pairs.
func sseEvents(t *testing.T, body string) [][2]string {
	t.Helper()
	var out [][2]string
	sc := bufio.NewScanner(strings.NewReader(body))
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			out = append(out, [2]string{event, strings.TrimPrefix(line, "data: ")})
			event = ""
		}
	}
	return out
}

func TestOpenAIChatStream(t *testing.T) {
	_, srv := newTestServer(t, Config{ChunkDelay: time.Millisecond})
	body := strings.Replace(groundedPassChat, `{"model"`, `{"stream":true,"stream_options":{"include_usage":true},"model"`, 1)
	resp, b := do(t, srv, call{path: "/v1/chat/completions", body: body})
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	events := sseEvents(t, string(b))
	if last := events[len(events)-1][1]; last != "[DONE]" {
		t.Fatalf("last event %q", last)
	}
	var text strings.Builder
	finish, usage := "", false
	for _, e := range events[:len(events)-1] {
		m := decodeJSON(t, []byte(e[1]))
		if m["object"] != "chat.completion.chunk" {
			t.Fatalf("chunk %s", e[1])
		}
		if _, ok := m["usage"]; ok {
			usage = true
			continue
		}
		c := m["choices"].([]any)[0].(map[string]any)
		if d, ok := c["delta"].(map[string]any)["content"].(string); ok {
			text.WriteString(d)
		}
		if f, ok := c["finish_reason"].(string); ok {
			finish = f
		}
	}
	if text.String() != groundedPassAnswer || finish != "stop" || !usage {
		t.Errorf("stream text %q finish %q usage %v", text.String(), finish, usage)
	}
}

func TestOpenAICompletions(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	_, b := do(t, srv, call{path: "/v1/completions", body: `{"model":"m","prompt":"hi"}`})
	m := decodeJSON(t, b)
	if m["object"] != "text_completion" || !strings.Contains(m["choices"].([]any)[0].(map[string]any)["text"].(string), "mock answer") {
		t.Errorf("completion %s", b)
	}
	_, b = do(t, srv, call{path: "/v1/completions", body: `{"model":"m","prompt":"hi","stream":true}`})
	events := sseEvents(t, string(b))
	var text strings.Builder
	for _, e := range events[:len(events)-1] {
		text.WriteString(decodeJSON(t, []byte(e[1]))["choices"].([]any)[0].(map[string]any)["text"].(string))
	}
	if text.String() != "This is a mock answer from mock-upstream." || events[len(events)-1][1] != "[DONE]" {
		t.Errorf("completion stream %q", text.String())
	}
}

func TestAnthropicMessages(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	body := `{"model":"claude-haiku-4-5","max_tokens":100,"system":"s","messages":[{"role":"user","content":"How long do I have to get a refund on an annual plan?"}]}`
	_, b := do(t, srv, call{path: "/v1/messages", body: body})
	m := decodeJSON(t, b)
	if m["type"] != "message" || m["stop_reason"] != "end_turn" || m["content"].([]any)[0].(map[string]any)["text"] != groundedPassAnswer {
		t.Errorf("message %s", b)
	}
	_, b = do(t, srv, call{path: "/v1/messages", body: strings.Replace(body, `"max_tokens"`, `"stream":true,"max_tokens"`, 1)})
	events := sseEvents(t, string(b))
	var names []string
	var text strings.Builder
	for _, e := range events {
		names = append(names, e[0])
		d := decodeJSON(t, []byte(e[1]))
		if d["type"] != e[0] {
			t.Errorf("event %s carries type %v", e[0], d["type"])
		}
		if e[0] == "content_block_delta" {
			text.WriteString(d["delta"].(map[string]any)["text"].(string))
		}
	}
	got := strings.Join(names, ",")
	if !strings.HasPrefix(got, "message_start,content_block_start,ping,content_block_delta") ||
		!strings.HasSuffix(got, "content_block_delta,content_block_stop,message_delta,message_stop") || text.String() != groundedPassAnswer {
		t.Errorf("events %s text %q", got, text.String())
	}
}

func TestOpenRouterChat(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	resp, _ := do(t, srv, call{path: "/api/v1/chat/completions", body: `{"model":"anthropic/claude-haiku-4.5","messages":[]}`})
	if resp.StatusCode != 401 {
		t.Errorf("no key: %d", resp.StatusCode)
	}
	auth := map[string]string{"Authorization": "Bearer sk-or-v1-x"}
	_, b := do(t, srv, call{path: "/api/v1/chat/completions", header: auth, body: `{"model":"anthropic/claude-haiku-4.5","messages":[],"usage":{"include":true}}`})
	m := decodeJSON(t, b)
	u := m["usage"].(map[string]any)
	if m["provider"] != "Mock" || u["cost"] == nil || u["cost"].(float64) <= 0 {
		t.Errorf("openrouter chat %s", b)
	}
	_, b = do(t, srv, call{path: "/api/v1/chat/completions", header: auth, body: `{"model":"m","messages":[]}`})
	if strings.Contains(string(b), `"cost"`) {
		t.Error("usage.cost only with usage.include")
	}
}

func decisionBody(model, session string) string {
	return `{"model":"` + model + `","session_id":"` + session + `","state":{"assistant_response":"x"},"questions":{` +
		`"groundedness__v1":{"type":"noul","instructions":"i","criteria":{"true":"t","false":"f"}},` +
		`"refusal__v1":{"type":"choice","instructions":"i","criteria":{"a":{"what":"x"},"b":null,"c":null}},` +
		`"quality__v1":{"type":"score","instructions":"i","criteria":["bad","ok","good"]}}}`
}

var orKey = map[string]string{"Authorization": "Bearer sk-or-v1-test"}

func TestDecisions(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	resp, b := do(t, srv, call{path: "/api/alpha/decisions", header: orKey, body: decisionBody("typesafe/jev-1.13", "s1")})
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	m := decodeJSON(t, b)
	usage := m["usage"].(map[string]any)
	if m["model"] != DefaultSnapshot || m["provider"] != "TypeSafe" || usage["input_tokens"].(float64) <= requestOverhead || usage["cost"] == nil {
		t.Errorf("decision %s", b)
	}
	answers := m["answers"].(map[string]any)
	noul := answers["groundedness__v1"].(map[string]any)
	if p := noul["noul"].(float64); p < 0 || p > 1 || p != math.Round(p*100)/100 || noul["type"] != "noul" {
		t.Errorf("noul %v", noul)
	}
	choice := answers["refusal__v1"].(map[string]any)
	var sum float64
	for _, p := range choice["probabilities"].(map[string]any) {
		sum += p.(float64)
	}
	if math.Abs(sum-1) > 0.03 || choice["choice"] == nil || choice["confidence"] == nil {
		t.Errorf("choice %v (sum %v)", choice, sum)
	}
	score := answers["quality__v1"].(map[string]any)
	if len(score["probabilities"].(map[string]any)) != 3 || len(score["legend"].(map[string]any)) != 3 || score["score"] == nil {
		t.Errorf("score %v", score)
	}
	// Deterministic.
	_, again := do(t, srv, call{path: "/api/alpha/decisions", header: orKey, body: decisionBody("typesafe/jev-1.13", "s1")})
	a1, _ := json.Marshal(answers)
	a2, _ := json.Marshal(decodeJSON(t, again)["answers"])
	if string(a1) != string(a2) {
		t.Errorf("hash answers differ:\n%s\n%s", a1, a2)
	}
	// systemone on OpenRouter has the same shape.
	_, b = do(t, srv, call{path: "/api/v1/systemone", header: orKey, body: decisionBody("~typesafe/jev-latest", "s1")})
	if m := decodeJSON(t, b); m["model"] != DefaultSnapshot || m["usage"].(map[string]any)["cost"] == nil {
		t.Errorf("systemone %s", b)
	}
	// TypeSafe-shaped /v1/systemone: no cost, no provider, model as requested.
	_, b = do(t, srv, call{path: "/v1/systemone", header: orKey, body: decisionBody("jev-1.13.0", "")})
	if m := decodeJSON(t, b); m["model"] != "jev-1.13.0" || m["provider"] != nil || m["usage"].(map[string]any)["cost"] != nil {
		t.Errorf("typesafe %s", b)
	}
}

func TestDecisionScenarioAnswersAndSnapshot(t *testing.T) {
	s, srv := newTestServer(t, Config{})
	h := map[string]string{"Authorization": "Bearer k", "X-TrustAgent-Scenario": "refund-contradiction-flag"}
	body := `{"model":"typesafe/jev-1.13","state":{"x":1},"questions":{"groundedness__v1":{"type":"noul","instructions":"i"},"other":{"type":"noul","instructions":"i"}}}`
	_, b := do(t, srv, call{path: "/api/alpha/decisions", header: h, body: body})
	answers := decodeJSON(t, b)["answers"].(map[string]any)
	if answers["groundedness__v1"].(map[string]any)["noul"] != 0.96 || answers["other"] == nil {
		t.Errorf("scenario answers %s", b)
	}
	s.SetSnapshot("typesafe/jev-1.13-20261015")
	_, b = do(t, srv, call{path: "/api/alpha/decisions", header: h, body: body})
	if decodeJSON(t, b)["model"] != "typesafe/jev-1.13-20261015" {
		t.Errorf("snapshot override %s", b)
	}
	h["X-Mock-Snapshot"] = "typesafe/jev-1.14-20261101"
	_, b = do(t, srv, call{path: "/api/alpha/decisions", header: h, body: body})
	if decodeJSON(t, b)["model"] != "typesafe/jev-1.14-20261101" {
		t.Errorf("snapshot header %s", b)
	}
}

func TestDecisionValidationAndKeys(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	cases := []struct {
		path, key, body string
		status          int
		contains        string
	}{
		{"/api/alpha/decisions", "", decisionBody("m", ""), 401, "No auth credentials"},
		{"/api/alpha/decisions", "sk-invalid", decisionBody("m", ""), 401, "User not found"},
		{"/api/alpha/decisions", "sk-forbidden", decisionBody("m", ""), 403, "disabled"},
		{"/api/alpha/decisions", "sk-nocredits", decisionBody("m", ""), 402, "Insufficient credits"},
		{"/api/alpha/decisions", "k", `{`, 400, "not a JSON object"},
		{"/api/alpha/decisions", "k", `{"state":{},"questions":{"q":{"type":"noul","instructions":"i"}}}`, 400, "model: field required"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":[],"questions":{"q":{"type":"noul","instructions":"i"}}}`, 400, "state"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{},"questions":{}}`, 400, "at least one question"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{},"questions":{"q":{"type":"noul"}}}`, 400, "q.instructions"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{},"questions":{"q":{"type":"noul","instructions":"i","criteria":{"true":"t"}}}}`, 400, "both true and false"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{},"questions":{"q":{"type":"choice","instructions":"i","criteria":{}}}}`, 400, "1 to 255"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{},"questions":{"q":{"type":"score","instructions":"i","criteria":[]}}}`, 400, "1 to 10"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{},"questions":{"q":{"type":"rank","instructions":"i"}}}`, 400, "must be one of"},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{},"questions":{"q":{"type":"score","instructions":"i","criteria":["one"]}}}`, 200, "answers"},
		{"/v1/systemone", "k", `{"model":"m","state":{},"questions":{"q":{"type":"score","instructions":"i","criteria":["one"]}}}`, 422, `"loc":["body","questions","q","criteria"]`},
		{"/v1/systemone", "sk-invalid", decisionBody("m", ""), 401, `"detail":{"error_type":"authentication_error"`},
		{"/api/alpha/decisions", "k", `{"model":"m","state":{"big":"` + strings.Repeat("x", 140000) + `"},"questions":{"q":{"type":"noul","instructions":"i"}}}`, 413, "32000-token"},
	}
	for _, c := range cases {
		h := map[string]string{}
		if c.key != "" {
			h["Authorization"] = "Bearer " + c.key
		}
		resp, b := do(t, srv, call{path: c.path, header: h, body: c.body})
		if resp.StatusCode != c.status || !strings.Contains(string(b), c.contains) {
			t.Errorf("%s %s: %d %.200s, want %d containing %q", c.path, c.body[:min(len(c.body), 60)], resp.StatusCode, b, c.status, c.contains)
		}
	}
}

func TestFaults(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	cases := []struct {
		fault, path       string
		status            int
		contentType, body string
		retryAfter        string
	}{
		{"html200", "/api/alpha/decisions", 200, "text/html; charset=utf-8", "<!DOCTYPE html>", ""},
		{"402_inflight", "/api/alpha/decisions", 402, "application/json", `"limit_source":"openrouter_in_flight_budget"`, "1"},
		{"402_credits", "/api/alpha/decisions", 402, "application/json", "Insufficient credits", ""},
		{"429", "/api/alpha/decisions", 429, "application/json", `"code":429`, "1"},
		{"529", "/api/alpha/decisions", 529, "application/json", "overloaded", ""},
		{"413", "/api/alpha/decisions", 413, "application/json", "32000-token", ""},
		{"404_data_policy", "/api/alpha/decisions", 404, "application/json", "No endpoints found matching your data policy", ""},
		{"400_context", "/api/alpha/decisions", 400, "application/json", "maximum context length", ""},
		{"422", "/v1/systemone", 422, "application/json", `"detail":[`, ""},
		{"503", "/v1/systemone", 503, "application/json", `"detail":{"error_type":"api_error"`, ""},
		{"bad_json", "/api/alpha/decisions", 200, "application/json", `{"id":"broken"`, ""},
		{"no_answers", "/api/alpha/decisions", 200, "application/json", `"model"`, ""},
		{"529", "/v1/messages", 529, "application/json", `{"error":{"message":"The provider is overloaded","type":"overloaded_error"},"type":"error"}`, ""},
		{"429", "/v1/chat/completions", 429, "application/json", `"type":"rate_limit_error"`, "1"},
		{"401", "/api/v1/key", 401, "application/json", "No auth credentials", ""},
	}
	for _, c := range cases {
		resp, b := do(t, srv, call{path: c.path, header: map[string]string{"Authorization": "Bearer k", "X-Mock-Fault": c.fault}, body: decisionBody("m", "")})
		if c.path == "/api/v1/key" {
			resp, b = do(t, srv, call{method: http.MethodGet, path: c.path, header: map[string]string{"Authorization": "Bearer k", "X-Mock-Fault": c.fault}})
		}
		if resp.StatusCode != c.status || resp.Header.Get("Content-Type") != c.contentType || !strings.Contains(string(b), c.body) || resp.Header.Get("Retry-After") != c.retryAfter {
			t.Errorf("%s on %s: %d %q %q %s", c.fault, c.path, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Retry-After"), b)
		}
	}
}

func TestFaultSelection(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	status := func(path string, h map[string]string) int {
		if h == nil {
			h = map[string]string{}
		}
		h["Authorization"] = "Bearer k"
		resp, _ := do(t, srv, call{path: path, header: h, body: decisionBody("m", "sess")})
		return resp.StatusCode
	}
	if s := status("/api/alpha/decisions?fault=529", nil); s != 529 {
		t.Errorf("query fault: %d", s)
	}
	if s := status("/fault/429/api/alpha/decisions", nil); s != 429 {
		t.Errorf("path fault: %d", s)
	}
	if s := status("/fault/"+url.PathEscape("503@1")+"/v1/systemone", map[string]string{"X-Mock-Key": "k1"}); s != 503 {
		t.Errorf("counted fault, first: %d", s)
	}
	if s := status("/fault/"+url.PathEscape("503@1")+"/v1/systemone", map[string]string{"X-Mock-Key": "k1"}); s != 200 {
		t.Errorf("counted fault clears: %d", s)
	}
	if s := status("/fault/"+url.PathEscape("503@1")+"/v1/systemone", map[string]string{"X-Mock-Key": "k2"}); s != 503 {
		t.Errorf("counted per key: %d", s)
	}
	// Counted per session_id without X-Mock-Key.
	if s := status("/api/alpha/decisions", map[string]string{"X-Mock-Fault": "413@1"}); s != 413 {
		t.Errorf("session count, first: %d", s)
	}
	if s := status("/api/alpha/decisions", map[string]string{"X-Mock-Fault": "413@1"}); s != 200 {
		t.Errorf("session count, second: %d", s)
	}
	if s := status("/api/alpha/decisions", map[string]string{"X-TrustAgent-Scenario": "fault:402_credits"}); s != 402 {
		t.Errorf("scenario fault: %d", s)
	}
	if s := status("/api/alpha/decisions", map[string]string{"X-TrustAgent-Scenario": "529"}); s != 529 {
		t.Errorf("bare fault scenario: %d", s)
	}
	if s := status("/api/alpha/decisions", map[string]string{"X-Mock-Fault": "not-a-fault"}); s != 200 {
		t.Errorf("an unknown fault is ignored: %d", s)
	}
	_, def := newTestServer(t, Config{Fault: "524"})
	resp, _ := do(t, def, call{path: "/v1/chat/completions", body: `{"model":"m","messages":[]}`})
	if resp.StatusCode != 524 {
		t.Errorf("server default fault: %d", resp.StatusCode)
	}
	if _, err := New(Config{Fault: "nope"}); err == nil {
		t.Error("an unknown default fault is refused")
	}
	for _, bad := range []string{"429@0", "429@x", "teapot"} {
		if _, _, err := parseFault(bad); err == nil {
			t.Errorf("parseFault(%q) must fail", bad)
		}
	}
}

func TestHangEndsWithTheClient(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/alpha/decisions", strings.NewReader(decisionBody("m", "")))
	req.Header.Set("X-Mock-Fault", "hang")
	start := time.Now()
	_, err := http.DefaultClient.Do(req)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("hang: err %v after %s", err, time.Since(start))
	}
	_, short := newTestServer(t, Config{HangMax: 30 * time.Millisecond})
	resp, _ := do(t, short, call{path: "/v1/messages", header: map[string]string{"X-Mock-Fault": "hang"}, body: `{}`})
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("a bounded hang ends with 504: %d", resp.StatusCode)
	}
}

func TestLatencyKnobs(t *testing.T) {
	_, srv := newTestServer(t, Config{JevLatency: 60 * time.Millisecond})
	start := time.Now()
	do(t, srv, call{path: "/api/alpha/decisions", header: orKey, body: decisionBody("m", "")})
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Errorf("jev latency: %s", el)
	}
	_, slow := newTestServer(t, Config{JevLatency: 5 * time.Second})
	start = time.Now()
	do(t, slow, call{path: "/api/alpha/decisions?latency_ms=0", header: orKey, body: decisionBody("m", "")})
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("latency override to 0: %s", el)
	}
	start = time.Now()
	do(t, srv, call{path: "/v1/chat/completions", header: map[string]string{"X-Mock-Latency-Ms": "80"}, body: `{"model":"m","messages":[]}`})
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Errorf("header latency: %s", el)
	}
	start = time.Now()
	do(t, srv, call{path: "/v1/chat/completions", header: map[string]string{"X-Mock-Chunk-Delay-Ms": "15"}, body: `{"model":"m","stream":true,"messages":[]}`})
	if el := time.Since(start); el < 15*time.Millisecond*8 {
		t.Errorf("chunk delay: %s for 9+ events", el)
	}
}

func TestPKCE(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	verifier := "a-very-long-random-code-verifier-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	resp, _ := do(t, srv, call{method: http.MethodGet,
		path: "/auth?callback_url=" + url.QueryEscape("http://localhost:3000/connect/callback/?nonce=n1") + "&code_challenge=" + challenge + "&code_challenge_method=S256"})
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")
	if loc.Host != "localhost:3000" || loc.Query().Get("nonce") != "n1" || !strings.HasPrefix(code, "mockcode_") {
		t.Fatalf("redirect %s", loc)
	}
	resp, b := do(t, srv, call{path: "/api/v1/auth/keys", body: `{"code":"` + code + `","code_verifier":"wrong","code_challenge_method":"S256"}`})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong verifier: %d %s", resp.StatusCode, b)
	}
	// Codes are single-use, so a failed exchange burns it; authorize again.
	resp, _ = do(t, srv, call{method: http.MethodGet, path: "/auth?callback_url=" + url.QueryEscape("http://localhost:3000/cb") + "&code_challenge=" + challenge + "&code_challenge_method=S256"})
	loc, _ = url.Parse(resp.Header.Get("Location"))
	code = loc.Query().Get("code")
	resp, b = do(t, srv, call{path: "/api/v1/auth/keys", body: `{"code":"` + code + `","code_verifier":"` + verifier + `","code_challenge_method":"S256"}`})
	if resp.StatusCode != 200 || !strings.HasPrefix(decodeJSON(t, b)["key"].(string), "sk-or-v1-mock-") {
		t.Fatalf("exchange: %d %s", resp.StatusCode, b)
	}
	resp, _ = do(t, srv, call{path: "/api/v1/auth/keys", body: `{"code":"` + code + `","code_verifier":"` + verifier + `"}`})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("reused code: %d", resp.StatusCode)
	}
	for _, q := range []string{"/auth?callback_url=javascript:x&code_challenge=c", "/auth?callback_url=http://x/cb", "/auth?callback_url=http://x/cb&code_challenge=c&code_challenge_method=MD5"} {
		if resp, _ := do(t, srv, call{method: http.MethodGet, path: q}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", q, resp.StatusCode)
		}
	}
}

func TestKeyEndpoint(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	get := func(key string) (int, map[string]any) {
		h := map[string]string{}
		if key != "" {
			h["Authorization"] = "Bearer " + key
		}
		resp, b := do(t, srv, call{method: http.MethodGet, path: "/api/v1/key", header: h})
		if resp.StatusCode != 200 {
			return resp.StatusCode, nil
		}
		return 200, decodeJSON(t, b)["data"].(map[string]any)
	}
	if s, d := get("sk-or-v1-abcdefghijklmnop"); s != 200 || d["label"] != "sk-or-v1-a…nop" || d["limit"] != nil || d["usage"] != 0.0123 {
		t.Errorf("key %d %v", s, d)
	}
	if s, d := get("sk-or-nocredits-12345"); s != 200 || d["limit_remaining"] != 0.0 {
		t.Errorf("exhausted key %d %v", s, d)
	}
	for _, k := range []string{"", "sk-invalid-1", "sk-forbidden-1"} {
		if s, _ := get(k); s != 401 && s != 403 {
			t.Errorf("key %q: %d", k, s)
		}
	}
}

func TestRecordingAndReset(t *testing.T) {
	s, srv := newTestServer(t, Config{MaxRecorded: 2})
	do(t, srv, call{path: "/v1/chat/completions", header: map[string]string{"Authorization": "Bearer secret", "X-Api-Key": "secret2"}, body: `{"model":"a","messages":[]}`})
	do(t, srv, call{path: "/fault/529/v1/messages", body: `{"model":"b"}`})
	do(t, srv, call{method: http.MethodGet, path: "/healthz"})
	reqs := s.Requests()
	if len(reqs) != 2 || reqs[0].Path != "/v1/messages" || reqs[1].Path != "/healthz" || reqs[0].Header.Get("X-Mock-Path-Fault") != "529" {
		t.Fatalf("recorded %+v", reqs)
	}
	_, b := do(t, srv, call{method: http.MethodGet, path: "/_mock/requests"})
	if strings.Contains(string(b), "secret") {
		t.Error("credentials must be redacted")
	}
	s.Reset()
	do(t, srv, call{path: "/v1/chat/completions", header: map[string]string{"Authorization": "Bearer secret"}, body: `{}`})
	if r := s.Requests(); len(r) != 1 || r[0].Header.Get("Authorization") != "[redacted]" {
		t.Errorf("after reset %+v", r)
	}
	if resp, _ := do(t, srv, call{path: "/_mock/reset"}); resp.StatusCode != http.StatusNoContent || len(s.Requests()) != 0 {
		t.Error("reset endpoint")
	}
}

func TestScenarioFile(t *testing.T) {
	if _, err := New(Config{ScenariosPath: "/nonexistent/scenarios.json"}); err == nil {
		t.Error("a missing file fails")
	}
	for name, body := range map[string]string{"dup": `{"scenarios":[{"id":"a"},{"id":"a"}]}`, "noid": `{"scenarios":[{}]}`, "json": `{`} {
		if _, err := parseScenarios([]byte(body)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if got := lastUserText(map[string]json.RawMessage{"prompt": json.RawMessage(`["a","b"]`)}); got != "b" {
		t.Errorf("prompt list %q", got)
	}
}
