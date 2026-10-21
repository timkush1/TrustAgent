package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/store"
)

// The policy dry-run ("Preview impact (free)", SPEC §2.4 row 26, §0 rows 36/47): a pure replay of
// the stored answers of past traffic through decide(), once with the active policy and once with
// the proposed one, so the difference is exactly the policy change. It never calls the decision
// model. A trace is skipped, and counted, when the proposal needs something the recording does
// not have: an evaluator (or params) the trace was never asked, answers at all (sampled out), or
// a trace body to re-run a detector whose settings changed.

// ReplaySource reads recorded decisions and, when a detector must be re-run, trace bodies;
// *store.PostgresStore implements it.
type ReplaySource interface {
	ForEachReplayDecision(ctx context.Context, f store.ReplayFilter, fn func(*store.ReplayRow) error) error
	GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error)
}

// Dry-run skip reasons (PolicyDryRunResult.skipped[].reason), in report order.
const (
	SkipNotInHistory = "evaluator_not_in_history"
	SkipNoAnswers    = "no_answers"
	SkipBodyExpired  = "body_expired"
)

// MaxExamples caps PolicyDryRunResult.examples.
const MaxExamples = 20

// windows are the TimeWindow values.
var windows = map[string]time.Duration{
	"15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

// ParseWindow returns the duration of a TimeWindow ("15m", "1h", "24h", "7d", "30d").
func ParseWindow(w string) (time.Duration, bool) {
	d, ok := windows[w]
	return d, ok
}

var gateActions = []string{decision.ActionObserve, decision.ActionPass, decision.ActionFlag, decision.ActionBlock, decision.ActionFallback}

// ActionCounts counts traces per GateAction; every action is present.
type ActionCounts map[string]int

func newCounts() ActionCounts {
	c := ActionCounts{}
	for _, a := range gateActions {
		c[a] = 0
	}
	return c
}

// RouteImpact is one route of PolicyDryRunResult.by_route.
type RouteImpact struct {
	Route    string       `json:"route"`
	Current  ActionCounts `json:"current"`
	Proposed ActionCounts `json:"proposed"`
}

// AuditAgreement compares would-be blocks with deep-audit verdicts. Unaudited counts blocks
// without a GROUNDED or HALLUCINATED verdict.
type AuditAgreement struct {
	WouldBlockHallucinated int `json:"would_block_hallucinated"`
	WouldBlockGrounded     int `json:"would_block_grounded"`
	WouldBlockUnaudited    int `json:"would_block_unaudited"`
}

// DryRunExample is a trace whose action changes.
type DryRunExample struct {
	TraceID  string `json:"trace_id"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
}

// SkipCount is one skip reason with its count.
type SkipCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// DryRunResult is api.ts PolicyDryRunResult.
type DryRunResult struct {
	Window             string          `json:"window"`
	Evaluated          int             `json:"evaluated"`
	ReplayOnly         bool            `json:"replay_only"`
	ByActionCurrent    ActionCounts    `json:"by_action_current"`
	ByActionProposed   ActionCounts    `json:"by_action_proposed"`
	Changed            int             `json:"changed"`
	NewlyBlocked       int             `json:"newly_blocked"`
	NewlyFlagged       int             `json:"newly_flagged"`
	NoLongerFlagged    int             `json:"no_longer_flagged"`
	ByRoute            []RouteImpact   `json:"by_route"`
	DeepAuditAgreement AuditAgreement  `json:"deep_audit_agreement"`
	Examples           []DryRunExample `json:"examples"`
	Skipped            []SkipCount     `json:"skipped"`
	Caveats            []string        `json:"caveats"`
}

// DryRun is one preview.
type DryRun struct {
	// Current and Proposed are the policies to compare.
	Current, Proposed *Policy
	// Specs resolves evaluator refs (the registry).
	Specs Resolver
	// Calibrations finds the calibration of each evaluator for a trace's snapshot.
	Calibrations CalibrationLookup
	// Recorded returns the policy a trace was gated under (by version id), nil when unknown.
	Recorded func(ctx context.Context, version int64) *Policy
	Window   string
	Limit    int
	Now      time.Time
}

// Run replays the window. The result depends only on the recorded rows and the inputs.
func (d *DryRun) Run(ctx context.Context, src ReplaySource) (*DryRunResult, error) {
	span, ok := ParseWindow(d.Window)
	if !ok {
		return nil, fmt.Errorf("policy: unknown window %q", d.Window)
	}
	limit := d.Limit
	if limit <= 0 {
		limit = 10000
	}
	limit = min(limit, store.MaxReplayRows)
	if d.Calibrations == nil {
		d.Calibrations = NoCalibration{}
	}
	res := &DryRunResult{
		Window: d.Window, ReplayOnly: true, ByActionCurrent: newCounts(), ByActionProposed: newCounts(),
		ByRoute: []RouteImpact{}, Examples: []DryRunExample{}, Skipped: []SkipCount{}, Caveats: []string{},
	}
	byRoute := map[string]*RouteImpact{}
	skipped := map[string]int{}
	notes := map[string]bool{}
	cache := map[int64]*Policy{}
	recorded := func(v *int64) *Policy {
		if v == nil || d.Recorded == nil {
			return nil
		}
		if p, ok := cache[*v]; ok {
			return p
		}
		p := d.Recorded(ctx, *v)
		cache[*v] = p
		return p
	}
	rows, late := 0, 0
	body := func(traceID string) *store.BodyRecord {
		b, err := src.GetTraceBody(ctx, traceID)
		if err != nil {
			return nil
		}
		return b
	}
	err := src.ForEachReplayDecision(ctx, store.ReplayFilter{Since: d.Now.Add(-span), Limit: limit},
		func(row *store.ReplayRow) error {
			rows++
			rec, err := parseRecorded(row.Decision)
			if err != nil || (rec.SkipReason != nil && *rec.SkipReason == decision.SkipSampledOut) {
				skipped[SkipNoAnswers]++
				return nil
			}
			old := recorded(row.PolicyVersion)
			cur, skip := d.input(d.Current, row, rec, old, notes, body)
			if skip != "" {
				skipped[skip]++
				return nil
			}
			prop, skip := d.input(d.Proposed, row, rec, old, notes, body)
			if skip != "" {
				skipped[skip]++
				return nil
			}
			c, err := decision.Decide(cur)
			if err != nil {
				notes["unreplayable"] = true
				skipped[SkipNoAnswers]++
				return nil
			}
			p, err := decision.Decide(prop)
			if err != nil {
				notes["unreplayable"] = true
				skipped[SkipNoAnswers]++
				return nil
			}
			if row.Late {
				late++
			}
			d.count(res, byRoute, row, c.Action, p.Action)
			return nil
		})
	if err != nil {
		return nil, err
	}
	for _, reason := range []string{SkipNotInHistory, SkipNoAnswers, SkipBodyExpired} {
		if n := skipped[reason]; n > 0 {
			res.Skipped = append(res.Skipped, SkipCount{Reason: reason, Count: n})
		}
	}
	names := slices.Sorted(maps.Keys(byRoute))
	for _, name := range names {
		res.ByRoute = append(res.ByRoute, *byRoute[name])
	}
	res.Caveats = d.caveats(res, notes, skipped, rows, late, limit)
	return res, nil
}

func (d *DryRun) count(res *DryRunResult, byRoute map[string]*RouteImpact, row *store.ReplayRow, cur, prop string) {
	res.Evaluated++
	res.ByActionCurrent[cur]++
	res.ByActionProposed[prop]++
	ri := byRoute[row.Route]
	if ri == nil {
		ri = &RouteImpact{Route: row.Route, Current: newCounts(), Proposed: newCounts()}
		byRoute[row.Route] = ri
	}
	ri.Current[cur]++
	ri.Proposed[prop]++
	stops := func(a string) bool { return a == decision.ActionBlock || a == decision.ActionFallback }
	quiet := func(a string) bool { return a == decision.ActionPass || a == decision.ActionObserve }
	if cur != prop {
		res.Changed++
		if len(res.Examples) < MaxExamples {
			res.Examples = append(res.Examples, DryRunExample{TraceID: row.TraceID, Current: cur, Proposed: prop})
		}
	}
	switch {
	case stops(prop) && !stops(cur):
		res.NewlyBlocked++
	case prop == decision.ActionFlag && quiet(cur):
		res.NewlyFlagged++
	case cur == decision.ActionFlag && quiet(prop):
		res.NoLongerFlagged++
	}
	if stops(prop) {
		switch v := row.AuditVerdict; {
		case v != nil && *v == "HALLUCINATED":
			res.DeepAuditAgreement.WouldBlockHallucinated++
		case v != nil && *v == "GROUNDED":
			res.DeepAuditAgreement.WouldBlockGrounded++
		default:
			res.DeepAuditAgreement.WouldBlockUnaudited++
		}
	}
}

// recordedDecision is the part of a stored decision/v1 document a replay reads.
type recordedDecision struct {
	Status          string                    `json:"status"`
	SkipReason      *string                   `json:"skip_reason"`
	Evaluators      decision.EvaluatorResults `json:"evaluators"`
	Detectors       []decision.DetectorResult `json:"detectors"`
	Excluded        []decision.Exclusion      `json:"excluded"`
	EvidenceSummary *evidence.Summary         `json:"evidence_summary"`
	DecisionModel   *struct {
		Backend  string  `json:"backend"`
		Snapshot *string `json:"snapshot"`
	} `json:"decision_model"`
}

func parseRecorded(doc json.RawMessage) (*recordedDecision, error) {
	var r recordedDecision
	if err := json.Unmarshal(doc, &r); err != nil {
		return nil, err
	}
	if r.Status == "" || r.Detectors == nil {
		return nil, errors.New("not a decision/v1 document")
	}
	return &r, nil
}

// input builds the decide() input of a recorded trace under policy p. It returns a skip reason
// when the recording cannot answer for p.
func (d *DryRun) input(p *Policy, row *store.ReplayRow, rec *recordedDecision, old *Policy, notes map[string]bool,
	body func(traceID string) *store.BodyRecord) (*decision.Input, string) {
	route, ok := p.Route(row.Route)
	if !ok {
		route = p.Default
		notes["route_fallback"] = true
	}
	var oldRoute *Route
	if old != nil {
		if oldRoute, ok = old.Route(row.Route); !ok {
			oldRoute = old.Default
		}
		if !reflect.DeepEqual(oldRoute.Evidence, route.Evidence) || oldRoute.Decision.Model != route.Decision.Model {
			notes["compile_inputs:"+route.Name] = true
		}
	} else if row.PolicyVersion != nil {
		notes["unknown_version"] = true
	}

	in := &decision.Input{
		Policy: decision.Policy{
			ConfiguredMode: route.Mode, Fail: route.Fail, RequireCalibration: route.RequireCalibration, OnNoEvidence: route.OnNoEvidence,
			Detectors: decision.DetectorPolicy{FlagOn: route.Detectors.FlagOn, BlockOn: route.Detectors.BlockOn},
		},
		Answers: map[string]*decision.RawAnswer{}, QuestionMap: map[string]string{}, Excluded: []decision.Exclusion{},
		Calibration: map[string]decision.CalibrationEntry{},
		Meta: decision.Meta{
			Stream: "none", RequestStatus: map[string]string{}, QIDRequest: map[string]int{}, QuestionErrors: map[string]string{},
		},
		Detectors: decision.DetectorStates(rec.Detectors),
	}
	snapshot := ""
	if rec.DecisionModel != nil {
		in.Meta.Backend = rec.DecisionModel.Backend
		if rec.DecisionModel.Snapshot != nil {
			snapshot = *rec.DecisionModel.Snapshot
		}
	}
	if row.Stream {
		in.Meta.Stream = route.Streaming
	}
	if s := rec.EvidenceSummary; s != nil {
		other := false
		for _, u := range s.Used {
			if u.Items > 0 {
				in.Meta.EvidencePresent = true
				other = other || u.Source != evidence.SourceKnowledge
			}
		}
		in.Meta.EvidenceTruncated = s.Truncated
		in.Meta.KnowledgeOnly = in.Meta.EvidencePresent && !other
	}
	skipped := rec.SkipReason != nil
	if skipped {
		in.Meta.SkipReason = rec.SkipReason
	}

	// Detectors: re-run the numbers detector when min_integer differs from the recording.
	recordedMin := route.Detectors.MinInteger
	if oldRoute != nil {
		recordedMin = oldRoute.Detectors.MinInteger
	} else if cur, ok := d.Current.Route(row.Route); ok {
		recordedMin = cur.Detectors.MinInteger
	}
	if route.Detectors.MinInteger != recordedMin && in.Detectors[decision.DetNumbers].Ran {
		if row.Body == nil {
			row.Body = body(row.TraceID)
		}
		st, ok := rerunNumbers(row, route.Detectors.MinInteger, d.Now)
		if !ok {
			return nil, SkipBodyExpired
		}
		in.Detectors[decision.DetNumbers] = st
	}

	stored := map[string]*decision.EvaluatorResult{}
	for i := range rec.Evaluators {
		stored[rec.Evaluators[i].Ref] = &rec.Evaluators[i]
	}
	tooLong := slices.ContainsFunc(rec.Excluded, func(x decision.Exclusion) bool { return x.Reason == decision.ExcludedTooLong })
	requests := map[string]int{}
	request := func(status string) int {
		k, ok := requests[status]
		if !ok {
			k = len(requests)
			requests[status] = k
			in.Meta.RequestStatus[strconv.Itoa(k)] = status
		}
		return k
	}
	noEvidenceExcluded := false
	attached := false

	add := func(ref string, spec *decision.Spec, stage string, internal bool, th decision.Thresholds, params map[string]string, sr *decision.EvaluatorResult) string {
		qid := spec.QuestionID()
		switch {
		case sr == nil && skipped:
			// Every evaluator of a skipped decision fails with the skip reason.
		case sr == nil && tooLong:
			in.Excluded = append(in.Excluded, decision.Exclusion{Ref: ref, Reason: decision.ExcludedTooLong})
		case sr == nil && spec.Evidence.Requires == decision.EvidenceRequired && !in.Meta.EvidencePresent:
			in.Excluded = append(in.Excluded, decision.Exclusion{Ref: ref, Reason: decision.ExcludedNoEvidence})
			noEvidenceExcluded = true
		case sr == nil && stage == decision.StageShadow:
			return "" // never affects the action
		case sr == nil:
			return SkipNotInHistory
		case sr.Status == decision.EvalNotAssessed:
			reason := decision.ExcludedNoEvidence
			if sr.Reason != nil {
				reason = *sr.Reason
			}
			in.Excluded = append(in.Excluded, decision.Exclusion{Ref: ref, Reason: reason})
			noEvidenceExcluded = noEvidenceExcluded || (reason == decision.ExcludedNoEvidence && spec.Evidence.Requires == decision.EvidenceRequired)
		case sr.Status == decision.EvalError:
			reason := ""
			if sr.Reason != nil {
				reason = *sr.Reason
			}
			in.QuestionMap[qid] = ref
			switch {
			case skipped:
			case reason == decision.RequestTimeout || strings.HasPrefix(reason, "error:"):
				in.Meta.QIDRequest[qid] = request(reason)
			case sr.Answer != nil:
				in.Meta.QIDRequest[qid] = request(decision.RequestOK)
				in.Answers[qid] = sr.Answer
			default:
				in.Meta.QIDRequest[qid] = request(decision.RequestOK)
				in.Meta.QuestionErrors[qid] = reason
			}
		default:
			in.QuestionMap[qid] = ref
			in.Meta.QIDRequest[qid] = request(decision.RequestOK)
			in.Answers[qid] = sr.Answer
		}
		in.Evaluators = append(in.Evaluators, decision.EvaluatorInput{Ref: ref, Spec: spec, Stage: stage, Internal: internal, Thresholds: th})
		if cal, ok := DecideCalibration(d.Calibrations.Lookup(ref, resolvedParams(spec, params), snapshot)); ok {
			in.Calibration[ref] = cal
		}
		return ""
	}

	for _, e := range route.Evaluators {
		if !e.IsEnabled() {
			continue
		}
		spec, ok := d.Specs.Get(e.Ref)
		if !ok {
			return nil, SkipNotInHistory
		}
		if spec.ID == decision.FactualClaimsID {
			attached = true
		}
		sr := stored[e.Ref]
		if sr != nil && oldRoute != nil && !maps.Equal(resolvedParams(spec, paramsOf(oldRoute, e.Ref)), resolvedParams(spec, e.Params)) {
			sr = nil // the question state changed: the recorded answer does not apply
		}
		th := e.Thresholds.Decision()
		if th.Space == "" {
			th.Space = decision.SpaceRaw
		}
		if skip := add(e.Ref, spec, e.StageOrDefault(), false, th, e.Params, sr); skip != "" {
			return nil, skip
		}
	}
	// compile() step 3: the implicit factual_claims evaluator.
	if route.OnNoEvidence != decision.OnNoEvidenceIgnore && noEvidenceExcluded && !attached {
		spec, ok := d.Specs.Get(decision.FactualClaimsRef)
		sr := stored[decision.FactualClaimsRef]
		if !ok || (sr == nil && !skipped) {
			return nil, SkipNotInHistory
		}
		if skip := add(decision.FactualClaimsRef, spec, decision.StageActive, true, decision.Thresholds{Space: decision.SpaceRaw}, nil, sr); skip != "" {
			return nil, skip
		}
	}
	return in, ""
}

// paramsOf returns the params a route sets for ref.
func paramsOf(r *Route, ref string) map[string]string {
	for _, e := range r.Evaluators {
		if e.Ref == ref {
			return e.Params
		}
	}
	return nil
}

// resolvedParams resolves a spec's params with route overrides (defaults otherwise).
func resolvedParams(spec *decision.Spec, set map[string]string) map[string]string {
	out := make(map[string]string, len(spec.Params))
	for name, prm := range spec.Params {
		out[name] = prm.Default
		if v, ok := set[name]; ok {
			out[name] = v
		}
	}
	return out
}

// rerunNumbers runs the numbers detector on the stored body with a new min_integer.
func rerunNumbers(row *store.ReplayRow, minInteger int, now time.Time) (decision.DetectorState, bool) {
	b := row.Body
	if b == nil || b.ResponseText == nil || !now.Before(b.ExpiresAt) {
		return decision.DetectorState{}, false
	}
	var items []evidence.Item
	if len(b.Evidence) > 0 {
		if err := json.Unmarshal(b.Evidence, &items); err != nil {
			return decision.DetectorState{}, false
		}
	}
	ev := make([]decision.EvidenceItem, 0, len(items))
	for _, it := range items {
		ev = append(ev, it.Decision())
	}
	in := &decision.DetectorInput{Response: *b.ResponseText, Evidence: ev, Rules: b.Rules, MinInteger: minInteger, PreDedupItems: len(items)}
	if b.UserRequest != nil {
		in.UserRequest = *b.UserRequest
	}
	if b.SystemInstructions != nil {
		in.SystemInstructions = *b.SystemInstructions
	}
	r, err := decision.DetectOne(decision.DetNumbers, in)
	if err != nil {
		return decision.DetectorState{}, false
	}
	return decision.DetectorState{Ran: r.Ran, Fired: r.Fired}, true
}

// caveats explain what the preview cannot show.
func (d *DryRun) caveats(res *DryRunResult, notes map[string]bool, skipped map[string]int, rows, late, limit int) []string {
	out := []string{"Replays the stored fast-check answers through decide() with each policy; no model calls were made, so nothing was spent."}
	if n := skipped[SkipNotInHistory]; n > 0 {
		out = append(out, fmt.Sprintf("%d traces were skipped because the proposal asks evaluators (or params) they were never asked; use Test on past traffic (paid) in Evaluator Studio for those.", n))
	}
	if n := skipped[SkipNoAnswers]; n > 0 {
		out = append(out, fmt.Sprintf("%d traces were skipped because they have no stored answers (sampled out or not recorded).", n))
	}
	if notes["unreplayable"] {
		out = append(out, "Some recorded decisions could not be replayed (an inconsistent recording) and are counted as having no answers.")
	}
	if n := skipped[SkipBodyExpired]; n > 0 {
		out = append(out, fmt.Sprintf("%d traces were skipped because re-running the numbers detector for the new min_integer needs their body, which was not kept or has expired.", n))
	}
	if notes["route_fallback"] {
		out = append(out, "Traces are replayed on the route they were recorded on; traces whose route the policy does not have use its default route.")
	}
	var changed []string
	for k := range notes {
		if name, ok := strings.CutPrefix(k, "compile_inputs:"); ok {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	if len(changed) > 0 {
		out = append(out, "Evidence or decision-model settings differ from the recording on route(s) "+strings.Join(changed, ", ")+
			"; the preview reuses the answers computed on the evidence each request actually had.")
	}
	if notes["unknown_version"] {
		out = append(out, "Some traces were gated under a policy version that is no longer stored or does not validate; their params and detector settings are assumed unchanged.")
	}
	if late > 0 {
		out = append(out, fmt.Sprintf("%d of the replayed decisions arrived after their deadline and were not enforced when recorded; the preview treats them as on time.", late))
	}
	if rows >= limit {
		out = append(out, fmt.Sprintf("Only the newest %d decisions of the window were replayed.", limit))
	}
	if res.Evaluated == 0 {
		out = append(out, "No recorded decisions could be replayed in this window.")
	}
	return out
}

// Preview replays proposed against the active policy over the window (POST /api/policies/dry-run).
func (e *Engine) Preview(ctx context.Context, src ReplaySource, proposed *Policy, window string, limit int) (*DryRunResult, error) {
	return e.PreviewWith(ctx, src, proposed, window, limit, nil)
}

// PreviewWith is Preview with cal as the calibration lookup of both replays instead of the
// runtime's (nil keeps the runtime's): the dry-run's calibration_ids, which preview a calibration
// before it is activated (D62).
func (e *Engine) PreviewWith(ctx context.Context, src ReplaySource, proposed *Policy, window string, limit int, cal CalibrationLookup) (*DryRunResult, error) {
	rt := e.Runtime()
	if cal == nil {
		cal = rt.Calibrations
	}
	d := &DryRun{
		Current: e.Current(), Proposed: proposed, Specs: e.cfg.Env.Specs, Calibrations: cal,
		Recorded: e.recordedPolicy, Window: window, Limit: limit, Now: e.cfg.Now(),
	}
	res, err := d.Run(ctx, src)
	if err != nil {
		return nil, err
	}
	if !e.cfg.Env.Enforcing && slices.ContainsFunc(proposed.AllRoutes(), func(r *Route) bool { return slices.Contains(enforcingModes, r.Mode) }) {
		res.Caveats = append(res.Caveats, "This build does not enforce block and fallback yet: the proposal can be previewed but not saved.")
	}
	return res, nil
}

// recordedPolicy loads the policy version a trace was gated under, leniently (block, fallback,
// knowledge and the fallback upstream are accepted: the version was valid when it was active).
// nil when unknown.
func (e *Engine) recordedPolicy(ctx context.Context, id int64) *Policy {
	if e.cfg.Store == nil {
		if st := e.State(); st.Version.ID == id {
			return st.Policy
		}
		return nil
	}
	rec, err := e.cfg.Store.GetPolicyVersion(ctx, id)
	if err != nil {
		return nil
	}
	env := e.cfg.Env
	env.Enforcing, env.Knowledge, env.FallbackUpstream = true, true, true
	return Check([]byte(rec.YAML), env).Policy
}
