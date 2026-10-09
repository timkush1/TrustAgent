package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Input is the input of decide() (contracts/schema/decide-input.v1.schema.json).
type Input struct {
	Policy     Policy           `json:"policy"`
	Evaluators []EvaluatorInput `json:"evaluators"` // route order
	// Answers maps question ids to raw answers; nil when nothing was answered.
	Answers map[string]*RawAnswer `json:"answers"`
	// QuestionMap maps question ids to evaluator refs.
	QuestionMap map[string]string `json:"question_map"`
	Excluded    []Exclusion       `json:"excluded"`
	// Calibration is keyed by evaluator ref, looked up by the caller for the current snapshot.
	Calibration map[string]CalibrationEntry `json:"calibration"`
	Meta        Meta                        `json:"meta"`
	Detectors   map[string]DetectorState    `json:"detectors"`
}

// Policy is the route policy that decide() reads.
type Policy struct {
	ConfiguredMode     string         `json:"configured_mode"`
	Fail               string         `json:"fail"`
	RequireCalibration bool           `json:"require_calibration"`
	OnNoEvidence       string         `json:"on_no_evidence"`
	Detectors          DetectorPolicy `json:"detectors"`
}

// DetectorPolicy lists the detectors that flag or block on their own.
type DetectorPolicy struct {
	FlagOn  []string `json:"flag_on"`
	BlockOn []string `json:"block_on"`
}

// EvaluatorInput is one evaluator of the route, with its spec and route overrides.
type EvaluatorInput struct {
	Ref        string     `json:"ref"`
	Spec       *Spec      `json:"spec"`
	Stage      string     `json:"stage"`
	Internal   bool       `json:"internal"`
	Thresholds Thresholds `json:"thresholds"`
}

// Thresholds are the route overrides of an evaluator's thresholds. WarnAt falls back to the spec
// when absent or null; CriticalAt and EscalateFrom fall back only when absent (an explicit null
// overrides the spec value).
type Thresholds struct {
	Space        string   `json:"space"`
	WarnAt       *float64 `json:"warn_at,omitempty"`
	CriticalAt   OptFloat `json:"critical_at,omitzero"`
	EscalateFrom OptFloat `json:"escalate_from,omitzero"`
}

// OptFloat is a JSON number that distinguishes an absent key (Set false) from an explicit null
// (Set true, Value nil).
type OptFloat struct {
	Set   bool
	Value *float64
}

// Some returns an OptFloat set to v.
func Some(v float64) OptFloat { return OptFloat{Set: true, Value: &v} }

// Null returns an OptFloat set to an explicit null.
func Null() OptFloat { return OptFloat{Set: true} }

// IsZero reports whether the key is absent (used by omitzero).
func (o OptFloat) IsZero() bool { return !o.Set }

// MarshalJSON implements json.Marshaler.
func (o OptFloat) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }

// UnmarshalJSON implements json.Unmarshaler; it only runs when the key is present.
func (o *OptFloat) UnmarshalJSON(b []byte) error {
	o.Set = true
	o.Value = nil
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// or returns the override when set, otherwise def.
func (o OptFloat) or(def *float64) *float64 {
	if o.Set {
		return o.Value
	}
	return def
}

// Exclusion records an evaluator that compile() did not ask, and why.
type Exclusion struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// CalibrationEntry is the calibration that applies to an evaluator for the current snapshot.
type CalibrationEntry struct {
	Table   []float64 `json:"table,omitempty"`
	Quality string    `json:"quality,omitempty"`
	// State is a calibration state, or CalibrationOtherSnapshot when the target is calibrated
	// only on other snapshots; such an entry has no table and calibrates nothing (D56).
	State string `json:"state"`
	// FitStateFields are the state fields the calibration was fitted with (D56).
	FitStateFields []string `json:"fit_state_fields,omitempty"`
}

// CalibrationOtherSnapshot is decide-input.v1 CalibrationEntryState "other_snapshot" (D56). It
// never appears in decision.v1 calibration_state.
const CalibrationOtherSnapshot = "other_snapshot"

// Meta describes how the gate ran.
type Meta struct {
	Backend           string  `json:"backend"`
	Stream            string  `json:"stream"`
	EvidencePresent   bool    `json:"evidence_present"`
	EvidenceTruncated bool    `json:"evidence_truncated"`
	KnowledgeOnly     bool    `json:"knowledge_only"`
	SkipReason        *string `json:"skip_reason"`
	// RequestStatus maps each compiled request index ("0", "1", ...) to ok, timeout or
	// error:<kind>.
	RequestStatus map[string]string `json:"request_status"`
	// QIDRequest maps a question id to the index of the request that carried it.
	QIDRequest map[string]int `json:"qid_request"`
	// QuestionErrors maps a question id to a per-question error.
	QuestionErrors map[string]string `json:"question_errors"`
}

// DetectorState is whether a detector ran and fired.
type DetectorState struct {
	Ran   bool `json:"ran"`
	Fired bool `json:"fired"`
}

// Core is the output of decide(): the core of decision/v1 (contracts/schema/decision.v1.schema.json).
// The gate adds the trace, route, detector, compile and usage fields.
type Core struct {
	Status        string           `json:"status"`
	SkipReason    *string          `json:"skip_reason"`
	EffectiveMode string           `json:"effective_mode"`
	Action        string           `json:"action"`
	WouldAction   string           `json:"would_action"`
	Enforced      bool             `json:"enforced"`
	Verdict       *string          `json:"verdict"`
	MaxRisk       *float64         `json:"max_risk"`
	Calibration   string           `json:"calibration"`
	Degraded      *string          `json:"degraded"`
	Disagreement  bool             `json:"disagreement"`
	DeepAudit     string           `json:"deep_audit"`
	Reasons       []string         `json:"reasons"`
	Evaluators    EvaluatorResults `json:"evaluators"`
}

// EvaluatorResult is one evaluator's outcome (decision.v1 EvaluatorResult).
type EvaluatorResult struct {
	Ref                string             `json:"ref"`
	Stage              string             `json:"stage"`
	Internal           bool               `json:"internal"`
	Status             string             `json:"status"`
	Reason             *string            `json:"reason"`
	Type               string             `json:"type"`
	Answer             *RawAnswer         `json:"answer"`
	Probabilities      map[string]float64 `json:"probabilities"`
	RiskRaw            *float64           `json:"risk_raw"`
	Risk               *float64           `json:"risk"`
	ThresholdSpace     string             `json:"threshold_space"`
	Calibrated         bool               `json:"calibrated"`
	CalibrationState   string             `json:"calibration_state"`
	CalibrationWarning *string            `json:"calibration_warning"`
	Level              *string            `json:"level"`
	Capped             *string            `json:"capped"`
	Top                *string            `json:"top"`
	VendorConfidence   *float64           `json:"vendor_confidence"`
	ConfidenceSource   *string            `json:"confidence_source"`
	Escalate           *string            `json:"escalate"`
	Corroborated       *bool              `json:"corroborated"`
	BlockEligible      bool               `json:"block_eligible"`
	Thresholds         ResolvedThresholds `json:"thresholds"`
}

// ResolvedThresholds are the thresholds an evaluator was judged against.
type ResolvedThresholds struct {
	WarnAt       float64  `json:"warn_at"`
	CriticalAt   *float64 `json:"critical_at"`
	EscalateFrom *float64 `json:"escalate_from"`
}

// EvaluatorResults keeps route order; it is a JSON object keyed by ref.
type EvaluatorResults []EvaluatorResult

// Get returns the result for ref.
func (rs EvaluatorResults) Get(ref string) (*EvaluatorResult, bool) {
	for i := range rs {
		if rs[i].Ref == ref {
			return &rs[i], true
		}
	}
	return nil, false
}

// MarshalJSON writes the results as an object keyed by ref, in route order.
func (rs EvaluatorResults) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, r := range rs {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(r.Ref)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON reads an object keyed by ref, keeping the document order.
func (rs *EvaluatorResults) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		*rs = nil
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("evaluator results: want an object")
	}
	out := EvaluatorResults{}
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			return err
		}
		var r EvaluatorResult
		if err := dec.Decode(&r); err != nil {
			return err
		}
		out = append(out, r)
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	*rs = out
	return nil
}

// evalState is the working state of one evaluator inside Decide.
type evalState struct {
	in     *EvaluatorInput
	res    *EvaluatorResult
	active bool // stage active and not internal: a member of A
	status string
	level  string // "" unless assessed
	risk   float64
	calib  bool
	escal  string
}

func (e *evalState) atLeastWarn() bool { return e.level == LevelWarn || e.level == LevelCritical }

// Decide computes the gate decision (SPEC §2.9 decide). It returns an error only for a
// structurally invalid input (unknown mode, missing spec, duplicate ref, bad calibration table).
func Decide(in *Input) (*Core, error) {
	if err := checkInput(in); err != nil {
		return nil, err
	}
	excluded := make(map[string]string, len(in.Excluded))
	for _, x := range in.Excluded {
		excluded[x.Ref] = x.Reason
	}
	qidByRef := make(map[string]string, len(in.QuestionMap))
	for qid, ref := range in.QuestionMap {
		qidByRef[ref] = qid
	}
	fired := func(d string) bool { return in.Detectors[d].Fired }

	// PER EVALUATOR
	evals := make([]*evalState, len(in.Evaluators))
	results := make(EvaluatorResults, len(in.Evaluators))
	for i := range in.Evaluators {
		e := &evalState{in: &in.Evaluators[i], res: &results[i]}
		e.active = e.in.Stage == StageActive && !e.in.Internal
		qid, ok := qidByRef[e.in.Ref]
		if !ok {
			qid = e.in.Spec.QuestionID()
		}
		assessEvaluator(e, in, qid, excluded, fired)
		evals[i] = e
	}

	var active []*evalState
	for _, e := range evals {
		if e.active {
			active = append(active, e)
		}
	}
	byID := func(id string) *evalState {
		for _, e := range active {
			if e.in.Spec.ID == id {
				return e
			}
		}
		return nil
	}

	// AGGREGATE
	critical := map[string]bool{}
	for _, e := range active {
		if e.level == LevelCritical {
			critical[e.in.Spec.ID] = true
		}
	}
	corroborators := map[string]bool{} // detectors that corroborated a block-set member
	corroborated := func(e *evalState) bool {
		req := e.in.Spec.Outcomes.RequiresCorroboration
		ok := len(req) == 0
		for _, x := range req {
			if d, isDet := strings.CutPrefix(x, "det:"); isDet {
				if fired(d) {
					ok = true
				}
			} else if critical[x] {
				ok = true
			}
		}
		return ok
	}
	var blockSet []*evalState
	needsSecond := false
	for _, e := range active {
		if e.level != LevelCritical || !e.in.Spec.Outcomes.BlockEligible {
			continue
		}
		c := corroborated(e)
		e.res.Corroborated = &c
		if c {
			blockSet = append(blockSet, e)
			for _, x := range e.in.Spec.Outcomes.RequiresCorroboration {
				if d, isDet := strings.CutPrefix(x, "det:"); isDet && fired(d) {
					corroborators[d] = true
				}
			}
		} else {
			needsSecond = true
		}
	}
	disagreement := false
	for _, e := range active {
		if e.level != LevelCritical {
			continue
		}
		for _, x := range e.in.Spec.Outcomes.Implies {
			if o := byID(x); o != nil && o.status == EvalAssessed && o.level == LevelOK {
				disagreement = true
			}
		}
	}
	detFlag := slices.ContainsFunc(in.Policy.Detectors.FlagOn, fired)
	detBlock := slices.ContainsFunc(in.Policy.Detectors.BlockOn, fired)

	would := ActionPass
	if detFlag || disagreement || slices.ContainsFunc(active, (*evalState).atLeastWarn) {
		would = ActionFlag
	}
	if len(blockSet) > 0 || detBlock {
		would = ActionBlock
	}

	// NO-EVIDENCE RULE
	noEvidence := false
	if in.Policy.OnNoEvidence != OnNoEvidenceIgnore && noEvidenceApplies(in, evals, active, excluded, fired) {
		would = maxWould(would, in.Policy.OnNoEvidence)
		noEvidence = true
	}

	status := gateStatus(in, active)

	effectiveMode := in.Policy.ConfiguredMode
	streamNotBuffered := false
	if (effectiveMode == ModeBlock || effectiveMode == ModeFallback) && in.Meta.Stream == StreamPassthrough {
		effectiveMode = ModeFlag
		streamNotBuffered = true
	}
	action := actionFor(effectiveMode, would)

	// FAIL
	skip := ""
	if in.Meta.SkipReason != nil {
		skip = *in.Meta.SkipReason
	}
	var degraded *string
	failing := status == StatusSkipped || status == StatusTimeout || status == StatusError
	if status == StatusPartial {
		failing = slices.ContainsFunc(active, func(e *evalState) bool {
			return e.in.Spec.Outcomes.BlockEligible && e.status == EvalError
		})
	}
	failClosed := false
	if failing && skip != SkipSampledOut {
		d := degradedFor(status, skip)
		degraded = &d
		if in.Policy.Fail == FailClosed && (effectiveMode == ModeBlock || effectiveMode == ModeFallback) {
			action = ActionBlock
			failClosed = true
		}
	}

	// CALIBRATION CAP. The reason says why (D56): uncalibrated_snapshot when every uncalibrated
	// member is calibrated only on another snapshot (the decision model moved on), otherwise
	// calibration_missing.
	uncalibratedCap := false
	capReason := ReasonCalibrationMissing
	if (action == ActionBlock || action == ActionFallback) && !failClosed && in.Policy.RequireCalibration && !detBlock {
		var uncalRefs []string
		for _, e := range blockSet {
			if !e.calib {
				uncalRefs = append(uncalRefs, e.in.Ref)
			}
		}
		if noEvidence {
			if fc := findFactualClaims(evals); fc == nil {
				uncalRefs = append(uncalRefs, FactualClaimsRef)
			} else if !fc.calib {
				uncalRefs = append(uncalRefs, fc.in.Ref)
			}
		}
		if len(uncalRefs) > 0 {
			action = ActionFlag
			if degraded == nil {
				d := DegradedUncalibrated
				degraded = &d
			}
			uncalibratedCap = true
			if !slices.ContainsFunc(uncalRefs, func(ref string) bool { return in.Calibration[ref].State != CalibrationOtherSnapshot }) {
				capReason = ReasonUncalibratedSnapshot
			}
		}
	}

	out := &Core{
		Status:        status,
		SkipReason:    in.Meta.SkipReason,
		EffectiveMode: effectiveMode,
		Action:        action,
		WouldAction:   would,
		Enforced:      effectiveMode != ModeObserve,
		Verdict:       verdict(active),
		Calibration:   CalibrationNone,
		Degraded:      degraded,
		Disagreement:  disagreement,
		Evaluators:    results,
	}

	// max_risk and calibration summary over assessed A
	assessed, calibrated := 0, 0
	for _, e := range active {
		if e.status != EvalAssessed {
			continue
		}
		assessed++
		if e.calib {
			calibrated++
		}
		if out.MaxRisk == nil || e.risk > *out.MaxRisk {
			r := e.risk
			out.MaxRisk = &r
		}
	}
	switch {
	case assessed > 0 && calibrated == assessed:
		out.Calibration = CalibrationAll
	case calibrated > 0:
		out.Calibration = CalibrationPartial
	}

	// deep_audit
	groundingTrigger := disagreement
	for _, e := range active {
		if e.in.Spec.VerdictRole != "" && (e.atLeastWarn() || e.escal != "") {
			groundingTrigger = true
		}
	}
	for _, d := range append(slices.Clone(in.Policy.Detectors.FlagOn), in.Policy.Detectors.BlockOn...) {
		if groundingDetectors[d] && fired(d) {
			groundingTrigger = true
		}
	}
	switch {
	case status == StatusSkipped || status == StatusTimeout || status == StatusError:
		out.DeepAudit = DeepAuditSampleError
		if skip == SkipSampledOut {
			out.DeepAudit = DeepAuditSample
		}
	case status == StatusNotAssessed && !in.Meta.EvidencePresent:
		out.DeepAudit = DeepAuditNone
	case groundingTrigger:
		out.DeepAudit = DeepAuditHigh
	default:
		out.DeepAudit = DeepAuditSample
	}

	// reasons
	out.Reasons = levelReasons(active)
	var dets []string
	for d, st := range in.Detectors {
		if st.Fired && (slices.Contains(in.Policy.Detectors.FlagOn, d) || slices.Contains(in.Policy.Detectors.BlockOn, d) || corroborators[d]) {
			dets = append(dets, d)
		}
	}
	SortUTF16(dets)
	for _, d := range dets {
		out.Reasons = append(out.Reasons, "det:"+d)
	}
	for _, r := range []struct {
		on     bool
		reason string
	}{
		{disagreement, ReasonDisagreement},
		{needsSecond, ReasonNeedsSecondSignal},
		{noEvidence, ReasonNoEvidence},
		{streamNotBuffered, ReasonStreamNotBuffered},
		{failClosed, ReasonFailClosed},
		{uncalibratedCap, capReason},
	} {
		if r.on {
			out.Reasons = append(out.Reasons, r.reason)
		}
	}
	return out, nil
}

// assessEvaluator runs the per-evaluator part of decide() and fills e.res.
func assessEvaluator(e *evalState, in *Input, qid string, excluded map[string]string, fired func(string) bool) {
	spec := e.in.Spec
	r := e.res
	*r = EvaluatorResult{
		Ref:              e.in.Ref,
		Stage:            e.in.Stage,
		Internal:         e.in.Internal,
		Type:             spec.Question.Type,
		ThresholdSpace:   e.in.Thresholds.Space,
		CalibrationState: CalibrationNone,
		BlockEligible:    spec.Outcomes.BlockEligible,
	}
	if r.ThresholdSpace == "" {
		r.ThresholdSpace = SpaceRaw
	}
	cal, hasCal := in.Calibration[e.in.Ref]
	if hasCal && cal.State == CalibrationOtherSnapshot {
		cal, hasCal = CalibrationEntry{}, false // calibrated only on other snapshots: nothing applies here
	}
	if hasCal {
		r.CalibrationState = cal.State
		if len(cal.FitStateFields) > 0 && !sameFieldSet(cal.FitStateFields, spec.StateFields) {
			r.CalibrationWarning = ptr(CalibrationWarningStateFields)
		}
	}
	warn := spec.Outcomes.WarnAt
	if e.in.Thresholds.WarnAt != nil {
		warn = *e.in.Thresholds.WarnAt
	}
	crit := e.in.Thresholds.CriticalAt.or(spec.Outcomes.CriticalAt)
	ef := e.in.Thresholds.EscalateFrom.or(spec.Escalation.EscalateFrom)
	r.Thresholds = ResolvedThresholds{WarnAt: warn, CriticalAt: crit, EscalateFrom: ef}

	setError := func(reason string) {
		e.status = EvalError
		r.Status = EvalError
		r.Reason = &reason
		if spec.Escalation.OnError == OnErrorEscalate {
			e.escal = EscalateError
			r.Escalate = ptr(EscalateError)
		}
	}

	if reason, ok := excluded[e.in.Ref]; ok {
		e.status = EvalNotAssessed
		r.Status = EvalNotAssessed
		r.Reason = &reason
		return
	}
	if in.Meta.SkipReason != nil {
		setError(*in.Meta.SkipReason)
		return
	}
	if k, ok := in.Meta.QIDRequest[qid]; ok {
		if st, ok := in.Meta.RequestStatus[strconv.Itoa(k)]; ok && st != RequestOK {
			setError(st)
			return
		}
	}
	if qe, ok := in.Meta.QuestionErrors[qid]; ok {
		setError(qe)
		return
	}
	answer := in.Answers[qid]
	r.Answer = answer
	n := Normalize(answer, spec)
	if n.Error != "" {
		setError(n.Error)
		return
	}

	e.status = EvalAssessed
	r.Status = EvalAssessed
	r.Probabilities = n.Probabilities
	r.RiskRaw = ptr(n.RiskRaw)
	r.VendorConfidence = n.VendorConfidence
	if n.Top != "" {
		r.Top = ptr(n.Top)
	}
	if n.ConfidenceSource != "" {
		r.ConfidenceSource = ptr(n.ConfidenceSource)
	}

	usable := hasCal && (cal.Quality == QualityMeasured || (cal.Quality == QualityMock && in.Meta.Backend == BackendMock))
	risk := n.RiskRaw
	if r.ThresholdSpace == SpaceCalibrated && hasCal {
		risk = ApplyCalibration(cal.Table, n.RiskRaw)
	}
	e.risk = risk
	e.calib = r.ThresholdSpace == SpaceCalibrated && usable
	r.Risk = ptr(risk)
	r.Calibrated = e.calib

	confOK := spec.Outcomes.MinConfidence == nil || max(risk, 1-risk) >= *spec.Outcomes.MinConfidence-EPS
	level := LevelOK
	switch {
	case crit != nil && risk >= *crit-EPS && confOK:
		level = LevelCritical
	case risk >= warn-EPS:
		level = LevelWarn
	}
	if level == LevelCritical && spec.VerdictRole != "" && in.Meta.KnowledgeOnly {
		level = LevelWarn
		r.Capped = ptr(CappedKnowledgeOnly)
	} else if level == LevelCritical && spec.VerdictRole == RoleUnsupported && in.Meta.EvidenceTruncated {
		level = LevelWarn
		r.Capped = ptr(CappedEvidenceTruncated)
	}
	e.level = level
	r.Level = ptr(level)

	switch {
	case ef != nil && *ef-EPS <= risk && risk < warn-EPS:
		e.escal = EscalateUncertain
	case spec.Escalation.NumericOrDate && (fired(DetNumbers) || fired(DetDates)):
		e.escal = EscalateNumericOrDate
	}
	if e.escal != "" {
		r.Escalate = ptr(e.escal)
	}
}

// noEvidenceApplies is the condition of the no-evidence rule (on_no_evidence != ignore is checked
// by the caller): every required active evaluator was excluded for lack of evidence, and either
// factual_claims found checkable facts or the numbers or dates detector fired.
func noEvidenceApplies(in *Input, evals, active []*evalState, excluded map[string]string, fired func(string) bool) bool {
	required := 0
	for _, e := range active {
		if e.in.Spec.Evidence.Requires != EvidenceRequired {
			continue
		}
		required++
		switch excluded[e.in.Ref] {
		case ExcludedNoEvidence, ExcludedEvidenceTooShort, ExcludedEvidencePackedAway:
		default:
			return false
		}
	}
	if required == 0 {
		return false
	}
	if fc := findFactualClaims(evals); fc != nil && fc.status == EvalAssessed && fc.atLeastWarn() {
		return true
	}
	return fired(DetNumbers) || fired(DetDates)
}

// sameFieldSet reports whether two lists name the same state fields (order and repeats aside).
func sameFieldSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// findFactualClaims returns the factual_claims evaluator (internal or attached), if any.
func findFactualClaims(evals []*evalState) *evalState {
	for _, e := range evals {
		if e.in.Spec.ID == FactualClaimsID {
			return e
		}
	}
	return nil
}

var wouldRank = map[string]int{ActionPass: 0, ActionFlag: 1, ActionBlock: 2}

func maxWould(a, b string) string {
	if wouldRank[b] > wouldRank[a] {
		return b
	}
	return a
}

// gateStatus is the decision status (SPEC §2.9 status).
func gateStatus(in *Input, active []*evalState) string {
	if in.Meta.SkipReason != nil {
		return StatusSkipped
	}
	if len(active) > 0 && !slices.ContainsFunc(active, func(e *evalState) bool { return e.status != EvalNotAssessed }) {
		return StatusNotAssessed
	}
	if len(in.Meta.RequestStatus) > 0 {
		allFailed, anyTimeout := true, false
		for _, st := range in.Meta.RequestStatus {
			if st == RequestOK {
				allFailed = false
			}
			if st == RequestTimeout {
				anyTimeout = true
			}
		}
		if allFailed {
			if anyTimeout {
				return StatusTimeout
			}
			return StatusError
		}
	}
	if slices.ContainsFunc(active, func(e *evalState) bool { return e.status == EvalError }) {
		return StatusPartial
	}
	return StatusOK
}

// actionFor maps the would-action to the effective mode's action.
func actionFor(mode, would string) string {
	switch mode {
	case ModeObserve:
		return ActionObserve
	case ModeFlag:
		if would == ActionPass {
			return ActionPass
		}
		return ActionFlag
	case ModeFallback:
		if would == ActionBlock {
			return ActionFallback
		}
	}
	return would
}

// degradedFor maps a failing status (or, for skipped, its skip reason) to decision.degraded.
func degradedFor(status, skip string) string {
	key := status
	if status == StatusSkipped {
		key = skip
	}
	switch key {
	case "budget":
		return DegradedBudget
	case "breaker_open":
		return DegradedBreaker
	case "rate_limited":
		return DegradedRateLimited
	case "decision_model_off", "credentials_unavailable":
		return DegradedOff
	case "overload":
		return DegradedOverload
	case StatusTimeout:
		return DegradedTimeout
	}
	return DegradedError
}

// verdict is the gate verdict over the active evaluators with a verdict role.
func verdict(active []*evalState) *string {
	var rb []*evalState
	for _, e := range active {
		if e.in.Spec.VerdictRole != "" {
			rb = append(rb, e)
		}
	}
	role := func(r string) func(*evalState) bool {
		return func(e *evalState) bool { return e.in.Spec.VerdictRole == r && e.atLeastWarn() }
	}
	switch {
	case len(rb) == 0:
		return nil
	case slices.ContainsFunc(rb, role(RoleContradicted)):
		return ptr(VerdictContradicted)
	case slices.ContainsFunc(rb, role(RoleUnsupported)):
		return ptr(VerdictUnsupported)
	case slices.ContainsFunc(rb, func(e *evalState) bool { return e.status == EvalError }):
		return ptr(VerdictUnknown)
	case !slices.ContainsFunc(rb, func(e *evalState) bool { return e.status != EvalNotAssessed }):
		return ptr(VerdictNotAssessed)
	}
	return ptr(VerdictSupported)
}

// levelReasons lists "<id>:<level>" for active evaluators at warn or critical: critical first,
// then risk descending, then id ascending.
func levelReasons(active []*evalState) []string {
	var hits []*evalState
	for _, e := range active {
		if e.atLeastWarn() {
			hits = append(hits, e)
		}
	}
	slices.SortStableFunc(hits, func(a, b *evalState) int {
		if (a.level == LevelCritical) != (b.level == LevelCritical) {
			if a.level == LevelCritical {
				return -1
			}
			return 1
		}
		if a.risk != b.risk {
			if a.risk > b.risk {
				return -1
			}
			return 1
		}
		return CompareUTF16(a.in.Spec.ID, b.in.Spec.ID)
	})
	reasons := make([]string, 0, len(hits))
	for _, e := range hits {
		reasons = append(reasons, e.in.Spec.ID+":"+e.level)
	}
	return reasons
}

// checkInput rejects inputs decide() cannot interpret.
func checkInput(in *Input) error {
	switch in.Policy.ConfiguredMode {
	case ModeObserve, ModeFlag, ModeBlock, ModeFallback:
	default:
		return fmt.Errorf("decide: unknown configured_mode %q", in.Policy.ConfiguredMode)
	}
	switch in.Policy.Fail {
	case FailOpen, FailClosed:
	default:
		return fmt.Errorf("decide: unknown fail mode %q", in.Policy.Fail)
	}
	switch in.Policy.OnNoEvidence {
	case OnNoEvidenceIgnore, OnNoEvidenceFlag, OnNoEvidenceBlock:
	default:
		return fmt.Errorf("decide: unknown on_no_evidence %q", in.Policy.OnNoEvidence)
	}
	seen := make(map[string]bool, len(in.Evaluators))
	for _, e := range in.Evaluators {
		if e.Spec == nil {
			return fmt.Errorf("decide: evaluator %s has no spec", e.Ref)
		}
		if seen[e.Ref] {
			return fmt.Errorf("decide: duplicate evaluator %s", e.Ref)
		}
		seen[e.Ref] = true
		if e.Stage != StageActive && e.Stage != StageShadow {
			return fmt.Errorf("decide: evaluator %s: unknown stage %q", e.Ref, e.Stage)
		}
		if s := e.Thresholds.Space; s != "" && s != SpaceRaw && s != SpaceCalibrated {
			return fmt.Errorf("decide: evaluator %s: unknown threshold space %q", e.Ref, s)
		}
	}
	for ref, c := range in.Calibration {
		if c.State == CalibrationOtherSnapshot {
			if len(c.Table) > 0 {
				return fmt.Errorf("decide: calibration for %s: an other_snapshot entry has no table", ref)
			}
			continue
		}
		if err := CheckCalibrationTable(c.Table); err != nil {
			return fmt.Errorf("decide: calibration for %s: %w", ref, err)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
