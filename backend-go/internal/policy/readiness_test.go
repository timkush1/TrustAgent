package policy

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
)

// specsWith is the built-in registry plus test specs.
type specsWith struct {
	base  Resolver
	extra map[string]*decision.Spec
}

func (s specsWith) Get(ref string) (*decision.Spec, bool) {
	if sp, ok := s.extra[ref]; ok {
		return sp, true
	}
	return s.base.Get(ref)
}

// lonelySpec is block-eligible and corroborated only by an evaluator named "partner".
const lonelySpec = `{"spec_version":"evaluator/v1","id":"lonely","version":1,"name":"Lonely","category":"custom",
  "state_fields":["assistant_response"],"question":{"type":"noul","instructions":"Is it bad?","criteria":{"true":"Bad","false":"Fine"}},
  "evidence":{"requires":"none"},"signal":{"invert":false},
  "outcomes":{"warn_at":0.6,"critical_at":0.9,"block_eligible":true,"requires_corroboration":["partner"]}}`

func testSpecs(t *testing.T) specsWith {
	t.Helper()
	s, err := decision.ParseSpec([]byte(lonelySpec))
	if err != nil {
		t.Fatal(err)
	}
	return specsWith{base: registry(t), extra: map[string]*decision.Spec{"lonely@1": s}}
}

// calTable is a valid identity calibration table.
func calTable() []float64 {
	t := make([]float64, decision.CalibrationTableSize)
	for i := range t {
		t[i] = float64(i) / 100
	}
	return t
}

// fakeCalibrations answers per ref.
type fakeCalibrations map[string]CalibrationStatus

func (f fakeCalibrations) Lookup(ref string, _ map[string]string, _ string) CalibrationStatus {
	if c, ok := f[ref]; ok {
		return c
	}
	return CalibrationStatus{State: CalibrationNone}
}

func measured() CalibrationStatus {
	return CalibrationStatus{State: CalibrationMeasured, Quality: CalibrationMeasured, Table: calTable()}
}

func readinessOf(t *testing.T, doc string, rt Runtime) map[string][]BlockReadiness {
	t.Helper()
	specs := testSpecs(t)
	e := Env{Seeds: DefaultSeeds(), Specs: specs, Enforcing: true}
	return mustCheck(t, doc, e).BlockReadiness(specs, e, rt)
}

func missing(rs []BlockReadiness, ref string) []string {
	for _, r := range rs {
		if r.Evaluator == ref {
			return r.Missing
		}
	}
	return nil
}

// TestBlockReadiness walks SPEC §7.2 items 1-4 for block-eligible evaluators.
func TestBlockReadiness(t *testing.T) {
	calibrated := Runtime{Backend: "openrouter-decisions", Calibrations: fakeCalibrations{"groundedness@1": measured(), "contradiction@1": measured(), "lonely@1": measured()}}
	cases := []struct {
		name string
		doc  string
		rt   Runtime
		ref  string
		want []string
	}{
		{"flag route lacks mode", "version: 1\ndefaults: {mode: flag}\n", Runtime{}, "groundedness@1",
			[]string{MissingMode, MissingCalibration, MissingBuffering}},
		{"ready", `version: 1
defaults:
  mode: block
  streaming: buffer
  evaluators:
    - {ref: groundedness@1, thresholds: {space: calibrated}}
    - {ref: contradiction@1, thresholds: {space: calibrated}}
`, calibrated, "groundedness@1", []string{}},
		{"calibration not required", "version: 1\ndefaults: {mode: block, streaming: buffer, require_calibration: false}\n", Runtime{}, "contradiction@1", []string{}},
		{"raw space is not calibrated", "version: 1\ndefaults: {mode: block, streaming: buffer}\n", calibrated, "groundedness@1", []string{MissingCalibration}},
		{"critical_at null cannot block", `version: 1
defaults:
  mode: block
  streaming: buffer
  require_calibration: false
  evaluators: [{ref: contradiction@1, thresholds: {critical_at: null}}, {ref: groundedness@1}]
`, Runtime{}, "contradiction@1", []string{MissingCalibration}},
		{"mock quality needs the mock backend", `version: 1
defaults: {mode: block, streaming: buffer, evaluators: [{ref: groundedness@1, thresholds: {space: calibrated}}]}
`, Runtime{Backend: "openrouter-decisions", Calibrations: fakeCalibrations{"groundedness@1": {State: CalibrationMock, Quality: CalibrationMock, Table: calTable()}}},
			"groundedness@1", []string{MissingCalibration}},
		{"mock quality on the mock backend", `version: 1
defaults: {mode: block, streaming: buffer, evaluators: [{ref: groundedness@1, thresholds: {space: calibrated}}]}
`, Runtime{Backend: "mock", Calibrations: fakeCalibrations{"groundedness@1": {State: CalibrationMock, Quality: CalibrationMock, Table: calTable()}}},
			"groundedness@1", []string{}},
		{"no evidence sources", `version: 1
defaults: {mode: block, streaming: buffer, require_calibration: false, evidence: {sources: []}}
`, Runtime{}, "groundedness@1", []string{MissingEvidence, MissingCorroborator}},
		{"system prompt off yields nothing", `version: 1
defaults: {mode: block, streaming: buffer, require_calibration: false, evidence: {sources: [request_system], system_mode: "off"}}
`, Runtime{}, "contradiction@1", []string{MissingEvidence, MissingCorroborator}},
		{"rules make numbers reachable", `version: 1
defaults:
  mode: block
  streaming: buffer
  require_calibration: false
  evidence: {sources: []}
  evaluators: [{ref: commitments@1}]
`, Runtime{}, "commitments@1", []string{}},
		{"corroborator not attached", `version: 1
defaults: {mode: block, streaming: buffer, require_calibration: false, evaluators: [{ref: lonely@1}]}
`, Runtime{}, "lonely@1", []string{MissingCorroborator}},
		{"shadow corroborator does not count", `version: 1
defaults: {mode: block, streaming: buffer, require_calibration: false, evaluators: [{ref: lonely@1}, {ref: partner_x@1, stage: shadow}]}
`, Runtime{}, "", nil},
		{"passthrough needs buffering", "version: 1\ndefaults: {mode: fallback, require_calibration: false}\n", Runtime{}, "pii_secret_leak@1", []string{MissingBuffering}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.ref == "" {
				// partner_x is not registered: the document itself is invalid.
				if res := Check([]byte(c.doc), Env{Seeds: DefaultSeeds(), Specs: testSpecs(t), Enforcing: true}); res.Policy != nil {
					t.Fatal("want an unknown evaluator error")
				}
				return
			}
			got := missing(readinessOf(t, c.doc, c.rt)[DefaultRouteName], c.ref)
			if got == nil || !slices.Equal(got, c.want) {
				t.Errorf("%s missing %v, want %v", c.ref, got, c.want)
			}
		})
	}
	// Only active, enabled, block-eligible evaluators get a checklist.
	rs := readinessOf(t, `version: 1
defaults:
  evaluators: [{ref: refusal@1}, {ref: groundedness@1, stage: shadow}, {ref: contradiction@1, enabled: false}, {ref: pii_secret_leak@1}]
`, Runtime{})[DefaultRouteName]
	if len(rs) != 1 || rs[0].Evaluator != "pii_secret_leak@1" || rs[0].Ready {
		t.Errorf("readiness %+v", rs)
	}
}

func downgradeCodes(er EffectiveRoute) []string {
	var out []string
	for _, d := range er.Downgrades {
		out = append(out, d.Code)
	}
	return out
}

func effective(t *testing.T, doc string, rt Runtime) EffectiveRoute {
	t.Helper()
	specs := testSpecs(t)
	e := Env{Seeds: DefaultSeeds(), Specs: specs, Enforcing: true}
	ers := mustCheck(t, doc, e).EffectiveRoutes(specs, e, rt)
	return ers[len(ers)-1]
}

// TestEffectiveModeAndDowngrades: a block or fallback route that can only flag says so, with a fix.
func TestEffectiveModeAndDowngrades(t *testing.T) {
	calibrated := fakeCalibrations{"groundedness@1": measured(), "contradiction@1": measured()}
	blockDoc := `version: 1
defaults:
  mode: block
  streaming: buffer
  evidence: {sources: [request_system, tool_results]}
  evaluators:
    - {ref: groundedness@1, thresholds: {space: calibrated}}
    - {ref: contradiction@1, thresholds: {space: calibrated}}
`
	cases := []struct {
		name      string
		doc       string
		rt        Runtime
		effective string
		codes     []string
	}{
		{"ready block route", blockDoc, Runtime{Backend: "openrouter-decisions", Calibrations: calibrated}, decision.ModeBlock, nil},
		{"uncalibrated", blockDoc, Runtime{Backend: "openrouter-decisions"}, decision.ModeFlag, []string{DowngradeUncalibrated}},
		{"stale calibration", blockDoc, Runtime{Calibrations: fakeCalibrations{"groundedness@1": {State: CalibrationNone, Stale: true}, "contradiction@1": measured()}},
			decision.ModeBlock, []string{DowngradeSnapshotChanged}},
		{"detector block survives the cap", strings.Replace(blockDoc, "streaming: buffer", "streaming: buffer\n  detectors: {block_on: [urls_not_in_evidence]}", 1),
			Runtime{}, decision.ModeBlock, []string{DowngradeUncalibrated}},
		{"unreachable detector does not", strings.Replace(blockDoc, "streaming: buffer\n  evidence: {sources: [request_system, tool_results]}",
			"streaming: buffer\n  evidence: {sources: []}\n  detectors: {block_on: [urls_not_in_evidence]}", 1),
			Runtime{}, decision.ModeFlag, []string{DowngradeUncalibrated}},
		{"passthrough", strings.Replace(blockDoc, "streaming: buffer", "streaming: passthrough", 1), Runtime{Calibrations: calibrated},
			decision.ModeFlag, []string{DowngradeStreamNotBuffered}},
		{"decision model off, fail open", blockDoc, Runtime{Backend: "off", Calibrations: calibrated}, decision.ModeFlag, []string{DowngradeDecisionModelOff}},
		{"decision model off, fail closed", strings.Replace(blockDoc, "mode: block", "mode: block\n  fail: closed", 1),
			Runtime{Backend: "off", Calibrations: calibrated}, decision.ModeBlock, []string{DowngradeDecisionModelOff}},
		{"breaker", blockDoc, Runtime{BreakerOpen: true, Calibrations: calibrated}, decision.ModeFlag, []string{DowngradeBreakerOpen}},
		{"budget", blockDoc, Runtime{BudgetExhausted: true, Calibrations: calibrated}, decision.ModeFlag, []string{DowngradeBudget}},
		{"trusts user context", strings.Replace(blockDoc, "[request_system, tool_results]", "[request_system, request_context]", 1),
			Runtime{Calibrations: calibrated}, decision.ModeBlock, []string{DowngradeTrustsUserContext}},
		{"no evidence rule, uncalibrated", "version: 1\ndefaults: {mode: block, streaming: buffer, on_no_evidence: block, evidence: {sources: [extension]}, evaluators: [{ref: refusal@1}]}\n",
			Runtime{}, decision.ModeFlag, []string{DowngradeUncalibrated}},
		{"no evidence rule without the requirement", "version: 1\ndefaults: {mode: block, streaming: buffer, require_calibration: false, on_no_evidence: block, evidence: {sources: [extension]}, evaluators: [{ref: refusal@1}]}\n",
			Runtime{}, decision.ModeBlock, nil},
		{"observe route", "version: 1\n", Runtime{Backend: "off"}, decision.ModeObserve, []string{DowngradeDecisionModelOff}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			er := effective(t, c.doc, c.rt)
			if er.EffectiveMode != c.effective || !reflect.DeepEqual(downgradeCodes(er), c.codes) {
				t.Errorf("effective %s downgrades %v, want %s %v", er.EffectiveMode, downgradeCodes(er), c.effective, c.codes)
			}
			for _, d := range er.Downgrades {
				if d.Message == "" || d.Fix == nil || d.Fix.Target == "" {
					t.Errorf("downgrade %+v lacks a message or a fix", d)
				}
				// Both calibration downgrades link to the evaluator's calibration tab, which the
				// dashboard's evaluators/$id route opens from ?tab=calibration.
				if d.Code == DowngradeUncalibrated || d.Code == DowngradeSnapshotChanged {
					id := "groundedness"
					if strings.Contains(c.doc, "on_no_evidence: block") {
						id = decision.FactualClaimsID
					}
					if d.Fix.Kind != "link" || d.Fix.Target != "/app/evaluators/"+id+"?tab=calibration" {
						t.Errorf("%s fix %+v, want a link to %s's calibration tab", d.Code, d.Fix, id)
					}
				}
			}
		})
	}

	// Fix targets name the route path; evaluator entries carry resolved thresholds.
	doc := `version: 1
routes:
  - name: pay
    match: {}
    mode: block
    evaluators: [{ref: groundedness@1, thresholds: {warn_at: 0.7}}, {ref: refusal@1, stage: shadow}, {ref: contradiction@1, enabled: false}]
`
	specs := testSpecs(t)
	e := Env{Seeds: DefaultSeeds(), Specs: specs, Enforcing: true}
	ers := mustCheck(t, doc, e).EffectiveRoutes(specs, e, Runtime{})
	pay := ers[0]
	var fixes []string
	for _, d := range pay.Downgrades {
		fixes = append(fixes, d.Fix.Kind+" "+d.Fix.Target)
	}
	want := []string{"link /app/evaluators/groundedness?tab=calibration", "action set:routes[0].streaming=buffer",
		"action remove:routes[0].evidence.sources=request_context"}
	if !reflect.DeepEqual(fixes, want) {
		t.Errorf("fixes %v, want %v", fixes, want)
	}
	if len(pay.Evaluators) != 2 || pay.Evaluators[0].WarnAt != 0.7 || *pay.Evaluators[0].CriticalAt != 0.95 ||
		pay.Evaluators[1].Stage != decision.StageShadow || pay.Evaluators[0].CalibrationState != CalibrationNone || !pay.Evaluators[0].Resolved {
		t.Errorf("evaluators %+v", pay.Evaluators)
	}
	if ers[1].Name != DefaultRouteName || pay.Match.Paths != nil {
		t.Errorf("routes %s %+v", ers[1].Name, pay.Match)
	}
	// RouteSettings serializes as policy/v1 RouteSettings.
	b, err := json.Marshal(pay.Settings)
	if err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, "policy.v1.schema.json#/$defs/RouteSettings", b)
}

// TestDowngradeMessagesMatchCopy: the downgrade messages are contracts/ui/copy.v1.json's, and
// every code has one.
func TestDowngradeMessagesMatchCopy(t *testing.T) {
	b, err := contracts.FS.ReadFile("ui/copy.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Copy struct {
			Downgrades map[string]string `json:"downgrades"`
		} `json:"copy"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Copy.Downgrades, downgradeMessages) {
		t.Errorf("downgrade messages differ from contracts/ui/copy.v1.json:\ncopy: %v\ncode: %v", doc.Copy.Downgrades, downgradeMessages)
	}
	for _, code := range []string{DowngradeStreamNotBuffered, DowngradeUncalibrated, DowngradeSnapshotChanged, DowngradeBudget,
		DowngradeBreakerOpen, DowngradeDecisionModelOff, DowngradeTrustsUserContext} {
		if downgradeMessages[code] == "" {
			t.Errorf("no message for %s", code)
		}
	}
}
