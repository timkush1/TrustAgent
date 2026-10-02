package decision

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/truthtable/contracts"
)

func TestOptFloatJSON(t *testing.T) {
	var th Thresholds
	if err := json.Unmarshal([]byte(`{"space":"raw","critical_at":null,"escalate_from":0.5}`), &th); err != nil {
		t.Fatal(err)
	}
	if !th.CriticalAt.Set || th.CriticalAt.Value != nil || !th.EscalateFrom.Set || *th.EscalateFrom.Value != 0.5 || th.WarnAt != nil {
		t.Errorf("thresholds = %+v", th)
	}
	spec := 0.95
	if got := th.CriticalAt.or(&spec); got != nil {
		t.Errorf("an explicit null must override the spec value, got %v", *got)
	}
	if got := (OptFloat{}).or(&spec); got == nil || *got != 0.95 {
		t.Error("an absent override must fall back to the spec value")
	}
	b, err := json.Marshal(Thresholds{Space: SpaceRaw, CriticalAt: Null(), EscalateFrom: Some(0.4)})
	if err != nil || string(b) != `{"space":"raw","critical_at":null,"escalate_from":0.4}` {
		t.Errorf("marshal = %s, %v", b, err)
	}
	if b, _ := json.Marshal(Thresholds{Space: SpaceRaw}); string(b) != `{"space":"raw"}` {
		t.Errorf("absent overrides must be omitted, got %s", b)
	}
}

func TestEvaluatorResultsJSONKeepsOrder(t *testing.T) {
	rs := EvaluatorResults{{Ref: "zeta@1", Status: EvalAssessed}, {Ref: "alpha@1", Status: EvalNotAssessed}}
	b, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), `{"zeta@1":{"ref":"zeta@1"`) || !strings.Contains(string(b), `,"alpha@1":{`) {
		t.Errorf("marshal = %s", b)
	}
	var back EvaluatorResults
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, rs) {
		t.Errorf("round trip = %+v", back)
	}
	if r, ok := back.Get("alpha@1"); !ok || r.Status != EvalNotAssessed {
		t.Error("Get(alpha@1) failed")
	}
	if _, ok := back.Get("missing@1"); ok {
		t.Error("Get(missing@1) found something")
	}
	var null EvaluatorResults
	if err := json.Unmarshal([]byte(`null`), &null); err != nil || null != nil {
		t.Errorf("null: %v %v", null, err)
	}
	if err := json.Unmarshal([]byte(`[]`), &null); err == nil {
		t.Error("an array must be rejected")
	}
}

// TestCoreCarriesDecisionV1Keys checks the JSON of Core and EvaluatorResult against the required keys
// of contracts/schema/decision.v1.schema.json, so the Go structs cannot drift from the contract.
func TestCoreCarriesDecisionV1Keys(t *testing.T) {
	b, err := contracts.FS.ReadFile("schema/decision.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required []string `json:"required"`
		Defs     map[string]struct {
			Required []string `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	specs := loadTemplates(t)
	in := s0Input(specs, 0.96, 0.98, true)
	core, err := Decide(in)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	out, _ := json.Marshal(core)
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	coreKeys := []string{"status", "skip_reason", "effective_mode", "action", "would_action", "enforced", "verdict", "max_risk",
		"calibration", "degraded", "disagreement", "deep_audit", "reasons", "evaluators"}
	for _, k := range coreKeys {
		if !slices.Contains(schema.Required, k) {
			t.Errorf("%s is not a decision/v1 key", k)
		}
	}
	if len(got) != len(coreKeys) {
		t.Errorf("Core has %d keys, want the %d core keys", len(got), len(coreKeys))
	}
	for _, k := range coreKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("Core JSON lacks %s", k)
		}
	}
	res := got["evaluators"].(map[string]any)["groundedness@1"].(map[string]any)
	want := schema.Defs["EvaluatorResult"].Required
	if len(want) == 0 {
		t.Fatal("decision.v1 has no EvaluatorResult.required")
	}
	for _, k := range want {
		if _, ok := res[k]; !ok {
			t.Errorf("EvaluatorResult JSON lacks %s", k)
		}
	}
	if len(res) != len(want) {
		t.Errorf("EvaluatorResult has %d keys, decision.v1 requires %d", len(res), len(want))
	}
}

func TestDecideRejectsInvalidInput(t *testing.T) {
	specs := loadTemplates(t)
	mutations := map[string]func(*Input){
		"mode":           func(in *Input) { in.Policy.ConfiguredMode = "enforce" },
		"fail":           func(in *Input) { in.Policy.Fail = "maybe" },
		"on_no_evidence": func(in *Input) { in.Policy.OnNoEvidence = "warn" },
		"no spec":        func(in *Input) { in.Evaluators[0].Spec = nil },
		"duplicate":      func(in *Input) { in.Evaluators[1] = in.Evaluators[0] },
		"stage":          func(in *Input) { in.Evaluators[0].Stage = "" },
		"space":          func(in *Input) { in.Evaluators[0].Thresholds.Space = "log" },
		"calibration": func(in *Input) {
			in.Calibration = map[string]CalibrationEntry{"groundedness@1": {Table: []float64{0, 1}, Quality: QualityMeasured, State: "measured"}}
		},
		"other_snapshot with a table": func(in *Input) {
			in.Calibration = map[string]CalibrationEntry{"groundedness@1": {Table: []float64{0, 0.5, 1}, State: CalibrationOtherSnapshot}}
		},
	}
	for name, mutate := range mutations {
		in := s0Input(specs, 0.1, 0.1, false)
		mutate(in)
		if _, err := Decide(in); err == nil {
			t.Errorf("%s: Decide succeeded, want an error", name)
		}
	}
}

// An other_snapshot entry (D56) carries no table and calibrates nothing on this snapshot:
// decide() accepts it and decision.v1 calibration_state never takes the value other_snapshot.
func TestDecideOtherSnapshotEntryCalibratesNothing(t *testing.T) {
	specs := loadTemplates(t)
	in := s0Input(specs, 0.1, 0.1, false)
	in.Calibration = map[string]CalibrationEntry{"groundedness@1": {State: CalibrationOtherSnapshot}}
	d, err := Decide(in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	for _, r := range d.Evaluators {
		if r.CalibrationState == CalibrationOtherSnapshot || r.Calibrated {
			t.Errorf("%s: calibration_state %q calibrated %v, want no calibration", r.Ref, r.CalibrationState, r.Calibrated)
		}
	}
}

// s0Input is the support-bot decide() input of the S0 scenarios (flag mode, groundedness@1 and
// contradiction@1, mock backend, no calibration).
func s0Input(specs map[string]*Spec, g, c float64, numbersFired bool) *Input {
	return &Input{
		Policy: Policy{
			ConfiguredMode: ModeFlag, Fail: FailOpen, RequireCalibration: true, OnNoEvidence: OnNoEvidenceIgnore,
			Detectors: DetectorPolicy{FlagOn: []string{DetURLs, DetQuotes, DetCitation, DetSecret, DetPromptLeak}},
		},
		Evaluators: []EvaluatorInput{
			{Ref: "groundedness@1", Spec: specs["groundedness@1"], Stage: StageActive, Thresholds: Thresholds{Space: SpaceRaw}},
			{Ref: "contradiction@1", Spec: specs["contradiction@1"], Stage: StageActive, Thresholds: Thresholds{Space: SpaceRaw}},
		},
		Answers: map[string]*RawAnswer{
			"groundedness__v1":  {Type: QuestionNoul, Noul: &g},
			"contradiction__v1": {Type: QuestionNoul, Noul: &c},
		},
		QuestionMap: map[string]string{"groundedness__v1": "groundedness@1", "contradiction__v1": "contradiction@1"},
		Meta: Meta{
			Backend: BackendMock, Stream: "none", EvidencePresent: true,
			RequestStatus: map[string]string{"0": RequestOK},
			QIDRequest:    map[string]int{"groundedness__v1": 0, "contradiction__v1": 0},
		},
		Detectors: map[string]DetectorState{DetNumbers: {Ran: true, Fired: numbersFired}},
	}
}

// TestS0ScenariosEndToEnd chains compile() and decide() for the S0 demo scenarios in
// contracts/demo/scenarios.v1.json and checks the lead's hand-derived expectations. Evidence
// extraction and the detectors land in P2, so the extracted evidence and the detector outcome of
// each scenario are given here (SPEC §2.10 applied to the scenario request by hand).
func TestS0ScenariosEndToEnd(t *testing.T) {
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []struct {
			ID       string                `json:"id"`
			Sets     []string              `json:"sets"`
			Response string                `json:"response"`
			Mock     map[string]*RawAnswer `json:"mock_answers"`
			Request  struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			} `json:"request"`
			Expect struct {
				Mode        string   `json:"mode"`
				Action      string   `json:"action"`
				WouldAction string   `json:"would_action"`
				Status      string   `json:"status"`
				Verdict     string   `json:"verdict"`
				MaxRisk     *float64 `json:"max_risk"`
				Reasons     []string `json:"reasons"`
				DeepAudit   string   `json:"deep_audit"`
			} `json:"expect"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	evidence := map[string][]EvidenceItem{
		"grounded-pass":             {refundItem},
		"refund-contradiction-flag": {refundItem},
		"no-evidence-not-assessed":  nil,
	}
	numbersFired := map[string]bool{"refund-contradiction-flag": true}

	specs := loadTemplates(t)
	seen := 0
	for _, sc := range pack.Scenarios {
		if !slices.Contains(sc.Sets, "s0") {
			continue
		}
		seen++
		items, ok := evidence[sc.ID]
		if !ok {
			t.Fatalf("scenario %s: no hand-extracted evidence in this test", sc.ID)
		}
		var user string
		for _, m := range sc.Request.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		route := testRoute("groundedness@1", "contradiction@1")
		c, err := Compile(route, specs, &CompileInputs{UserRequest: user, AssistantResponse: sc.Response, Evidence: items}, DefaultBudget())
		if err != nil {
			t.Fatalf("%s: compile: %v", sc.ID, err)
		}
		// The mock decision backend answers exactly the compiled questions.
		for qid := range c.QuestionMap {
			if sc.Mock[qid] == nil {
				t.Errorf("%s: no mock answer for compiled question %s", sc.ID, qid)
			}
		}
		if len(c.QuestionMap) != len(sc.Mock) {
			t.Errorf("%s: compiled %d questions, the scenario scripts %d answers", sc.ID, len(c.QuestionMap), len(sc.Mock))
		}
		evs, err := c.DecideEvaluators(specs, nil)
		if err != nil {
			t.Fatal(err)
		}
		requestStatus := map[string]string{}
		for k := range c.Requests {
			requestStatus[strconv.Itoa(k)] = RequestOK
		}
		in := &Input{
			Policy:      s0Input(specs, 0, 0, false).Policy,
			Evaluators:  evs,
			Answers:     sc.Mock,
			QuestionMap: c.QuestionMap,
			Excluded:    c.Excluded,
			Meta: Meta{
				Backend: BackendMock, Stream: "none", EvidencePresent: len(items) > 0, EvidenceTruncated: c.Truncated,
				RequestStatus: requestStatus, QIDRequest: c.QIDRequest,
			},
			Detectors: map[string]DetectorState{DetNumbers: {Ran: len(items) > 0, Fired: numbersFired[sc.ID]}},
		}
		if len(c.Requests) == 0 {
			in.Answers = nil
		}
		d, err := Decide(in)
		if err != nil {
			t.Fatalf("%s: decide: %v", sc.ID, err)
		}
		exp := sc.Expect
		if d.EffectiveMode != exp.Mode || d.Action != exp.Action || d.WouldAction != exp.WouldAction || d.Status != exp.Status ||
			deref(d.Verdict) != exp.Verdict || d.DeepAudit != exp.DeepAudit {
			t.Errorf("%s: decision %s/%s/would %s/%s/%s/%s, scenario expects %s/%s/would %s/%s/%s/%s", sc.ID,
				d.EffectiveMode, d.Action, d.WouldAction, d.Status, deref(d.Verdict), d.DeepAudit,
				exp.Mode, exp.Action, exp.WouldAction, exp.Status, exp.Verdict, exp.DeepAudit)
		}
		switch {
		case exp.MaxRisk == nil && d.MaxRisk != nil, exp.MaxRisk != nil && (d.MaxRisk == nil || FormatRisk(*d.MaxRisk) != FormatRisk(*exp.MaxRisk)):
			t.Errorf("%s: max_risk %v, scenario expects %v", sc.ID, d.MaxRisk, exp.MaxRisk)
		}
		for _, r := range exp.Reasons {
			if !slices.Contains(d.Reasons, r) {
				t.Errorf("%s: reasons %v lack %s", sc.ID, d.Reasons, r)
			}
		}
	}
	if seen != 3 {
		t.Errorf("found %d s0 scenarios, want 3", seen)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
