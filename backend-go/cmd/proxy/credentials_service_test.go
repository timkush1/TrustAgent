package main

import (
	"slices"
	"testing"

	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// bindingChanges is the audit diff and settings_updated paths of a credential write's bind: the
// bindings, and the backend, provider and models a bind moved (D76, D76b), each with from and to.
func TestBindingChangesNameWhatTheBindMoved(t *testing.T) {
	before := runtimecfg.NewStatic(nil).Current().Settings
	tsModel, haiku := "jev-1.13.0", "claude-haiku-4-5"
	before.DecisionModel.Backend, before.DecisionModel.Model = "typesafe", &tsModel
	before.Judge.Provider, before.Judge.Model = "anthropic", &haiku
	after, applied, _ := runtimecfg.ApplyBindings(before, "openrouter", []string{runtimecfg.TargetDecisionModel, runtimecfg.TargetJudge}, nil, false)
	if len(applied) != 2 {
		t.Fatalf("applied %v", applied)
	}
	changed, diff := bindingChanges(before, after)
	want := []string{"bindings.decision_model", "bindings.judge", "decision_model.backend", "decision_model.model", "judge.provider", "judge.model"}
	if !slices.Equal(changed, want) {
		t.Errorf("changed %v, want %v", changed, want)
	}
	if d, _ := diff["decision_model.model"].(map[string]any); d["from"] != "jev-1.13.0" || d["to"] != nil {
		t.Errorf("decision_model.model diff %v", d)
	}
	if d, _ := diff["judge.model"].(map[string]any); d["from"] != "claude-haiku-4-5" || d["to"] != nil {
		t.Errorf("judge.model diff %v", d)
	}
}
