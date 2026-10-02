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

// TestApplyBindingsUsesTheBoundKey (D76b): an applied target uses the key the write reports. A
// persisted explicit backend the credential cannot serve, or an explicit judge provider other than
// the credential's own, becomes auto when that target applies, and only then; a backend or provider
// the credential serves, an env-managed path and a target that did not apply stay as stored.
func TestApplyBindingsUsesTheBoundKey(t *testing.T) {
	base := NewStatic(nil).Current().Settings
	cases := []struct {
		name               string
		backend, provider  string
		cred               string
		targets            []string
		envManaged         []string
		wantBack, wantJudg string
	}{
		{"typesafe backend, openrouter key", "typesafe", "anthropic", "openrouter", []string{TargetDecisionModel}, nil, "auto", "anthropic"},
		{"off backend, openrouter key", "off", "auto", "openrouter", []string{TargetDecisionModel}, nil, "auto", "auto"},
		{"openrouter backend, typesafe key", "openrouter-systemone", "auto", "typesafe", []string{TargetDecisionModel}, nil, "auto", "auto"},
		{"compatible backend, openrouter key", "compatible", "auto", "openrouter", []string{TargetDecisionModel}, nil, "auto", "auto"},
		{"a backend the key serves", "openrouter-decisions", "openrouter", "openrouter", []string{TargetDecisionModel, TargetJudge}, nil, "openrouter-decisions", "openrouter"},
		{"compatible backend, compatible key", "compatible", "auto", "compatible", []string{TargetDecisionModel}, nil, "compatible", "auto"},
		{"typesafe backend, typesafe key", "typesafe", "auto", "typesafe", []string{TargetDecisionModel}, nil, "typesafe", "auto"},
		{"anthropic judge, openrouter key", "auto", "anthropic", "openrouter", []string{TargetJudge}, nil, "auto", "auto"},
		{"ollama judge, openrouter key", "auto", "ollama", "openrouter", []string{TargetJudge}, nil, "auto", "auto"},
		{"no judge, openai key", "auto", "none", "openai", []string{TargetJudge}, nil, "auto", "auto"},
		{"openai judge, openai key", "auto", "openai", "openai", []string{TargetJudge}, nil, "auto", "openai"},
		{"anthropic judge, anthropic key", "auto", "anthropic", "anthropic", []string{TargetJudge}, nil, "auto", "anthropic"},
		// Only the target that applies moves: the judge bind leaves the typesafe backend alone.
		{"judge-only bind, typesafe backend", "typesafe", "anthropic", "openrouter", []string{TargetJudge}, nil, "typesafe", "auto"},
		// A skipped target moves nothing of its own.
		{"skipped judge", "typesafe", "anthropic", "openrouter", []string{TargetDecisionModel, TargetJudge}, []string{"bindings.judge"}, "auto", "anthropic"},
		// An env-managed path is the one in effect and stays as stored.
		{"env-managed backend", "typesafe", "anthropic", "openrouter", []string{TargetDecisionModel, TargetJudge},
			[]string{"decision_model.backend", "judge.provider"}, "typesafe", "anthropic"},
		// D76 still applies to mock whichever target applies.
		{"mock backend, judge-only bind", "mock", "mock", "anthropic", []string{TargetJudge}, nil, "auto", "auto"},
	}
	for _, c := range cases {
		in := base
		in.DecisionModel.Backend, in.Judge.Provider = c.backend, c.provider
		out, applied, _ := ApplyBindings(in, c.cred, c.targets, c.envManaged, false)
		if len(applied) == 0 {
			t.Errorf("%s: nothing applied", c.name)
		}
		if out.DecisionModel.Backend != c.wantBack || out.Judge.Provider != c.wantJudg {
			t.Errorf("%s: backend %q judge %q, want %q %q", c.name, out.DecisionModel.Backend, out.Judge.Provider, c.wantBack, c.wantJudg)
		}
		if in.DecisionModel.Backend != c.backend || in.Judge.Provider != c.provider {
			t.Errorf("%s: ApplyBindings is pure", c.name)
		}
	}
	// A bind that applies nothing changes nothing.
	in := base
	in.DecisionModel.Backend, in.Judge.Provider = "typesafe", "anthropic"
	out, applied, _ := ApplyBindings(in, "openrouter", []string{TargetDecisionModel, TargetJudge}, []string{"bindings.judge"}, true)
	if len(applied) != 0 || out.DecisionModel.Backend != "typesafe" || out.Judge.Provider != "anthropic" {
		t.Errorf("nothing applied, nothing moves: %v %+v %+v", applied, out.DecisionModel, out.Judge)
	}
}
