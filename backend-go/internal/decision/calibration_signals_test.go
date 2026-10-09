package decision_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/decision/vectors"
)

// TestDecisionNeverReportsOtherSnapshot: whatever the calibration input, decision.v1
// calibration_state never takes the decide-input-only value other_snapshot (D56). Every
// decide vector is replayed as written and with each evaluator calibrated only on another snapshot.
func TestDecisionNeverReportsOtherSnapshot(t *testing.T) {
	all, err := vectors.Load(contracts.FS, "vectors")
	if err != nil {
		t.Fatal(err)
	}
	runs, snapshotCaps := 0, 0
	for _, v := range all {
		if v.Kind != "decide" {
			continue
		}
		for _, moved := range []bool{false, true} {
			var in decision.Input
			if err := json.Unmarshal(v.Input, &in); err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			if moved {
				in.Calibration = map[string]decision.CalibrationEntry{}
				for _, e := range in.Evaluators {
					in.Calibration[e.Ref] = decision.CalibrationEntry{State: decision.CalibrationOtherSnapshot}
				}
			}
			d, err := decision.Decide(&in)
			if err != nil {
				t.Fatalf("%s (moved %v): %v", v.Name, moved, err)
			}
			runs++
			b, _ := json.Marshal(d)
			if strings.Contains(string(b), decision.CalibrationOtherSnapshot) {
				t.Errorf("%s (moved %v): the decision mentions other_snapshot", v.Name, moved)
			}
			if !moved {
				continue
			}
			for _, r := range d.Evaluators {
				if r.CalibrationState != "none" || r.Calibrated || r.CalibrationWarning != nil {
					t.Errorf("%s: %s is calibrated on this snapshot: %+v", v.Name, r.Ref, r)
				}
			}
			// With every evaluator calibrated on another snapshot a capped block names the snapshot
			// change, unless the cap also counted a factual_claims that the input does not list (the
			// no-evidence rule), which has no entry and so is missing.
			if slices.Contains(d.Reasons, decision.ReasonUncalibratedSnapshot) {
				snapshotCaps++
			}
			if slices.Contains(d.Reasons, decision.ReasonCalibrationMissing) {
				attached := slices.ContainsFunc(in.Evaluators, func(e decision.EvaluatorInput) bool { return e.Ref == decision.FactualClaimsRef })
				if attached || !slices.Contains(d.Reasons, "no_evidence") {
					t.Errorf("%s: calibration_missing although every evaluator is calibrated on another snapshot: %v", v.Name, d.Reasons)
				}
			}
		}
	}
	if runs < 100 || snapshotCaps < 5 {
		t.Fatalf("only %d decide runs, %d of them capped for a snapshot change", runs, snapshotCaps)
	}
}
