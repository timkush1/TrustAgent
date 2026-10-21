package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRouteLabelCapsDistinctRoutes(t *testing.T) {
	l := &routeLabels{max: 3}
	for i := range 3 {
		name := fmt.Sprintf("r%d", i)
		if got := l.label(name); got != name {
			t.Fatalf("label(%q) = %q, want the name while under the cap", name, got)
		}
	}
	if got := l.label("r3"); got != "other" {
		t.Fatalf("the fourth distinct route = %q, want other", got)
	}
	if got := l.label("r1"); got != "r1" {
		t.Fatalf("a route seen before the cap = %q, want its name", got)
	}
	if got := l.label(""); got != "none" {
		t.Fatalf("no route = %q, want none", got)
	}
}

func TestObserveRequestCountsAndObservesOverhead(t *testing.T) {
	labels := []string{"route", "metrics-test", "format", "openai_chat", "action", "none"}
	before := sample(t, "trustagent_gateway_requests_total", labels...)
	ObserveRequest("metrics-test", "openai_chat", "flag", "", 3*time.Millisecond)
	ObserveRequest("metrics-test", "openai_chat", "flag", "", -1)
	if got := sample(t, "trustagent_gateway_requests_total", labels...) - before; got != 2 {
		t.Fatalf("requests_total grew by %v, want 2", got)
	}
	if n := sample(t, "trustagent_gateway_overhead_seconds", "route", "metrics-test", "mode", "flag"); n != 1 {
		t.Fatalf("overhead_seconds sample count = %v, want 1 (a negative overhead is not observed)", n)
	}
}

func TestGatewaySeriesUseTheGatewayNamespace(t *testing.T) {
	ObserveUpstream("openai_chat", time.Second)
	ObserveGate("metrics-test", "flag", "flag", "ok")
	ObserveGateStage("decide", time.Millisecond)
	GateError("internal")
	EvaluatorLevel("groundedness@1", "ok", "gate")
	DetectorFired("secret_leak")
	Fallback("delivered")
	SetUniformSamplesToday(2)
	SetRetentionLag(-time.Second)
	SetSpend("decision", 0.5, 0.01)
	if got := sample(t, "trustagent_gateway_retention_lag_seconds"); got != 0 {
		t.Fatalf("retention_lag_seconds = %v after a negative lag, want 0", got)
	}
	if got := sample(t, "trustagent_gateway_spend_reserved_usd", "kind", "decision"); got != 0.01 {
		t.Fatalf("spend_reserved_usd{kind=decision} = %v, want 0.01", got)
	}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		seen[f.GetName()] = true
	}
	for _, name := range []string{
		"requests_total", "overhead_seconds", "upstream_seconds", "gate_decisions_total", "gate_seconds",
		"gate_errors_total", "evaluator_levels_total", "detector_fired_total", "fallback_total",
		"uniform_samples_today", "retention_lag_seconds", "spend_usd_today", "spend_reserved_usd",
		"cost_write_dropped_total",
	} {
		if !seen["trustagent_gateway_"+name] {
			t.Errorf("trustagent_gateway_%s is not registered", name)
		}
	}
	for name := range seen {
		if strings.HasPrefix(name, "trustagent_") && !strings.HasPrefix(name, "trustagent_gateway_") {
			t.Errorf("series %s is outside trustagent_gateway_*", name)
		}
	}
}

// sample reads a series from the default registry by name and label pairs: a counter's or
// gauge's value, or a histogram's sample count (prometheus testutil would add a module
// dependency).
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
