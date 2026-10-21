package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/contracts"
)

// s0Scenarios pins the three walking-skeleton scenarios byte for byte (sha256 of each scenario's
// JSON as the pack spells it): the S0 exit gate, the dashboard fixtures and the engine tests read
// them, so they never change; new stories are new scenarios.
var s0Scenarios = map[string]string{
	"grounded-pass":             "95e5fb57391fd7d9589dfa6a0517f97922be04583087fcfec6cef8e08023eb53",
	"refund-contradiction-flag": "9f5876cb684d614a5de0661c5540d18d3948982e3ed4badb708951d8e7a15794",
	"no-evidence-not-assessed":  "d90e27eada5e6535c16ec420dd4a1568fe92520e4def96f3b1b6ed38cd3a2d76",
}

// TestScenarioPhaseTags checks that every scenario carries exactly the phase tag its features
// need, so `--scenarios demo` and the S0 gate never send a request this build cannot serve, and
// that the other selection tags mean what they say.
func TestScenarioPhaseTags(t *testing.T) {
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	dp := loadDemoPolicy(t, reg)
	for _, s := range loadRefScenarios(t) {
		t.Run(s.ID, func(t *testing.T) {
			route := routeNamed(dp, s.Route)
			if route == nil {
				t.Fatalf("route %s is in neither config/policies.yaml nor config/policies.pending.yaml", s.Route)
			}
			knowledge := slices.Contains(route.Evidence.Sources, evidence.SourceKnowledge)
			var needs []string
			if s.Stream {
				needs = append(needs, "streaming gating")
			}
			if s.APIFormat != llmapi.FormatOpenAIChat {
				needs = append(needs, s.APIFormat+" gating")
			}
			if route.Mode == decision.ModeBlock || route.Mode == decision.ModeFallback {
				needs = append(needs, route.Mode+" enforcement")
			}
			if dp.pending[route.Name] {
				needs = append(needs, "a route in config/policies.pending.yaml")
			}
			if knowledge {
				needs = append(needs, "the knowledge add-on")
			}
			want := phaseS0
			if len(needs) > 0 {
				want = "p3"
			}
			if got := scenarioPhase(s.Sets); got != want {
				t.Errorf("phase %s from its sets %v, but its features need %s %v", got, s.Sets, want, needs)
			}
			for _, tag := range s.Sets {
				if i := slices.Index(phases, tag); i > 0 && tag != want {
					t.Errorf("phase tag %s: the scenario needs phase %s", tag, want)
				}
			}
			if got := slices.Contains(s.Sets, "knowledge"); got != knowledge {
				t.Errorf("tag knowledge is %v, but route %s reads the knowledge add-on: %v", got, route.Name, knowledge)
			}
			if knowledge && (slices.Contains(s.Sets, "demo") || slices.Contains(s.Sets, "e2e")) {
				t.Error("the demo and e2e stacks run without the knowledge add-on")
			}
			if got := slices.Contains(s.Sets, answerGenerated); got != (s.AnswerSource == answerGenerated) {
				t.Errorf("tag generated is %v, answer_source %s", got, s.AnswerSource)
			}
			if slices.Contains(s.Sets, "untagged") && (s.Expect.Status != "not_assessed" || s.Expect.CheckedAgainst != "none") {
				t.Error("an untagged scenario's documents are not extracted: status not_assessed, checked against none")
			}
			if _, isS0 := s0Scenarios[s.ID]; isS0 != slices.Contains(s.Sets, phaseS0) {
				t.Errorf("set s0 is the walking skeleton's three scenarios only")
			}
			// The e2e stack runs the compatible backend against mock-upstream: a calibration of
			// quality mock never counts there, so a route that adopts calibrated thresholds acts
			// differently.
			if slices.Contains(s.Sets, "e2e") {
				for _, e := range route.Evaluators {
					if e.Thresholds != nil && e.Thresholds.Space == decision.SpaceCalibrated {
						t.Errorf("route %s adopts the mock calibration (%s), so its scenarios cannot be in set e2e", route.Name, e.Ref)
					}
				}
			}
		})
	}
}

// TestS0ScenariosAreByteStable pins the walking-skeleton scenarios.
func TestS0ScenariosAreByteStable(t *testing.T) {
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
	seen := 0
	for i, raw := range pack.Scenarios {
		var id struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &id)
		want, ok := s0Scenarios[id.ID]
		if !ok {
			continue
		}
		seen++
		if i >= len(s0Scenarios) {
			t.Errorf("%s moved to position %d; the S0 scenarios stay first", id.ID, i)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s changed (sha256 %s, pinned %s): add a new scenario instead", id.ID, got, want)
		}
	}
	if seen != len(s0Scenarios) {
		t.Errorf("found %d of the %d S0 scenarios", seen, len(s0Scenarios))
	}
}

// TestDemoSelectionRunsOnThisBuild: this build's default phase sends every scenario of the demo
// loop, the S0 gate and the e2e check (with --expect a skip fails the check, D18), and --phase p2,
// the gateway before P3, still serves the S0 gate and skips exactly the phase-tagged scenarios.
func TestDemoSelectionRunsOnThisBuild(t *testing.T) {
	all, err := loadScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{"demo", "s0", "e2e"} {
		picked, err := selectScenarios(all, sel, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, skipped := byPhase(picked, defaultPhase); len(skipped) > 0 {
			t.Errorf("--scenarios %s at the default phase %s skips %d scenarios", sel, defaultPhase, len(skipped))
		}
		run, skipped := byPhase(picked, "p2")
		for _, s := range run {
			if scenarioPhase(s.Sets) != phaseS0 {
				t.Errorf("%s: --scenarios %s --phase p2 sends %s, which needs %s", sel, sel, s.ID, scenarioPhase(s.Sets))
			}
		}
		if sel == "s0" && len(skipped) > 0 {
			t.Errorf("the S0 gate would skip %d scenarios at --phase p2", len(skipped))
		}
	}
}

// TestFaultScenariosNeedThisPhase: every vendor-fault scenario carries set faults and phase tag
// p5 (it needs this phase's fault channel), the main pack never does, and this build's default
// phase sends the whole faults pack (make e2e-faults passes no --phase), while an earlier phase
// skips all of it.
func TestFaultScenariosNeedThisPhase(t *testing.T) {
	if defaultPhase != "p5" {
		t.Fatalf("defaultPhase %s: the vendor-fault scenarios need p5", defaultPhase)
	}
	faults, err := loadPack(packFaults, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(faults) == 0 {
		t.Fatal("the faults pack is empty")
	}
	for _, s := range faults {
		if scenarioPhase(s.Sets) != "p5" || !slices.Contains(s.Sets, "faults") {
			t.Errorf("%s: sets %v, want faults and p5", s.ID, s.Sets)
		}
	}
	picked, err := selectScenarios(faults, "faults", "")
	if err != nil {
		t.Fatal(err)
	}
	if run, skipped := byPhase(picked, defaultPhase); len(skipped) > 0 || len(run) != len(faults) {
		t.Errorf("--pack faults --scenarios faults at the default phase sends %d and skips %d of %d", len(run), len(skipped), len(faults))
	}
	previous := phases[slices.Index(phases, defaultPhase)-1]
	if run, _ := byPhase(picked, previous); len(run) != 0 {
		t.Errorf("a gateway of phase %s would be sent %d vendor-fault scenarios", previous, len(run))
	}
	main, err := loadScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range main {
		if slices.Contains(s.Sets, defaultPhase) || slices.Contains(s.Sets, previous) {
			t.Errorf("%s: no main-pack scenario needs phase %s or %s (sets %v)", s.ID, previous, defaultPhase, s.Sets)
		}
	}
}

// TestEnforcingDemoRoutesTrustNoUserContext: a demo route in block or fallback mode reads no
// user-message context, so its block-readiness checklist is clean (SPEC §7.2: validation warns
// when an enforcing route trusts request_context, which an end user can fill with "documents").
func TestEnforcingDemoRoutesTrustNoUserContext(t *testing.T) {
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	dp := loadDemoPolicy(t, reg)
	enforcing := 0
	for _, r := range append([]*policy.Route{dp.Default}, dp.Routes...) {
		if r.Mode != decision.ModeBlock && r.Mode != decision.ModeFallback {
			continue
		}
		enforcing++
		if slices.Contains(r.Evidence.Sources, evidence.SourceRequestContext) {
			t.Errorf("route %s (%s) trusts %s: list evidence.sources without it", r.Name, r.Mode, evidence.SourceRequestContext)
		}
	}
	if enforcing == 0 {
		t.Error("no demo route blocks or falls back")
	}
}

func routeNamed(dp *demoPolicy, name string) *policy.Route {
	for _, r := range dp.Routes {
		if r.Name == name {
			return r
		}
	}
	if dp.Default != nil && dp.Default.Name == name {
		return dp.Default
	}
	return nil
}
