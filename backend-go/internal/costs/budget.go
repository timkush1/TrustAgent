package costs

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/metrics"
)

// Budget kinds: the decision model's cap and the LLM (deep-audit judge) cap. They are also the
// kind label of spend_usd_today and spend_reserved_usd, and the names a WS budget status carries
// (D49).
const (
	KindDecision = "decision"
	KindLLM      = "llm"
)

// budgetEps absorbs float rounding in cap comparisons (a millionth of a cent).
const budgetEps = 1e-8

// ErrCapReached is the refusal of a Reserve that would take the day's spend past the caller's
// limit: the cap, the observe line below it, or the caller's per-key cap.
var ErrCapReached = errors.New("daily spend cap reached")

// Budget is one daily cap (SPEC §7.4 caps, D46, D48): spend and holds of the UTC day, an
// optional enforcing reserve that only enforcing callers may spend, and an optional per-key cap.
// Spend restarts at 0 each UTC day; holds of calls still running survive the rollover and settle
// into the new day. A cap of 0 is unlimited, but spend is still counted. The ledger seeds it from
// the stored rows after a restart (Seed). Only real spend reaches it: a simulated (mock) call
// settles $0 (D46).
type Budget struct {
	kind string
	now  func() time.Time

	mu               sync.Mutex
	capUSD           float64
	enforcingReserve float64
	perKeyCapUSD     float64
	day              string
	spent, reserved  float64
	keys             map[string]*keySpend
	// capHitAt is when a call was first refused today, or when settled spend first reached the
	// cap; nil until then.
	capHitAt *time.Time
}

type keySpend struct{ spent, reserved float64 }

// BudgetConfig configures a Budget.
type BudgetConfig struct {
	// CapUSD is the daily cap (0 = unlimited).
	CapUSD float64
	// EnforcingReserve is the share of the cap only enforcing callers may spend (0 to 1); the
	// others stop at (1 − EnforcingReserve)·CapUSD, the observe line.
	EnforcingReserve float64
	// PerKeyCapUSD is the daily cap of one ingest key (0 = off).
	PerKeyCapUSD float64
	Now          func() time.Time
}

// NewBudget builds the budget of kind (KindDecision or KindLLM).
func NewBudget(kind string, c BudgetConfig) *Budget {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	b := &Budget{kind: kind, now: now, keys: map[string]*keySpend{}}
	b.SetCaps(c.CapUSD, c.EnforcingReserve, c.PerKeyCapUSD)
	b.mu.Lock()
	b.rollover()
	b.report()
	b.mu.Unlock()
	return b
}

// Kind is the budget's kind.
func (b *Budget) Kind() string { return b.kind }

// SetCaps changes the caps at runtime (settings.budgets). Negative values are 0 and the reserve
// is at most 1. Spend and holds are kept.
func (b *Budget) SetCaps(capUSD, enforcingReserve, perKeyCapUSD float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.capUSD, b.perKeyCapUSD = nonNegative(capUSD), nonNegative(perKeyCapUSD)
	b.enforcingReserve = min(nonNegative(enforcingReserve), 1)
}

// SetCap changes the daily cap only.
func (b *Budget) SetCap(capUSD float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.capUSD = nonNegative(capUSD)
}

// Reserve holds usd for one call. It fails with ErrCapReached when the day's spend and holds plus
// usd would pass the caller's limit (the cap when enforcing, the observe line otherwise), when the
// spend already reached that limit, or when key's spend would pass the per-key cap. usd of 0 still
// fails once the limit is reached: a caller that books its cost afterwards (a knowledge ingest)
// asks this way.
func (b *Budget) Reserve(usd float64, enforcing bool, key string) (*Hold, error) {
	usd = nonNegative(usd)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	if limit, capped := b.limitLocked(enforcing); capped {
		if b.spent+b.reserved+usd > limit+budgetEps || b.spent >= limit-budgetEps {
			b.hitLocked()
			return nil, ErrCapReached
		}
	}
	if key != "" {
		ks := b.keySpendLocked(key)
		if b.perKeyCapUSD > 0 && (ks.spent+ks.reserved+usd > b.perKeyCapUSD+budgetEps || ks.spent >= b.perKeyCapUSD-budgetEps) {
			return nil, ErrCapReached
		}
		ks.reserved += usd
	}
	b.reserved += usd
	b.report()
	return &Hold{b: b, usd: usd, key: key}, nil
}

// Headroom is the spend still allowed today: up to the cap when enforcing, up to the observe line
// otherwise, minus the day's spend and holds; +Inf without a cap, and 0 once it is gone.
func (b *Budget) Headroom(enforcing bool) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	limit, capped := b.limitLocked(enforcing)
	if !capped {
		return math.Inf(1)
	}
	if h := limit - b.spent - b.reserved; h > budgetEps {
		return h
	}
	return 0 // float rounding below a millionth of a cent is no room
}

// CapHitAt is when today's cap was first hit: a call refused by the cap or the observe line, or
// settled spend reaching the cap. nil when it was not hit today.
func (b *Budget) CapHitAt() *time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	if b.capHitAt == nil {
		return nil
	}
	t := *b.capHitAt
	return &t
}

// BudgetState is a budget's day.
type BudgetState struct {
	Day         string // UTC date
	CapUSD      float64
	SoftCapUSD  float64 // the observe line; equals CapUSD without a reserve (0 without a cap)
	SpentUSD    float64
	ReservedUSD float64
	// Exhausted: the spend reached the cap. ReserveOnly: not exhausted, but the spend (or the
	// spend and holds) reached the observe line, so only enforcing calls still spend.
	Exhausted   bool
	ReserveOnly bool
	CapHitAt    *time.Time
}

// State reports the budget's day.
func (b *Budget) State() BudgetState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	s := BudgetState{Day: b.day, CapUSD: b.capUSD, SpentUSD: b.spent, ReservedUSD: b.reserved}
	if b.capHitAt != nil {
		t := *b.capHitAt
		s.CapHitAt = &t
	}
	if b.capUSD > 0 {
		s.SoftCapUSD = b.capUSD * (1 - b.enforcingReserve)
		s.Exhausted = b.spent >= b.capUSD-budgetEps
		s.ReserveOnly = !s.Exhausted && s.SoftCapUSD < b.capUSD-budgetEps && b.spent+b.reserved >= s.SoftCapUSD-budgetEps
	}
	return s
}

// seed raises the day's spend to the stored totals when they are higher (max(memory, db)): rows
// another replica wrote, or rows written before a restart. The totals are of day; a seed read
// before a rollover is ignored.
func (b *Budget) seed(day string, total float64, byKey map[string]float64, crossedAt *time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	if day != b.day {
		return
	}
	// The stored spend reached the cap before this process saw it: the hit is when the row that
	// crossed it was written, not now (CO-11).
	if crossedAt != nil && b.capHitAt == nil && crossedAt.UTC().Format(time.DateOnly) == day {
		t := crossedAt.UTC()
		b.capHitAt = &t
	}
	b.spent = max(b.spent, nonNegative(total))
	for key, usd := range byKey {
		if key == "" {
			continue
		}
		ks := b.keySpendLocked(key)
		ks.spent = max(ks.spent, nonNegative(usd))
	}
	b.afterSpendLocked()
}

// currentDay is the budget's UTC date.
func (b *Budget) currentDay() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	return b.day
}

// limitLocked is the caller's limit and whether there is one. Caller holds mu.
func (b *Budget) limitLocked(enforcing bool) (float64, bool) {
	if b.capUSD <= 0 {
		return 0, false
	}
	if enforcing {
		return b.capUSD, true
	}
	return b.capUSD * (1 - b.enforcingReserve), true
}

// hitLocked records the first cap hit of the day. Caller holds mu.
func (b *Budget) hitLocked() {
	if b.capHitAt == nil {
		t := b.now().UTC()
		b.capHitAt = &t
	}
}

// afterSpendLocked records a cap hit when the spend reached the cap and reports the gauges.
// Caller holds mu.
func (b *Budget) afterSpendLocked() {
	if b.capUSD > 0 && b.spent >= b.capUSD-budgetEps {
		b.hitLocked()
	}
	b.report()
}

func (b *Budget) keySpendLocked(key string) *keySpend {
	ks := b.keys[key]
	if ks == nil {
		ks = &keySpend{}
		b.keys[key] = ks
	}
	return ks
}

// report sets spend_usd_today and spend_reserved_usd of the budget's kind. Caller holds mu.
func (b *Budget) report() { metrics.SetSpend(b.kind, b.spent, b.reserved) }

// rollover starts a new UTC day: spend, per-key spend and the cap hit restart; holds of running
// calls stay reserved. Caller holds mu.
func (b *Budget) rollover() {
	day := b.now().UTC().Format(time.DateOnly)
	if day == b.day {
		return
	}
	b.day, b.spent, b.capHitAt = day, 0, nil
	for k, ks := range b.keys {
		if ks.reserved <= budgetEps {
			delete(b.keys, k) // idle: recreated on its next call
			continue
		}
		ks.spent = 0
	}
	b.report()
}

// Hold is spend held for one call until it settles.
type Hold struct {
	b    *Budget
	usd  float64
	key  string
	once sync.Once
}

// USD is the amount held.
func (h *Hold) USD() float64 { return h.usd }

// Settle replaces the hold with the call's actual cost (0 releases it): the cost joins the spend
// of the day the call ends. Only the first call counts.
func (h *Hold) Settle(actualUSD float64) {
	h.once.Do(func() {
		b := h.b
		b.mu.Lock()
		defer b.mu.Unlock()
		b.rollover()
		actual := nonNegative(actualUSD)
		b.reserved = max(b.reserved-h.usd, 0)
		b.spent += actual
		if h.key != "" {
			ks := b.keySpendLocked(h.key)
			ks.reserved = max(ks.reserved-h.usd, 0)
			ks.spent += actual
		}
		b.afterSpendLocked()
	})
}

// nonNegative is v, or 0 when v is negative, infinite or not a number: a cap of 0 is unlimited
// and a cost that is not a number never reaches the spend.
func nonNegative(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
