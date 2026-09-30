package jev

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// gauge reads a gauge of the default registry by name and kind label.
func gauge(t *testing.T, name, kind string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "kind" && l.GetValue() == kind {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return -1
}

func TestMemoryBudgetReportsItsSpend(t *testing.T) {
	clk := newFakeClock()
	b := NewMemoryBudget(BudgetConfig{CapUSD: 1, EnforcingReserve: 0.25, Now: clk.Now})
	r, err := b.Reserve(SpendRequest{USD: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if got := gauge(t, "trustagent_gateway_spend_reserved_usd", SpendKindDecision); !near(got, 0.5) {
		t.Errorf("spend_reserved_usd = %v, want 0.5", got)
	}
	r.Settle(0.4)
	if today, held := gauge(t, "trustagent_gateway_spend_usd_today", SpendKindDecision),
		gauge(t, "trustagent_gateway_spend_reserved_usd", SpendKindDecision); !near(today, 0.4) || held != 0 {
		t.Errorf("after the settle: spend_usd_today %v, reserved %v", today, held)
	}
	// A new UTC day starts the day's spend at 0.
	clk.Add(25 * time.Hour)
	b.Spend()
	if got := gauge(t, "trustagent_gateway_spend_usd_today", SpendKindDecision); got != 0 {
		t.Errorf("a new day: spend_usd_today %v", got)
	}
}
