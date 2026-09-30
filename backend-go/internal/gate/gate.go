// Package gate is the gateway's decision pipeline for one exchange (SPEC §3.1 internal/gate):
// detectors, evidence, compile, the decision-model call, the calibration lookup, decide(), and the
// deep-audit and body retention plans. The proxy handler owns the HTTP side (formats, streams,
// block and fallback bodies) and hands each exchange to Evaluate, the fallback re-gate included
// (Exchange.Attempt 1); a Sink persists and broadcasts the lifecycle and submits audits.
//
// Retries, hedging, spend and rate limits run inside the decision-model client (internal/jev); a
// refusal before any vendor request becomes the gate's skip reason. Each evaluator's calibration
// is looked up for the snapshot that answered (Config.Calibrations). Knowledge retrieval on the
// hot path is P4.
package gate

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strconv"
	"time"

	"github.com/truthtable/backend-go/internal/calibration"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
)

// Skip reasons (SkipReason) the gate and the handler use.
const (
	SkipUnparseableRequest = "unparseable_request"
	SkipUpstreamError      = "upstream_error"
	SkipNoText             = "no_text"
	SkipSampledOut         = decision.SkipSampledOut
	SkipOverload           = "overload"
	SkipDecisionModelOff   = "decision_model_off"
	SkipClientDisconnected = "client_disconnected"
	SkipMultiChoice        = "multi_choice"
	SkipUndecodable        = "undecodable"
	SkipUnsupportedAPI     = "unsupported_api"
)

// Config configures a Gate.
type Config struct {
	Jev        jev.Client
	Evaluators *evaluators.Registry
	// Budget holds the env token budget (TRUTHTABLE_JEV_TOKEN_BUDGET, _CHARS_PER_TOKEN,
	// _MAX_QUESTIONS_PER_CALL); each route supplies max_response_tokens and evidence.max_tokens.
	Budget decision.Budget
	// AttemptTimeout bounds one decision-model call (TRUTHTABLE_JEV_ATTEMPT_TIMEOUT).
	AttemptTimeout time.Duration
	// AsyncDeadline bounds an observe-mode gate (TRUTHTABLE_JEV_ASYNC_DEADLINE).
	AsyncDeadline time.Duration
	// UniformAuditsPerDay caps the uniform deep-audit sample (TRUTHTABLE_UNIFORM_AUDITS_PER_DAY).
	UniformAuditsPerDay int
	// Rand returns a uniform number in [0, 1); nil uses math/rand/v2.
	Rand func() float64
	// Now returns the current time; nil uses time.Now.
	Now    func() time.Time
	Logger *slog.Logger
	// Calibrations looks up each evaluator's calibration for the snapshot that answered (decide()'s
	// calibration input); nil means none, so every evaluator is uncalibrated.
	Calibrations policy.CalibrationLookup
	// OnLate receives a decision recomputed after a late decision-model answer (SPEC §2.9 late
	// decisions): the same attempt with late=true and enforced=false, never audited. It runs on a
	// goroutine of its own, possibly before the sink recorded the original result
	// (Result.LateExpected). nil only logs late answers.
	OnLate func(context.Context, *Result)
}

// Gate evaluates exchanges. It is safe for concurrent use.
type Gate struct {
	cfg     Config
	uniform *dailyCounter
	log     *slog.Logger
}

// New builds a Gate.
func New(cfg Config) (*Gate, error) {
	if cfg.Jev == nil || cfg.Evaluators == nil {
		return nil, errors.New("gate: a decision-model client and an evaluator registry are required")
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = 3 * time.Second
	}
	if cfg.AsyncDeadline <= 0 {
		cfg.AsyncDeadline = 10 * time.Second
	}
	if cfg.Budget.CPT == 0 {
		cfg.Budget = decision.DefaultBudget()
	}
	return &Gate{cfg: cfg, uniform: &dailyCounter{limit: cfg.UniformAuditsPerDay}, log: cfg.Logger}, nil
}

// AsyncDeadline is the deadline of an observe-mode gate.
func (g *Gate) AsyncDeadline() time.Duration { return g.cfg.AsyncDeadline }

// Backend is the decision-model backend the gate calls.
func (g *Gate) Backend() jev.Info { return g.cfg.Jev.Info() }

// Exchange is one gated request and its upstream answer.
type Exchange struct {
	// Trace is the trace as the sink recorded it; Trace.Mode is the configured mode.
	Trace  *TraceStart
	Policy *policy.Policy
	Route  *policy.Route
	// Request and Evidence are nil when the request did not parse.
	Request  *llmapi.ChatRequest
	Evidence *evidence.Result
	// Response is the gated text (choice 0).
	Response string
	// SkipReason is set by the handler when the exchange cannot be gated (unparseable_request,
	// upstream_error, no_text, client_disconnected, ...) or the async gate queue is full (overload).
	SkipReason string
	// Stream is how the answer reached the client: none (a JSON body), passthrough or buffer
	// (decision meta.stream); "" means none.
	Stream string
	// Attempt is 0 for the original answer and 1 for the fallback re-gate (Decision.Attempt).
	Attempt int
	// Unenforced records the decision with enforced=false: the answer was delivered regardless of
	// it (an upstream error or a tool-call-only answer passed through, a client that went away
	// after the finish reason).
	Unenforced bool
	// NoAudit plans no deep audit (the fallback re-gate: the audit stays on the original answer);
	// the result's AuditPlan is then empty.
	NoAudit bool
	// AuditSelection, when set, always selects a deep audit of an answer with text, with this
	// selection (playground for a Playground run), in the high lane with inclusion probability 1,
	// whatever the route's deep_audit settings. It takes no uniform draw. NoAudit wins over it.
	AuditSelection string
	// KnowledgeOnly marks evidence that came only from knowledge retrieval (decision
	// meta.knowledge_only): a critical grounding level is capped to warn.
	KnowledgeOnly bool
}

// hasText reports whether there is an answer to check against a parsed request.
func (x *Exchange) hasText() bool {
	switch x.SkipReason {
	case SkipUnparseableRequest, SkipUpstreamError, SkipNoText, SkipClientDisconnected:
		return false
	}
	return x.Evidence != nil && x.Response != ""
}

// streamOf is the exchange's decision meta.stream: none unless the handler set one.
func streamOf(x *Exchange) string {
	if x.Stream == "" {
		return StreamNone
	}
	return x.Stream
}

// Evaluate gates one exchange. It never fails: problems become a skipped, error or timeout status.
// ctx bounds the decision-model call together with the route deadline (sync) or the async
// deadline (observe).
func (g *Gate) Evaluate(ctx context.Context, x *Exchange) *Result {
	start := g.cfg.Now()
	route, mode := x.Route, x.Trace.Mode
	res := &Result{
		Trace:          x.Trace,
		RouteLabel:     x.Policy.Label(route),
		ResponseText:   x.Response,
		Headers:        route.Headers,
		Flags:          slices.Clone(x.Trace.Flags),
		CheckedAgainst: "none",
	}
	if x.Request != nil {
		res.Messages = Messages(x.Request.Messages)
	}
	var reqRules map[string]string
	if x.Request != nil {
		reqRules = x.Request.Extension.Rules
	}
	res.Rules = g.resolveRules(route, x.Policy.Overrides.ClientRules, reqRules)
	if x.Evidence != nil {
		res.Evidence = slices.Clone(x.Evidence.Items)
		res.UserRequest = x.Evidence.UserRequest
		res.SystemInstructions = x.Evidence.SystemInstructions
	}
	hasText := x.hasText()

	// Pre-gate uniform draw: an unbiased sample of all gated traces, capped per day. An exchange
	// that plans no audit (the fallback re-gate) or whose audit is chosen already (a Playground
	// run) takes no draw.
	uniform := !x.NoAudit && x.AuditSelection == "" && route.DeepAudit.Enabled && hasText &&
		route.DeepAudit.UniformSampleRate > 0 && g.cfg.Rand() < route.DeepAudit.UniformSampleRate && g.uniform.take(start)

	// Observe routes decide by sample_rate whether the decision model is asked at all. A run
	// whose audit is chosen already (a Playground run, D27) is always checked: it takes no draw.
	skip := x.SkipReason
	if skip == "" && x.AuditSelection == "" && mode == decision.ModeObserve && route.SampleRate < 1 &&
		g.cfg.Rand() >= route.SampleRate {
		skip = SkipSampledOut
	}

	// Tier 0: detectors run whenever there is an answer, sampled or not.
	detectors := notRun()
	if hasText {
		var err error
		if detectors, err = decision.Detect(&decision.DetectorInput{
			Response: x.Response, Evidence: x.Evidence.DecisionItems(),
			UserRequest: x.Evidence.UserRequest, SystemInstructions: x.Evidence.SystemInstructions, Rules: res.Rules,
			PreDedupItems: x.Evidence.PreDedupItems, MaxMarker: x.Evidence.MaxMarker, MinInteger: route.Detectors.MinInteger,
		}); err != nil {
			g.log.Error("gate: detectors failed", "trace_id", x.Trace.TraceID, "err", err)
			detectors = notRun()
		}
	}

	model := route.Decision.Model
	if model == "" {
		model = g.cfg.Jev.Info().Model
	}
	if model == "" {
		model = "unset"
	}

	in := &decision.Input{
		Policy: decision.Policy{
			ConfiguredMode: mode, Fail: route.Fail, RequireCalibration: route.RequireCalibration, OnNoEvidence: route.OnNoEvidence,
			Detectors: decision.DetectorPolicy{FlagOn: route.Detectors.FlagOn, BlockOn: route.Detectors.BlockOn},
		},
		QuestionMap: map[string]string{},
		Excluded:    []decision.Exclusion{},
		Calibration: map[string]decision.CalibrationEntry{},
		Meta: decision.Meta{
			Backend: g.cfg.Jev.Info().Backend, Stream: streamOf(x), KnowledgeOnly: x.KnowledgeOnly,
			EvidencePresent: x.Evidence != nil && x.Evidence.Trusted(),
			RequestStatus:   map[string]string{}, QIDRequest: map[string]int{},
		},
		Detectors: decision.DetectorStates(detectors),
	}

	var compiled *decision.Compiled
	var usage *DecisionModelUsage
	var round *callRound
	if hasText {
		var err error
		compiled, err = decision.Compile(g.compileRoute(route, x.Policy, model), g.cfg.Evaluators.Specs(), g.compileInputs(x, reqRules), g.budget(route))
		if err != nil {
			g.log.Warn("gate: compile failed", "trace_id", x.Trace.TraceID, "route", route.Name, "err", err)
			kind := "compile"
			if errors.Is(err, decision.ErrRequestTooLarge) {
				kind = jev.KindTooLarge
			}
			g.failAll(in, route, "error:"+kind)
			compiled = nil
		}
	}

	if compiled != nil {
		if g.adopt(in, route, compiled, x.Trace.TraceID) && skip == "" && len(compiled.Requests) > 0 {
			if g.cfg.Jev.Info().Backend == jev.BackendOff {
				skip = SkipDecisionModelOff
			} else {
				round = g.consult(ctx, x, model, reqRules, compiled)
				if round.compiled != compiled {
					// A too_large resend: the answers are about the smaller compile.
					compiled = round.compiled
					g.adopt(in, route, compiled, x.Trace.TraceID)
				}
				var answers map[string]*decision.RawAnswer
				usage, answers, in.Meta.RequestStatus, skip = g.collect(round, model)
				in.Answers = answers
			}
		}
	} else if len(in.Evaluators) == 0 {
		in.Evaluators = g.routeEvaluators(route)
	}
	if usage != nil && usage.Snapshot != nil {
		// Calibrations are per snapshot: the one that answered, not the one the status last saw.
		in.Calibration = calibration.Entries(g.cfg.Calibrations, in.Evaluators, routeParams(route), *usage.Snapshot)
	}
	if skip != "" {
		in.Meta.SkipReason = &skip
	}

	core, err := decision.Decide(in)
	if err != nil {
		g.log.Error("gate: decide failed", "trace_id", x.Trace.TraceID, "err", err)
		core = internalError(mode)
		in.Meta.RequestStatus = map[string]string{"0": "error:internal"}
	}
	if x.Unenforced {
		core.Enforced = false
	}

	gateMs := g.cfg.Now().Sub(start).Milliseconds()
	version := x.Policy.Version
	d := &Decision{
		Spec: DecisionSpec, TraceID: x.Trace.TraceID, Attempt: x.Attempt, CreatedAt: start.UTC(), Route: route.Name,
		PolicyVersion: &version, Mode: mode, Fail: route.Fail, Core: *core,
		Detectors: detectors, Excluded: in.Excluded, DecisionModel: usage, GateMs: &gateMs,
	}
	if compiled != nil {
		d.Compile = &CompileInfo{
			CPT: compiled.CPT, Requests: len(compiled.Requests), QuestionSetSHA256: compiled.QuestionSetSHA256,
			RequestSHA256: compiled.RequestSHA256, PackedEvidenceIDs: compiled.PackedIDs,
			TruncatedIDs: compiled.TruncatedIDs, EstTokens: compiled.EstTokens,
		}
		for i := range res.Evidence {
			res.Evidence[i].Truncated = slices.Contains(compiled.TruncatedIDs, res.Evidence[i].ID)
		}
	}
	if x.Evidence != nil {
		sum := evidence.Summarize(x.Evidence, route.Evidence.Sources, compiled != nil && compiled.Truncated, g.cfg.Budget.CPT)
		d.EvidenceSummary = &sum
		res.CheckedAgainst = sum.CheckedAgainst()
	}
	res.Decision = d
	res.Status = statusValue(core, in.Meta.RequestStatus)
	if usage != nil && usage.Backend == jev.BackendMock {
		res.Flags = appendFlag(res.Flags, FlagMockDecision)
	}
	if uniform {
		res.Flags = appendFlag(res.Flags, FlagUniformSample)
	}
	if !x.NoAudit {
		res.Audit = g.planAudit(route, core, auditDraw{uniform: uniform, hasText: hasText, forced: x.AuditSelection, scenario: x.Trace.Scenario})
	}
	res.Body = g.planBody(route, core)
	if round != nil {
		// Requests still running at the deadline may answer late: the late decision is made then
		// from what this evaluation had (SPEC §2.9 late decisions).
		ev := &lateEval{round: round, in: *in, model: model, route: route, start: start, decision: *d, result: lateResult(res)}
		ev.decision.Core = decision.Core{}
		res.LateExpected = round.late.arm(ev)
	}
	return res
}

// compileInputs are the compile() inputs of an exchange with an answer.
func (g *Gate) compileInputs(x *Exchange, reqRules map[string]string) *decision.CompileInputs {
	return &decision.CompileInputs{
		UserRequest: x.Evidence.UserRequest, AssistantResponse: x.Response, SystemInstructions: x.Evidence.SystemInstructions,
		Evidence: x.Evidence.DecisionItems(), Rules: reqRules,
	}
}

// adopt makes a compile the decide() input's evaluators, question map, exclusions and request
// map. When the compiled evaluators cannot be decided (a bug), every route evaluator fails with
// error:compile and adopt reports false.
func (g *Gate) adopt(in *decision.Input, route *policy.Route, c *decision.Compiled, traceID string) bool {
	evs, err := c.DecideEvaluators(g.cfg.Evaluators.Specs(), thresholds(route))
	if err != nil {
		g.log.Error("gate: decide evaluators", "trace_id", traceID, "err", err)
		g.failAll(in, route, "error:compile")
		return false
	}
	in.Evaluators, in.QuestionMap, in.Excluded = evs, c.QuestionMap, c.Excluded
	in.Meta.EvidenceTruncated, in.Meta.QIDRequest = c.Truncated, c.QIDRequest
	return true
}

// compileRoute is the part of a route compile() reads.
func (g *Gate) compileRoute(r *policy.Route, p *policy.Policy, model string) *decision.CompileRoute {
	cr := &decision.CompileRoute{
		OnNoEvidence: r.OnNoEvidence, EvidenceSources: r.Evidence.Sources,
		ClientRules: p.Overrides.ClientRules, Model: model,
	}
	for _, e := range r.Evaluators {
		cr.Evaluators = append(cr.Evaluators, decision.RouteEvaluator{Ref: e.Ref, Stage: e.StageOrDefault(), Enabled: e.Enabled, Params: e.Params})
	}
	return cr
}

func (g *Gate) budget(r *policy.Route) decision.Budget {
	b := g.cfg.Budget
	// HTTP backends learn chars-per-token from usage.input_tokens (SPEC §2.9 cpt); compile packs
	// evidence with the current estimate.
	if src, ok := g.cfg.Jev.(jev.CPTSource); ok {
		if cpt := src.CPT(); cpt > 0 {
			b.CPT = cpt
		}
	}
	b.MaxResponseTokens = r.Decision.MaxResponseTokens
	b.EvidenceMaxTokens = r.Evidence.MaxTokens
	return b
}

// routeEvaluators are the decide() evaluators of a route that was not compiled (no answer text).
func (g *Gate) routeEvaluators(r *policy.Route) []decision.EvaluatorInput {
	var out []decision.EvaluatorInput
	th := thresholds(r)
	for _, e := range r.Evaluators {
		spec, ok := g.cfg.Evaluators.Get(e.Ref)
		if !ok || !e.IsEnabled() {
			continue
		}
		out = append(out, decision.EvaluatorInput{Ref: e.Ref, Spec: spec, Stage: e.StageOrDefault(), Thresholds: th[e.Ref]})
	}
	return out
}

// failAll makes every route evaluator fail with status, as if its request had failed.
func (g *Gate) failAll(in *decision.Input, r *policy.Route, status string) {
	in.Evaluators = g.routeEvaluators(r)
	in.QuestionMap, in.Excluded = map[string]string{}, []decision.Exclusion{}
	in.Meta.QIDRequest = map[string]int{}
	for _, e := range in.Evaluators {
		qid := e.Spec.QuestionID()
		in.QuestionMap[qid] = e.Ref
		in.Meta.QIDRequest[qid] = 0
	}
	in.Meta.RequestStatus = map[string]string{"0": status}
}

// resolveRules resolves the rules params of the route's enabled evaluators: the request value when
// overrides.client_rules allows it, else the route value, else the spec default.
func (g *Gate) resolveRules(r *policy.Route, clientRules bool, reqRules map[string]string) map[string]string {
	out := map[string]string{}
	for _, e := range r.Evaluators {
		spec, ok := g.cfg.Evaluators.Get(e.Ref)
		if !ok || !e.IsEnabled() {
			continue
		}
		for name, p := range spec.Params {
			v := p.Default
			if rv, ok := e.Params[name]; ok {
				v = rv
			}
			if cv, ok := reqRules[name]; ok && clientRules {
				v = cv
			}
			out[name] = v
		}
	}
	return out
}

// routeParams are the route params of each enabled evaluator, by ref (the calibration target's
// params_sha256).
func routeParams(r *policy.Route) map[string]map[string]string {
	out := make(map[string]map[string]string, len(r.Evaluators))
	for _, e := range r.Evaluators {
		if e.IsEnabled() && len(e.Params) > 0 {
			out[e.Ref] = e.Params
		}
	}
	return out
}

func thresholds(r *policy.Route) map[string]decision.Thresholds {
	out := make(map[string]decision.Thresholds, len(r.Evaluators))
	for _, e := range r.Evaluators {
		out[e.Ref] = e.Thresholds.Decision()
	}
	return out
}

// notRun is the detector record of an exchange without an answer to check.
func notRun() []decision.DetectorResult {
	out := make([]decision.DetectorResult, len(decision.DetectorIDs))
	for i, id := range decision.DetectorIDs {
		out[i] = decision.DetectorResult{ID: id, Findings: []decision.DetectorFinding{}}
	}
	return out
}

// internalError is the decision recorded when decide() rejects its input (a bug): nothing is
// enforced and the trace is sampled like any gate error.
func internalError(mode string) *decision.Core {
	action := decision.ActionPass
	if mode == decision.ModeObserve {
		action = decision.ActionObserve
	}
	degraded := decision.DegradedError
	return &decision.Core{
		Status: decision.StatusError, EffectiveMode: mode, Action: action, WouldAction: decision.ActionPass,
		Enforced: mode != decision.ModeObserve, Calibration: decision.CalibrationNone, Degraded: &degraded,
		DeepAudit: decision.DeepAuditSampleError, Reasons: []string{}, Evaluators: decision.EvaluatorResults{},
	}
}

func appendFlag(flags []string, f string) []string {
	if slices.Contains(flags, f) {
		return flags
	}
	return append(flags, f)
}

func addInt(p *int, v int) *int {
	if p != nil {
		v += *p
	}
	return &v
}

func addFloat(p *float64, v float64) *float64 {
	if p != nil {
		v += *p
	}
	return &v
}

func itoa(k int) string { return strconv.Itoa(k) }
