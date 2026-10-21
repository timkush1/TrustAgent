package policy

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/policy/policytest"
	"github.com/truthtable/backend-go/internal/store"
)

const currentYAML = `version: 1
defaults:
  mode: observe
routes:
  - name: support-bot
    match: {headers: {X-TrustAgent-Route: support-bot}}
    mode: flag
    evaluators: [{ref: groundedness@1}, {ref: contradiction@1}]
`

var replayNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func noul(p float64) *decision.RawAnswer { return &decision.RawAnswer{Type: "noul", Noul: &p} }

// recordedTrace describes one past exchange: the route, the answers the decision model gave and
// what the request carried.
type recordedTrace struct {
	id       string
	route    string
	answers  map[string]*decision.RawAnswer // by ref
	evidence bool
	numbers  bool // the numbers detector fired
	skip     string
	stream   bool
	late     bool
	audit    string
	ago      time.Duration
}

// recordTrace runs decide() on a trace under the route the policy gives it, as the gate did, and
// returns the stored row.
func recordTrace(t *testing.T, p *Policy, rt recordedTrace) store.ReplayRow {
	t.Helper()
	specs := registry(t)
	route, ok := p.Route(rt.route)
	if !ok {
		route = p.Default
	}
	in := &decision.Input{
		Policy: decision.Policy{ConfiguredMode: route.Mode, Fail: route.Fail, RequireCalibration: route.RequireCalibration,
			OnNoEvidence: route.OnNoEvidence, Detectors: decision.DetectorPolicy{FlagOn: route.Detectors.FlagOn, BlockOn: route.Detectors.BlockOn}},
		Answers: map[string]*decision.RawAnswer{}, QuestionMap: map[string]string{}, Excluded: []decision.Exclusion{},
		Calibration: map[string]decision.CalibrationEntry{},
		Meta: decision.Meta{Backend: "mock", Stream: "none", EvidencePresent: rt.evidence, RequestStatus: map[string]string{"0": "ok"},
			QIDRequest: map[string]int{}},
		Detectors: map[string]decision.DetectorState{},
	}
	for _, id := range decision.DetectorIDs {
		ran := rt.evidence
		in.Detectors[id] = decision.DetectorState{Ran: ran, Fired: ran && id == decision.DetNumbers && rt.numbers}
	}
	if rt.skip != "" {
		in.Meta.SkipReason = &rt.skip
	}
	for _, e := range route.Evaluators {
		spec, _ := specs.Get(e.Ref)
		in.Evaluators = append(in.Evaluators, decision.EvaluatorInput{Ref: e.Ref, Spec: spec, Stage: e.StageOrDefault(), Thresholds: e.Thresholds.Decision()})
		if spec.Evidence.Requires == decision.EvidenceRequired && !rt.evidence {
			in.Excluded = append(in.Excluded, decision.Exclusion{Ref: e.Ref, Reason: decision.ExcludedNoEvidence})
			continue
		}
		in.QuestionMap[spec.QuestionID()] = e.Ref
		in.Meta.QIDRequest[spec.QuestionID()] = 0
		in.Answers[spec.QuestionID()] = rt.answers[e.Ref]
	}
	core, err := decision.Decide(in)
	if err != nil {
		t.Fatal(err)
	}
	var dets []decision.DetectorResult
	for _, id := range decision.DetectorIDs {
		st := in.Detectors[id]
		dets = append(dets, decision.DetectorResult{ID: id, Ran: st.Ran, Fired: st.Fired, Findings: []decision.DetectorFinding{}})
	}
	sum := evidence.Summary{Configured: route.Evidence.Sources, Used: []evidence.SourceCount{}, Empty: []string{}, Knowledge: "not_requested"}
	if rt.evidence {
		sum.Used = append(sum.Used, evidence.SourceCount{Source: evidence.SourceRequestSystem, Items: 1, Labels: []string{"doc"}})
	}
	doc := map[string]any{
		"spec": "decision/v1", "trace_id": rt.id, "status": core.Status, "skip_reason": core.SkipReason, "action": core.Action,
		"evaluators": core.Evaluators, "detectors": dets, "excluded": in.Excluded, "evidence_summary": sum,
		"decision_model": map[string]any{"backend": "mock", "snapshot": "mock/jev-heuristic-v1"},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	v := p.Version
	row := store.ReplayRow{TraceID: rt.id, CreatedAt: replayNow.Add(-rt.ago), Route: route.Name, PolicyVersion: &v, Stream: rt.stream,
		Source: "proxy", Late: rt.late, Decision: b}
	if rt.audit != "" {
		row.AuditVerdict = &rt.audit
	}
	return row
}

// history is a recorded window on the current policy.
func history(t *testing.T, cur *Policy) *policytest.Store {
	t.Helper()
	g, c := "groundedness@1", "contradiction@1"
	traces := []recordedTrace{
		{id: "t1", route: "support-bot", answers: map[string]*decision.RawAnswer{g: noul(0.97), c: noul(0.98)}, evidence: true, numbers: true, audit: "HALLUCINATED", ago: time.Minute},
		{id: "t2", route: "support-bot", answers: map[string]*decision.RawAnswer{g: noul(0.85), c: noul(0.2)}, evidence: true, numbers: true, ago: 2 * time.Minute},
		{id: "t3", route: "support-bot", answers: map[string]*decision.RawAnswer{g: noul(0.1), c: noul(0.05)}, evidence: true, audit: "GROUNDED", ago: 3 * time.Minute},
		{id: "t4", route: "support-bot", answers: nil, evidence: false, ago: 4 * time.Minute},
		{id: "t5", route: "default", skip: decision.SkipSampledOut, evidence: true, ago: 5 * time.Minute},
		{id: "t6", route: "default", answers: map[string]*decision.RawAnswer{g: noul(0.97), c: noul(0.98),
			"refusal@1":         {Type: "choice", Probabilities: map[string]float64{"answers_request": 1}},
			"pii_secret_leak@1": {Type: "choice", Probabilities: map[string]float64{"no_sensitive_data": 1}}}, evidence: true, stream: true, late: true, ago: 6 * time.Minute},
		{id: "old", route: "support-bot", answers: map[string]*decision.RawAnswer{g: noul(0.97), c: noul(0.98)}, evidence: true, ago: 48 * time.Hour},
	}
	s := policytest.New()
	for _, tr := range traces {
		s.Rows = append(s.Rows, recordTrace(t, cur, tr))
	}
	resp := "Refunds within 60 days."
	s.Bodies["t1"] = &store.BodyRecord{TraceID: "t1", ResponseText: &resp, Evidence: json.RawMessage(`[{"id":"doc","source":"request_system","label":"doc","text":"Refunds within 30 days.","truncated":false}]`),
		ExpiresAt: replayNow.Add(time.Hour)}
	return s
}

func preview(t *testing.T, cur, prop *Policy, src ReplaySource, mut ...func(*DryRun)) *DryRunResult {
	t.Helper()
	d := &DryRun{Current: cur, Proposed: prop, Specs: registry(t), Window: "24h", Now: replayNow}
	for _, m := range mut {
		m(d)
	}
	res, err := d.Run(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func counts(pairs ...any) ActionCounts {
	c := newCounts()
	for i := 0; i < len(pairs); i += 2 {
		c[pairs[i].(string)] = pairs[i+1].(int)
	}
	return c
}

func skipsOf(res *DryRunResult) map[string]int {
	out := map[string]int{}
	for _, s := range res.Skipped {
		out[s.Reason] = s.Count
	}
	return out
}

func TestDryRunSamePolicyChangesNothing(t *testing.T) {
	cur := mustCheck(t, currentYAML, env(t))
	src := history(t, cur)
	res := preview(t, cur, cur, src)
	// t5 was sampled out (no answers); "old" is outside the window.
	if res.Evaluated != 5 || res.Changed != 0 || !reflect.DeepEqual(res.ByActionCurrent, res.ByActionProposed) || !res.ReplayOnly {
		t.Fatalf("result %+v", res)
	}
	if want := counts("flag", 2, "pass", 2, "observe", 1); !reflect.DeepEqual(res.ByActionCurrent, want) {
		t.Errorf("current %v, want %v (what was recorded)", res.ByActionCurrent, want)
	}
	if !reflect.DeepEqual(skipsOf(res), map[string]int{SkipNoAnswers: 1}) {
		t.Errorf("skipped %+v", res.Skipped)
	}
	if len(res.ByRoute) != 2 || res.ByRoute[0].Route != "default" || res.ByRoute[1].Route != "support-bot" || res.ByRoute[1].Current["flag"] != 2 {
		t.Errorf("by route %+v", res.ByRoute)
	}
	joined := strings.Join(res.Caveats, "\n")
	for _, want := range []string{"no model calls", "1 traces were skipped because they have no stored answers", "1 of the replayed decisions arrived after their deadline"} {
		if !strings.Contains(joined, want) {
			t.Errorf("caveats lack %q:\n%s", want, joined)
		}
	}
	// Determinism: the same inputs give the same bytes.
	a, _ := json.Marshal(res)
	b, _ := json.Marshal(preview(t, cur, cur, src))
	if string(a) != string(b) {
		t.Errorf("two runs differ:\n%s\n%s", a, b)
	}
	// Every GateAction key is present, as the wire type requires.
	var wire map[string]any
	_ = json.Unmarshal(a, &wire)
	if len(wire["by_action_current"].(map[string]any)) != 5 || wire["replay_only"] != true {
		t.Errorf("wire %s", a)
	}
}

func TestDryRunThresholdAndModeChanges(t *testing.T) {
	cur := mustCheck(t, currentYAML, env(t))
	src := history(t, cur)

	// A higher groundedness warn_at: t2 (0.85) is no longer flagged.
	quieter := mustCheck(t, strings.Replace(currentYAML, "{ref: groundedness@1}", "{ref: groundedness@1, thresholds: {warn_at: 0.9}}", 1), env(t))
	res := preview(t, cur, quieter, src)
	if res.Changed != 1 || res.NoLongerFlagged != 1 || res.NewlyFlagged != 0 || len(res.Examples) != 1 ||
		res.Examples[0] != (DryRunExample{TraceID: "t2", Current: "flag", Proposed: "pass"}) {
		t.Errorf("quieter %+v", res)
	}

	// Block with buffering and no calibration requirement: t1 (both critical, corroborated) blocks.
	block := mustCheck(t, strings.Replace(currentYAML, "mode: flag", "mode: block\n    streaming: buffer\n    require_calibration: false", 1), enforcingEnv(t))
	res = preview(t, cur, block, src)
	if res.NewlyBlocked != 1 || res.ByActionProposed["block"] != 1 || res.DeepAuditAgreement != (AuditAgreement{WouldBlockHallucinated: 1}) {
		t.Errorf("block %+v", res)
	}
	// With the calibration requirement the block is capped to flag: nothing changes.
	capped := mustCheck(t, strings.Replace(currentYAML, "mode: flag", "mode: block\n    streaming: buffer", 1), enforcingEnv(t))
	if res := preview(t, cur, capped, src); res.Changed != 0 {
		t.Errorf("uncalibrated block changed %d traces", res.Changed)
	}
	// A measured calibration for the snapshot lifts the cap when the route adopts calibrated space.
	calibrated := mustCheck(t, strings.Replace(capped.YAML, "{ref: groundedness@1}, {ref: contradiction@1}",
		"{ref: groundedness@1, thresholds: {space: calibrated}}, {ref: contradiction@1, thresholds: {space: calibrated}}", 1), enforcingEnv(t))
	cal := fakeCalibrations{"groundedness@1": measured(), "contradiction@1": measured()}
	if res := preview(t, cur, calibrated, src, func(d *DryRun) { d.Calibrations = cal }); res.NewlyBlocked != 1 {
		t.Errorf("calibrated block %+v", res)
	}

	// Observe everywhere: flags become observes.
	observe := mustCheck(t, strings.Replace(currentYAML, "mode: flag", "mode: observe", 1), env(t))
	res = preview(t, cur, observe, src)
	if res.NoLongerFlagged != 2 || res.ByActionProposed["observe"] != 5 {
		t.Errorf("observe %+v", res)
	}

	// The streamed trace t6 would block, but on a passthrough block route it is only flagged.
	const defBlock = "version: 1\ndefaults: {mode: block, require_calibration: false}\nroutes:\n  - {name: support-bot, match: {}, mode: flag, evaluators: [{ref: groundedness@1}, {ref: contradiction@1}]}\n"
	res = preview(t, cur, mustCheck(t, defBlock, enforcingEnv(t)), src)
	if res.ByRoute[0].Route != DefaultRouteName || res.ByRoute[0].Proposed["flag"] != 1 || res.ByRoute[0].Current["observe"] != 1 {
		t.Errorf("passthrough default route %+v", res.ByRoute[0])
	}
	buffered := mustCheck(t, strings.Replace(defBlock, "mode: block,", "mode: block, streaming: buffer,", 1), enforcingEnv(t))
	if res := preview(t, cur, buffered, src); res.ByRoute[0].Proposed["block"] != 1 || res.DeepAuditAgreement.WouldBlockUnaudited != 1 {
		t.Errorf("buffered default route %+v %+v", res.ByRoute[0], res.DeepAuditAgreement)
	}
}

func TestDryRunSkips(t *testing.T) {
	cur := mustCheck(t, currentYAML, env(t))
	src := history(t, cur)

	// A new active evaluator was never asked: its traces are skipped (the one without evidence
	// too, since commitments needs no evidence).
	more := mustCheck(t, strings.Replace(currentYAML, "{ref: contradiction@1}]", "{ref: contradiction@1}, {ref: commitments@1}]", 1), env(t))
	res := preview(t, cur, more, src)
	if !reflect.DeepEqual(skipsOf(res), map[string]int{SkipNotInHistory: 4, SkipNoAnswers: 1}) || res.Evaluated != 1 {
		t.Errorf("new evaluator %+v %+v", res.Skipped, res)
	}
	if !strings.Contains(strings.Join(res.Caveats, "\n"), "4 traces were skipped because the proposal asks evaluators") {
		t.Errorf("caveats %v", res.Caveats)
	}
	// A new required evaluator on a trace without evidence would have been excluded: it replays.
	onlyGrounded := mustCheck(t, strings.Replace(currentYAML, "[{ref: groundedness@1}, {ref: contradiction@1}]", "[{ref: contradiction@1}]", 1), env(t))
	withBoth := mustCheck(t, currentYAML, env(t))
	src2 := policytest.New()
	src2.Rows = []store.ReplayRow{recordTrace(t, onlyGrounded, recordedTrace{id: "n", route: "support-bot", evidence: false, ago: time.Minute})}
	if res := preview(t, onlyGrounded, withBoth, src2); res.Evaluated != 1 || len(res.Skipped) != 0 {
		t.Errorf("no-evidence exclusion %+v", res)
	}
	// A new shadow evaluator is left out of the replay: it never changes an action.
	shadow := mustCheck(t, strings.Replace(currentYAML, "{ref: contradiction@1}]", "{ref: contradiction@1}, {ref: off_brand_tone@1, stage: shadow}]", 1), env(t))
	if res := preview(t, cur, shadow, src); res.Evaluated != 5 || res.Changed != 0 {
		t.Errorf("shadow %+v", res)
	}
	// on_no_evidence needs factual_claims answers the recording does not have.
	onNoEv := mustCheck(t, strings.Replace(currentYAML, "mode: flag", "mode: flag\n    on_no_evidence: flag", 1), env(t))
	if res := preview(t, cur, onNoEv, src); skipsOf(res)[SkipNotInHistory] != 1 {
		t.Errorf("on_no_evidence %+v", res.Skipped)
	}
	// A different min_integer re-runs the numbers detector from the body; traces without a
	// body are skipped.
	minInt := mustCheck(t, strings.Replace(currentYAML, "mode: flag", "mode: flag\n    detectors: {min_integer: 100, flag_on: [numbers_not_in_evidence]}", 1), env(t))
	res = preview(t, cur, minInt, src)
	if skipsOf(res)[SkipBodyExpired] != 2 || res.Evaluated != 3 {
		t.Errorf("min_integer %+v %+v", res.Skipped, res)
	}
	// Routes the proposal does not have fall back to its default route.
	gone := mustCheck(t, "version: 1\ndefaults: {mode: observe}\n", env(t))
	res = preview(t, cur, gone, src)
	if res.ByActionProposed["observe"] != 1 || !strings.Contains(strings.Join(res.Caveats, "\n"), "default route") {
		t.Errorf("route removed %+v", res)
	}
	// The limit caps the rows read.
	if res := preview(t, cur, cur, src, func(d *DryRun) { d.Limit = 2 }); res.Evaluated != 2 || !strings.Contains(strings.Join(res.Caveats, "\n"), "Only the newest 2") {
		t.Errorf("limit %+v", res)
	}
	if _, err := (&DryRun{Current: cur, Proposed: cur, Specs: registry(t), Window: "2d"}).Run(context.Background(), src); err == nil {
		t.Error("unknown window")
	}
}

// TestDryRunParamsChange: answers depend on the question state, so a changed rules param makes a
// recorded answer unusable.
func TestDryRunParamsChange(t *testing.T) {
	withParams := func(v string) *Policy {
		return mustCheck(t, strings.Replace(currentYAML, "{ref: contradiction@1}]",
			`{ref: contradiction@1}, {ref: commitments@1, params: {allowed_commitments: "`+v+`"}}]`, 1), env(t))
	}
	recorded := withParams("Refunds within 30 days.")
	src := policytest.New()
	src.Rows = []store.ReplayRow{recordTrace(t, recorded, recordedTrace{id: "p", route: "support-bot", evidence: true, ago: time.Minute,
		answers: map[string]*decision.RawAnswer{"groundedness@1": noul(0.1), "contradiction@1": noul(0.1),
			"commitments@1": {Type: "choice", Probabilities: map[string]float64{"no_commitment": 1}}}})}
	byVersion := func(context.Context, int64) *Policy { return recorded }
	if res := preview(t, recorded, withParams("Refunds within 30 days."), src, func(d *DryRun) { d.Recorded = byVersion }); res.Evaluated != 1 {
		t.Errorf("same params %+v", res)
	}
	res := preview(t, recorded, withParams("No refunds."), src, func(d *DryRun) { d.Recorded = byVersion })
	if res.Evaluated != 0 || skipsOf(res)[SkipNotInHistory] != 1 {
		t.Errorf("changed params %+v", res)
	}
	// Evidence settings changes are replayed with a caveat.
	ev := mustCheck(t, strings.Replace(recorded.YAML, "mode: flag", "mode: flag\n    evidence: {sources: [extension]}", 1), env(t))
	res = preview(t, recorded, ev, src, func(d *DryRun) { d.Recorded = byVersion })
	if res.Evaluated != 1 || !slices.ContainsFunc(res.Caveats, func(c string) bool { return strings.Contains(c, "route(s) support-bot") }) {
		t.Errorf("evidence change %+v", res)
	}
	// An unknown recorded version is assumed unchanged, with a caveat.
	res = preview(t, recorded, recorded, src, func(d *DryRun) { d.Recorded = func(context.Context, int64) *Policy { return nil } })
	if !slices.ContainsFunc(res.Caveats, func(c string) bool { return strings.Contains(c, "no longer stored") }) {
		t.Errorf("unknown version caveats %v", res.Caveats)
	}
}

func TestDryRunErrorsAndSkippedDecisions(t *testing.T) {
	cur := mustCheck(t, currentYAML, env(t))
	src := policytest.New()
	timeout := recordTrace(t, cur, recordedTrace{id: "to", route: "support-bot", evidence: true, ago: time.Minute})
	// Rewrite the recording as a timed-out call.
	var doc map[string]any
	_ = json.Unmarshal(timeout.Decision, &doc)
	for _, v := range doc["evaluators"].(map[string]any) {
		ev := v.(map[string]any)
		ev["status"], ev["reason"], ev["answer"] = "error", "timeout", nil
	}
	doc["status"] = "timeout"
	timeout.Decision, _ = json.Marshal(doc)
	budget := recordTrace(t, cur, recordedTrace{id: "bu", route: "support-bot", evidence: true, skip: "budget", ago: 2 * time.Minute})
	src.Rows = []store.ReplayRow{timeout, budget, {TraceID: "junk", CreatedAt: replayNow, Route: "support-bot", Decision: json.RawMessage(`{}`)}}

	// Fail closed on an enforcing route blocks what could not be checked.
	closed := mustCheck(t, strings.Replace(currentYAML, "mode: flag", "mode: block\n    fail: closed\n    streaming: buffer", 1), enforcingEnv(t))
	res := preview(t, cur, closed, src)
	if res.Evaluated != 2 || res.NewlyBlocked != 2 || skipsOf(res)[SkipNoAnswers] != 1 {
		t.Errorf("fail closed %+v", res)
	}
	// A new evaluator does not matter for a skipped decision (every evaluator fails with the skip).
	more := mustCheck(t, strings.Replace(currentYAML, "{ref: contradiction@1}]", "{ref: contradiction@1}, {ref: commitments@1}]", 1), env(t))
	if res := preview(t, cur, more, src); !reflect.DeepEqual(skipsOf(res), map[string]int{SkipNotInHistory: 1, SkipNoAnswers: 1}) {
		t.Errorf("new evaluator on errors %+v", res.Skipped)
	}
}

// TestPreviewAgainstPostgres records decisions through the store and previews a change with the
// engine: the recorded policy version comes from policy_versions.
func TestPreviewAgainstPostgres(t *testing.T) {
	b := backends(t)["postgres"](t)
	pg := b.store.(*store.PostgresStore)
	ctx := context.Background()
	f := newFixture(t, b, currentYAML)
	e := f.engine(t) // the store stamps decisions with the real time; the window ends now
	cur := e.Current()
	g, c := "groundedness@1", "contradiction@1"
	for _, tr := range []recordedTrace{
		{id: store.NewID(), route: "support-bot", answers: map[string]*decision.RawAnswer{g: noul(0.97), c: noul(0.98)}, evidence: true},
		{id: store.NewID(), route: "support-bot", answers: map[string]*decision.RawAnswer{g: noul(0.85), c: noul(0.2)}, evidence: true},
	} {
		row := recordTrace(t, cur, tr)
		v := cur.Version
		trace := &store.TraceRecord{TraceID: tr.id, CreatedAt: replayNow, Source: "proxy", APIFormat: "openai_chat", Path: "/v1/chat/completions",
			Route: tr.route, PolicyVersion: &v, Mode: "flag"}
		d := &store.DecisionRecord{TraceID: tr.id, PolicyVersion: &v, Route: tr.route, Mode: "flag", EffectiveMode: "flag",
			Status: "ok", Action: "flag", Calibration: "none", Decision: row.Decision}
		if err := pg.RecordDecision(ctx, trace, d, nil); err != nil {
			t.Fatal(err)
		}
	}
	quieter, err := e.Proposed(ctx, Source{YAML: strings.Replace(currentYAML, "{ref: groundedness@1}", "{ref: groundedness@1, thresholds: {warn_at: 0.9}}", 1)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Preview(ctx, pg, quieter, "24h", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Evaluated != 2 || res.NoLongerFlagged != 1 || res.Changed != 1 || len(res.Skipped) != 0 {
		t.Errorf("preview %+v", res)
	}
	block, err := e.Proposed(ctx, Source{YAML: strings.Replace(currentYAML, "mode: flag", "mode: block\n    streaming: buffer\n    require_calibration: false", 1)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = e.Preview(ctx, pg, block, "1h", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.NewlyBlocked != 1 || !slices.ContainsFunc(res.Caveats, func(c string) bool { return strings.Contains(c, "does not enforce block") }) {
		t.Errorf("block preview %+v", res)
	}
	if _, err := e.Preview(ctx, pg, block, "1y", 0); err == nil {
		t.Error("unknown window")
	}

	// Adopting calibrations (D62): calibrated space with calibration required. The runtime has
	// no calibrations, so the block is capped to flag; previewed with calibrations given in its
	// place (a dry-run's calibration_ids), the first trace would be blocked.
	adoptYAML := strings.Replace(currentYAML, "mode: flag", "mode: block\n    streaming: buffer", 1)
	adoptYAML = strings.Replace(adoptYAML, "[{ref: groundedness@1}, {ref: contradiction@1}]",
		"[{ref: groundedness@1, thresholds: {space: calibrated}}, {ref: contradiction@1, thresholds: {space: calibrated}}]", 1)
	adopt, err := e.Proposed(ctx, Source{YAML: adoptYAML}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, err = e.Preview(ctx, pg, adopt, "1h", 10); err != nil || res.NewlyBlocked != 0 {
		t.Errorf("adopt preview without calibrations: %+v %v", res, err)
	}
	with := fakeCalibrations{"groundedness@1": measured(), "contradiction@1": measured()}
	if res, err = e.PreviewWith(ctx, pg, adopt, "1h", 10, with); err != nil || res.NewlyBlocked != 1 {
		t.Errorf("adopt preview with calibrations: %+v %v", res, err)
	}
}
