package gate

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
)

// sample reads a series from the default registry by name and label pairs: a counter's or
// gauge's value, or a histogram's sample count.
func sample(t *testing.T, name string, labelPairs ...string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	metrics:
		for _, m := range f.GetMetric() {
			for i := 0; i+1 < len(labelPairs); i += 2 {
				found := false
				for _, l := range m.GetLabel() {
					found = found || l.GetName() == labelPairs[i] && l.GetValue() == labelPairs[i+1]
				}
				if !found {
					continue metrics
				}
			}
			return m.GetCounter().GetValue() + m.GetGauge().GetValue() + float64(m.GetHistogram().GetSampleCount())
		}
	}
	return 0
}

// delta runs f and returns how much each series grew.
func delta(t *testing.T, f func(), series ...[]string) []float64 {
	t.Helper()
	before := make([]float64, len(series))
	for i, s := range series {
		before[i] = sample(t, s[0], s[1:]...)
	}
	f()
	out := make([]float64, len(series))
	for i, s := range series {
		out[i] = sample(t, s[0], s[1:]...) - before[i]
	}
	return out
}

func TestGateMetricsCountEachDecision(t *testing.T) {
	p := repoPolicy(t)
	var r *Result
	series := [][]string{
		{"trustagent_gateway_gate_decisions_total", "route", "support-bot", "mode", "flag", "action", "flag", "status", "ok"},
		{"trustagent_gateway_evaluator_levels_total", "evaluator", "contradiction@1", "level", "critical", "stage", "active"},
		{"trustagent_gateway_gate_seconds", "stage", StageDetectors},
		{"trustagent_gateway_gate_seconds", "stage", StageCompile},
		{"trustagent_gateway_gate_seconds", "stage", StageJev},
		{"trustagent_gateway_gate_seconds", "stage", StageDecide},
	}
	got := delta(t, func() {
		r = newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), scenarioExchange(t, p, "refund-contradiction-flag"))
	}, series...)
	if r.Decision.Action != decision.ActionFlag {
		t.Fatalf("action %s", r.Decision.Action)
	}
	for i, want := range []float64{1, 1, 1, 1, 1, 1} {
		if got[i] != want {
			t.Errorf("%v grew by %v, want %v", series[i], got[i], want)
		}
	}
	// Every detector that fired is counted.
	for _, d := range r.Decision.Detectors {
		if !d.Fired {
			continue
		}
		g := delta(t, func() {
			newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), scenarioExchange(t, p, "refund-contradiction-flag"))
		}, []string{"trustagent_gateway_detector_fired_total", "detector", d.ID})
		if g[0] != 1 {
			t.Errorf("detector_fired_total{detector=%s} grew by %v", d.ID, g[0])
		}
	}
}

func TestGateMetricsCountFailuresNotSkips(t *testing.T) {
	p := flagPolicy(t, "")
	auth := []string{"trustagent_gateway_gate_errors_total", "kind", jev.KindAuth}
	budget := []string{"trustagent_gateway_gate_errors_total", "kind", jev.KindBudget}
	got := delta(t, func() {
		cl := &scriptedJev{Client: strictMock(t, 0).Client, err: &jev.Error{Kind: jev.KindAuth, Status: 401, Attempts: 1}}
		newGate(t, cl, seq()).Evaluate(context.Background(), exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", ""))
		// A refusal before any vendor request is a skip, not a gate failure.
		cl = &scriptedJev{Client: strictMock(t, 0).Client, err: &jev.Error{Kind: jev.KindBudget}}
		newGate(t, cl, seq()).Evaluate(context.Background(), exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", ""))
	}, auth, budget)
	if got[0] != 1 || got[1] != 0 {
		t.Errorf("gate_errors_total grew auth %v budget %v, want 1 and 0", got[0], got[1])
	}
}

func TestUniformSamplesTodayGauge(t *testing.T) {
	p := repoPolicy(t)
	g := newGate(t, strictMock(t, 0), func() float64 { return 0 })
	g.Evaluate(context.Background(), exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", ""))
	g.Evaluate(context.Background(), exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", ""))
	if got := sample(t, "trustagent_gateway_uniform_samples_today"); got != 2 {
		t.Errorf("uniform_samples_today = %v, want 2", got)
	}
}
