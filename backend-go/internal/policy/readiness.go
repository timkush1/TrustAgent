package policy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
)

// Block readiness and effective routes (SPEC §7.2, api.ts BlockReadiness, EffectiveRoute,
// Downgrade). They say, per route, whether each block-eligible evaluator can actually block and
// what the route will really do, so a block route that can only flag is never a silent
// downgrade.

// backendOff is the decision backend id of a disabled decision model.
const backendOff = "off"

// Readiness items (BlockReadiness.missing), in checklist order.
const (
	MissingMode         = "mode"
	MissingEvidence     = "evidence"
	MissingCorroborator = "corroborator"
	MissingCalibration  = "calibration"
	MissingBuffering    = "buffering"
)

// Downgrade codes (api.ts Downgrade.code).
const (
	DowngradeStreamNotBuffered = "stream_not_buffered"
	DowngradeUncalibrated      = "uncalibrated"
	DowngradeSnapshotChanged   = "snapshot_changed"
	DowngradeBudget            = "budget"
	DowngradeBreakerOpen       = "breaker_open"
	DowngradeDecisionModelOff  = "decision_model_off"
	DowngradeTrustsUserContext = "trusts_user_context"
)

// downgradeMessages is contracts/ui/copy.v1.json downgrades.
var downgradeMessages = map[string]string{
	DowngradeStreamNotBuffered: "Streams pass straight through, so this route can only flag. Buffer streams to block.",
	DowngradeUncalibrated:      "Blocking stays off until every blocking check is calibrated for the current snapshot.",
	DowngradeSnapshotChanged:   "The fast-check version changed; blocking pauses until its checks are re-calibrated.",
	DowngradeBudget:            "Today's fast-check budget is used up.",
	DowngradeBreakerOpen:       "The fast check is paused after repeated errors.",
	DowngradeDecisionModelOff:  "The fast check is turned off.",
	DowngradeTrustsUserContext: "This route treats text pasted by users as documents; a user could make a false answer look supported.",
}

// BlockReadiness is one block-eligible evaluator's checklist on a route.
type BlockReadiness struct {
	Evaluator string   `json:"evaluator"`
	Ready     bool     `json:"ready"`
	Missing   []string `json:"missing"`
}

// Fix is a one-click fix for a downgrade: a link into the dashboard, or an action on the policy
// ("set:<path>=<value>" or "remove:<path>=<value>", with FieldError paths).
type Fix struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// Downgrade explains why a route does less than its mode says.
type Downgrade struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     *Fix   `json:"fix"`
}

// EffectiveEvaluator is an evaluator of an effective route.
type EffectiveEvaluator struct {
	Ref              string   `json:"ref"`
	Stage            string   `json:"stage"`
	Resolved         bool     `json:"resolved"`
	CalibrationState string   `json:"calibration_state"`
	ThresholdSpace   string   `json:"threshold_space"`
	WarnAt           float64  `json:"warn_at"`
	CriticalAt       *float64 `json:"critical_at"`
}

// EffectiveRoute is a route as it will act (api.ts EffectiveRoute).
type EffectiveRoute struct {
	Name           string               `json:"name"`
	Match          Match                `json:"match"`
	Mode           string               `json:"mode"`
	EffectiveMode  string               `json:"effective_mode"`
	Fail           string               `json:"fail"`
	Downgrades     []Downgrade          `json:"downgrades"`
	BlockReadiness []BlockReadiness     `json:"block_readiness"`
	Evaluators     []EffectiveEvaluator `json:"evaluators"`
	Settings       Settings             `json:"settings"`
}

// Calibration states (CalibrationState).
const (
	CalibrationMeasured    = "measured"
	CalibrationProvisional = "provisional"
	CalibrationProxy       = "proxy"
	CalibrationMock        = "mock"
	CalibrationFamily      = "family"
	CalibrationNone        = "none"
)

// CalibrationStatus is the calibration that applies to an evaluator on the current snapshot.
type CalibrationStatus struct {
	// State is the CalibrationState (none when there is no calibration).
	State string
	// Quality is the record quality (measured, provisional, proxy, mock), "" without a record.
	Quality string
	// Table is the 101-point calibration table, nil without a record.
	Table []float64
	// Stale reports that a calibration exists only for an earlier snapshot.
	Stale bool
}

// CalibrationLookup finds the calibration of an evaluator ref (with the route's resolved params)
// for a decision-model snapshot ("" = the current one). The calibration store (P4) implements it.
type CalibrationLookup interface {
	Lookup(ref string, params map[string]string, snapshot string) CalibrationStatus
}

// NoCalibration is the lookup of an install without calibrations: every evaluator is
// uncalibrated.
type NoCalibration struct{}

// Lookup implements CalibrationLookup.
func (NoCalibration) Lookup(string, map[string]string, string) CalibrationStatus {
	return CalibrationStatus{State: CalibrationNone}
}

// Runtime is the decision-model state that effective routes depend on.
type Runtime struct {
	// Backend is the decision backend id (openrouter-decisions, typesafe, mock, off, ...).
	Backend string
	// Snapshot is the current decision-model snapshot, "" when unknown.
	Snapshot        string
	BreakerOpen     bool
	BudgetExhausted bool
	Calibrations    CalibrationLookup
}

// usable reports whether a calibration satisfies require_calibration: measured, or mock on the
// mock backend (SPEC §2.9 decide, §2.11).
func (rt Runtime) usable(c CalibrationStatus) bool {
	return c.Table != nil && (c.Quality == CalibrationMeasured || (c.Quality == CalibrationMock && rt.Backend == decision.BackendMock))
}

func (rt Runtime) lookup(ref string, params map[string]string) CalibrationStatus {
	if rt.Calibrations == nil {
		return CalibrationStatus{State: CalibrationNone}
	}
	c := rt.Calibrations.Lookup(ref, params, rt.Snapshot)
	if c.State == "" {
		c.State = CalibrationNone
	}
	return c
}

// routePath is the FieldError path prefix of a route.
func (p *Policy) routePath(r *Route) string {
	for i, x := range p.Routes {
		if x == r {
			return fmt.Sprintf("routes[%d]", i)
		}
	}
	return "defaults"
}

// EffectiveRoutes describes every route (configured routes, then the default route).
func (p *Policy) EffectiveRoutes(specs Resolver, env Env, rt Runtime) []EffectiveRoute {
	out := make([]EffectiveRoute, 0, len(p.Routes)+1)
	for _, r := range p.AllRoutes() {
		out = append(out, p.effectiveRoute(r, specs, env, rt))
	}
	return out
}

// BlockReadiness returns the readiness checklists keyed by route name (ValidateResult).
func (p *Policy) BlockReadiness(specs Resolver, env Env, rt Runtime) map[string][]BlockReadiness {
	out := map[string][]BlockReadiness{}
	for _, r := range p.AllRoutes() {
		out[r.Name] = p.readiness(r, specs, env, rt)
	}
	return out
}

// routeFacts are the derived properties readiness and downgrades share.
type routeFacts struct {
	enforcing bool
	// sources are the evidence sources that can produce evidence on this build.
	sources []string
	// hasRules: some enabled evaluator declares params, so rules are never empty.
	hasRules bool
	params   map[string]map[string]string
}

func facts(r *Route, specs Resolver, env Env) routeFacts {
	f := routeFacts{enforcing: slices.Contains(enforcingModes, r.Mode), params: map[string]map[string]string{}}
	for _, src := range r.Evidence.Sources {
		switch {
		case src == evidence.SourceRequestSystem && r.Evidence.SystemMode == evidence.SystemModeOff:
		case src == evidence.SourceKnowledge && !env.Knowledge:
		default:
			f.sources = append(f.sources, src)
		}
	}
	for _, e := range r.Evaluators {
		if !e.IsEnabled() {
			continue
		}
		spec, ok := specs.Get(e.Ref)
		if !ok {
			continue
		}
		resolved := map[string]string{}
		for name, prm := range spec.Params {
			v := prm.Default
			if rv, ok := e.Params[name]; ok {
				v = rv
			}
			resolved[name] = v
		}
		f.params[e.Ref] = resolved
		if len(spec.Params) > 0 {
			f.hasRules = true
		}
	}
	return f
}

// evaluatorSources are the evidence sources an evaluator reads on the route.
func (f routeFacts) evaluatorSources(e EvaluatorRef) []string {
	if len(e.EvidenceSources) == 0 {
		return f.sources
	}
	var out []string
	for _, s := range f.sources {
		if slices.Contains(e.EvidenceSources, s) {
			out = append(out, s)
		}
	}
	return out
}

// detectorReachable reports whether a detector can fire on the route (SPEC §2.10: numbers and
// dates run when evidence or rules are non-empty; URLs, quotes and citations need evidence).
func (f routeFacts) detectorReachable(d string) bool {
	switch d {
	case decision.DetNumbers, decision.DetDates:
		return len(f.sources) > 0 || f.hasRules
	case decision.DetURLs, decision.DetQuotes, decision.DetCitation:
		return len(f.sources) > 0
	}
	return true
}

// canBeCritical reports whether an attached, active evaluator can reach critical on the route
// and has the evidence it needs: the condition for corroborating another evaluator.
func (f routeFacts) canBeCritical(r *Route, id string, specs Resolver) bool {
	for _, e := range r.Evaluators {
		if !e.IsEnabled() || e.StageOrDefault() != decision.StageActive {
			continue
		}
		spec, ok := specs.Get(e.Ref)
		if !ok || spec.ID != id {
			continue
		}
		_, crit, _ := resolvedThresholds(e.Thresholds.Decision(), spec)
		if crit == nil {
			continue
		}
		if spec.Evidence.Requires == decision.EvidenceRequired && len(f.evaluatorSources(e)) == 0 {
			continue
		}
		return true
	}
	return false
}

// corroboratorReachable is readiness item 2: no corroboration needed, or at least one listed
// corroborator can fire on the route.
func (f routeFacts) corroboratorReachable(r *Route, spec *decision.Spec, specs Resolver) bool {
	req := spec.Outcomes.RequiresCorroboration
	if len(req) == 0 {
		return true
	}
	for _, x := range req {
		if d, isDet := strings.CutPrefix(x, "det:"); isDet {
			if f.detectorReachable(d) {
				return true
			}
			continue
		}
		if f.canBeCritical(r, x, specs) {
			return true
		}
	}
	return false
}

// readinessItem is one block-eligible evaluator with its missing items.
type readinessItem struct {
	ref     string
	spec    *decision.Spec
	missing []string
	// calibration is the calibration state; stale marks a calibration of an earlier snapshot.
	calibrationReady bool
	stale            bool
}

func (p *Policy) readinessItems(r *Route, specs Resolver, env Env, rt Runtime) []readinessItem {
	f := facts(r, specs, env)
	var out []readinessItem
	for _, e := range r.Evaluators {
		if !e.IsEnabled() || e.StageOrDefault() != decision.StageActive {
			continue
		}
		spec, ok := specs.Get(e.Ref)
		if !ok || !spec.Outcomes.BlockEligible {
			continue
		}
		it := readinessItem{ref: e.Ref, spec: spec, missing: []string{}}
		if !f.enforcing {
			it.missing = append(it.missing, MissingMode)
		}
		if spec.Evidence.Requires == decision.EvidenceRequired && len(f.evaluatorSources(e)) == 0 {
			it.missing = append(it.missing, MissingEvidence)
		}
		if !f.corroboratorReachable(r, spec, specs) {
			it.missing = append(it.missing, MissingCorroborator)
		}
		dt := e.Thresholds.Decision()
		_, crit, _ := resolvedThresholds(dt, spec)
		cal := rt.lookup(e.Ref, f.params[e.Ref])
		it.stale = cal.Stale
		it.calibrationReady = !r.RequireCalibration || (dt.Space == decision.SpaceCalibrated && rt.usable(cal))
		if crit == nil || !it.calibrationReady {
			it.missing = append(it.missing, MissingCalibration)
		}
		if r.Streaming != StreamingBuffer {
			it.missing = append(it.missing, MissingBuffering)
		}
		out = append(out, it)
	}
	return out
}

// readiness is the route's checklist (SPEC §7.2 items 1-4; item 5 is a downgrade and a warning).
func (p *Policy) readiness(r *Route, specs Resolver, env Env, rt Runtime) []BlockReadiness {
	items := p.readinessItems(r, specs, env, rt)
	out := make([]BlockReadiness, 0, len(items))
	for _, it := range items {
		out = append(out, BlockReadiness{Evaluator: it.ref, Ready: len(it.missing) == 0, Missing: it.missing})
	}
	return out
}

func (p *Policy) effectiveRoute(r *Route, specs Resolver, env Env, rt Runtime) EffectiveRoute {
	er := EffectiveRoute{
		Name: r.Name, Match: r.Match, Mode: r.Mode, EffectiveMode: r.Mode, Fail: r.Fail,
		Downgrades: []Downgrade{}, BlockReadiness: p.readiness(r, specs, env, rt), Evaluators: []EffectiveEvaluator{},
		Settings: r.Settings,
	}
	f := facts(r, specs, env)
	for _, e := range r.Evaluators {
		if !e.IsEnabled() {
			continue
		}
		dt := e.Thresholds.Decision()
		ee := EffectiveEvaluator{Ref: e.Ref, Stage: e.StageOrDefault(), ThresholdSpace: dt.Space, CalibrationState: CalibrationNone}
		if spec, ok := specs.Get(e.Ref); ok {
			ee.Resolved = true
			ee.WarnAt, ee.CriticalAt, _ = resolvedThresholds(dt, spec)
			ee.CalibrationState = rt.lookup(e.Ref, f.params[e.Ref]).State
		}
		er.Evaluators = append(er.Evaluators, ee)
	}
	path := p.routePath(r)
	add := func(code string, fix *Fix) { er.Downgrades = append(er.Downgrades, p.downgrade(code, fix)) }
	settingsLink := &Fix{Kind: "link", Target: "/app/settings#decision-model"}

	// Without decisions no evaluator runs, whatever the mode.
	decisions := true
	if len(er.Evaluators) > 0 {
		switch {
		case rt.Backend == backendOff:
			add(DowngradeDecisionModelOff, settingsLink)
			decisions = false
		case rt.BreakerOpen:
			add(DowngradeBreakerOpen, settingsLink)
			decisions = false
		case rt.BudgetExhausted:
			add(DowngradeBudget, &Fix{Kind: "link", Target: "/app/settings#budgets"})
			decisions = false
		}
	}
	if !f.enforcing {
		return er
	}

	// Can anything block? Detectors in block_on bypass the calibration cap; an evaluator blocks
	// when it is calibration-ready with a reachable corroborator and its evidence; the no-evidence
	// rule blocks through factual_claims (subject to calibration).
	detBlock := slices.ContainsFunc(r.Detectors.BlockOn, f.detectorReachable)
	items := p.readinessItems(r, specs, env, rt)
	evalBlock, uncalibrated, stale := false, "", ""
	for _, it := range items {
		structural := !slices.ContainsFunc(it.missing, func(m string) bool {
			return m == MissingEvidence || m == MissingCorroborator
		})
		_, crit, _ := resolvedThresholds(thresholdsFor(r, it.ref), it.spec)
		if structural && crit != nil && it.calibrationReady {
			evalBlock = true
		}
		if !it.calibrationReady {
			switch {
			case it.stale && stale == "":
				stale = it.spec.ID
			case !it.stale && uncalibrated == "":
				uncalibrated = it.spec.ID
			}
		}
	}
	noEvidenceBlock := false
	if r.OnNoEvidence == decision.OnNoEvidenceBlock {
		noEvidenceBlock = !r.RequireCalibration || rt.usable(rt.lookup(decision.FactualClaimsRef, nil))
		if !noEvidenceBlock && uncalibrated == "" {
			uncalibrated = decision.FactualClaimsID
		}
	}

	if stale != "" {
		add(DowngradeSnapshotChanged, &Fix{Kind: "link", Target: "/app/evaluators/" + stale + "?tab=calibration"})
	}
	if uncalibrated != "" {
		add(DowngradeUncalibrated, &Fix{Kind: "link", Target: "/app/evaluators/" + uncalibrated + "?tab=calibration"})
	}
	if r.Streaming != StreamingBuffer {
		add(DowngradeStreamNotBuffered, &Fix{Kind: "action", Target: "set:" + path + ".streaming=" + StreamingBuffer})
		er.EffectiveMode = decision.ModeFlag
	}
	if slices.Contains(r.Evidence.Sources, evidence.SourceRequestContext) {
		add(DowngradeTrustsUserContext, &Fix{Kind: "action", Target: "remove:" + path + ".evidence.sources=" + evidence.SourceRequestContext})
	}
	canBlock := detBlock || (decisions && (evalBlock || noEvidenceBlock)) || (!decisions && r.Fail == decision.FailClosed)
	if !canBlock {
		er.EffectiveMode = decision.ModeFlag
	}
	return er
}

func (p *Policy) downgrade(code string, fix *Fix) Downgrade {
	return Downgrade{Code: code, Message: downgradeMessages[code], Fix: fix}
}

// thresholdsFor returns the route's threshold overrides of ref.
func thresholdsFor(r *Route, ref string) decision.Thresholds {
	for _, e := range r.Evaluators {
		if e.Ref == ref {
			return e.Thresholds.Decision()
		}
	}
	return decision.Thresholds{Space: decision.SpaceRaw}
}
