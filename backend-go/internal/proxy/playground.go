package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/upstream"
)

// Playground modes (PlaygroundRequest.mode): chat asks the upstream for the answer, audit checks
// an answer the caller supplies.
const (
	PlaygroundChat  = "chat"
	PlaygroundAudit = "audit"
)

// Error codes of a Playground run the /api surface does not share with /v1.
const (
	CodeValidationFailed       = "validation_failed"
	CodeCredentialsUnavailable = "credentials_unavailable"
)

// playgroundPath is the API a Playground run stands for: an OpenAI chat completion. Routes match
// it, the trace records it, and chat mode sends the upstream call there.
const playgroundPath = llmapi.PathChatCompletions

// maxPieceRunes caps one token event of a streamed run.
const maxPieceRunes = 32

// PlaygroundMessage is one message of a run's conversation (api.ts ChatMessage).
type PlaygroundMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// PlaygroundDocument is one evidence document of a run (PlaygroundRequest.evidence). It reaches
// the gate as a trustagent.context item, so the route's evidence.sources decide whether it counts
// (source extension; the document's source becomes its label).
type PlaygroundDocument struct {
	ID     string `json:"id,omitempty"`
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
}

// PlaygroundInput is one Playground run (SPEC §2.4 row 61, D12). The caller checks the members
// first (sizes, roles, ids, a user message, a model in chat mode, a response in audit mode);
// Playground checks what needs the policy in effect and the upstreams.
type PlaygroundInput struct {
	// Mode is PlaygroundChat or PlaygroundAudit.
	Mode string
	// Route names the route the run is checked on; "" matches the run as the admin's own POST
	// /v1/chat/completions of Model: by KeyLabel, with no header of the dashboard's.
	Route    string
	Model    string
	Messages []PlaygroundMessage
	Evidence []PlaygroundDocument
	// Response is the answer an audit-mode run checks.
	Response string
	// ModeOverride, when set, is the mode the run is checked in: the admin's override, which
	// overrides.client_mode does not restrict. Block and fallback still need the enforcing
	// gateway. "" keeps the route's mode.
	ModeOverride string
	// Scenario is a demo scenario id: the mock decision model and judge answer with its scripted
	// answers. It is honoured only in test mode, like X-TrustAgent-Scenario.
	Scenario string
	// KeyLabel is the label of the admin who runs it, recorded on the trace.
	KeyLabel string
	// Accepted, when set, is called once the run passed every check, before the trace is
	// recorded and the upstream called: a streamed run commits its event stream there, so what
	// fails later (the upstream call) reaches it as an error event.
	Accepted func()
}

// PlaygroundUpstream is the upstream call of a chat-mode run.
type PlaygroundUpstream struct {
	Status int   `json:"status"`
	Ms     int64 `json:"ms"`
}

// PlaygroundResult is api.ts PlaygroundResult: the trace, the upstream call (null in audit
// mode), the answer that was checked, what the app would have received, the response headers the
// gateway would have sent, the attempt-0 decision and the deep audit's status.
type PlaygroundResult struct {
	TraceID       string              `json:"trace_id"`
	Upstream      *PlaygroundUpstream `json:"upstream"`
	ResponseText  string              `json:"response_text"`
	DeliveredText string              `json:"delivered_text"`
	Headers       map[string]string   `json:"headers"`
	Decision      *gate.Decision      `json:"decision"`
	AuditStatus   string              `json:"audit_status"`
}

// PlaygroundError is a run that was refused, or whose upstream failed: Status and Code are the
// ApiErrorBody the caller answers with.
type PlaygroundError struct {
	Status  int
	Code    string
	Message string
	// Field names the request member at fault (a 422).
	Field string
	// TraceID is the trace the run recorded before it failed; "" when it recorded none.
	TraceID string
	// UpstreamStatus is the upstream's non-2xx status; 0 when the call itself failed.
	UpstreamStatus int
}

func (e *PlaygroundError) Error() string { return e.Message }

// playgroundBody is the OpenAI chat completion a run stands for: the upstream call of chat mode
// (always stream:false, D12; ForwardBody drops the gateway's members), and the request the gate
// reads its evidence from.
type playgroundBody struct {
	Model        string               `json:"model,omitempty"`
	Messages     []PlaygroundMessage  `json:"messages"`
	Stream       bool                 `json:"stream"`
	TrustAgent   *playgroundExtension `json:"trustagent,omitempty"`
	TestResponse *string              `json:"test_response,omitempty"`
}

type playgroundExtension struct {
	Context []PlaygroundDocument `json:"context"`
}

// Playground runs one Playground request (SPEC §2.4 row 61, §3.3; D12) through the /v1 exchange
// helpers, the way POST /v1/chat/completions would have handled it:
//
//   - the flow comes from newFlow, on a request of its own (runContext): the /v1 path and JSON
//     content headers only, so nothing the dashboard's browser sent (its cookies and origin, a
//     credential a reverse proxy added) reaches the route match or an upstream call made for the
//     run, the route's fallback call included. The route is the named one, or the one the
//     policy matches for the admin's key label; the mode is the route's or the admin's
//     ModeOverride; source and audit selection are playground, so every run with an answer gets
//     a deep audit (disabled without an engine);
//   - audit mode answers through the test_response path (scriptedAnswer, no upstream call);
//     chat mode calls the upstream of the OpenAI family with stream:false, and only with the
//     gateway's own key (inject mode): without one the run is refused with 503
//     credentials_unavailable, so no dashboard credential ever goes upstream;
//   - the answer is gated synchronously, even on an observe route, and enforceJSON (with the
//     route's fallback) decides what the app would have received;
//   - decided records it once; the headers are the ones setDecisionHeaders writes on /v1.
//
// ctx is the run's context: the upstream and fallback calls run on it, so a caller that goes
// away cancels them; the trace is still recorded. onToken, when set, receives the checked answer
// in pieces before the gate runs (a streamed run; real token streaming is P4). A refused run
// records nothing; a run whose upstream failed records its trace and returns a PlaygroundError
// that names it.
func (h *Handler) Playground(ctx context.Context, c *gin.Context, in PlaygroundInput, onToken func(text string)) (*PlaygroundResult, error) {
	start := h.now()
	chat := in.Mode == PlaygroundChat
	body := playgroundBody{Model: in.Model, Messages: in.Messages}
	if body.Messages == nil {
		body.Messages = []PlaygroundMessage{}
	}
	if len(in.Evidence) > 0 {
		body.TrustAgent = &playgroundExtension{Context: in.Evidence}
	}
	if !chat {
		response := in.Response
		body.TestResponse = &response
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("playground: the request does not marshal: %w", err)
	}
	req, err := llmapi.ParseRequest(llmapi.FormatOpenAIChat, raw)
	if err != nil {
		return nil, fmt.Errorf("playground: the request does not parse: %w", err)
	}

	f := h.newFlow(runContext(c), llmapi.FormatOpenAIChat, playgroundPath, raw, req, start)
	x, t := f.x, f.x.Trace
	var route *policy.Route
	if in.Route != "" {
		named, ok := x.Policy.Route(in.Route)
		if !ok {
			return nil, &PlaygroundError{Status: http.StatusUnprocessableEntity, Code: CodeValidationFailed, Field: "route",
				Message: fmt.Sprintf("No route is named %q in the policy in effect.", in.Route)}
		}
		route = named
	} else {
		// newFlow matched without a key label: the /api caller's identity is not an ingest key's.
		route = x.Policy.Match(&policy.Request{
			Path: playgroundPath, Model: in.Model, Headers: f.c.Request.Header, KeyLabel: in.KeyLabel,
		})
	}
	mode := route.Mode
	if in.ModeOverride != "" && in.ModeOverride != route.Mode {
		// The admin's override is not restricted by overrides.client_mode, but a mode this build
		// does not enforce stays refused, as for every client.
		admin := *x.Policy
		admin.Overrides.ClientMode = policy.ClientModeAny
		var ok bool
		if mode, ok = admin.Mode(route, in.ModeOverride); !ok {
			return nil, &PlaygroundError{Status: http.StatusUnprocessableEntity, Code: CodeValidationFailed, Field: "mode_override",
				Message: fmt.Sprintf("%s is not enforced by this gateway yet: use observe or flag.", in.ModeOverride)}
		}
	}
	if chat && (f.up == nil || !f.up.Injecting()) {
		return nil, &PlaygroundError{Status: http.StatusServiceUnavailable, Code: CodeCredentialsUnavailable,
			Message: "Chat mode needs the gateway's own upstream key (TRUTHTABLE_UPSTREAM_API_KEY): the Playground never sends a dashboard credential upstream. Use audit mode to check an answer you paste."}
	}

	if route != f.route {
		h.useRoute(f, route)
	}
	t.Mode, t.Source, t.KeyLabel = mode, gate.SourcePlayground, in.KeyLabel
	t.Scenario = ""
	if h.testMode && scenarioID.MatchString(in.Scenario) {
		t.Scenario = in.Scenario
	}
	// The answer is the caller's (audit) or the upstream's (chat); the run is no test-mode traffic.
	t.Flags = slices.DeleteFunc(t.Flags, func(flag string) bool { return flag == gate.FlagTestMode })
	t.AnswerSource = gate.AnswerLive
	if !chat {
		t.AnswerSource = gate.AnswerScripted
	}
	x.AuditSelection = gate.SelectionPlayground

	if in.Accepted != nil {
		in.Accepted()
	}
	h.sink.TraceStarted(f.record, t)

	var ans *answer
	var up gate.Upstream // audit mode: no upstream call
	if chat {
		f.upStart = h.now()
		resp, cancel, err := f.up.Do(ctx, http.MethodPost, playgroundPath, "", f.c.Request.Header, f.forward)
		if err != nil {
			return nil, h.playgroundUpstreamFailed(ctx, f, 0, err)
		}
		status := resp.StatusCode
		if ans, err = h.upstreamAnswer(f, resp, cancel); err != nil {
			return nil, h.playgroundUpstreamFailed(ctx, f, status, err)
		}
		up = ans.upstreamRecord()
	} else {
		ans = h.scriptedAnswer(f, in.Response)
	}
	defer ans.close()
	f.takeAnswer(ans)
	if onToken != nil {
		for _, piece := range playgroundPieces(ans.text) {
			onToken(piece)
		}
	}

	// As on /v1, the gate waits on the record context: only its own deadline stops it.
	res := h.gate.Evaluate(f.record, x)
	out := h.enforceJSON(ctx, f, res, ans)
	audit := h.decided(f, res, up, h.now(), out.final)

	if ans.status < 200 || ans.status > 299 {
		return nil, &PlaygroundError{
			Status: http.StatusBadGateway, Code: CodeUpstreamError, TraceID: t.TraceID, UpstreamStatus: ans.status,
			Message: fmt.Sprintf("The upstream LLM API answered HTTP %d%s", ans.status, upstreamHint(ans.status)),
		}
	}
	result := &PlaygroundResult{
		TraceID: t.TraceID, ResponseText: ans.text, DeliveredText: out.final.Text,
		Headers: playgroundHeaders(res, audit), Decision: res.Decision, AuditStatus: audit,
	}
	if chat && up.Ms != nil {
		result.Upstream = &PlaygroundUpstream{Status: up.Status, Ms: *up.Ms}
	}
	return result, nil
}

// useRoute moves f to route, a route the caller named instead of the one the policy matched: the
// exchange's route and the trace's, and the evidence, read again with the route's settings.
func (h *Handler) useRoute(f *flow, route *policy.Route) {
	f.route, f.x.Route, f.x.Trace.Route = route, route, route.Name
	f.x.Evidence, f.x.Trace.UserRequest = nil, ""
	settings := route.Evidence.Extraction()
	ev, err := evidence.Extract(f.req, &settings)
	if err != nil {
		h.log.Error("evidence extraction failed", "trace_id", f.x.Trace.TraceID, "route", route.Name, "err", err)
		return
	}
	f.x.Evidence, f.x.Trace.UserRequest = ev, ev.UserRequest
}

// runContext is the gin context a run's flow is built on: c's values and writer, with a request
// of its own that stands for the admin's POST /v1/chat/completions (the same context, the /v1
// path without a query, no body left to read, and only the JSON content headers). The flow's
// helpers match the route on the request's headers and forward them on the upstream calls they
// make (the route's fallback call sends the client's headers), so none of the dashboard's reach
// them.
func runContext(c *gin.Context) *gin.Context {
	r := c.Request.Clone(c.Request.Context())
	r.URL.Path, r.URL.RawPath, r.URL.RawQuery, r.RequestURI = playgroundPath, "", "", playgroundPath
	r.Header = http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}
	r.Body, r.GetBody, r.ContentLength = http.NoBody, nil, 0
	rc := c.Copy()
	rc.Request, rc.Writer = r, c.Writer
	return rc
}

// playgroundUpstreamFailed records a chat-mode run whose upstream call failed, as upstreamFailed
// does on /v1 (skipped:upstream_error; undecodable for a 2xx whose body cannot be decoded, D24;
// client_disconnected when the caller went away; never enforced), and returns the 502, or 504 on
// a timeout, that names the trace. status is the status the upstream answered with before its
// body failed to read; 0 when the call itself failed.
func (h *Handler) playgroundUpstreamFailed(ctx context.Context, f *flow, status int, err error) *PlaygroundError {
	x := f.x
	ms := h.now().Sub(f.upStart).Milliseconds()
	x.SkipReason, x.Unenforced = gate.SkipUpstreamError, true
	pe := &PlaygroundError{Status: http.StatusBadGateway, Code: CodeUpstreamError, TraceID: x.Trace.TraceID,
		Message: "The upstream LLM API could not be reached."}
	answered := status >= 200 && status <= 299
	switch {
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		x.SkipReason = gate.SkipClientDisconnected
	case upstream.IsTimeout(err):
		pe.Status, pe.Code, pe.Message = http.StatusGatewayTimeout, CodeUpstreamTimeout, "The upstream LLM API did not answer in time."
	case status != 0 && !answered:
		// An error status whose body could not be read: reported as the status it is.
		pe.UpstreamStatus = status
		pe.Message = fmt.Sprintf("The upstream LLM API answered HTTP %d%s", status, upstreamHint(status))
	case answered && errors.Is(err, upstream.ErrUndecodable):
		x.SkipReason = gate.SkipUndecodable
		pe.Message = "The upstream LLM API answered with a body the gateway cannot decode (its Content-Encoding)."
	case answered:
		pe.Message = "The upstream LLM API's answer could not be read."
	}
	if x.SkipReason != gate.SkipClientDisconnected {
		h.log.Warn("playground: upstream call failed", "trace_id", x.Trace.TraceID, "status", status, "skip", x.SkipReason, "err", err)
	}
	res := h.gate.Evaluate(f.record, x)
	fin := &gate.Final{Action: unenforcedAction(res.Decision.Action), Delivered: gate.DeliveredOriginal}
	h.decided(f, res, gate.Upstream{Status: status, Ms: &ms}, h.now(), fin)
	return pe
}

// upstreamHint says what an upstream's error status usually means, without repeating its body
// (which can quote a credential).
func upstreamHint(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ": check the gateway's upstream key (TRUTHTABLE_UPSTREAM_API_KEY)."
	case status == http.StatusNotFound:
		return ": check the model id."
	case status == http.StatusTooManyRequests:
		return ": the upstream is rate limiting this key."
	case status >= 400 && status < 500:
		return ": it refused the request (check the model and the messages)."
	}
	return "."
}

// playgroundHeaders are the response headers the run's answer would have carried on /v1, by
// their SPEC names: the trace and request ids, then the decision headers (setDecisionHeaders).
func playgroundHeaders(res *gate.Result, audit string) map[string]string {
	h := http.Header{}
	h.Set(HeaderTraceID, res.Trace.TraceID)
	h.Set(HeaderRequestID, res.Trace.TraceID)
	setDecisionHeaders(h, res, audit)
	out := make(map[string]string, len(h))
	for _, name := range ResponseHeaders {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}
	return out
}

// playgroundPieces splits an answer into the token events of a streamed run: each word with the
// white space after it, at most maxPieceRunes runes. Joined, the pieces are the answer.
func playgroundPieces(text string) []string {
	var out []string
	for text != "" {
		n := pieceEnd(text)
		out = append(out, text[:n])
		text = text[n:]
	}
	return out
}

// pieceEnd is the byte length of the first piece of s (s is not empty).
func pieceEnd(s string) int {
	runes, space := 0, false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			space = true
		} else if space {
			return i
		}
		i += size
		if runes++; runes >= maxPieceRunes {
			return i
		}
	}
	return len(s)
}
