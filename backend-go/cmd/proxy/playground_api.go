package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/proxy"
)

// playgroundLimit is the Playground's own per-caller limit, runs a minute (SPEC §2.4 rate limits).
const playgroundLimit = 10

// Playground request limits besides TRUTHTABLE_MAX_TEXT_CHARS: a model id as a direct audit's, and
// as many evidence documents as a deep audit reads (SPEC §2.2).
const (
	maxPlaygroundModelRunes  = 200
	maxPlaygroundSourceRunes = 64
	maxPlaygroundDocs        = maxAuditDocs
)

var (
	playgroundRoles = []string{"system", "developer", "user", "assistant", "tool"}
	playgroundModes = []string{decision.ModeObserve, decision.ModeFlag, decision.ModeBlock, decision.ModeFallback}
	// messageNamePattern is the name an OpenAI chat message may carry.
	messageNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// playgroundAPI serves POST /api/playground/run (SPEC §2.4 row 61, §3.3; D12): an admin runs a
// conversation through the gateway's check without an app. The run itself is
// proxy.Handler.Playground; this layer reads and checks the PlaygroundRequest and answers with a
// PlaygroundResult, or, with stream:true, an event stream: token events (the checked answer in
// pieces), then decision (the attempt-0 decision) and done (the PlaygroundResult), or error (an
// ApiErrorBody) when the run fails once the stream has started.
type playgroundAPI struct {
	h *proxy.Handler
	// maxTextChars caps each message, document and answer (TRUTHTABLE_MAX_TEXT_CHARS).
	maxTextChars int
	// deadline is the write deadline of a run (0: none), set when the run starts.
	deadline time.Duration
	log      *slog.Logger
}

// playgroundHandler builds the Playground API over the gateway's handler (p3_api.go mounts it).
func (s *server) playgroundHandler() *playgroundAPI {
	return &playgroundAPI{
		h: s.gateway.handler, maxTextChars: s.cfg.MaxTextChars, deadline: playgroundDeadline(s.cfg), log: s.log,
	}
}

// playgroundDeadline is the write deadline of a Playground run: an upstream call and a fallback
// call, each bounded by TRUTHTABLE_UPSTREAM_TIMEOUT, plus 30 s for the gate and the records, as on
// /v1; none when the upstream timeout is off.
func playgroundDeadline(cfg *config.Config) time.Duration {
	if cfg.UpstreamTimeout <= 0 {
		return 0
	}
	return 2*cfg.UpstreamTimeout + 30*time.Second
}

// playgroundRequest is api.ts PlaygroundRequest.
type playgroundRequest struct {
	Mode         string                     `json:"mode"`
	Route        string                     `json:"route"`
	Model        string                     `json:"model"`
	Messages     []proxy.PlaygroundMessage  `json:"messages"`
	Evidence     []proxy.PlaygroundDocument `json:"evidence"`
	Response     *string                    `json:"response"`
	Stream       bool                       `json:"stream"`
	ModeOverride string                     `json:"mode_override"`
	Scenario     string                     `json:"scenario"`
}

// playgroundToken is the data of a token event.
type playgroundToken struct {
	Text string `json:"text"`
}

// playgroundErrorBody is api.ts ApiErrorBody with the members a run's failure fills: details
// (the upstream's status) and trace_id (the trace the run recorded before it failed).
type playgroundErrorBody struct {
	Error   playgroundErrorDetail `json:"error"`
	TraceID *string               `json:"trace_id,omitempty"`
}

type playgroundErrorDetail struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	FieldErrors []fieldError   `json:"field_errors,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// run serves POST /api/playground/run.
func (p *playgroundAPI) run(c *gin.Context) {
	var req playgroundRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			abortAPI(c, http.StatusRequestEntityTooLarge, codeTooLarge, "The request body is too large.")
			return
		}
		abortAPI(c, http.StatusBadRequest, codeInvalidJSON, "The body is not a PlaygroundRequest JSON object.")
		return
	}
	if fields := p.validate(&req); len(fields) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The Playground request is not valid.", fields...)
		return
	}
	in := proxy.PlaygroundInput{
		Mode: req.Mode, Route: req.Route, Model: req.Model, Messages: req.Messages, Evidence: req.Evidence,
		ModeOverride: req.ModeOverride, Scenario: req.Scenario,
	}
	if req.Response != nil {
		in.Response = *req.Response
	}
	if id := callerIdentity(c); id != nil {
		in.KeyLabel = id.Label
	}
	// A run can outlast the /api write deadline: it may wait for the upstream and a fallback call.
	var until time.Time
	if p.deadline > 0 {
		until = time.Now().Add(p.deadline)
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(until)

	if req.Stream {
		p.stream(c, in)
		return
	}
	res, err := p.h.Playground(c.Request.Context(), c, in, nil)
	if err != nil {
		p.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// stream serves a run with stream:true. The event stream starts once the run is accepted; a run
// refused before that answers with its status like a JSON run.
func (p *playgroundAPI) stream(c *gin.Context, in proxy.PlaygroundInput) {
	sw := llmapi.NewStreamWriter(c.Writer)
	if p.deadline > 0 {
		sw.KeepWriteDeadline(p.deadline)
	}
	in.Accepted = func() { _ = sw.Commit() }
	send := func(event string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			p.log.Error("api: a playground event does not marshal", "event", event, "err", err)
			return
		}
		// A caller that went away makes every write fail; the run is recorded all the same.
		_ = sw.WriteEvent(llmapi.DataEvent(event, data))
	}
	res, err := p.h.Playground(c.Request.Context(), c, in, func(text string) { send("token", playgroundToken{Text: text}) })
	switch {
	case err != nil && !sw.Committed():
		p.fail(c, err)
		return
	case err != nil:
		_, body := p.errorBody(err)
		send("error", body)
	default:
		send("decision", res.Decision)
		send("done", res)
	}
	_ = sw.Finish(nil, nil)
}

// fail answers a run that failed before its response started.
func (p *playgroundAPI) fail(c *gin.Context, err error) {
	status, body := p.errorBody(err)
	c.AbortWithStatusJSON(status, body)
}

// errorBody is the status and ApiErrorBody of a failed run.
func (p *playgroundAPI) errorBody(err error) (int, playgroundErrorBody) {
	var pe *proxy.PlaygroundError
	if !errors.As(err, &pe) {
		p.log.Error("api: a playground run failed", "err", err)
		return http.StatusInternalServerError, playgroundErrorBody{Error: playgroundErrorDetail{
			Code: codeInternal, Message: "The run could not be completed; see the proxy log."}}
	}
	body := playgroundErrorBody{Error: playgroundErrorDetail{Code: pe.Code, Message: pe.Message}}
	if pe.Field != "" {
		body.Error.Message = "The Playground request is not valid."
		body.Error.FieldErrors = []fieldError{{Path: pe.Field, Message: pe.Message}}
	}
	if pe.UpstreamStatus != 0 {
		body.Error.Details = map[string]any{"upstream_status": pe.UpstreamStatus}
	}
	if pe.TraceID != "" {
		id := pe.TraceID
		body.TraceID = &id
	}
	return pe.Status, body
}

// validate checks a PlaygroundRequest's members (the route's existence and the mode override's
// enforceability are checked by the run, against the policy it runs on).
func (p *playgroundAPI) validate(req *playgroundRequest) []fieldError {
	var out []fieldError
	bad := func(path, format string, args ...any) {
		out = append(out, fieldError{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	text := func(path, v string, required bool) {
		switch n := utf8.RuneCountInString(v); {
		case required && strings.TrimSpace(v) == "":
			bad(path, "is required")
		case n > p.maxTextChars:
			bad(path, "must be at most %d characters", p.maxTextChars)
		}
	}
	chat := req.Mode == proxy.PlaygroundChat
	if !chat && req.Mode != proxy.PlaygroundAudit {
		bad("mode", "must be chat or audit")
	}

	user := false
	for i, m := range req.Messages {
		path := fmt.Sprintf("messages[%d]", i)
		if !slices.Contains(playgroundRoles, m.Role) {
			bad(path+".role", "must be one of %s", strings.Join(playgroundRoles, ", "))
		}
		text(path+".content", m.Content, false)
		if m.Name != "" && !messageNamePattern.MatchString(m.Name) {
			bad(path+".name", "must match ^[A-Za-z0-9_-]{1,64}$")
		}
		user = user || (m.Role == "user" && strings.TrimSpace(m.Content) != "")
	}
	if !user {
		bad("messages", "needs a user message with text")
	}

	switch n := utf8.RuneCountInString(req.Model); {
	case n > maxPlaygroundModelRunes:
		bad("model", "must be at most %d characters", maxPlaygroundModelRunes)
	case chat && strings.TrimSpace(req.Model) == "":
		bad("model", "is required in chat mode: the upstream answers with this model")
	}
	if req.Route != "" && !routeNamePattern.MatchString(req.Route) {
		bad("route", "must be a route name (lowercase letters, digits, - and _)")
	}

	if len(req.Evidence) > maxPlaygroundDocs {
		bad("evidence", "must hold at most %d documents", maxPlaygroundDocs)
	}
	for i, doc := range req.Evidence {
		path := fmt.Sprintf("evidence[%d]", i)
		text(path+".text", doc.Text, true)
		if doc.ID != "" && !docIDPattern.MatchString(doc.ID) {
			bad(path+".id", "must match ^[A-Za-z0-9._:-]{1,64}$")
		}
		if utf8.RuneCountInString(doc.Source) > maxPlaygroundSourceRunes {
			bad(path+".source", "must be at most %d characters", maxPlaygroundSourceRunes)
		}
	}

	switch {
	case chat && req.Response != nil:
		bad("response", "is read only in audit mode: in chat mode the upstream answers")
	case !chat && req.Mode == proxy.PlaygroundAudit:
		response := ""
		if req.Response != nil {
			response = *req.Response
		}
		text("response", response, true)
	}
	if req.ModeOverride != "" && !slices.Contains(playgroundModes, req.ModeOverride) {
		bad("mode_override", "must be one of %s", strings.Join(playgroundModes, ", "))
	}
	if req.Scenario != "" && !scenarioPattern.MatchString(req.Scenario) {
		bad("scenario", "must be a scenario id (^[a-z0-9][a-z0-9-]{1,63}$)")
	}
	return out
}
