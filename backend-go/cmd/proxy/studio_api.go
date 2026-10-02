package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/studio"
)

// The Evaluator Studio endpoints (SPEC §2.4 rows 34-38, §7.3; P4-PLAN §7), the decision request
// (row 12, decision_request_api.go) and the evidence suggestions (row 51, evidence_api.go):
//
//   - POST /api/studio/draft drafts an evaluator from a description through the engine's judge;
//   - POST /api/studio/test runs a spec (saved or not) on up to 50 samples;
//   - POST /api/studio/samples picks test-bench samples from recent traffic;
//   - POST /api/studio/calibrate is the quick calibration (pure; mirrored in TypeScript);
//   - POST /api/studio/dry-run runs a spec on up to TRUTHTABLE_DRYRUN_MAX_SAMPLES past answers of
//     a route and streams the progress (text/event-stream).
//
// Every decision-model call they make is non-enforcing (D82): it can never spend the enforcing
// reserve of the daily decision cap, and a test whose estimate exceeds what is left before the
// observe line is refused with 503 budget_exhausted. Studio never saves or attaches an evaluator.

// Per-caller limits (SPEC §2.4 rate limits).
const (
	studioDraftLimit  = 10
	studioTestLimit   = 20
	studioDryRunLimit = 2
)

// Studio bounds.
const (
	// maxStudioSamples caps a test bench (SPEC §2.4 row 35) and a samples request (row 36).
	maxStudioSamples = 50
	// defaultStudioSamples is a samples request's limit when it gives none.
	defaultStudioSamples = 20
	// maxQuickLabels caps a quick calibration: its leave-one-out range is quadratic in the labels.
	maxQuickLabels = 5000
	// studioCallTimeout bounds the decision-model calls of one sample.
	studioCallTimeout = 30 * time.Second
	// studioConcurrency is how many samples run at once.
	studioConcurrency = 4
	// dryRunBatch is how many samples a dry-run evaluates between two progress events.
	dryRunBatch = 10
	// defaultDryRunMaxSamples applies when TRUTHTABLE_DRYRUN_MAX_SAMPLES is unset.
	defaultDryRunMaxSamples = 200
	// sseWriteWindow is the write deadline of each server-sent event (the /api deadline is 60 s,
	// and a dry-run streams longer).
	sseWriteWindow = 60 * time.Second
	// studioPersistTimeout bounds the writes made after a run (results, the audit row), which
	// outlive a cancelled request.
	studioPersistTimeout = 10 * time.Second
)

// componentStudioDraft is the cost component of a draft's LLM call (CostRecord.component).
const componentStudioDraft = "studio_draft"

// Run id prefixes of studio_results rows.
const (
	testRunPrefix   = "st_"
	dryRunRunPrefix = "dr_"
)

// studioStore is the storage the Studio endpoints use; *store.PostgresStore implements it.
type studioStore interface {
	InsertStudioResults(ctx context.Context, rows []store.StudioResult) error
	StudioSampleTraces(ctx context.Context, f store.StudioSampleFilter) ([]string, error)
	GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error)
	DryRunPopulation(ctx context.Context, f store.DryRunFilter) (population, withBody int64, err error)
	DryRunSample(ctx context.Context, f store.DryRunFilter, limit int) ([]store.DryRunCandidate, error)
	GetRecordedCompile(ctx context.Context, traceID string, attempt int) (*store.RecordedCompile, error)
	NoEvidenceBodies(ctx context.Context, f store.ScanFilter) ([]store.ScanBody, error)
	AppendAdminAudit(ctx context.Context, e store.AdminAuditEntry) error
}

// studioDrafter is the engine's drafter; *grpc.AuditClient implements it.
type studioDrafter interface {
	DraftEvaluator(ctx context.Context, req *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error)
}

// studioAPI holds what the Studio endpoints read. Every function field is set by newStudioAPI;
// tests replace them.
type studioAPI struct {
	cfg *config.Config
	// client is the decision model in effect (hot-swapped by GCONN).
	client   func() jev.Client
	registry *evaluators.Registry
	// policy is the active policy; policyEnv and policyVersion load a recorded one (D63).
	policy        func() *policy.Policy
	policyEnv     func() policy.Env
	policyVersion func(ctx context.Context, id int64) (*policy.Version, error)
	// db is nil without a database.
	db studioStore
	// barrier flushes a trace's queued writes before it is read (D83).
	barrier func(ctx context.Context, traceID string) error
	costs   costHooks
	// decisionPrice is the input price in USD per million tokens of a decision model.
	decisionPrice func(backend, model string) float64
	// budget is the gate's base compile budget (the env token budget).
	budget decision.Budget
	// drafter is nil without an engine; judge reports the judge the engine uses and judgePrice
	// its price (ok false: unknown).
	drafter    studioDrafter
	judge      func() (provider, model string)
	judgePrice func(provider, model string) (decision.PriceEntry, bool)
	now        func() time.Time
	log        *slog.Logger
}

// newStudioAPI wires the Studio endpoints to the server.
func (s *server) newStudioAPI() *studioAPI {
	cfg := s.cfg
	a := &studioAPI{
		cfg:           cfg,
		client:        func() jev.Client { return s.gateway.client },
		registry:      s.policies.Registry,
		policy:        s.policies.Engine.Current,
		policyEnv:     s.policies.Engine.Env,
		policyVersion: s.policies.Engine.Version,
		barrier:       s.barrier,
		costs:         s.costs,
		budget: decision.Budget{
			Tokens: cfg.Jev.TokenBudget, CPT: cfg.Jev.CharsPerToken, MaxQuestions: cfg.Jev.MaxQuestionsPerCall,
			Overhead: decision.Overhead, MaxResponseTokens: cfg.Jev.MaxResponseTokens, EvidenceMaxTokens: 8000,
		},
		judge: func() (string, string) { return "", "" },
		now:   time.Now,
		log:   s.log,
	}
	if a.budget.CPT == 0 {
		a.budget = decision.DefaultBudget()
	}
	if s.pg != nil {
		a.db = s.pg
	}
	if s.engine != nil {
		a.drafter = s.engine
	}
	if j := s.auditJudge(); j != nil {
		a.judge = j
	}
	prices := s.costs.prices
	a.decisionPrice = func(backend, model string) float64 { return decisionPricePerMTok(prices, backend, model) }
	var book *costs.PriceBook
	if l := s.ledger(); l != nil {
		book = l.Prices()
	}
	a.judgePrice = func(provider, model string) (decision.PriceEntry, bool) { return judgePriceOf(book, provider, model) }
	return a
}

// decisionPricePerMTok is the input price (USD per million tokens) the Studio estimates a
// decision model's calls at: $0 for the mock and off backends, the cost ledger's price book when
// it knows the model, else the embedded table.
func decisionPricePerMTok(prices jev.PriceFunc, backend, model string) float64 {
	switch backend {
	case jev.BackendMock, jev.BackendOff, "":
		return 0
	}
	if prices != nil {
		if p, _, ok := prices(backend, model); ok {
			return p
		}
	}
	if usd, ok := jev.EstimateUSD(jev.Info{Backend: backend, Model: model}, 1_000_000); ok {
		return usd
	}
	return jev.DefaultPricePerMTok
}

// judgePriceOf is the price a draft reserves at (D48): $0 for a mock judge and for a local one
// the book does not price, the judge's own price when the book has one, else the reference
// judge's (Claude Haiku 4.5, assumptions.v1.json). Without a price book nothing is reserved.
func judgePriceOf(book *costs.PriceBook, provider, model string) (decision.PriceEntry, bool) {
	if book == nil || decision.IsSimulatedProvider(provider) {
		return decision.PriceEntry{}, true
	}
	if e, _, ok := book.Lookup(provider, model); ok {
		return e, true
	}
	if decision.IsLocalProvider(provider) {
		return decision.PriceEntry{}, true
	}
	if a, err := costs.ContractsAssumptions(); err == nil {
		if e, _, ok := book.Lookup(a.Judge.Provider, a.Judge.Model); ok {
			return e, true
		}
	}
	return decision.PriceEntry{}, false
}

// mountStudioAPI mounts the decision request, Studio and evidence-suggestion endpoints (SPEC §2.4
// rows 12, 34-38 and 51; P4, GSTUDIO).
func (s *server) mountStudioAPI(api *gin.RouterGroup, g apiGuards) {
	s.newStudioAPI().register(api, g)
}

// register mounts the endpoints with the guards.
func (a *studioAPI) register(api *gin.RouterGroup, g apiGuards) {
	api.GET("/traces/:id/decision-request", g.content, g.rateLimit, a.decisionRequest)
	api.POST("/studio/draft", g.writable, g.admin, g.limitBy(studioDraftLimit, time.Minute), g.bodyLimit, a.draft)
	api.POST("/studio/test", g.writable, g.admin, g.limitBy(studioTestLimit, time.Minute), g.bodyLimit, a.test)
	api.POST("/studio/samples", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.samples)
	api.POST("/studio/calibrate", g.writable, g.read, g.rateLimit, g.bodyLimit, a.calibrate)
	api.POST("/studio/dry-run", g.writable, g.admin, g.limitBy(studioDryRunLimit, time.Minute), g.bodyLimit, a.dryRun)
	api.GET("/evidence/suggestions", g.admin, g.rateLimit, a.suggestions)
}

// --------------------------------------------------------------------------- shared

// studioThresholds is the {warn_at?, critical_at?} override of a test or a dry-run (raw space).
type studioThresholds struct {
	WarnAt     *float64          `json:"warn_at"`
	CriticalAt decision.OptFloat `json:"critical_at"`
}

// check reports a threshold outside [0,1].
func (t *studioThresholds) check() []fieldError {
	if t == nil {
		return nil
	}
	var out []fieldError
	bad := func(v *float64) bool { return v != nil && (math.IsNaN(*v) || *v < 0 || *v > 1) }
	if bad(t.WarnAt) {
		out = append(out, fieldError{Path: "thresholds.warn_at", Message: "must be between 0 and 1"})
	}
	if bad(t.CriticalAt.Value) {
		out = append(out, fieldError{Path: "thresholds.critical_at", Message: "must be between 0 and 1, or null"})
	}
	return out
}

// decision is the override in decide() form, nil without one.
func (t *studioThresholds) decision() *decision.Thresholds {
	if t == nil {
		return nil
	}
	return &decision.Thresholds{Space: decision.SpaceRaw, WarnAt: t.WarnAt, CriticalAt: t.CriticalAt}
}

// policy is the override as a route evaluator's thresholds (raw space).
func (t *studioThresholds) policy() *policy.Thresholds {
	out := &policy.Thresholds{Space: decision.SpaceRaw}
	if t != nil {
		out.WarnAt = t.WarnAt
		out.CriticalAt = policy.OptFloat{Set: t.CriticalAt.Set, Value: t.CriticalAt.Value}
	}
	return out
}

// testedEvaluator is the evaluator a test or a dry-run runs.
type testedEvaluator struct {
	// spec is the spec as compiled; ref its ref (<id>@<latest+1> for an unsaved spec, D58).
	spec *decision.Spec
	ref  string
	// resultRef names it in studio_results and DryRunResult: the ref of a saved version, or
	// <id>@draft:<sha12> for an unsaved spec.
	resultRef string
}

// tested resolves the evaluator of a test or a dry-run: exactly one of spec (an unsaved spec, run
// as D58 says) and ref (a saved version). The spec must pass the schema and the hard rules, with
// the threshold override merged in; otherwise it answers 422 and returns nil.
func (a *studioAPI) tested(c *gin.Context, spec json.RawMessage, ref string, th *studioThresholds) *testedEvaluator {
	hasSpec := len(spec) > 0 && string(spec) != "null"
	switch {
	case hasSpec && ref != "":
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "Give either a spec or the ref of a saved version, not both.",
			fieldError{Path: "ref", Message: "give either spec or ref"})
		return nil
	case !hasSpec && ref == "":
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "Give the spec to test or the ref of a saved version.",
			fieldError{Path: "spec", Message: "spec or ref is required"})
		return nil
	}
	if errs := th.check(); len(errs) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The threshold override is not valid.", errs...)
		return nil
	}
	opts := decision.LintOptions{Registry: a.evaluatorExists, Thresholds: th.decision()}
	if ref != "" {
		s, ok := a.registry.Get(ref)
		if !ok {
			abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "No saved evaluator version "+ref+".",
				fieldError{Path: "ref", Message: "unknown evaluator version", Code: decision.CodeUnknownEvaluatorReference})
			return nil
		}
		if hard := decision.HardErrors(decision.LintSpec(s, opts)); len(hard) > 0 {
			abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The thresholds do not fit "+ref+".", lintFields(hard)...)
			return nil
		}
		return &testedEvaluator{spec: s, ref: ref, resultRef: ref}
	}
	v := decision.ValidateSpec(spec, opts)
	if !v.Valid() {
		fields := make([]fieldError, 0, len(v.Errors))
		for _, e := range v.Errors {
			fields = append(fields, fieldError{Path: e.Path, Message: e.Message})
		}
		fields = append(fields, lintFields(v.HardErrors())...)
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The spec is not valid.", fields...)
		return nil
	}
	latest := 0
	if e, ok := a.registry.Evaluator(v.Spec.ID); ok {
		latest = e.LatestVersion
	}
	u, err := studio.CompileUnsaved(spec, latest)
	if err != nil {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The spec is not valid: "+err.Error(),
			fieldError{Path: "spec", Message: err.Error()})
		return nil
	}
	return &testedEvaluator{spec: u.Spec, ref: u.Spec.Ref(), resultRef: u.ResultRef}
}

// evaluatorExists reports whether the registry has an evaluator id (LintOptions.Registry).
func (a *studioAPI) evaluatorExists(id string) bool {
	_, ok := a.registry.Evaluator(id)
	return ok
}

// lintFields are hard lints as field errors.
func lintFields(lints []decision.Lint) []fieldError {
	out := make([]fieldError, 0, len(lints))
	for _, l := range lints {
		out = append(out, fieldError{Path: l.Path, Message: l.Message, Code: l.Code})
	}
	return out
}

// runner builds the runner of a test or a dry-run: route r with only the tested evaluator (its
// thresholds overridden), observe mode and on_no_evidence ignore, so exactly one question is
// asked and an answer without the evidence it needs is simply not assessed. Calls are
// non-enforcing (D82) and labelled with component, the route and the caller.
func (a *studioAPI) runner(c *gin.Context, client jev.Client, base *policy.Route, ev *testedEvaluator, th *studioThresholds, component string) *studio.Runner {
	r := *base
	r.Evaluators = []policy.EvaluatorRef{{Ref: ev.ref, Thresholds: th.policy()}}
	r.Mode, r.OnNoEvidence, r.RequireCalibration = decision.ModeObserve, decision.OnNoEvidenceIgnore, false
	info := client.Info()
	model := r.Decision.Model
	if model == "" {
		model = info.Model
	}
	if model == "" {
		model = "unset"
	}
	cpt := 0.0
	if src, ok := client.(jev.CPTSource); ok {
		cpt = src.CPT()
	}
	specs := maps.Clone(a.registry.Specs())
	if specs == nil {
		specs = map[string]*decision.Spec{}
	}
	specs[ev.ref] = ev.spec
	label := ""
	if id := callerIdentity(c); id != nil {
		label = id.Label
	}
	routeLabel := r.Name
	if component == jev.ComponentStudioTest {
		routeLabel = ""
	}
	return &studio.Runner{
		Client: client, Route: &r, Mode: decision.ModeObserve, Model: model, Specs: specs,
		Budget: gate.RouteBudget(a.budget, &r, cpt), PricePerMTok: a.decisionPrice(info.Backend, model),
		CallTimeout: studioCallTimeout,
		Call: jev.CallOptions{
			Class: jev.ClassAsync, Component: component, Enforcing: false, Route: routeLabel, KeyLabel: label,
		},
		Now: a.now,
	}
}

// decisionModel returns the decision model in effect, or answers 503 decision_model_unavailable
// when it is off.
func (a *studioAPI) decisionModel(c *gin.Context) jev.Client {
	client := a.client()
	if client == nil || client.Info().Backend == jev.BackendOff {
		msg := "The decision model is off, so nothing can be tested."
		if client != nil && client.Info().Reason != "" {
			msg += " " + client.Info().Reason
		}
		abortAPI(c, http.StatusServiceUnavailable, codeDecisionModelUnavailable, msg)
		return nil
	}
	return client
}

// budgetLeft is the decision spend still allowed before the observe line, nil when unlimited
// (JSON has no infinity).
func (a *studioAPI) budgetLeft() *float64 {
	h := a.costs.headroom(false)
	if math.IsInf(h, 1) || math.IsNaN(h) {
		return nil
	}
	h = max(h, 0)
	return &h
}

// overBudget answers 503 budget_exhausted when estimate exceeds what is left before the observe
// line of the daily decision cap (D82).
func (a *studioAPI) overBudget(c *gin.Context, estimate float64, details map[string]any) bool {
	left := a.costs.headroom(false)
	if estimate <= left {
		return false
	}
	if details == nil {
		details = map[string]any{}
	}
	details["estimate_usd"], details["decision_budget_left_usd"] = roundUSD(estimate), a.budgetLeft()
	abortAPIDetails(c, http.StatusServiceUnavailable, codeBudgetExhausted,
		fmt.Sprintf("This test would cost about $%.4f, more than today's decision budget has left.", estimate), details)
	return true
}

// runSamples evaluates samples with at most studioConcurrency at once, in order. Once ctx ends
// (the caller went away) no further sample starts: its outcome stays nil.
func runSamples(ctx context.Context, run *studio.Runner, samples []*studio.Sample) []*studio.Outcome {
	out := make([]*studio.Outcome, len(samples))
	sem := make(chan struct{}, studioConcurrency)
	var wg sync.WaitGroup
	for i, s := range samples {
		sem <- struct{}{}
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			out[i] = run.Evaluate(ctx, s)
		}()
	}
	wg.Wait()
	return out
}

// studioRow is the studio_results row of a sample's result.
func studioRow(runID, kind, sampleID, ref string, o *studio.Outcome, r studio.Result) store.StudioResult {
	row := store.StudioResult{
		RunID: runID, Kind: kind, SampleID: sampleID, EvaluatorRef: ref, RiskRaw: r.RiskRaw, Risk: r.Risk,
		Level: r.Level, Status: &r.Status, Escalate: r.Escalate,
	}
	if r.Answer != nil {
		if b, err := json.Marshal(r.Answer); err == nil {
			row.Answer = b
		}
	}
	if o.Usage != nil {
		row.Snapshot = o.Usage.Snapshot
	}
	if !o.Skipped {
		latency, cost := o.LatencyMs, o.CostUSD
		row.LatencyMs, row.CostUSD = &latency, &cost
	}
	return row
}

// persistContext is a context for writes made after a run: it outlives the request (a cancelled
// dry-run still records what it spent) but is bounded.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), studioPersistTimeout)
}

// --------------------------------------------------------------------------- #35 test

// studioSampleBody is api.ts StudioSample as a test bench sends it.
type studioSampleBody struct {
	ID                 string               `json:"id"`
	TraceID            *string              `json:"trace_id"`
	UserRequest        *string              `json:"user_request"`
	AssistantResponse  string               `json:"assistant_response"`
	Evidence           []studioEvidenceBody `json:"evidence"`
	SystemInstructions *string              `json:"system_instructions"`
	Rules              map[string]string    `json:"rules"`
	Expected           *string              `json:"expected"`
}

type studioEvidenceBody struct {
	ID     *string `json:"id"`
	Text   *string `json:"text"`
	Source *string `json:"source"`
}

// Expected outcomes of a labelled sample.
const (
	expectFire   = "fire"
	expectNoFire = "no_fire"
)

// studioTestRequest is api.ts StudioTestRequest.
type studioTestRequest struct {
	Spec       json.RawMessage    `json:"spec"`
	Ref        string             `json:"ref"`
	Samples    []studioSampleBody `json:"samples"`
	Thresholds *studioThresholds  `json:"thresholds"`
}

// studioTestResult is api.ts StudioTestResponse.results[].
type studioTestResult struct {
	SampleID      string              `json:"sample_id"`
	Status        string              `json:"status"`
	Reason        *string             `json:"reason"`
	Answer        *decision.RawAnswer `json:"answer"`
	Probabilities map[string]float64  `json:"probabilities"`
	RiskRaw       *float64            `json:"risk_raw"`
	Risk          *float64            `json:"risk"`
	Level         *string             `json:"level"`
	Escalate      *string             `json:"escalate"`
	Fires         *bool               `json:"fires"`
	LatencyMs     int64               `json:"latency_ms"`
	CostUSD       float64             `json:"cost_usd"`
	Expected      *string             `json:"expected"`
	Agrees        *bool               `json:"agrees"`
}

// studioTestSummary is api.ts StudioTestResponse.summary.
type studioTestSummary struct {
	N         int      `json:"n"`
	Assessed  int      `json:"assessed"`
	Agreement *float64 `json:"agreement"`
	CostUSD   float64  `json:"cost_usd"`
	Snapshot  *string  `json:"snapshot"`
	Backend   string   `json:"backend"`
}

// studioTestResponse is api.ts StudioTestResponse.
type studioTestResponse struct {
	RunID   string             `json:"run_id"`
	Results []studioTestResult `json:"results"`
	Summary studioTestSummary  `json:"summary"`
}

// checkSamples validates a test bench and converts it.
func checkSamples(in []studioSampleBody) ([]*studio.Sample, []fieldError) {
	var errs []fieldError
	if len(in) == 0 || len(in) > maxStudioSamples {
		return nil, []fieldError{{Path: "samples", Message: fmt.Sprintf("give 1 to %d samples", maxStudioSamples)}}
	}
	seen := map[string]bool{}
	out := make([]*studio.Sample, 0, len(in))
	for i, b := range in {
		at := fmt.Sprintf("samples[%d]", i)
		id := strings.TrimSpace(b.ID)
		switch {
		case id == "" || utf8.RuneCountInString(id) > 128:
			errs = append(errs, fieldError{Path: at + ".id", Message: "a sample needs an id of 1 to 128 characters"})
		case seen[id]:
			errs = append(errs, fieldError{Path: at + ".id", Message: "the id " + id + " repeats"})
		}
		seen[id] = true
		if strings.TrimSpace(b.AssistantResponse) == "" {
			errs = append(errs, fieldError{Path: at + ".assistant_response", Message: "the answer to check is required"})
		}
		if b.Expected != nil && *b.Expected != expectFire && *b.Expected != expectNoFire {
			errs = append(errs, fieldError{Path: at + ".expected", Message: "must be fire or no_fire"})
		}
		s := &studio.Sample{ID: id, Response: b.AssistantResponse, Rules: b.Rules, Evidence: []decision.EvidenceItem{}}
		if b.UserRequest != nil {
			s.UserRequest = *b.UserRequest
		}
		if b.SystemInstructions != nil {
			s.SystemInstructions = *b.SystemInstructions
		}
		ids := map[string]bool{}
		for k, e := range b.Evidence {
			path := fmt.Sprintf("%s.evidence[%d]", at, k)
			item := decision.EvidenceItem{ID: fmt.Sprintf("doc-%d", k+1), Source: evidence.SourceRequestContext}
			if e.Text == nil {
				errs = append(errs, fieldError{Path: path + ".text", Message: "the evidence text is required"})
				continue
			}
			item.Text = *e.Text
			if e.ID != nil && *e.ID != "" {
				item.ID = *e.ID
			}
			if e.Source != nil {
				item.Source = *e.Source
			}
			if !slices.Contains(store.EvidenceSources, item.Source) {
				errs = append(errs, fieldError{Path: path + ".source", Message: "unknown evidence source " + item.Source})
			}
			if ids[item.ID] {
				errs = append(errs, fieldError{Path: path + ".id", Message: "the evidence id " + item.ID + " repeats"})
			}
			ids[item.ID] = true
			s.Evidence = append(s.Evidence, item)
		}
		out = append(out, s)
	}
	return out, errs
}

// studioRoute is the route a test bench runs on: the active policy's defaults with every evidence
// source on (a pasted sample names its own sources).
func (a *studioAPI) studioRoute() *policy.Route {
	r := &policy.Route{Name: policy.DefaultRouteName}
	if p := a.policy(); p != nil && p.Default != nil {
		copied := *p.Default
		r = &copied
	}
	r.Evidence.Sources = slices.Clone(store.EvidenceSources)
	if r.Evidence.MaxTokens <= 0 {
		r.Evidence.MaxTokens = decision.DefaultBudget().EvidenceMaxTokens
	}
	return r
}

// POST /api/studio/test (SPEC §2.4 row 35): run a spec on up to 50 samples.
func (a *studioAPI) test(c *gin.Context) {
	var req studioTestRequest
	if !bindJSON(c, &req, false) {
		return
	}
	samples, errs := checkSamples(req.Samples)
	if len(errs) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The samples are not valid.", errs...)
		return
	}
	ev := a.tested(c, req.Spec, req.Ref, req.Thresholds)
	if ev == nil {
		return
	}
	client := a.decisionModel(c)
	if client == nil {
		return
	}
	run := a.runner(c, client, a.studioRoute(), ev, req.Thresholds, jev.ComponentStudioTest)
	estimate := 0.0
	for _, s := range samples {
		if compiled, err := run.Compile(s); err == nil {
			estimate += run.EstimateTokensUSD(compiled)
		}
	}
	if a.overBudget(c, estimate, map[string]any{"samples": len(samples)}) {
		return
	}

	outcomes := runSamples(c.Request.Context(), run, samples)
	if c.Request.Context().Err() != nil {
		c.Abort() // the caller went away: nobody reads the answer
		return
	}
	resp := studioTestResponse{RunID: testRunPrefix + store.NewID(), Results: make([]studioTestResult, 0, len(samples)),
		Summary: studioTestSummary{N: len(samples), Backend: client.Info().Backend}}
	rows := make([]store.StudioResult, 0, len(samples))
	compared, agreed := 0, 0
	for i, o := range outcomes {
		if o.Failed(jev.KindBudget) {
			abortAPIDetails(c, http.StatusServiceUnavailable, codeBudgetExhausted,
				"Today's decision budget ran out during the test.", map[string]any{"decision_budget_left_usd": a.budgetLeft()})
			return
		}
		r := studio.ResultOf(o, ev.ref)
		res := studioTestResult{
			SampleID: samples[i].ID, Status: r.Status, Reason: r.Reason, Answer: r.Answer, Probabilities: r.Probabilities,
			RiskRaw: r.RiskRaw, Risk: r.Risk, Level: r.Level, Escalate: r.Escalate, Fires: r.Fires,
			LatencyMs: o.LatencyMs, CostUSD: o.CostUSD, Expected: req.Samples[i].Expected,
		}
		if res.Expected != nil && res.Fires != nil {
			agrees := (*res.Expected == expectFire) == *res.Fires
			res.Agrees = &agrees
			compared++
			if agrees {
				agreed++
			}
		}
		if r.Status == decision.EvalAssessed {
			resp.Summary.Assessed++
		}
		resp.Summary.CostUSD += o.CostUSD
		if o.Usage != nil && o.Usage.Snapshot != nil {
			resp.Summary.Snapshot = o.Usage.Snapshot
		}
		resp.Results = append(resp.Results, res)
		rows = append(rows, studioRow(resp.RunID, store.StudioKindTest, samples[i].ID, ev.resultRef, o, r))
	}
	if compared > 0 {
		share := float64(agreed) / float64(compared)
		resp.Summary.Agreement = &share
	}
	resp.Summary.CostUSD = roundUSD(resp.Summary.CostUSD)
	if a.db != nil {
		ctx, cancel := persistContext(c.Request.Context())
		defer cancel()
		if err := a.db.InsertStudioResults(ctx, rows); err != nil {
			// The results were paid for: they are still answered, only not kept.
			a.log.Warn("studio: the test results could not be stored", "run_id", resp.RunID, "err", err)
		}
	}
	c.JSON(http.StatusOK, resp)
}

// --------------------------------------------------------------------------- #36 samples

// studioSamplesRequest is api.ts StudioSamplesRequest.
type studioSamplesRequest struct {
	Route  string `json:"route"`
	Window string `json:"window"`
	Filter string `json:"filter"`
	Limit  *int   `json:"limit"`
}

// studioSampleView is api.ts StudioSample as the samples endpoint returns it.
type studioSampleView struct {
	ID                 string               `json:"id"`
	TraceID            string               `json:"trace_id"`
	UserRequest        *string              `json:"user_request,omitempty"`
	AssistantResponse  string               `json:"assistant_response"`
	Evidence           []studioEvidenceView `json:"evidence,omitempty"`
	SystemInstructions *string              `json:"system_instructions,omitempty"`
	Rules              map[string]string    `json:"rules,omitempty"`
}

type studioEvidenceView struct {
	ID     string `json:"id,omitempty"`
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
}

// sampleOfBody is a trace body as a test-bench sample: the original answer (attempt 0), the
// request texts, and the trusted evidence (untrusted tool results never count as evidence).
func sampleOfBody(b *store.BodyRecord, now time.Time) (studioSampleView, bool) {
	if b == nil || !now.Before(b.ExpiresAt) || b.ResponseText == nil || strings.TrimSpace(*b.ResponseText) == "" {
		return studioSampleView{}, false
	}
	v := studioSampleView{ID: b.TraceID, TraceID: b.TraceID, AssistantResponse: *b.ResponseText}
	if b.UserRequest != nil && *b.UserRequest != "" {
		v.UserRequest = b.UserRequest
	}
	if b.SystemInstructions != nil && *b.SystemInstructions != "" {
		v.SystemInstructions = b.SystemInstructions
	}
	if len(b.Rules) > 0 {
		v.Rules = b.Rules
	}
	for _, it := range bodyEvidence(b) {
		if !it.Untrusted {
			v.Evidence = append(v.Evidence, studioEvidenceView{ID: it.ID, Text: it.Text, Source: it.Source})
		}
	}
	return v, true
}

// bodyEvidence decodes a body's evidence items; a malformed list is no evidence.
func bodyEvidence(b *store.BodyRecord) []evidence.Item {
	var items []evidence.Item
	if len(b.Evidence) > 0 {
		_ = json.Unmarshal(b.Evidence, &items)
	}
	return items
}

// POST /api/studio/samples (SPEC §2.4 row 36): test-bench samples from recent traffic.
func (a *studioAPI) samples(c *gin.Context) {
	var req studioSamplesRequest
	if !bindJSON(c, &req, false) {
		return
	}
	var errs []fieldError
	window, ok := policy.ParseWindow(req.Window)
	if !ok {
		errs = append(errs, fieldError{Path: "window", Message: "must be one of 15m, 1h, 24h, 7d, 30d"})
	}
	if !slices.Contains([]string{store.SampleFlagged, store.SampleRandom, store.SampleDisagreement}, req.Filter) {
		errs = append(errs, fieldError{Path: "filter", Message: "must be flagged, random or disagreement"})
	}
	limit := defaultStudioSamples
	if req.Limit != nil {
		limit = *req.Limit
		if limit < 1 || limit > maxStudioSamples {
			errs = append(errs, fieldError{Path: "limit", Message: fmt.Sprintf("must be between 1 and %d", maxStudioSamples)})
		}
	}
	if len(errs) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The samples request is not valid.", errs...)
		return
	}
	if a.db == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "Samples come from stored traffic, and this gateway has no database.")
		return
	}
	ctx := c.Request.Context()
	now := a.now().UTC()
	ids, err := a.db.StudioSampleTraces(ctx, store.StudioSampleFilter{
		Route: req.Route, Since: now.Add(-window), Filter: req.Filter, Limit: limit, Now: now,
	})
	if err != nil {
		a.log.Error("studio: sample query failed", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The samples could not be read.")
		return
	}
	out := []studioSampleView{}
	for _, id := range ids {
		// A trace's queued writes land before its body is read (D83).
		if err := a.barrier(ctx, id); err != nil {
			a.log.Warn("studio: a sample's queued writes did not land; skipping it", "trace_id", id, "err", err)
			continue
		}
		b, err := a.db.GetTraceBody(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			a.log.Error("studio: sample body read failed", "trace_id", id, "err", err)
			abortAPI(c, http.StatusInternalServerError, codeInternal, "The samples could not be read.")
			return
		}
		if v, ok := sampleOfBody(b, now); ok {
			out = append(out, v)
		}
	}
	c.JSON(http.StatusOK, gin.H{"samples": out})
}

// --------------------------------------------------------------------------- #37 calibrate

// POST /api/studio/calibrate (SPEC §2.4 row 37): the quick calibration, pure (decision.
// QuickCalibrate, mirrored in TypeScript). It never emits critical_at.
func (a *studioAPI) calibrate(c *gin.Context) {
	var req decision.QuickCalibrationRequest
	if !bindJSON(c, &req, false) {
		return
	}
	if len(req.Labeled) > maxQuickLabels {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed,
			fmt.Sprintf("A quick calibration takes at most %d labels; use Calibrate now for more.", maxQuickLabels),
			fieldError{Path: "labeled", Message: fmt.Sprintf("at most %d labels", maxQuickLabels)})
		return
	}
	if req.Labeled == nil {
		req.Labeled = []decision.LabeledScore{}
	}
	q, err := decision.QuickCalibrate(&req)
	if err != nil {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, err.Error(), fieldError{Path: "labeled", Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, q)
}

// --------------------------------------------------------------------------- #38 dry-run

// studioDryRunRequest is api.ts StudioDryRunRequest.
type studioDryRunRequest struct {
	Spec        json.RawMessage   `json:"spec"`
	Ref         string            `json:"ref"`
	Thresholds  *studioThresholds `json:"thresholds"`
	Route       string            `json:"route"`
	Window      string            `json:"window"`
	MaxSamples  int               `json:"max_samples"`
	ConfirmCost bool              `json:"confirm_cost"`
}

// dryRunResult is api.ts DryRunResult.
type dryRunResult struct {
	RunID        string `json:"run_id"`
	EvaluatorRef string `json:"evaluator_ref"`
	Route        string `json:"route"`
	Window       string `json:"window"`
	Population   int64  `json:"population"`
	studio.DryRunStats
}

// Dry-run outcomes in the studio.dry_run audit row.
const (
	dryRunDone      = "done"
	dryRunCancelled = "cancelled"
	dryRunNoBudget  = "budget_exhausted"
)

// dryRunMaxSamples is TRUTHTABLE_DRYRUN_MAX_SAMPLES (200 when unset).
func (a *studioAPI) dryRunMaxSamples() int {
	if a.cfg.DryRunMaxSamples > 0 {
		return a.cfg.DryRunMaxSamples
	}
	return defaultDryRunMaxSamples
}

// POST /api/studio/dry-run (SPEC §2.4 row 38, §7.3 step 6): "Test on past traffic (paid)". The
// checks that can refuse run before the stream starts: 409 confirmation_required when the
// estimate exceeds TRUTHTABLE_STUDIO_CONFIRM_ABOVE_USD without confirm_cost, 503
// budget_exhausted when it exceeds what the decision budget has left before the observe line.
// Then the stream sends estimate, a progress event per batch (with the cost so far), and done
// (or error). Aborting the request stops the run between calls; what it spent is recorded in the
// studio.dry_run audit row either way.
func (a *studioAPI) dryRun(c *gin.Context) {
	var req studioDryRunRequest
	if !bindJSON(c, &req, false) {
		return
	}
	var errs []fieldError
	window, ok := policy.ParseWindow(req.Window)
	if !ok || (req.Window != "24h" && req.Window != "7d") {
		errs = append(errs, fieldError{Path: "window", Message: "must be 24h or 7d"})
	}
	if req.MaxSamples < 1 {
		errs = append(errs, fieldError{Path: "max_samples", Message: "must be at least 1"})
	}
	var route *policy.Route
	if p := a.policy(); p != nil {
		route, _ = p.Route(req.Route)
	}
	if req.Route == "" || route == nil {
		errs = append(errs, fieldError{Path: "route", Message: "no route named " + req.Route + " in the active policy"})
	}
	if len(errs) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The dry-run request is not valid.", errs...)
		return
	}
	ev := a.tested(c, req.Spec, req.Ref, req.Thresholds)
	if ev == nil {
		return
	}
	if a.db == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "A dry-run replays stored traffic, and this gateway has no database.")
		return
	}
	client := a.decisionModel(c)
	if client == nil {
		return
	}
	ctx := c.Request.Context()
	now := a.now().UTC()
	filter := store.DryRunFilter{Route: route.Name, Since: now.Add(-window), Now: now}
	population, withBody, err := a.db.DryRunPopulation(ctx, filter)
	if err != nil {
		a.log.Error("studio: dry-run population query failed", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The route's traffic could not be read.")
		return
	}
	n := min(req.MaxSamples, a.dryRunMaxSamples(), int(min(withBody, math.MaxInt32)))
	candidates, err := a.db.DryRunSample(ctx, filter, n)
	if err != nil {
		a.log.Error("studio: dry-run sample query failed", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The route's traffic could not be read.")
		return
	}
	run := a.runner(c, client, route, ev, req.Thresholds, jev.ComponentDryRun)
	samples := make([]*studio.Sample, len(candidates))
	traces := make([]studio.DryRunTrace, len(candidates))
	estimate := 0.0
	for i := range candidates {
		cand := &candidates[i]
		samples[i] = sampleOfCandidate(cand)
		traces[i] = studio.DryRunTrace{TraceID: cand.TraceID, InclusionProb: cand.Body.InclusionProb, Levels: cand.Levels, AuditVerdict: cand.AuditVerdict}
		if compiled, err := run.Compile(samples[i]); err == nil {
			estimate += run.EstimateTokensUSD(compiled)
		}
	}
	details := map[string]any{"estimate_usd": roundUSD(estimate), "samples": len(samples), "population": population,
		"decision_budget_left_usd": a.budgetLeft()}
	if estimate > a.cfg.StudioConfirmAboveUSD && !req.ConfirmCost {
		abortAPIDetails(c, http.StatusConflict, codeConfirmationRequired,
			fmt.Sprintf("This test costs about $%.4f; confirm to run it.", estimate), details)
		return
	}
	if a.overBudget(c, estimate, map[string]any{"samples": len(samples), "population": population}) {
		return
	}
	// The run never spends much more than the estimate the caller saw: reservations are twice
	// each call's estimate, so the cap is twice the estimate (0, unlimited, for a free model).
	if estimate > 0 {
		run.Spend = studio.NewSpend(estimate*studio.ReserveFactor*1.01 + 1e-9)
	}

	runID := dryRunRunPrefix + store.NewID()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	sse := &sseWriter{c: c}
	sse.send("estimate", gin.H{"est_cost_usd": roundUSD(estimate), "sampled": len(samples), "population": population})

	tally := studio.NewDryRunTally(ev.ref)
	rows := make([]store.StudioResult, 0, len(samples))
	outcome := dryRunDone
	for start := 0; start < len(samples); start += dryRunBatch {
		if ctx.Err() != nil {
			outcome = dryRunCancelled
			break
		}
		end := min(start+dryRunBatch, len(samples))
		outs := runSamples(ctx, run, samples[start:end])
		budgetHit := false
		for k, o := range outs {
			i := start + k
			if o == nil {
				continue // never started: the caller went away first
			}
			r := tally.Add(traces[i], o)
			rows = append(rows, studioRow(runID, store.StudioKindDryRun, traces[i].TraceID, ev.resultRef, o, r))
			budgetHit = budgetHit || o.Failed(jev.KindBudget)
		}
		if ctx.Err() != nil {
			outcome = dryRunCancelled
			break
		}
		evaluated, fired, cost := tally.Progress()
		sse.send("progress", gin.H{"evaluated": evaluated, "fired": fired, "cost_usd": roundUSD(cost)})
		if budgetHit {
			outcome = dryRunNoBudget
			break
		}
		if run.Spend.Stopped() {
			// The calls cost more than twice the estimate the caller confirmed: stop, and report
			// what ran.
			a.log.Warn("studio: the dry-run stopped at twice its estimate", "run_id", runID, "estimate_usd", estimate, "spent_usd", run.Spend.Total())
			break
		}
	}

	stats := tally.Stats(population)
	stats.CostUSD = roundUSD(stats.CostUSD)
	pctx, cancel := persistContext(ctx)
	defer cancel()
	if err := a.db.InsertStudioResults(pctx, rows); err != nil {
		a.log.Warn("studio: the dry-run results could not be stored", "run_id", runID, "err", err)
	}
	if err := a.db.AppendAdminAudit(pctx, store.AdminAuditEntry{Actor: actor(c), Action: "studio.dry_run", Target: route.Name,
		Diff: map[string]any{"run_id": runID, "evaluator_ref": ev.resultRef, "window": req.Window, "sampled": stats.Sampled,
			"cost_usd": stats.CostUSD, "outcome": outcome}}); err != nil {
		a.log.Error("studio: the dry-run audit row could not be written", "run_id", runID, "err", err)
	}
	switch outcome {
	case dryRunDone:
		sse.send("done", dryRunResult{RunID: runID, EvaluatorRef: ev.resultRef, Route: route.Name, Window: req.Window,
			Population: population, DryRunStats: stats})
	case dryRunNoBudget:
		sse.send("error", apiErrorBody{Error: apiError{Code: codeBudgetExhausted,
			Message: "Today's decision budget ran out during the test; the results so far were kept.",
			Details: map[string]any{"cost_usd": stats.CostUSD, "evaluated": stats.Sampled}}})
	}
}

// sampleOfCandidate is a dry-run trace as a runner sample: the original answer, the request
// texts, the evidence as the gate packed it (untrusted items stay untrusted) and the rules.
func sampleOfCandidate(cand *store.DryRunCandidate) *studio.Sample {
	b := &cand.Body
	s := &studio.Sample{ID: cand.TraceID, Rules: b.Rules, Evidence: []decision.EvidenceItem{}}
	if b.ResponseText != nil {
		s.Response = *b.ResponseText
	}
	if b.UserRequest != nil {
		s.UserRequest = *b.UserRequest
	}
	if b.SystemInstructions != nil {
		s.SystemInstructions = *b.SystemInstructions
	}
	for _, it := range bodyEvidence(b) {
		s.Evidence = append(s.Evidence, it.Decision())
	}
	return s
}

// sseWriter writes server-sent events, each within its own write deadline. After a failed write
// it writes nothing more.
type sseWriter struct {
	c      *gin.Context
	broken bool
}

func (w *sseWriter) send(event string, data any) {
	if w.broken {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		w.broken = true
		return
	}
	_ = http.NewResponseController(w.c.Writer).SetWriteDeadline(time.Now().Add(sseWriteWindow))
	if _, err := fmt.Fprintf(w.c.Writer, "event: %s\ndata: %s\n\n", event, b); err != nil {
		w.broken = true
		return
	}
	w.c.Writer.Flush()
}

// --------------------------------------------------------------------------- #34 draft

// studioDraftRequest is api.ts StudioDraftRequest.
type studioDraftRequest struct {
	Description string  `json:"description"`
	Route       *string `json:"route"`
}

// studioDraftResponse is api.ts StudioDraftResponse.
type studioDraftResponse struct {
	Draft      json.RawMessage       `json:"draft"`
	Errors     []decision.FieldError `json:"errors"`
	Lints      []decision.Lint       `json:"lints"`
	Model      string                `json:"model"`
	CostUSD    float64               `json:"cost_usd"`
	CostSource string                `json:"cost_source"`
	Attempts   int                   `json:"attempts"`
}

// POST /api/studio/draft (SPEC §2.4 row 34, §7.3 step 1): "Draft with AI". The call reserves its
// estimate of the LLM budget first (D48), so a spent budget answers 503 budget_exhausted; no
// judge answers 503 no_judge; an engine that cannot be reached, 503 engine_unavailable. The draft
// is validated here (evaluator-draft.v1 and the hard rules) and is null while the schema still
// fails. It is never saved or attached.
func (a *studioAPI) draft(c *gin.Context) {
	var req studioDraftRequest
	if !bindJSON(c, &req, false) {
		return
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" || utf8.RuneCountInString(req.Description) > studio.MaxDraftDescription {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed,
			fmt.Sprintf("Describe what to check in 1 to %d characters.", studio.MaxDraftDescription),
			fieldError{Path: "description", Message: fmt.Sprintf("1 to %d characters", studio.MaxDraftDescription)})
		return
	}
	in := studio.DraftInput{Description: desc, ExistingIDs: a.existingIDs()}
	if req.Route != nil && *req.Route != "" {
		var route *policy.Route
		if p := a.policy(); p != nil {
			route, _ = p.Route(*req.Route)
		}
		if route == nil {
			abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "No route named "+*req.Route+" in the active policy.",
				fieldError{Path: "route", Message: "unknown route"})
			return
		}
		in.RouteName, in.RouteDescription = route.Name, route.Description
	}
	if a.drafter == nil {
		abortAPI(c, http.StatusServiceUnavailable, codeEngineUnavailable, "Drafting needs the audit engine, and this gateway has none.")
		return
	}
	estimate, err := a.draftEstimate(in)
	if err != nil {
		a.log.Error("studio: the draft could not be priced", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The draft could not be priced.")
		return
	}
	settle, err := a.costs.reserve(c.Request.Context(), estimate, componentStudioDraft)
	if err != nil {
		abortAPIDetails(c, http.StatusServiceUnavailable, codeBudgetExhausted,
			"Today's deep-audit budget is used up, so nothing can be drafted until it resets.", map[string]any{"estimate_usd": roundUSD(estimate)})
		return
	}
	resp, err := a.drafter.DraftEvaluator(c.Request.Context(), &pb.DraftEvaluatorRequest{
		Description: in.Description, RouteName: in.RouteName, RouteDescription: in.RouteDescription, ExistingIds: in.ExistingIDs,
	})
	if err != nil {
		st := status.Convert(err)
		switch {
		case st.Code() == codes.FailedPrecondition && strings.Contains(st.Message(), codeNoJudge):
			settle(0)
			abortAPI(c, http.StatusServiceUnavailable, codeNoJudge, "No judge is configured, so Draft with AI is off. Start from a template instead.")
		case st.Code() == codes.DeadlineExceeded || st.Code() == codes.Canceled:
			// The judge may have been called and billed: the estimate stays spent (D48).
			settle(estimate)
			abortAPI(c, http.StatusServiceUnavailable, codeEngineUnavailable, "The audit engine did not finish the draft in time.")
		default:
			settle(0)
			a.log.Warn("studio: draft failed", "code", st.Code().String(), "err", err)
			abortAPI(c, http.StatusServiceUnavailable, codeEngineUnavailable, "The audit engine could not draft: "+st.Message())
		}
		return
	}
	records := []*pb.CostRecord{}
	if resp.GetCost() != nil {
		records = append(records, resp.GetCost())
	}
	settle(costs.RealCostUSD(records))
	a.costs.record(context.WithoutCancel(c.Request.Context()), records, CostLink{Route: in.RouteName, KeyLabel: actor(c)})

	out := studioDraftResponse{
		Errors: []decision.FieldError{}, Lints: []decision.Lint{}, Model: resp.GetModel(),
		CostUSD: resp.GetCost().GetCostUsd(), CostSource: resp.GetCost().GetCostSource(), Attempts: int(resp.GetAttempts()),
	}
	if out.CostSource == "" {
		out.CostSource = decision.CostUnknown
	}
	if !finiteNonNegative(out.CostUSD) {
		out.CostUSD = 0
	}
	doc := []byte(strings.TrimSpace(resp.GetSpecJson()))
	if len(doc) == 0 {
		out.Errors = append(out.Errors, decision.FieldError{Message: "The drafter returned no draft."})
	} else {
		v := decision.ValidateSpec(doc, decision.LintOptions{Draft: true, Registry: a.evaluatorExists})
		out.Errors, out.Lints = v.Errors, v.Lints
		if len(v.Errors) == 0 {
			out.Draft = json.RawMessage(doc)
		}
	}
	if out.Draft == nil {
		out.Draft = json.RawMessage("null")
	}
	c.JSON(http.StatusOK, out)
}

// draftEstimate is what a draft reserves of the LLM budget: its prompt at the judge's prices.
func (a *studioAPI) draftEstimate(in studio.DraftInput) (float64, error) {
	chars, err := studio.DraftPromptChars(in)
	if err != nil {
		return 0, err
	}
	provider, model := a.judge()
	price, ok := a.judgePrice(provider, model)
	if !ok {
		return 0, fmt.Errorf("no price for the judge %s/%s nor the reference judge", provider, model)
	}
	return studio.DraftReserveUSD(chars, decision.DefaultCPT, price)
}

// existingIDs are the ids of the registry's evaluators (archived ones left out), sorted: the only
// ids a draft may reference.
func (a *studioAPI) existingIDs() []string {
	out := []string{}
	for _, e := range a.registry.Evaluators(false) {
		out = append(out, e.ID)
	}
	slices.Sort(out)
	return out
}

// finiteNonNegative reports whether x is a finite number >= 0.
func finiteNonNegative(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 }
