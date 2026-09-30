package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/store"
)

const (
	playAdminKey = "playground-admin-secret"
	playPath     = "/api/playground/run"
	// schemaGateDecision is the decision a PlaygroundResult carries (api.ts GateDecisionView).
	schemaGateDecision = "api.v1.schema.json#/$defs/GateDecisionView"
)

// newPlaygroundEnv is newTestEnv with an admin key and a hook on the configuration (an upstream
// with the gateway's own key).
func newPlaygroundEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	cfg := testConfig(t, envOpts{adminKey: playAdminKey})
	if mutate != nil {
		mutate(cfg)
	}
	eng := scenarioEngine(t, false)
	client, err := grpc.NewAuditClient(eng.Target, 5*time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	st := newMemStore()
	cfg.DatabaseURL = "postgres://configured-for-status"
	srv, err := newServer(serverDeps{cfg: cfg, engine: client, store: st, log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv.start()
	hs := httptest.NewServer(srv.router)
	t.Cleanup(func() {
		hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.shutdown(ctx)
	})
	deadline := time.Now().Add(5 * time.Second)
	for srv.pinger.Health().State == "unknown" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return &testEnv{t: t, srv: srv, http: hs, store: st, engine: eng}
}

// traceAuditStatuses are the values of api.ts TraceAuditStatus.
var traceAuditStatuses = []string{"none", "queued", "sampled_out", "dropped", "disabled", "running", "completed", "degraded", "failed", "not_assessed"}

// playRun is a PlaygroundRequest from a demo scenario's conversation, checked in audit mode (its
// scripted answer) or in chat mode (the upstream's answer).
func playRun(t *testing.T, scenarioID, mode string) map[string]any {
	t.Helper()
	s, ok := loadDemoScenarios(t)[scenarioID]
	if !ok {
		t.Fatalf("no scenario %s", scenarioID)
	}
	var req struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(s.Request, &req); err != nil {
		t.Fatal(err)
	}
	run := map[string]any{"mode": mode, "model": req.Model, "messages": req.Messages, "scenario": s.ID}
	if r := s.Headers["X-TrustAgent-Route"]; r != "" {
		run["route"] = r
	}
	if mode == "audit" {
		run["response"] = s.Response
	}
	return run
}

// post sends a PlaygroundRequest (a string is sent as it is) and returns the response.
func post(t *testing.T, env *testEnv, key string, body any) *http.Response {
	t.Helper()
	b, ok := body.(string)
	if !ok {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		b = string(raw)
	}
	r, _ := http.NewRequest(http.MethodPost, env.http.URL+playPath, strings.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("X-TrustAgent-Key", key)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// runPlayground posts a run and returns the status and body.
func runPlayground(t *testing.T, env *testEnv, key string, body any) (int, []byte) {
	t.Helper()
	resp := post(t, env, key, body)
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// playResult is api.ts PlaygroundResult as the tests read it.
type playResult struct {
	TraceID  string `json:"trace_id"`
	Upstream *struct {
		Status int   `json:"status"`
		Ms     int64 `json:"ms"`
	} `json:"upstream"`
	ResponseText  string            `json:"response_text"`
	DeliveredText string            `json:"delivered_text"`
	Headers       map[string]string `json:"headers"`
	Decision      json.RawMessage   `json:"decision"`
	AuditStatus   string            `json:"audit_status"`
}

// decodeResult checks a PlaygroundResult body against api.ts: exactly its members, a decision
// that is a GateDecisionView (decision/v1), and an audit status of TraceAuditStatus.
func decodeResult(t *testing.T, body []byte) (playResult, map[string]any) {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	var keys []string
	for k := range members {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"audit_status", "decision", "delivered_text", "headers", "response_text", "trace_id", "upstream"}; !slices.Equal(keys, want) {
		t.Errorf("members %v, want %v", keys, want)
	}
	var res playResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, schemaGateDecision, res.Decision)
	contractstest.ValidateJSON(t, schemaDecisionV1, res.Decision)
	if !slices.Contains(traceAuditStatuses, res.AuditStatus) {
		t.Errorf("audit_status %q is not a TraceAuditStatus", res.AuditStatus)
	}
	var d map[string]any
	_ = json.Unmarshal(res.Decision, &d)
	if d["trace_id"] != res.TraceID || res.Headers["X-TrustAgent-Trace-Id"] != res.TraceID || res.Headers["X-TrustAgent-Action"] == "" {
		t.Errorf("trace %s, decision trace %v, headers %v", res.TraceID, d["trace_id"], res.Headers)
	}
	return res, d
}

// errorBody decodes an ApiErrorBody (validated against the schema) with its trace_id.
func errorBody(t *testing.T, body []byte) (code string, fields []string, traceID string) {
	t.Helper()
	contractstest.ValidateJSON(t, schemaError, body)
	var e struct {
		Error struct {
			Code        string `json:"code"`
			FieldErrors []struct {
				Path string `json:"path"`
			} `json:"field_errors"`
		} `json:"error"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatal(err)
	}
	for _, f := range e.Error.FieldErrors {
		fields = append(fields, f.Path)
	}
	return e.Error.Code, fields, e.TraceID
}

// TestPlaygroundRunIsForWritingAdmins: the Playground needs an admin, and refuses runs in
// read-only mode.
func TestPlaygroundRunIsForWritingAdmins(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: playAdminKey})
	run := playRun(t, "refund-contradiction-flag", "audit")
	if code, body := runPlayground(t, env, "", run); code != http.StatusUnauthorized {
		t.Errorf("no key: %d %s", code, body)
	}
	if code, body := runPlayground(t, env, testKey, run); code != http.StatusForbidden || errorCodeOf(t, body) != codeForbidden {
		t.Errorf("a non-admin key: %d %s", code, body)
	}
	ro := newTestEnv(t, envOpts{adminKey: playAdminKey, readonly: true})
	if code, body := runPlayground(t, ro, playAdminKey, run); code != http.StatusForbidden || errorCodeOf(t, body) != "readonly" {
		t.Errorf("read-only mode: %d %s", code, body)
	}
}

// TestPlaygroundRunValidation: a body that is not a PlaygroundRequest is a 400, and a request
// with invalid members a 422 naming each of them; neither records a trace.
func TestPlaygroundRunValidation(t *testing.T) {
	// Each server takes playgroundLimit runs a minute from one admin: the cases share them out.
	var servers []*testEnv
	runs := 0
	env := func() *testEnv {
		if runs%playgroundLimit == 0 {
			servers = append(servers, newTestEnv(t, envOpts{adminKey: playAdminKey}))
		}
		runs++
		return servers[len(servers)-1]
	}
	if code, body := runPlayground(t, env(), playAdminKey, `{"mode":`); code != http.StatusBadRequest || errorCodeOf(t, body) != codeInvalidJSON {
		t.Errorf("invalid JSON: %d %s", code, body)
	}
	user := map[string]any{"role": "user", "content": "Can I get a refund?"}
	long := strings.Repeat("x", testConfig(t, envOpts{}).MaxTextChars+1)
	cases := []struct {
		name  string
		run   map[string]any
		field string
	}{
		{"mode", map[string]any{"mode": "draft", "messages": []any{user}}, "mode"},
		{"no user message", map[string]any{"mode": "audit", "response": "Yes.", "messages": []any{map[string]any{"role": "system", "content": "Be brief."}}}, "messages"},
		{"role", map[string]any{"mode": "audit", "response": "Yes.", "messages": []any{user, map[string]any{"role": "robot", "content": "hi"}}}, "messages[1].role"},
		{"long message", map[string]any{"mode": "audit", "response": "Yes.", "messages": []any{map[string]any{"role": "user", "content": long}}}, "messages[0].content"},
		{"audit without an answer", map[string]any{"mode": "audit", "messages": []any{user}}, "response"},
		{"chat with an answer", map[string]any{"mode": "chat", "model": "gpt-4o-mini", "response": "Yes.", "messages": []any{user}}, "response"},
		{"chat without a model", map[string]any{"mode": "chat", "messages": []any{user}}, "model"},
		{"route name", map[string]any{"mode": "audit", "response": "Yes.", "route": "Support Bot", "messages": []any{user}}, "route"},
		{"unknown route", map[string]any{"mode": "audit", "response": "Yes.", "route": "no-such-route", "messages": []any{user}}, "route"},
		{"mode override", map[string]any{"mode": "audit", "response": "Yes.", "mode_override": "strict", "messages": []any{user}}, "mode_override"},
		{"document id", map[string]any{"mode": "audit", "response": "Yes.", "messages": []any{user}, "evidence": []any{map[string]any{"id": "a b", "text": "Doc."}}}, "evidence[0].id"},
		{"empty document", map[string]any{"mode": "audit", "response": "Yes.", "messages": []any{user}, "evidence": []any{map[string]any{"text": " "}}}, "evidence[0].text"},
		{"scenario", map[string]any{"mode": "audit", "response": "Yes.", "scenario": "Refund!", "messages": []any{user}}, "scenario"},
	}
	for _, tc := range cases {
		code, body := runPlayground(t, env(), playAdminKey, tc.run)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", tc.name, code, body)
			continue
		}
		if c, fields, _ := errorBody(t, body); c != codeValidationFailed || !slices.Contains(fields, tc.field) {
			t.Errorf("%s: code %s fields %v, want %s", tc.name, c, fields, tc.field)
		}
	}
	docs := make([]any, maxPlaygroundDocs+1)
	for i := range docs {
		docs[i] = map[string]any{"text": "Doc."}
	}
	code, body := runPlayground(t, env(), playAdminKey, map[string]any{"mode": "audit", "response": "Yes.", "messages": []any{user}, "evidence": docs})
	if _, fields, _ := errorBody(t, body); code != http.StatusUnprocessableEntity || !slices.Contains(fields, "evidence") {
		t.Errorf("too many documents: %d %s", code, body)
	}
	for i, srv := range servers {
		if code, body := srv.get("/api/traces?source=playground", testKey); code != http.StatusOK || !strings.Contains(string(body), `"items":[]`) {
			t.Errorf("server %d: refused runs recorded traces: %d %s", i, code, body)
		}
	}
}

// TestPlaygroundRunAuditMode: an audit-mode run checks the pasted answer on the named route and
// records a playground trace whose deep audit has selection playground.
func TestPlaygroundRunAuditMode(t *testing.T) {
	env := newPlaygroundEnv(t, nil)
	run := playRun(t, "refund-contradiction-flag", "audit")
	code, body := runPlayground(t, env, playAdminKey, run)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	res, d := decodeResult(t, body)
	if res.Upstream != nil || res.ResponseText != run["response"] || res.DeliveredText != res.ResponseText || res.AuditStatus != "queued" {
		t.Errorf("result %+v", res)
	}
	if d["route"] != "support-bot" || d["mode"] != "flag" || d["action"] != "flag" || d["would_action"] != "block" || d["attempt"] != 0.0 {
		t.Errorf("decision route %v mode %v action %v would %v attempt %v", d["route"], d["mode"], d["action"], d["would_action"], d["attempt"])
	}
	h := res.Headers
	if h["X-TrustAgent-Action"] != "flag" || h["X-TrustAgent-Would-Action"] != "block" || h["X-TrustAgent-Source"] != "playground" ||
		!strings.HasPrefix(h["X-TrustAgent-Route"], "support-bot@v") || h["X-TrustAgent-Audit"] != "queued" {
		t.Errorf("headers %v", h)
	}

	tr := env.settledTrace(res.TraceID)
	if tr.Source != "playground" || tr.APIFormat != "openai_chat" || tr.AnswerSource == nil || *tr.AnswerSource != "scripted" ||
		tr.Audit.Selection == nil || *tr.Audit.Selection != "playground" || tr.Audit.Status != store.AuditCompleted {
		t.Errorf("trace source %s format %s answer %v selection %v audit %s", tr.Source, tr.APIFormat, tr.AnswerSource, tr.Audit.Selection, tr.Audit.Status)
	}
	if slices.Contains(tr.Flags, "test_mode") {
		t.Errorf("flags %v: a Playground run is no test-mode traffic", tr.Flags)
	}
	code, list := env.get("/api/traces?source=playground", testKey)
	if code != http.StatusOK || !strings.Contains(string(list), res.TraceID) {
		t.Errorf("the run is not listed as a playground trace: %d %s", code, list)
	}
}

// TestPlaygroundRunModeOverride: the admin's override is not bound by overrides.client_mode
// (none in config/policies.yaml); block needs the enforcing gateway.
func TestPlaygroundRunModeOverride(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: playAdminKey})
	run := playRun(t, "refund-contradiction-flag", "audit")
	run["route"], run["mode_override"] = "faq-bot", "flag" // an observe route
	code, body := runPlayground(t, env, playAdminKey, run)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if _, d := decodeResult(t, body); d["mode"] != "flag" || d["action"] != "flag" {
		t.Errorf("flag override on an observe route: mode %v action %v", d["mode"], d["action"])
	}

	run["route"], run["mode_override"] = "support-bot", "block"
	code, body = runPlayground(t, env, playAdminKey, run)
	if !gatewayEnforces {
		if c, fields, _ := errorBody(t, body); code != http.StatusUnprocessableEntity || c != codeValidationFailed || !slices.Contains(fields, "mode_override") {
			t.Errorf("block before the gateway enforces: %d %s", code, body)
		}
		return
	}
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	res, d := decodeResult(t, body)
	if d["mode"] != "block" || res.Headers["X-TrustAgent-Action"] != "block" || res.DeliveredText == res.ResponseText || res.DeliveredText == "" {
		t.Errorf("block override: mode %v headers %v delivered %q", d["mode"], res.Headers, res.DeliveredText)
	}
}

// fakeChatUpstream answers every chat completion with text, and records the requests.
func fakeChatUpstream(t *testing.T, text string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	body, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-play", "object": "chat.completion", "created": 1, "model": "gpt-4o-mini",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 20, "completion_tokens": 9, "total_tokens": 29},
	})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(up.Close)
	return up, &seen
}

// TestPlaygroundRunChatMode: chat mode needs the gateway's own upstream key (503
// credentials_unavailable without one); with it, the upstream answers and the result reports the
// call.
func TestPlaygroundRunChatMode(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: playAdminKey})
	run := playRun(t, "refund-contradiction-flag", "chat")
	code, body := runPlayground(t, env, playAdminKey, run)
	if c, _, traceID := errorBody(t, body); code != http.StatusServiceUnavailable || c != "credentials_unavailable" || traceID != "" {
		t.Errorf("without an upstream key: %d %s", code, body)
	}

	answer := "You can get a full refund within 60 days of purchase."
	up, seen := fakeChatUpstream(t, answer)
	env = newPlaygroundEnv(t, func(c *config.Config) { c.UpstreamURL, c.UpstreamAPIKey = up.URL, "sk-gateway-own-key" })
	code, body = runPlayground(t, env, playAdminKey, run)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	res, d := decodeResult(t, body)
	if res.Upstream == nil || res.Upstream.Status != http.StatusOK || res.ResponseText != answer || d["action"] != "flag" {
		t.Errorf("result %+v, action %v", res, d["action"])
	}
	if len(*seen) != 1 || (*seen)[0].Header.Get("Authorization") != "Bearer sk-gateway-own-key" || (*seen)[0].Header.Get("X-Trustagent-Key") != "" {
		t.Errorf("upstream calls %d", len(*seen))
	}
	if tr := env.settledTrace(res.TraceID); tr.AnswerSource == nil || *tr.AnswerSource != "live" || tr.UpstreamStatus == nil || *tr.UpstreamStatus != http.StatusOK {
		t.Errorf("trace answer source %v upstream %v", tr.AnswerSource, tr.UpstreamStatus)
	}
}

// sseEvent is one event of a streamed run.
type sseEvent struct {
	name string
	data []byte
}

// readEvents reads a streamed run's events.
func readEvents(t *testing.T, resp *http.Response) []sseEvent {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%d %s: %s", resp.StatusCode, ct, b)
	}
	var out []sseEvent
	rd := llmapi.NewReader(resp.Body)
	for {
		ev, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Incomplete || !ev.HasData {
			t.Fatalf("event %+v", ev)
		}
		out = append(out, sseEvent{ev.Name, bytes.Clone(ev.Data)})
	}
}

// TestPlaygroundRunStream: stream:true sends the answer as token events, then the decision, then
// the result (done).
func TestPlaygroundRunStream(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: playAdminKey})
	run := playRun(t, "refund-contradiction-flag", "audit")
	run["stream"] = true
	events := readEvents(t, post(t, env, playAdminKey, run))
	var names []string
	var text strings.Builder
	for _, ev := range events {
		if len(names) == 0 || names[len(names)-1] != ev.name {
			names = append(names, ev.name)
		}
		if ev.name == "token" {
			var tok struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.data, &tok); err != nil {
				t.Fatal(err)
			}
			text.WriteString(tok.Text)
		}
	}
	if !slices.Equal(names, []string{"token", "decision", "done"}) {
		t.Fatalf("event order %v", names)
	}
	if text.String() != run["response"] {
		t.Errorf("tokens %q", text.String())
	}
	decisionEv, done := events[len(events)-2], events[len(events)-1]
	contractstest.ValidateJSON(t, schemaDecisionV1, decisionEv.data)
	res, d := decodeResult(t, done.data)
	var streamed map[string]any
	_ = json.Unmarshal(decisionEv.data, &streamed)
	if streamed["trace_id"] != res.TraceID || d["action"] != "flag" || res.ResponseText != run["response"] {
		t.Errorf("decision event trace %v, result %+v", streamed["trace_id"], res)
	}
}

// TestPlaygroundRunStreamError: a streamed run whose upstream fails once the stream has started
// ends with an error event (an ApiErrorBody naming the trace), and the trace is recorded.
func TestPlaygroundRunStreamError(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	env := newPlaygroundEnv(t, func(c *config.Config) { c.UpstreamURL, c.UpstreamAPIKey = closed.URL, "sk-gateway-own-key" })
	run := playRun(t, "refund-contradiction-flag", "chat")
	run["stream"] = true
	events := readEvents(t, post(t, env, playAdminKey, run))
	if len(events) != 1 || events[0].name != "error" {
		t.Fatalf("events %+v", events)
	}
	code, _, traceID := errorBody(t, events[0].data)
	if code != "upstream_error" || traceID == "" {
		t.Errorf("error event %s", events[0].data)
	}
	tr := env.settledTrace(traceID)
	if tr.Source != "playground" || tr.SkipReason == nil || *tr.SkipReason != "upstream_error" {
		t.Errorf("trace source %s skip %v", tr.Source, tr.SkipReason)
	}
}

// TestPlaygroundRunLimit: the Playground has its own limit of 10 runs a minute, counted once per
// run (the general limit is not stacked on it).
func TestPlaygroundRunLimit(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: playAdminKey})
	for i := range playgroundLimit {
		if code, body := runPlayground(t, env, playAdminKey, `{}`); code != http.StatusUnprocessableEntity {
			t.Fatalf("run %d: %d %s", i+1, code, body)
		}
	}
	resp := post(t, env, playAdminKey, `{}`)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests || errorCodeOf(t, body) != codeRateLimited || resp.Header.Get("Retry-After") == "" {
		t.Errorf("run %d: %d %s", playgroundLimit+1, resp.StatusCode, body)
	}
}
