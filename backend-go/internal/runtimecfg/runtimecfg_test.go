package runtimecfg

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
)

func TestNewStaticSeedsFromTheEnv(t *testing.T) {
	cfg := config.LoadWith(config.LoadOptions{Environ: []string{"TRUTHTABLE_JEV_BACKEND=mock", "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY=7"}})
	m := NewStatic(cfg)
	s := m.Current()
	if s.Version != 0 || len(s.Epoch) != 36 || s.Settings.DecisionModel.Backend != "mock" || s.Settings.Budgets.LLMUSDDay != 7 {
		t.Errorf("static snapshot %+v", s)
	}
	if NewStatic(cfg).Current().Epoch == s.Epoch {
		t.Error("each process gets its own epoch (D31)")
	}
	// The JSON names are api.ts RuntimeSettings.
	b, err := json.Marshal(s.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"decision_model":{"backend":"mock"`, `"judge":{"provider":"auto","model":null}`,
		`"fallback":{"url":null,"credential":null,"auth_header":"authorization","format":"openai"}`,
		`"bindings":{"decision_model":null,"judge":null,"upstream_default":null}`, `"pricing_overrides":[]`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("settings JSON %s lacks %s", b, key)
		}
	}
	cancel := m.Subscribe(func(*Snapshot) {})
	cancel()
	if len(m.subs) != 0 {
		t.Error("cancel removes the subscriber")
	}
}

func TestKeyFuncAssertsTheOrigin(t *testing.T) {
	m := NewStatic(nil)
	m.cur.Credentials["openrouter"] = Credential{Name: "openrouter", Secret: "sk-test-key", AllowedOrigins: []string{"https://openrouter.ai"}}
	key := m.KeyFunc("openrouter")
	if k, err := key(context.Background(), "https://openrouter.ai"); err != nil || k != "sk-test-key" {
		t.Errorf("allowed origin: %v", err)
	}
	if _, err := key(context.Background(), "https://evil.example"); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("other origin: %v", err)
	}
	if _, err := m.KeyFunc("typesafe")(context.Background(), "https://api.typesafe.ai"); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("missing credential: %v", err)
	}
}

func TestApplyBindings(t *testing.T) {
	base := NewStatic(nil).Current().Settings
	base.DecisionModel.Backend, base.Judge.Provider = "mock", "mock"

	s, applied, skipped := ApplyBindings(base, "openrouter", []string{TargetJudge, TargetDecisionModel}, nil, false)
	if !slices.Equal(applied, []string{TargetDecisionModel, TargetJudge}) || len(skipped) != 0 {
		t.Errorf("applied %v skipped %v", applied, skipped)
	}
	if *s.Bindings.DecisionModel != "openrouter" || *s.Bindings.Judge != "openrouter" || s.DecisionModel.Backend != "auto" || s.Judge.Provider != "auto" {
		t.Errorf("a bind leaves simulated mode (D76): %+v", s)
	}
	if base.Bindings.DecisionModel != nil {
		t.Error("ApplyBindings is pure")
	}

	s, applied, skipped = ApplyBindings(base, "openrouter", []string{TargetDecisionModel, TargetJudge}, []string{"bindings.judge", "judge.provider"}, true)
	if len(applied) != 0 || !slices.Equal(skipped, []BindingSkip{{TargetDecisionModel, SkipBackendPinned}, {TargetJudge, SkipEnvManaged}}) {
		t.Errorf("applied %v skipped %v", applied, skipped)
	}
	if s.DecisionModel.Backend != "mock" || s.Judge.Provider != "mock" {
		t.Errorf("nothing applied, so simulated mode stays: %+v", s)
	}
}
