package jev

import (
	"testing"
	"time"
)

func TestWindow24hKeepsTheLastDay(t *testing.T) {
	var w window24h
	start := time.Date(2026, 9, 28, 10, 30, 0, 0, time.UTC)
	for h := 0; h < 30; h++ { // one call and $0.01 an hour for 30 hours
		at := start.Add(time.Duration(h) * time.Hour)
		i := w.bucket(at)
		w.calls[i]++
		w.cost[i] += 0.01
	}
	now := start.Add(29 * time.Hour)
	calls, cost := w.totals(now)
	if calls != 24 || cost < 0.2399 || cost > 0.2401 {
		t.Errorf("last 24 h: %d calls $%.4f, want 24 and $0.24", calls, cost)
	}
	// A day later with no traffic, nothing is left.
	if calls, _ := w.totals(now.Add(24 * time.Hour)); calls != 0 {
		t.Errorf("stale buckets counted: %d", calls)
	}
}

func TestStatusReportsTheDayWindow(t *testing.T) {
	var s statusTracker
	s.call()
	s.attempt(time.Now(), 0.002, nil, false, false)
	var st Status
	s.fill(&st, breakerView{state: BreakerClosed}, nil)
	if st.Calls != 1 || st.Calls24h != 1 || st.Cost24hUSD != 0.002 || st.CostUSD != 0.002 {
		t.Errorf("status %+v", st)
	}
}
