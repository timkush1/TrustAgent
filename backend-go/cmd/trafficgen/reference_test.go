package main

// The demo scenarios against the Go reference (SPEC §2.9, §2.10, §2.12). Every scenario's expect
// was derived by hand; these tests replay each one through the reference decision core under the
// demo policy (config/policies.yaml plus config/policies.pending.yaml) and the mock calibration
// (config/calibration), with the mock decision backend answering from mock_answers:
//
//   - TestScenariosMatchTheReference runs the SPEC pipeline for every scenario: route match,
//     evidence extraction, detectors, compile, the scripted answers, decide(), the X-TrustAgent-*
//     header values, the deep-audit plan, the fallback re-gate and the delivered text, and the
//     audit verdict the scoring v2 rules give the judge fixture;
//   - TestRunnableScenariosThroughTheGate sends each scenario this build can serve (no phase tag)
//     through the real internal/gate with the real mock backend, so the S0 gate keeps holding;
//   - TestMockCalibrationFitsTheDemoRoutes pins the mock calibration to the routes that adopt it.
//
// cmd/proxy's scenario tests then run the same pack through the whole gateway with trafficgen.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

// refScenario is a scenarios.v1 scenario with every member the reference replay reads.
type refScenario struct {
	ID           string                         `json:"id"`
	Sets         []string                       `json:"sets"`
	Route        string                         `json:"route"`
	APIFormat    string                         `json:"api_format"`
	Stream       bool                           `json:"stream"`
	AnswerSource string                         `json:"answer_source"`
	Headers      map[string]string              `json:"headers"`
	Request      json.RawMessage                `json:"request"`
	Response     *string                        `json:"response"`
	MockAnswers  map[string]*decision.RawAnswer `json:"mock_answers"`
	Knowledge    []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"knowledge"`
	Fallback *struct {
		Response    string                         `json:"response"`
		MockAnswers map[string]*decision.RawAnswer `json:"mock_answers"`
		Expect      struct {
			WouldAction string   `json:"would_action"`
			Verdict     *string  `json:"verdict"`
			MaxRisk     *float64 `json:"max_risk"`
			Reasons     []string `json:"reasons"`
		} `json:"expect"`
	} `json:"fallback"`
	Judge *struct {
		Claims []struct {
			Verdict    string  `json:"verdict"`
			Confidence float64 `json:"confidence"`
		} `json:"claims"`
	} `json:"judge"`
	Expect    refExpect                  `json:"expect"`
	ExpectRaw map[string]json.RawMessage `json:"-"`
}

type refExpect struct {
	Mode           string   `json:"mode"`
	Action         string   `json:"action"`
	WouldAction    *string  `json:"would_action"`
	Status         string   `json:"status"`
	Verdict        *string  `json:"verdict"`
	MaxRisk        *float64 `json:"max_risk"`
	Reasons        []string `json:"reasons"`
	CheckedAgainst string   `json:"checked_against"`
	DeepAudit      string   `json:"deep_audit"`
	Audit          string   `json:"audit"`
	AuditVerdict   *string  `json:"audit_verdict"`
	Outcome        string   `json:"outcome"`
	DeliveredText  *string  `json:"delivered_text"`
	HTTPStatus     *int     `json:"http_status"`
	StreamDecision *struct {
		Comment  bool     `json:"comment"`
		Trailers []string `json:"trailers"`
	} `json:"stream_decision"`
}

// coreExpectFields are the expect members every scenario states.
var coreExpectFields = []string{"mode", "action", "would_action", "status", "verdict", "max_risk", "reasons",
	"checked_against", "deep_audit", "audit", "audit_verdict", "outcome"}

func loadRefScenarios(t *testing.T) []*refScenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []json.RawMessage `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	out := make([]*refScenario, 0, len(pack.Scenarios))
	for _, raw := range pack.Scenarios {
		s := &refScenario{}
		if err := json.Unmarshal(raw, s); err != nil {
			t.Fatal(err)
		}
		var members struct {
			Expect map[string]json.RawMessage `json:"expect"`
		}
		_ = json.Unmarshal(raw, &members)
		s.ExpectRaw = members.Expect
		if s.APIFormat == "" {
			s.APIFormat = llmapi.FormatOpenAIChat
		}
		out = append(out, s)
	}
	return out
}

// repoFile reads a file relative to the repository root (the directory holding VERSION).
func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			b, err := os.ReadFile(filepath.Join(dir, rel)) // #nosec G304 -- test fixture under the repository root
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no repository root above the working directory (looking for %s)", rel)
		}
		dir = parent
	}
}

// demoPolicy is the demo policy: config/policies.yaml with the routes of
// config/policies.pending.yaml appended, as validate.mjs reads them.
type demoPolicy struct {
	*policy.Policy
	// pending names the routes that come from config/policies.pending.yaml.
	pending map[string]bool
}

// loadDemoPolicy parses the demo policy with this build's loader. The loader refuses block and
// fallback modes (P3) and the knowledge source (add-on), so those are swapped for flag and dropped
// before parsing and put back on the parsed routes: every other setting goes through the real
// merge, defaults and validation.
func loadDemoPolicy(t *testing.T, reg policy.Resolver) *demoPolicy {
	t.Helper()
	var base, pending map[string]any
	if err := yaml.Unmarshal(repoFile(t, "config/policies.yaml"), &base); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(repoFile(t, "config/policies.pending.yaml"), &pending); err != nil {
		t.Fatal(err)
	}
	for k := range pending {
		if k != "version" && k != "routes" {
			t.Fatalf("config/policies.pending.yaml: %s is not allowed (only version and routes)", k)
		}
	}
	dp := &demoPolicy{pending: map[string]bool{}}
	modes := map[string]string{}
	sources := map[string][]string{}
	routes, _ := base["routes"].([]any)
	pendingRoutes, _ := pending["routes"].([]any)
	for i, r := range append(slices.Clone(routes), pendingRoutes...) {
		m := r.(map[string]any)
		name := m["name"].(string)
		if i >= len(routes) {
			dp.pending[name] = true
		}
		if mode, _ := m["mode"].(string); mode == decision.ModeBlock || mode == decision.ModeFallback {
			modes[name] = mode
			m["mode"] = decision.ModeFlag
		}
		if ev, ok := m["evidence"].(map[string]any); ok {
			if srcs, ok := ev["sources"].([]any); ok && slices.Contains(srcs, any(evidence.SourceKnowledge)) {
				var all, kept []any
				for _, s := range srcs {
					all = append(all, s)
					if s != evidence.SourceKnowledge {
						kept = append(kept, s)
					}
				}
				for _, s := range all {
					sources[name] = append(sources[name], s.(string))
				}
				ev["sources"] = kept
			}
		}
	}
	base["routes"] = append(routes, pendingRoutes...)
	doc, err := yaml.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse(doc, policy.DefaultSeeds(), reg)
	if err != nil {
		t.Fatalf("demo policy: %v", err)
	}
	for _, r := range p.Routes {
		if m, ok := modes[r.Name]; ok {
			r.Mode = m
		}
		if s, ok := sources[r.Name]; ok {
			r.Evidence.Sources = s
		}
	}
	dp.Policy = p
	return dp
}

// mockCalibration is the calibration the gateway looks up for the mock snapshot (SPEC §2.11): the
// entries of config/calibration/mock__jev-heuristic-v1.json by target key.
func mockCalibration(t *testing.T) map[string]decision.CalibrationEntry {
	t.Helper()
	var f struct {
		DecisionModel struct {
			Backend, Snapshot string
		} `json:"decision_model"`
		Entries []struct {
			Target struct {
				EvaluatorID      string  `json:"evaluator_id"`
				EvaluatorVersion int     `json:"evaluator_version"`
				CalibrationKey   string  `json:"calibration_key"`
				ParamsSHA256     *string `json:"params_sha256"`
			} `json:"target"`
			Table   []float64 `json:"table"`
			Quality string    `json:"quality"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(repoFile(t, "config/calibration/mock__jev-heuristic-v1.json"), &f); err != nil {
		t.Fatal(err)
	}
	if f.DecisionModel.Snapshot != jev.DefaultMockModel || f.DecisionModel.Backend != jev.BackendMock {
		t.Fatalf("mock calibration is for %s/%s, want the mock backend's %s", f.DecisionModel.Backend, f.DecisionModel.Snapshot, jev.DefaultMockModel)
	}
	out := map[string]decision.CalibrationEntry{}
	for _, e := range f.Entries {
		params := "-"
		if e.Target.ParamsSHA256 != nil {
			params = (*e.Target.ParamsSHA256)[:16]
		}
		key := fmt.Sprintf("%s@%d#%s#%s", e.Target.EvaluatorID, e.Target.EvaluatorVersion, e.Target.CalibrationKey[:16], params)
		out[key] = decision.CalibrationEntry{Table: e.Table, Quality: e.Quality, State: e.Quality}
	}
	return out
}

// calibrationFor looks up the calibration of each evaluator a route attaches, by target key
// (calibration_key and the resolved params hash).
func calibrationFor(t *testing.T, route *policy.Route, specs map[string]*decision.Spec, cal map[string]decision.CalibrationEntry) map[string]decision.CalibrationEntry {
	t.Helper()
	out := map[string]decision.CalibrationEntry{}
	for _, e := range route.Evaluators {
		spec := specs[e.Ref]
		h, err := spec.Hashes()
		if err != nil {
			t.Fatal(err)
		}
		params := "-"
		if len(spec.Params) > 0 {
			resolved := map[string]string{}
			for name, p := range spec.Params {
				resolved[name] = p.Default
				if v, ok := e.Params[name]; ok {
					resolved[name] = v
				}
			}
			sum, err := decision.SHA256Hex(resolved)
			if err != nil {
				t.Fatal(err)
			}
			params = sum[:16]
		}
		if c, ok := cal[fmt.Sprintf("%s#%s#%s", e.Ref, h.CalibrationKey[:16], params)]; ok {
			out[e.Ref] = c
		}
	}
	return out
}

// chatRequest parses a scenario request the way the gateway reads its format (SPEC §2.10):
// OpenAI chat as is; Anthropic Messages with the top-level system as a system message (text
// blocks joined with a newline) and text blocks as parts; completions with the prompt as the only
// user message (it is the user request and is never scanned for blocks).
func chatRequest(t *testing.T, s *refScenario) *llmapi.ChatRequest {
	t.Helper()
	req, err := llmapi.ParseChatRequest(s.Request)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(s.Request, &body); err != nil {
		t.Fatal(err)
	}
	switch s.APIFormat {
	case llmapi.FormatAnthropicMessages:
		var msgs []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(body["messages"], &msgs); err != nil {
			t.Fatal(err)
		}
		req.Messages = nil
		if sys := blockTexts(t, s.ID, body["system"]); len(sys) > 0 {
			req.Messages = append(req.Messages, llmapi.Message{Role: "system", Parts: sys})
		}
		for _, m := range msgs {
			req.Messages = append(req.Messages, llmapi.Message{Role: m.Role, Parts: blockTexts(t, s.ID, m.Content)})
		}
	case llmapi.FormatOpenAICompletions:
		var prompt string
		if err := json.Unmarshal(body["prompt"], &prompt); err != nil {
			t.Fatalf("%s: a completions scenario needs a string prompt: %v", s.ID, err)
		}
		req.Messages = []llmapi.Message{{Role: "user", Parts: []string{prompt}}}
	}
	return req
}

// blockTexts reads an Anthropic content or system value: a string, or text blocks. Other block
// types (tool_use, tool_result) are not needed by any scenario yet and fail the test, so a new
// scenario that uses them extends this reader first.
func blockTexts(t *testing.T, id string, raw json.RawMessage) []string {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("%s: unreadable Anthropic content %s", id, raw)
	}
	var out []string
	for _, b := range blocks {
		if b.Type != "text" {
			t.Fatalf("%s: Anthropic %s blocks are not supported by the reference replay yet", id, b.Type)
		}
		out = append(out, b.Text)
	}
	return out
}

// reference is the outcome of one scenario under the reference pipeline.
type reference struct {
	route      *policy.Route
	core       *decision.Core
	status     string // X-TrustAgent-Status
	checked    string // X-TrustAgent-Checked-Against
	audit      string // X-TrustAgent-Audit (the deep-audit plan)
	delivered  string
	regate     *decision.Core
	evidence   *evidence.Result
	knowledge  bool // knowledge-only evidence
	httpStatus int
}

// runReference replays a scenario through the SPEC pipeline, the way internal/gate runs it plus
// the P3 parts (stream meta, calibration lookup, knowledge, block and fallback delivery).
func runReference(t *testing.T, s *refScenario, dp *demoPolicy, specs map[string]*decision.Spec, cal map[string]decision.CalibrationEntry) *reference {
	t.Helper()
	req := chatRequest(t, s)
	h := http.Header{}
	for k, v := range s.Headers {
		h.Set(k, v)
	}
	route := dp.Match(&policy.Request{Path: apiPaths[s.APIFormat], Model: req.Model, Headers: h, BodyRoute: req.Extension.Route})
	if route.Name != s.Route {
		t.Fatalf("the request lands on route %s, not %s", route.Name, s.Route)
	}
	ref := &reference{route: route, httpStatus: http.StatusOK}

	settings := route.Evidence.Extraction()
	ev, err := evidence.Extract(req, &settings)
	if err != nil {
		t.Fatal(err)
	}
	if s.APIFormat == llmapi.FormatOpenAICompletions && slices.ContainsFunc(ev.Items, func(it evidence.Item) bool { return it.Source == evidence.SourceRequestContext }) {
		t.Fatalf("a completions prompt is never scanned for blocks, but the replay found request_context items")
	}
	// Knowledge (when: fallback): the scenario's documents are what RetrieveContext returns when
	// the request sources gave no trusted evidence.
	if slices.Contains(route.Evidence.Sources, evidence.SourceKnowledge) {
		if len(s.Knowledge) == 0 {
			t.Fatalf("route %s reads the knowledge add-on, but the scenario lists no knowledge documents", route.Name)
		}
		if route.Evidence.Knowledge.When != "fallback" || !ev.Trusted() {
			for _, k := range s.Knowledge {
				ev.Items = append(ev.Items, evidence.Item{ID: k.ID, Source: evidence.SourceKnowledge, Label: "knowledge base", Text: k.Text})
				ev.PreDedupItems++
			}
			ref.knowledge = !slices.ContainsFunc(ev.Items, func(it evidence.Item) bool { return !it.Untrusted && it.Source != evidence.SourceKnowledge })
		}
	} else if len(s.Knowledge) > 0 {
		t.Fatalf("knowledge documents on route %s, which does not read the knowledge add-on", route.Name)
	}
	ref.evidence = ev
	if s.Response == nil {
		t.Fatal("the scenario has no response")
	}
	stream := "none"
	if s.Stream {
		stream = route.Streaming
	}

	decide := func(text string, answers map[string]*decision.RawAnswer) (*decision.Core, *decision.Compiled) {
		rules := resolveRules(route, dp.Overrides.ClientRules, req.Extension.Rules, specs)
		dets, err := decision.Detect(&decision.DetectorInput{
			Response: text, Evidence: ev.DecisionItems(), UserRequest: ev.UserRequest, SystemInstructions: ev.SystemInstructions,
			Rules: rules, PreDedupItems: ev.PreDedupItems, MaxMarker: ev.MaxMarker, MinInteger: route.Detectors.MinInteger,
		})
		if err != nil {
			t.Fatal(err)
		}
		cr := &decision.CompileRoute{OnNoEvidence: route.OnNoEvidence, EvidenceSources: route.Evidence.Sources, ClientRules: dp.Overrides.ClientRules, Model: jev.DefaultMockModel}
		th := map[string]decision.Thresholds{}
		for _, e := range route.Evaluators {
			cr.Evaluators = append(cr.Evaluators, decision.RouteEvaluator{Ref: e.Ref, Stage: e.StageOrDefault(), Enabled: e.Enabled, Params: e.Params})
			th[e.Ref] = e.Thresholds.Decision()
		}
		b := decision.DefaultBudget()
		b.MaxResponseTokens, b.EvidenceMaxTokens = route.Decision.MaxResponseTokens, route.Evidence.MaxTokens
		compiled, err := decision.Compile(cr, specs, &decision.CompileInputs{
			UserRequest: ev.UserRequest, AssistantResponse: text, SystemInstructions: ev.SystemInstructions,
			Evidence: ev.DecisionItems(), Rules: req.Extension.Rules,
		}, b)
		if err != nil {
			t.Fatal(err)
		}
		evs, err := compiled.DecideEvaluators(specs, th)
		if err != nil {
			t.Fatal(err)
		}
		in := &decision.Input{
			Policy: decision.Policy{
				ConfiguredMode: route.Mode, Fail: route.Fail, RequireCalibration: route.RequireCalibration, OnNoEvidence: route.OnNoEvidence,
				Detectors: decision.DetectorPolicy{FlagOn: route.Detectors.FlagOn, BlockOn: route.Detectors.BlockOn},
			},
			Evaluators: evs, QuestionMap: compiled.QuestionMap, Excluded: compiled.Excluded,
			Calibration: calibrationFor(t, route, specs, cal),
			Meta: decision.Meta{
				Backend: jev.BackendMock, Stream: stream, EvidencePresent: ev.Trusted(), EvidenceTruncated: compiled.Truncated,
				KnowledgeOnly: ref.knowledge, RequestStatus: map[string]string{}, QIDRequest: compiled.QIDRequest,
			},
			Detectors: decision.DetectorStates(dets),
		}
		// The mock backend answers every asked question from the script; a question it does not
		// script would get a hash answer (or fail in strict mode), so the script must be exact.
		asked := map[string]bool{}
		for k, r := range compiled.Requests {
			in.Meta.RequestStatus[fmt.Sprint(k)] = decision.RequestOK
			for qid := range r.Questions {
				asked[qid] = true
			}
		}
		for qid := range asked {
			if answers[qid] == nil {
				t.Errorf("%s is asked (compile sends it), but the script has no answer for it", qid)
			}
		}
		for qid := range answers {
			if !asked[qid] {
				t.Errorf("the script answers %s, which compile does not ask", qid)
			}
		}
		if len(asked) > 0 {
			in.Answers = answers
		}
		core, err := decision.Decide(in)
		if err != nil {
			t.Fatal(err)
		}
		return core, compiled
	}

	core, compiled := decide(*s.Response, s.MockAnswers)
	ref.core = core
	ref.status = core.Status
	if route.Mode == decision.ModeObserve {
		ref.status = "deferred"
	}
	ref.checked = evidence.Summarize(ev, route.Evidence.Sources, compiled.Truncated, decision.DefaultCPT).CheckedAgainst()
	ref.audit = auditPlan(t, route, core)

	ref.delivered = *s.Response
	blockText := func() string {
		if route.Block.Style == "error" {
			ref.httpStatus = route.Block.Status
			return ""
		}
		return route.Block.Message
	}
	switch core.Action {
	case decision.ActionBlock:
		ref.delivered = blockText()
	case decision.ActionFallback:
		if s.Fallback == nil {
			t.Fatal("the gate falls back, but the scenario has no fallback answer")
		}
		// SPEC §2.12 Fallback: re-gate the fallback answer (attempt 1), deliver it unless it would
		// block too, then apply on_fail.
		ref.regate, _ = decide(s.Fallback.Response, s.Fallback.MockAnswers)
		switch {
		case ref.regate.WouldAction != decision.ActionBlock:
			ref.delivered = s.Fallback.Response
		case route.Fallback.OnFail == "flag":
			ref.delivered = s.Fallback.Response
		case route.Fallback.OnFail == "original":
		default:
			ref.delivered = blockText()
		}
	default:
		if s.Fallback != nil {
			t.Error("the scenario has a fallback answer, but the gate does not fall back")
		}
	}
	return ref
}

// resolveRules resolves the rules params of a route's enabled evaluators (internal/gate).
func resolveRules(r *policy.Route, clientRules bool, reqRules map[string]string, specs map[string]*decision.Spec) map[string]string {
	out := map[string]string{}
	for _, e := range r.Evaluators {
		spec, ok := specs[e.Ref]
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

// auditPlan is the X-TrustAgent-Audit value of a decision (SPEC §2.9 caller rules). Demo routes
// must make it deterministic: no uniform draw, and sample rates of 0 or 1.
func auditPlan(t *testing.T, r *policy.Route, core *decision.Core) string {
	t.Helper()
	da := r.DeepAudit
	if !da.Enabled {
		return gate.AuditDisabled
	}
	if da.UniformSampleRate != 0 {
		t.Errorf("route %s draws uniform audit samples (%v): demo routes must set uniform_sample_rate 0", r.Name, da.UniformSampleRate)
	}
	rate := func(p float64) string {
		switch p {
		case 1:
			return gate.AuditQueued
		case 0:
			return gate.AuditSampledOut
		}
		t.Errorf("route %s samples audits at %v: demo routes must use 0 or 1", r.Name, p)
		return "random"
	}
	switch core.DeepAudit {
	case decision.DeepAuditHigh:
		return gate.AuditQueued
	case decision.DeepAuditSample:
		return rate(da.PassSampleRate)
	case decision.DeepAuditSampleError:
		return rate(da.ErrorSampleRate)
	}
	return gate.AuditNotAssessed
}

// auditVerdict is the verdict scoring v2 (SPEC §3.2, truthtable/scoring/aggregate.py) gives the
// judge fixture's claims, each with the LLM default distribution: P(verdict) = confidence, the rest
// split evenly. UNSUPPORTED means baseless (policy hallucination) when any evidence came with the
// request, and unverified (policy unverifiable) when it all came from the knowledge base.
func auditVerdict(claims []struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
}, hallucinationPolicy bool) string {
	const eps = 1e-9
	var sum, maxC, maxU float64
	n := 0
	for _, c := range claims {
		idx := map[string]int{"SUPPORTED": 0, "PARTIALLY_SUPPORTED": 1, "CONTRADICTED": 2, "UNSUPPORTED": 3}
		i, decided := idx[c.Verdict]
		if !decided {
			continue
		}
		p := [4]float64{}
		for k := range p {
			p[k] = (1 - c.Confidence) / 3
		}
		p[i] = c.Confidence
		cc, u := p[2], p[3]+0.5*p[1]
		n++
		sum += u
		maxC, maxU = math.Max(maxC, cc), math.Max(maxU, u)
	}
	switch {
	case len(claims) == 0:
		return store.VerdictNoClaims
	case n == 0:
		return store.VerdictNotAssessed
	case maxC >= 0.35-eps, hallucinationPolicy && (maxU >= 0.60-eps || sum/float64(n) >= 0.30-eps):
		return store.VerdictHallucinated
	case !hallucinationPolicy && 1-sum/float64(n) < 0.5-eps:
		return store.VerdictUnverifiable
	}
	return store.VerdictGrounded
}

func TestScenariosMatchTheReference(t *testing.T) {
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	specs := reg.Specs()
	dp := loadDemoPolicy(t, reg)
	cal := mockCalibration(t)
	for _, s := range loadRefScenarios(t) {
		t.Run(s.ID, func(t *testing.T) {
			for _, f := range coreExpectFields {
				if _, ok := s.ExpectRaw[f]; !ok {
					t.Errorf("expect.%s is missing (every scenario states the whole outcome)", f)
				}
			}
			ref := runReference(t, s, dp, specs, cal)
			x, c := s.Expect, ref.core
			eq := func(field, want, got string) {
				t.Helper()
				if want != got {
					t.Errorf("%s: expect %q, reference %q", field, want, got)
				}
			}
			eq("mode", x.Mode, c.EffectiveMode)
			eq("action", x.Action, c.Action)
			eq("would_action", deref(x.WouldAction), c.WouldAction)
			eq("status", x.Status, ref.status)
			eq("verdict", deref(x.Verdict), deref(c.Verdict))
			if (x.MaxRisk == nil) != (c.MaxRisk == nil) || x.MaxRisk != nil && math.Abs(*x.MaxRisk-*c.MaxRisk) > 1e-9 {
				t.Errorf("max_risk: expect %v, reference %v", fmtRisk(x.MaxRisk), fmtRisk(c.MaxRisk))
			}
			eq("reasons", strings.Join(x.Reasons, ","), strings.Join(c.Reasons, ","))
			eq("checked_against", x.CheckedAgainst, ref.checked)
			eq("deep_audit", x.DeepAudit, c.DeepAudit)
			eq("audit", x.Audit, ref.audit)
			eq("outcome", x.Outcome, store.Outcome(store.OutcomeFields{GateStatus: c.Status, Action: c.Action, WouldAction: c.WouldAction}))

			delivered := *s.Response
			if x.DeliveredText != nil {
				delivered = *x.DeliveredText
				if delivered == *s.Response {
					t.Error("expect.delivered_text equals response: leave it out")
				}
			}
			eq("delivered text", delivered, ref.delivered)
			status := http.StatusOK
			if x.HTTPStatus != nil {
				status = *x.HTTPStatus
			}
			if status != ref.httpStatus {
				t.Errorf("http_status: expect %d, reference %d", status, ref.httpStatus)
			}

			if ref.regate != nil {
				fx := s.Fallback.Expect
				eq("fallback would_action", fx.WouldAction, ref.regate.WouldAction)
				eq("fallback verdict", deref(fx.Verdict), deref(ref.regate.Verdict))
				if (fx.MaxRisk == nil) != (ref.regate.MaxRisk == nil) || fx.MaxRisk != nil && math.Abs(*fx.MaxRisk-*ref.regate.MaxRisk) > 1e-9 {
					t.Errorf("fallback max_risk: expect %v, reference %v", fmtRisk(fx.MaxRisk), fmtRisk(ref.regate.MaxRisk))
				}
				eq("fallback reasons", strings.Join(fx.Reasons, ","), strings.Join(ref.regate.Reasons, ","))
			}

			// Streams: a passthrough stream carries the decision in the comment and the trailers;
			// only streams state how.
			passthrough := s.Stream && ref.route.Streaming == policy.StreamingPassthrough
			switch {
			case !s.Stream && x.StreamDecision != nil:
				t.Error("expect.stream_decision on a scenario that does not stream")
			case passthrough && x.StreamDecision == nil:
				t.Error("a passthrough stream must state expect.stream_decision")
			case passthrough:
				wantComment := ref.route.StreamAnnotations != "none"
				if x.StreamDecision.Comment != wantComment {
					t.Errorf("stream_decision.comment %v, but the route's stream_annotations is %s", x.StreamDecision.Comment, ref.route.StreamAnnotations)
				}
				for _, h := range []string{headerMode, headerAction} {
					if !slices.Contains(x.StreamDecision.Trailers, h) {
						t.Errorf("stream_decision.trailers lacks %s (SPEC §2.12: Mode and Action are trailers on passthrough streams)", h)
					}
				}
			}

			// The deep audit: what the engine answers for the planned audit.
			var want string
			switch {
			case ref.audit != gate.AuditQueued:
				if ref.audit == gate.AuditNotAssessed {
					want = store.VerdictNotAssessed
				}
				if s.Judge != nil {
					t.Error("the judge is never called, so judge must be null")
				}
			case !ref.evidence.Trusted():
				// Queued without evidence: the engine's preflight answers NOT_ASSESSED, no LLM call.
				want = store.VerdictNotAssessed
				if s.Judge != nil {
					t.Error("an audit without evidence makes no LLM call, so judge must be null")
				}
			case s.Judge == nil:
				t.Error("the audit is queued with evidence, so the judge needs a fixture")
			default:
				want = auditVerdict(s.Judge.Claims, !ref.knowledge)
			}
			eq("audit_verdict", deref(x.AuditVerdict), want)
		})
	}
}

// TestRunnableScenariosThroughTheGate sends every scenario this build serves (no phase tag)
// through the real gate: the mock decision backend in strict scenario mode, the evaluator
// registry and the demo policy, as make demo runs them.
func TestRunnableScenariosThroughTheGate(t *testing.T) {
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	dp := loadDemoPolicy(t, reg)
	client, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario, Strict: true}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Jev: client, Evaluators: reg, Rand: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, s := range loadRefScenarios(t) {
		if scenarioPhase(s.Sets) != phaseS0 {
			continue
		}
		ran++
		t.Run(s.ID, func(t *testing.T) {
			if s.Stream || s.APIFormat != llmapi.FormatOpenAIChat || dp.pending[s.Route] {
				t.Fatal("a scenario without a phase tag must be a non-streaming chat request on a route in config/policies.yaml")
			}
			req, err := llmapi.ParseChatRequest(s.Request)
			if err != nil {
				t.Fatal(err)
			}
			h := http.Header{}
			for k, v := range s.Headers {
				h.Set(k, v)
			}
			route := dp.Match(&policy.Request{Path: apiPaths[s.APIFormat], Model: req.Model, Headers: h, BodyRoute: req.Extension.Route})
			settings := route.Evidence.Extraction()
			ev, err := evidence.Extract(req, &settings)
			if err != nil {
				t.Fatal(err)
			}
			x := &gate.Exchange{
				Trace:  &gate.TraceStart{TraceID: "trace-" + s.ID, Route: route.Name, Mode: route.Mode, Scenario: s.ID, Flags: []string{}},
				Policy: dp.Policy, Route: route, Request: req, Evidence: ev, Response: *s.Response,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res := g.Evaluate(ctx, x)
			d, e := res.Decision, s.Expect
			status := res.Status
			if route.Mode == decision.ModeObserve {
				status = "deferred" // the response goes out before this gate runs
			}
			got := fmt.Sprintf("mode=%s action=%s would=%s status=%s verdict=%s risk=%s reasons=%s checked=%s deep=%s audit=%s",
				d.EffectiveMode, d.Action, d.WouldAction, status, deref(d.Verdict), fmtRisk(d.MaxRisk), strings.Join(d.Reasons, ","),
				res.CheckedAgainst, d.DeepAudit, auditOf(res.Audit))
			want := fmt.Sprintf("mode=%s action=%s would=%s status=%s verdict=%s risk=%s reasons=%s checked=%s deep=%s audit=%s",
				e.Mode, e.Action, deref(e.WouldAction), e.Status, deref(e.Verdict), fmtRisk(e.MaxRisk), strings.Join(e.Reasons, ","),
				e.CheckedAgainst, e.DeepAudit, e.Audit)
			if got != want {
				t.Errorf("gate:\n got %s\nwant %s", got, want)
			}
		})
	}
	if ran < 15 {
		t.Errorf("only %d scenarios run on this build's gate", ran)
	}
}

// auditOf is the X-TrustAgent-Audit value of an audit plan.
func auditOf(p gate.AuditPlan) string {
	if p.Status != "" {
		return p.Status
	}
	return gate.AuditQueued
}

// fmtRisk formats a risk as X-TrustAgent-Risk does.
func fmtRisk(r *float64) string {
	if r == nil {
		return absent
	}
	return decision.FormatRisk(*r)
}

// TestMockCalibrationFitsTheDemoRoutes: every evaluator a demo route asks in calibrated threshold
// space has an entry in the mock calibration, keyed by its calibration key and params, and fitted
// with that route's question set and the evaluator's state fields (SPEC §2.11), so the gateway's
// lookup finds it for the mock snapshot.
func TestMockCalibrationFitsTheDemoRoutes(t *testing.T) {
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	specs := reg.Specs()
	dp := loadDemoPolicy(t, reg)
	cal := mockCalibration(t)
	var f struct {
		Entries []struct {
			Target struct {
				EvaluatorID      string `json:"evaluator_id"`
				EvaluatorVersion int    `json:"evaluator_version"`
			} `json:"target"`
			FitContext struct {
				StateFields       []string `json:"state_fields"`
				QuestionSetSHA256 string   `json:"question_set_sha256"`
			} `json:"fit_context"`
			Quality string `json:"quality"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(repoFile(t, "config/calibration/mock__jev-heuristic-v1.json"), &f); err != nil {
		t.Fatal(err)
	}
	adopted := 0
	for _, route := range append([]*policy.Route{dp.Default}, dp.Routes...) {
		cr := &decision.CompileRoute{OnNoEvidence: route.OnNoEvidence, EvidenceSources: route.Evidence.Sources, Model: jev.DefaultMockModel}
		var calibrated []string
		for _, e := range route.Evaluators {
			cr.Evaluators = append(cr.Evaluators, decision.RouteEvaluator{Ref: e.Ref, Stage: e.StageOrDefault(), Enabled: e.Enabled, Params: e.Params})
			if e.Thresholds != nil && e.Thresholds.Space == decision.SpaceCalibrated {
				calibrated = append(calibrated, e.Ref)
			}
		}
		if len(calibrated) == 0 {
			continue
		}
		adopted++
		compiled, err := decision.Compile(cr, specs, &decision.CompileInputs{
			UserRequest: "question", AssistantResponse: "answer",
			Evidence: []decision.EvidenceItem{{ID: "doc", Source: evidence.SourceRequestSystem, Text: strings.Repeat("Evidence text. ", 10)}},
		}, decision.DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		found := calibrationFor(t, route, specs, cal)
		for _, ref := range calibrated {
			if _, ok := found[ref]; !ok {
				t.Errorf("route %s adopts calibrated thresholds for %s, but the mock calibration has no entry for its key", route.Name, ref)
				continue
			}
			for _, e := range f.Entries {
				if fmt.Sprintf("%s@%d", e.Target.EvaluatorID, e.Target.EvaluatorVersion) != ref {
					continue
				}
				if e.Quality != "mock" {
					t.Errorf("%s: quality %s; the mock snapshot's calibration is quality mock", ref, e.Quality)
				}
				if e.FitContext.QuestionSetSHA256 != compiled.QuestionSetSHA256 {
					t.Errorf("%s: fit_context.question_set_sha256 %s, route %s asks %s", ref, e.FitContext.QuestionSetSHA256, route.Name, compiled.QuestionSetSHA256)
				}
				if want := specs[ref].StateFields; !slices.Equal(e.FitContext.StateFields, want) {
					t.Errorf("%s: fit_context.state_fields %v, the evaluator reads %v", ref, e.FitContext.StateFields, want)
				}
			}
		}
	}
	if adopted == 0 {
		t.Error("no demo route adopts the mock calibration")
	}
}
