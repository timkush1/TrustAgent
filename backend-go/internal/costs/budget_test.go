package costs

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *clock { return &clock{t: t} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

var day1 = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// gaugeValue reads a gauge of the default registry by name and kind label (-1 when absent).
func gaugeValue(t *testing.T, name, kind string) float64 {
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

// Observe and flag calls stop at the observe line, (1 − reserve)·cap = 75%; enforcing calls spend
// the reserve up to 100% (SPEC §2.9 caller rules).
func TestBudgetObserveLineAndEnforcingReserve(t *testing.T) {
	b := NewBudget(KindDecision, BudgetConfig{CapUSD: 1, EnforcingReserve: 0.25, Now: newClock(day1).Now})
	for i := range 7 {
		h, err := b.Reserve(0.1, false, "")
		if err != nil {
			t.Fatalf("observe call %d refused below the line: %v", i, err)
		}
		h.Settle(0.1)
	}
	if _, err := b.Reserve(0.1, false, ""); !errors.Is(err, ErrCapReached) {
		t.Fatalf("an observe call past 75%% = %v, want ErrCapReached", err)
	}
	if got := b.Headroom(false); !near(got, 0.05) {
		t.Errorf("Headroom(false) at $0.70 = %v, want 0.05", got)
	}
	if got := b.Headroom(true); !near(got, 0.30) {
		t.Errorf("Headroom(true) at $0.70 = %v, want 0.30", got)
	}
	st := b.State()
	if st.Exhausted || st.ReserveOnly || !near(st.SoftCapUSD, 0.75) {
		t.Errorf("state at $0.70 = %+v", st)
	}
	for i := range 3 {
		if st := b.State(); st.Exhausted || (i > 0) != st.ReserveOnly {
			t.Errorf("state before enforcing call %d = %+v", i, st)
		}
		h, err := b.Reserve(0.1, true, "")
		if err != nil {
			t.Fatalf("enforcing call %d refused below the cap: %v", i, err)
		}
		h.Settle(0.1)
	}
	if _, err := b.Reserve(0.01, true, ""); !errors.Is(err, ErrCapReached) {
		t.Fatalf("an enforcing call past the cap = %v", err)
	}
	if st := b.State(); !near(st.SpentUSD, 1) || !st.Exhausted || st.ReserveOnly {
		t.Errorf("state at the cap = %+v", st)
	}
	if b.Headroom(true) != 0 || b.Headroom(false) != 0 {
		t.Errorf("headroom at the cap = %v / %v", b.Headroom(true), b.Headroom(false))
	}
	// A zero hold is refused once the limit is spent (a knowledge ingest asks this way).
	if _, err := b.Reserve(0, true, ""); !errors.Is(err, ErrCapReached) {
		t.Errorf("a $0 hold at the cap = %v", err)
	}
}

// Many goroutines reserving and settling at once never take the spend past the cap by more than
// one estimate, and every hold is released (-race). Admission keeps spend and holds within the cap,
// so only settles above their hold can pass it, by their excess: with at most `goroutines` calls
// in flight, each over by at most est/goroutines, the overshoot stays within one estimate. The
// cap is also used: a refusal means spend and the other holds were within one estimate of the cap.
func TestBudgetConcurrentReserveAndSettleStayWithinTheCap(t *testing.T) {
	const capUSD, est, goroutines = 1.0, 0.01, 16
	b := NewBudget(KindLLM, BudgetConfig{CapUSD: capUSD, Now: newClock(day1).Now})
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted, refused := 0, 0
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				h, err := b.Reserve(est, true, "")
				mu.Lock()
				if err != nil {
					refused++
				} else {
					admitted++
				}
				mu.Unlock()
				if err != nil {
					continue
				}
				// Some calls cost more than held, some less.
				actual := est
				switch (g + i) % 3 {
				case 0:
					actual = est * (1 + 1.0/goroutines)
				case 1:
					actual = est * 0.5
				}
				h.Settle(actual)
				h.Settle(99) // only the first settle counts
			}
		}()
	}
	wg.Wait()
	st := b.State()
	if st.ReservedUSD > 1e-9 {
		t.Errorf("holds left after every settle: %v", st.ReservedUSD)
	}
	if st.SpentUSD > capUSD+est+1e-9 {
		t.Errorf("spent %v, past the cap plus one estimate", st.SpentUSD)
	}
	if admitted == 0 || refused == 0 || st.SpentUSD < capUSD-goroutines*est-1e-9 {
		t.Errorf("admitted %d, refused %d, spent %v: the cap was not used", admitted, refused, st.SpentUSD)
	}
}

// With every call settling at or below its hold, concurrent reservations never take the spend
// past the cap at all: a reservation is checked and taken under one lock (-race).
func TestBudgetConcurrentReservationsNeverPassTheCap(t *testing.T) {
	const capUSD, est = 0.5, 0.01
	b := NewBudget(KindDecision, BudgetConfig{CapUSD: capUSD, EnforcingReserve: 0.2, Now: newClock(day1).Now})
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 40 {
				h, err := b.Reserve(est, g%2 == 0, "")
				if err != nil {
					continue
				}
				if st := b.State(); st.SpentUSD+st.ReservedUSD > capUSD+1e-9 {
					t.Errorf("spend and holds %v past the cap", st.SpentUSD+st.ReservedUSD)
				}
				h.Settle(est * float64(1+i%2) / 2) // the hold, or half of it
			}
		}()
	}
	wg.Wait()
	if st := b.State(); st.SpentUSD > capUSD+1e-9 || st.ReservedUSD > 1e-9 {
		t.Errorf("state = %+v", st)
	}
}

// Settling above or below the hold books the actual cost; holds count against the cap while they
// are held.
func TestBudgetHoldsCountUntilTheySettle(t *testing.T) {
	b := NewBudget(KindLLM, BudgetConfig{CapUSD: 1, Now: newClock(day1).Now})
	h1, err := b.Reserve(0.6, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(0.5, true, ""); !errors.Is(err, ErrCapReached) {
		t.Fatalf("a hold past the cap with $0.60 held = %v", err)
	}
	if got := b.Headroom(true); !near(got, 0.4) {
		t.Errorf("headroom with $0.60 held = %v", got)
	}
	h1.Settle(0.2) // the audit cost less than estimated
	h2, err := b.Reserve(0.5, true, "")
	if err != nil {
		t.Fatalf("the released hold left room: %v", err)
	}
	h2.Settle(0.9) // and this one more
	if st := b.State(); !near(st.SpentUSD, 1.1) || st.ReservedUSD != 0 || !st.Exhausted {
		t.Errorf("state = %+v", st)
	}
	if b.CapHitAt() == nil {
		t.Error("spend past the cap did not record the cap hit")
	}
}

// The per-key cap refuses one key's calls while the others still spend.
func TestBudgetPerKeyCap(t *testing.T) {
	b := NewBudget(KindDecision, BudgetConfig{CapUSD: 10, EnforcingReserve: 0.25, PerKeyCapUSD: 0.5, Now: newClock(day1).Now})
	h, err := b.Reserve(0.3, false, "app-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(0.3, false, "app-a"); !errors.Is(err, ErrCapReached) {
		t.Fatalf("app-a past its key cap with a hold = %v", err)
	}
	h.Settle(0.5)
	if _, err := b.Reserve(0, true, "app-a"); !errors.Is(err, ErrCapReached) {
		t.Errorf("app-a at its key cap, even enforcing = %v", err)
	}
	h2, err := b.Reserve(0.3, false, "app-b")
	if err != nil {
		t.Fatalf("app-b refused by app-a's spend: %v", err)
	}
	h2.Settle(0.3)
	if _, err := b.Reserve(0.1, false, ""); err != nil {
		t.Errorf("a call without a key is not limited by key caps: %v", err)
	}
	if b.CapHitAt() != nil {
		t.Error("a key cap refusal is not a hit of the day's cap")
	}
}

// At midnight UTC spend and the cap hit restart; holds of calls still running survive and settle
// into the new day.
func TestBudgetRolloverKeepsHolds(t *testing.T) {
	clk := newClock(time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC))
	b := NewBudget(KindDecision, BudgetConfig{CapUSD: 1, EnforcingReserve: 0.25, PerKeyCapUSD: 0.4, Now: clk.Now})
	done, err := b.Reserve(0.4, true, "app-a")
	if err != nil {
		t.Fatal(err)
	}
	done.Settle(0.4)
	if _, err := b.Reserve(0.3, true, "app-a"); err == nil {
		t.Fatal("app-a past its key cap was admitted")
	}
	running, err := b.Reserve(0.3, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(0.5, true, ""); err == nil {
		t.Fatal("past the cap was admitted")
	}
	if b.CapHitAt() == nil {
		t.Fatal("no cap hit recorded")
	}

	clk.Set(time.Date(2026, 10, 3, 0, 0, 1, 0, time.UTC))
	st := b.State()
	if st.Day != "2026-10-03" || st.SpentUSD != 0 || !near(st.ReservedUSD, 0.3) || st.CapHitAt != nil {
		t.Fatalf("after midnight = %+v", st)
	}
	if h, err := b.Reserve(0.4, true, "app-a"); err != nil {
		t.Errorf("app-a's key cap did not restart: %v", err)
	} else {
		h.Settle(0)
	}
	running.Settle(0.25)
	if st := b.State(); !near(st.SpentUSD, 0.25) || st.ReservedUSD != 0 {
		t.Errorf("the hold from yesterday settled into %+v", st)
	}
}

// Without a cap nothing is refused, spend is still counted, and the headroom is unlimited.
func TestBudgetUnlimitedStillCountsSpend(t *testing.T) {
	b := NewBudget(KindDecision, BudgetConfig{EnforcingReserve: 0.25, Now: newClock(day1).Now})
	h, err := b.Reserve(1e6, false, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Settle(3)
	if !math.IsInf(b.Headroom(false), 1) || b.State().SpentUSD != 3 || b.State().Exhausted {
		t.Errorf("unlimited = %+v headroom %v", b.State(), b.Headroom(false))
	}
	b.SetCap(2)
	if st := b.State(); !st.Exhausted {
		t.Errorf("a cap lowered below the spend: %+v", st)
	}
	if _, err := b.Reserve(0.01, true, ""); err == nil {
		t.Error("a cap lowered below the spend still admits calls")
	}
	b.SetCaps(-1, 7, math.NaN()) // nonsense values: no cap, the reserve at most 1
	if _, err := b.Reserve(0.01, true, ""); err != nil {
		t.Errorf("a negative cap is no cap: %v", err)
	}
}

// Costs that are not numbers or below 0 never reach the spend.
func TestBudgetIgnoresCostsThatAreNotCosts(t *testing.T) {
	b := NewBudget(KindLLM, BudgetConfig{CapUSD: 1, Now: newClock(day1).Now})
	for _, v := range []float64{math.NaN(), math.Inf(1), -2} {
		h, err := b.Reserve(v, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if h.USD() != 0 {
			t.Errorf("hold of %v = %v", v, h.USD())
		}
		h.Settle(v)
	}
	if st := b.State(); st.SpentUSD != 0 || st.ReservedUSD != 0 {
		t.Errorf("state = %+v", st)
	}
}

// Both kinds report spend_usd_today and spend_reserved_usd.
func TestBudgetReportsSpendMetrics(t *testing.T) {
	b := NewBudget(KindLLM, BudgetConfig{CapUSD: 10, Now: newClock(day1).Now})
	h, err := b.Reserve(0.5, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := gaugeValue(t, "trustagent_gateway_spend_reserved_usd", KindLLM); !near(got, 0.5) {
		t.Errorf("spend_reserved_usd{kind=llm} = %v", got)
	}
	h.Settle(0.25)
	if today, held := gaugeValue(t, "trustagent_gateway_spend_usd_today", KindLLM),
		gaugeValue(t, "trustagent_gateway_spend_reserved_usd", KindLLM); !near(today, 0.25) || held != 0 {
		t.Errorf("after the settle: today %v, reserved %v", today, held)
	}
}

// A seed raises the spend to the stored total (max(memory, db)) per cap and per key, and is
// ignored when it was read for another day.
func TestBudgetSeedTakesTheHigherSpend(t *testing.T) {
	b := NewBudget(KindDecision, BudgetConfig{CapUSD: 1, EnforcingReserve: 0.25, PerKeyCapUSD: 0.5, Now: newClock(day1).Now})
	h, _ := b.Reserve(0.2, false, "")
	h.Settle(0.2)
	b.seed("2026-10-02", 0.1, map[string]float64{"app-a": 0.5})
	if st := b.State(); !near(st.SpentUSD, 0.2) {
		t.Errorf("a lower stored total lowered the spend: %+v", st)
	}
	if _, err := b.Reserve(0.01, false, "app-a"); err == nil {
		t.Error("the seeded key spend did not reach the key cap")
	}
	b.seed("2026-10-02", 0.6, nil)
	if st := b.State(); !near(st.SpentUSD, 0.6) {
		t.Errorf("a higher stored total = %+v", st)
	}
	b.seed("2026-10-01", 5, nil)
	if st := b.State(); !near(st.SpentUSD, 0.6) {
		t.Errorf("yesterday's total was applied: %+v", st)
	}
}
