package jev

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
)

// fakeClock is a settable clock for the breaker, limiter and budget.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestBreakerCountsConsecutiveFaults(t *testing.T) {
	clk := newFakeClock()
	var states []string
	b := newBreaker(3, 30*time.Second, 5*time.Minute, clk.Now, func(s string) { states = append(states, s) })
	allow := func() bool { ok, _ := b.allow(false); return ok }

	b.record(effectFault, false, KindRetryable)
	b.record(effectFault, false, KindRetryable)
	b.record(effectSuccess, false, "") // a success resets the count
	b.record(effectFault, false, KindRetryable)
	b.record(effectFault, false, KindRetryable)
	if !allow() || b.view().state != BreakerClosed {
		t.Fatal("two consecutive faults keep it closed")
	}
	// Neutral outcomes (our cancellations, hedge losers, data_policy) neither count nor reset.
	for range 10 {
		b.record(effectNeutral, false, "")
	}
	if b.view().consecutive != 2 {
		t.Fatalf("neutral outcomes changed the count to %d", b.view().consecutive)
	}
	b.record(effectFault, false, KindRetryable)
	v := b.view()
	if v.state != BreakerOpen || !v.until.Equal(clk.Now().Add(30*time.Second)) || v.cause != KindRetryable || allow() {
		t.Fatalf("third fault must open for the cooldown: %+v", v)
	}

	clk.Add(30 * time.Second)
	ok, trial := b.allow(false)
	if !ok || !trial || b.view().state != BreakerHalfOpen {
		t.Fatal("after the cooldown one trial goes through")
	}
	if ok, _ := b.allow(false); ok {
		t.Error("only one trial at a time")
	}
	if ok, _ := b.allow(true); ok {
		t.Error("hedges are never trials")
	}
	b.record(effectNeutral, true, "") // the trial was cancelled by us: another may try
	ok, trial = b.allow(false)
	if !ok || !trial {
		t.Fatal("a neutral trial frees the slot")
	}
	b.record(effectFault, true, KindTimeout)
	if v := b.view(); v.state != BreakerOpen || v.cause != KindTimeout {
		t.Fatalf("a failed trial reopens: %+v", v)
	}
	clk.Add(30 * time.Second)
	_, trial = b.allow(false)
	b.record(effectSuccess, true, "")
	if v := b.view(); v.state != BreakerClosed || v.consecutive != 0 {
		t.Fatalf("a successful trial closes: %+v", v)
	}
	want := []string{BreakerOpen, BreakerHalfOpen, BreakerOpen, BreakerHalfOpen, BreakerClosed}
	if len(states) != len(want) {
		t.Fatalf("transitions %v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("transitions %v, want %v", states, want)
		}
	}
}

func TestBreakerTripAndAuthCooldown(t *testing.T) {
	clk := newFakeClock()
	b := newBreaker(5, 30*time.Second, 5*time.Minute, clk.Now, nil)
	b.record(effectTrip, false, KindBadContentType)
	if v := b.view(); v.state != BreakerOpen || !v.until.Equal(clk.Now().Add(30*time.Second)) || v.keyCause != "" {
		t.Fatalf("200 text/html trips at once, as a vendor fault: %+v", v)
	}
	b.reset()
	b.record(effectAuth, false, KindAuth)
	if v := b.view(); v.state != BreakerOpen || !v.until.Equal(clk.Now().Add(5*time.Minute)) || v.cause != KindAuth || v.keyCause != KindAuth {
		t.Fatalf("auth opens for the auth cooldown, named by the key: %+v", v)
	}
	// A shorter fault cooldown never shortens the auth pause, nor renames it.
	b.record(effectTrip, false, KindBadContentType)
	if v := b.view(); !v.until.Equal(clk.Now().Add(5*time.Minute)) || v.keyCause != KindAuth {
		t.Fatalf("open period shortened or renamed: %+v", v)
	}
	// A late success from before the opening does not close it, nor rename it.
	b.record(effectSuccess, false, "")
	if v := b.view(); v.state != BreakerOpen || v.keyCause != KindAuth {
		t.Fatalf("a stale success closed or renamed the pause: %+v", v)
	}
	clk.Add(5 * time.Minute)
	if ok, trial := b.allow(false); !ok || !trial {
		t.Fatal("half-open after the auth cooldown")
	}
	if v := b.view(); v.keyCause != "" {
		t.Fatalf("a half-open breaker names no key pause: %+v", v)
	}
	b.reset()
	if v := b.view(); v.state != BreakerClosed || v.consecutive != 0 || v.keyCause != "" {
		t.Fatalf("reset: %+v", v)
	}
	if ok, _ := b.allow(false); !ok {
		t.Fatal("closed after reset")
	}
}

func TestLimiterSyncReserve(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(60, 0.5, 100, clk.Now) // burst 10, async floor 5
	var releases []func()
	for i := range 5 {
		rel, kind := l.acquire(ClassAsync)
		if kind != "" {
			t.Fatalf("async %d refused: %s", i, kind)
		}
		releases = append(releases, rel)
	}
	if _, kind := l.acquire(ClassAsync); kind != KindRateLimited {
		t.Fatalf("async past the reserve: %q, want rate_limited", kind)
	}
	for i := range 5 {
		if _, kind := l.acquire(ClassSync); kind != "" {
			t.Fatalf("sync %d must use the reserve: %s", i, kind)
		}
	}
	if _, kind := l.acquire(ClassSync); kind != KindRateLimited {
		t.Fatalf("empty bucket: %q", kind)
	}
	if in, used := l.load(); in != 10 || used != 10 {
		t.Errorf("load %d in flight, %d in the last minute", in, used)
	}
	for _, rel := range releases {
		rel()
		rel() // idempotent
	}
	if in, _ := l.load(); in != 5 {
		t.Errorf("in flight after releases: %d", in)
	}
	clk.Add(6 * time.Second) // 1 token per second → 6 tokens
	for i := range 1 {
		if _, kind := l.acquire(ClassAsync); kind != "" {
			t.Fatalf("async after refill %d: %s", i, kind)
		}
	}
	if _, kind := l.acquire(ClassAsync); kind != KindRateLimited {
		t.Fatalf("async keeps the reserve after a refill: %q", kind)
	}
	clk.Add(time.Hour)
	if _, used := l.load(); used != 0 {
		t.Errorf("the minute window forgets old requests: %d", used)
	}
}

func TestLimiterInFlightCap(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(100000, 0.5, 4, clk.Now) // async may hold 2 of 4 slots
	rel1, k1 := l.acquire(ClassAsync)
	_, k2 := l.acquire(ClassAsync)
	if k1 != "" || k2 != "" {
		t.Fatal(k1, k2)
	}
	if _, kind := l.acquire(ClassAsync); kind != KindOverload {
		t.Fatalf("async past its slots: %q", kind)
	}
	_, k3 := l.acquire(ClassSync)
	_, k4 := l.acquire(ClassSync)
	if k3 != "" || k4 != "" {
		t.Fatal("sync uses the reserved slots", k3, k4)
	}
	if _, kind := l.acquire(ClassSync); kind != KindOverload {
		t.Fatalf("full: %q", kind)
	}
	rel1()
	if _, kind := l.acquire(ClassSync); kind != "" {
		t.Fatalf("a released slot is reusable: %q", kind)
	}
}

func TestLimiterTinyLimits(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(1, 0.3, 1, clk.Now) // burst 1, one slot
	rel, kind := l.acquire(ClassAsync)
	if kind != "" {
		t.Fatalf("a tiny limit still admits async calls: %q", kind)
	}
	rel()
	if _, kind := l.acquire(ClassSync); kind != KindRateLimited {
		t.Fatalf("bucket empty: %q", kind)
	}
	full := newLimiter(600, 1, 10, clk.Now)
	if _, kind := full.acquire(ClassAsync); kind == "" {
		t.Error("a reserve of 1 keeps everything for sync calls")
	}
}

func TestMemoryBudget(t *testing.T) {
	clk := newFakeClock()
	b := NewMemoryBudget(BudgetConfig{CapUSD: 1, EnforcingReserve: 0.25, Now: clk.Now})
	r1, err := b.Reserve(SpendRequest{USD: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(SpendRequest{USD: 0.3}); err != ErrBudgetExhausted {
		t.Fatalf("observe/flag stop at 75%%: %v", err)
	}
	r2, err := b.Reserve(SpendRequest{USD: 0.3, Enforcing: true})
	if err != nil {
		t.Fatalf("enforcing calls may use the reserve: %v", err)
	}
	s := b.Spend()
	if !near(s.ReservedUSD, 0.8) || s.SpentUSD != 0 || s.CapUSD != 1 || !near(s.SoftCapUSD, 0.75) || s.Exhausted {
		t.Fatalf("spend %+v", s)
	}
	r1.Settle(0.4)
	r1.Settle(100) // only the first settle counts
	r2.Settle(0)   // released
	s = b.Spend()
	if !near(s.SpentUSD, 0.4) || !near(s.ReservedUSD, 0) {
		t.Fatalf("after settling %+v", s)
	}
	r3, _ := b.Reserve(SpendRequest{USD: 0.35})
	r3.Settle(0.35)
	if s = b.Spend(); !s.Exhausted {
		t.Fatalf("75%% spent is exhausted for observe/flag: %+v", s)
	}
	if _, err := b.Reserve(SpendRequest{USD: 0}); err != ErrBudgetExhausted {
		t.Fatal("nothing more for non-enforcing calls, even a free one")
	}
	if _, err := b.Reserve(SpendRequest{USD: 0.2, Enforcing: true}); err != nil {
		t.Fatalf("enforcing still fits: %v", err)
	}

	clk.Add(24 * time.Hour) // a new UTC day
	s = b.Spend()
	if s.SpentUSD != 0 || !near(s.ReservedUSD, 0.2) || s.Day != "2026-09-29" {
		t.Fatalf("a new day starts at 0 and keeps running holds: %+v", s)
	}
	b.SetCaps(0, 0.25, 0)
	if _, err := b.Reserve(SpendRequest{USD: 1e6}); err != nil {
		t.Fatalf("a zero cap is unlimited: %v", err)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func tokenCount(narrow, wide int) decision.TokenCount {
	return decision.TokenCount{Narrow: narrow, Wide: wide}
}

func TestMemoryBudgetPerKey(t *testing.T) {
	b := NewMemoryBudget(BudgetConfig{PerKeyCapUSD: 0.1})
	r, err := b.Reserve(SpendRequest{USD: 0.08, KeyLabel: "app-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reserve(SpendRequest{USD: 0.05, KeyLabel: "app-a"}); err != ErrBudgetExhausted {
		t.Fatal("per-key cap")
	}
	if _, err := b.Reserve(SpendRequest{USD: 0.05, KeyLabel: "app-b"}); err != nil {
		t.Fatalf("another key has its own cap: %v", err)
	}
	if _, err := b.Reserve(SpendRequest{USD: 5}); err != nil {
		t.Fatalf("no key label, no per-key cap: %v", err)
	}
	r.Settle(0.01)
	if _, err := b.Reserve(SpendRequest{USD: 0.05, KeyLabel: "app-a"}); err != nil {
		t.Fatalf("settling frees the key's hold: %v", err)
	}
}

func TestCPTTracker(t *testing.T) {
	c := newCPTTracker(0)
	if c.get() != 4 {
		t.Fatalf("default cpt %v", c.get())
	}
	// 4000 narrow characters in 275 + 800 tokens: 5 chars per token.
	c.observe(tokenCount(4000, 0), 1075)
	if got := c.get(); math.Abs(got-4.2) > 1e-9 {
		t.Fatalf("cpt %v, want 0.8·4 + 0.2·5 = 4.2", got)
	}
	// Wide code points are one token each: 100 CJK characters take 100 of the tokens.
	c = newCPTTracker(4)
	c.observe(tokenCount(4000, 100), 1175)
	if got := c.get(); math.Abs(got-4.2) > 1e-9 {
		t.Fatalf("cpt with wide characters %v, want 4.2", got)
	}
	c.observe(tokenCount(10, 0), 200) // at or below the overhead: no information
	if got := c.get(); math.Abs(got-4.2) > 1e-9 {
		t.Fatalf("an uninformative measurement moved cpt to %v", got)
	}
	for range 50 {
		c.observe(tokenCount(100000, 0), 1275) // 100 chars per token → clamped at 6
	}
	if got := c.get(); got != 6 {
		t.Fatalf("cpt clamps at 6: %v", got)
	}
	if newCPTTracker(9).get() != 6 || newCPTTracker(1).get() != 2.5 {
		t.Error("the initial cpt is clamped")
	}
}

// Decision calls are priced by the shared calculator: the env override, else the price table
// (provider first, a leading "~" ignored), else the list price.
func TestLookupPrice(t *testing.T) {
	cases := []struct {
		override       float64
		backend, model string
		perMTok        float64
		version        string
	}{
		{0, BackendOpenRouterDecisions, "typesafe/jev-1.13", 0.042, "2026-09-27"},
		{0, BackendOpenRouterDecisions, "typesafe/jev-1.13-20260917", 0.042, "2026-09-27"},
		{0, BackendOpenRouterSystemOne, "~typesafe/jev-1.13", 0.042, "2026-09-27"},
		{0, BackendTypeSafe, "jev-1.13.0", 0.042, "2026-09-27"},
		{0, BackendCompatible, "jev-latest", 0.042, "2026-09-27"},
		{0, BackendOpenRouterDecisions, "~typesafe/jev-latest", 0.042, "default"},
		{0, BackendCompatible, "typesafe-ai/jev", 0.042, "default"},
		{0.1, BackendOpenRouterDecisions, "typesafe/jev-1.13", 0.1, "override"},
	}
	for _, c := range cases {
		p := lookupPrice(c.override, nil, c.backend, c.model)
		if p.perMTok != c.perMTok || p.version != c.version {
			t.Errorf("%s %s: %+v, want %v %s", c.backend, c.model, p, c.perMTok, c.version)
		}
	}
	if got := (price{perMTok: 0.042}).usd(1_000_000); got != 0.042 {
		t.Errorf("usd %v", got)
	}
	// No float noise in the JSON of a computed cost.
	for tokens, want := range map[int]string{566: "0.000023772", 1: "4.2e-8", 24275: "0.00101955", 0: "0", -5: "0"} {
		if b, _ := json.Marshal((price{perMTok: 0.042}).usd(tokens)); string(b) != want {
			t.Errorf("usd(%d) = %s, want %s", tokens, b, want)
		}
	}
	if got := (price{perMTok: 0.0421234567}).usd(1000); got != 0.0000421234567 {
		t.Errorf("a finer price is exact too: %v", got)
	}
}

func TestPricingProvider(t *testing.T) {
	for backend, want := range map[string]string{
		BackendOpenRouterDecisions: "openrouter", BackendOpenRouterSystemOne: "openrouter",
		BackendTypeSafe: "typesafe", BackendMock: "mock", BackendCompatible: "compatible", BackendOff: "", "": "",
	} {
		if got := PricingProvider(backend); got != want {
			t.Errorf("PricingProvider(%q) = %q, want %q", backend, got, want)
		}
	}
}

func TestReferer(t *testing.T) {
	for _, c := range []struct{ app, public, want string }{
		{"", "", DefaultAppURL},
		{DefaultAppURL, "http://localhost:3000", DefaultAppURL},
		{DefaultAppURL, "https://trust.example.com", "https://trust.example.com"},
		{"https://my.app", "https://trust.example.com", "https://my.app"},
		{"", "http://proxy:8080", DefaultAppURL},
		{"", "not a url", DefaultAppURL},
	} {
		if got := referer(c.app, c.public); got != c.want {
			t.Errorf("referer(%q, %q) = %q, want %q", c.app, c.public, got, c.want)
		}
	}
}

func TestLocalHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST.": true, "app.localhost": true, "127.0.0.1": true, "::1": true,
		"mock-upstream": true, "openrouter.ai": false, "10.0.0.5": false, "": false,
	} {
		if localHost(host) != want {
			t.Errorf("localHost(%q) != %v", host, want)
		}
	}
}
