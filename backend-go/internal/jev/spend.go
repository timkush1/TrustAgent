package jev

import (
	"errors"
	"sync"
	"time"
)

// budgetEps absorbs float rounding in cap comparisons (a millionth of a cent).
const budgetEps = 1e-8

// ErrBudgetExhausted is returned by Budget.Reserve when the day's decision cap is reached.
var ErrBudgetExhausted = errors.New("daily decision spend cap reached")

// SpendRequest asks to hold the estimated cost of one attempt.
type SpendRequest struct {
	USD float64
	// Enforcing calls (block and fallback routes) may spend the enforcing reserve; observe and
	// flag calls stop at (1 − reserve)·cap.
	Enforcing bool
	// KeyLabel is the ingest key, for the optional per-key cap.
	KeyLabel string
}

// Budget admits decision-model spend against the daily caps (SPEC §7.4 caps; the cost ledger in
// internal/costs persists it). Reservations are held while an attempt runs and settled with the
// actual cost when it ends.
type Budget interface {
	Reserve(r SpendRequest) (Reservation, error)
}

// Reservation is spend held for one attempt.
type Reservation interface {
	// Settle replaces the held amount with the actual cost (0 releases it). Only the first call
	// counts.
	Settle(actualUSD float64)
}

// SpendStatus is a view of the day's decision spend.
type SpendStatus struct {
	Day         string  `json:"day"` // UTC date
	SpentUSD    float64 `json:"spent_usd"`
	ReservedUSD float64 `json:"reserved_usd"`
	// CapUSD is the daily cap (0 = unlimited); SoftCapUSD the limit of non-enforcing calls.
	CapUSD     float64 `json:"cap_usd"`
	SoftCapUSD float64 `json:"soft_cap_usd"`
	Exhausted  bool    `json:"exhausted"`
}

// SpendReporter is a Budget that can report its state.
type SpendReporter interface{ Spend() SpendStatus }

// MemoryBudget is the in-memory Budget: a UTC-daily decision cap with an enforcing reserve and an
// optional per-key cap. Spend restarts at 0 each UTC day and on restart; the persisted ledger
// (P4) seeds and replaces it.
type MemoryBudget struct {
	now func() time.Time

	mu               sync.Mutex
	capUSD           float64
	enforcingReserve float64
	perKeyCapUSD     float64
	day              string
	spent, reserved  float64
	keys             map[string]*keySpend
}

type keySpend struct{ spent, reserved float64 }

// BudgetConfig configures a MemoryBudget.
type BudgetConfig struct {
	// CapUSD is TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY (0 = unlimited).
	CapUSD float64
	// EnforcingReserve is TRUTHTABLE_SPEND_ENFORCING_RESERVE (0.25 by default).
	EnforcingReserve float64
	// PerKeyCapUSD is TRUTHTABLE_KEY_DECISION_USD_DAY (0 = off).
	PerKeyCapUSD float64
	Now          func() time.Time
}

// NewMemoryBudget builds an in-memory budget.
func NewMemoryBudget(c BudgetConfig) *MemoryBudget {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	b := &MemoryBudget{now: now, keys: map[string]*keySpend{}}
	b.SetCaps(c.CapUSD, c.EnforcingReserve, c.PerKeyCapUSD)
	return b
}

// SetCaps changes the caps at runtime (settings.budgets).
func (b *MemoryBudget) SetCaps(capUSD, enforcingReserve, perKeyCapUSD float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.capUSD, b.perKeyCapUSD = max(capUSD, 0), max(perKeyCapUSD, 0)
	b.enforcingReserve = min(max(enforcingReserve, 0), 1)
}

// Reserve holds r.USD unless it would take the day's spend and holds past the caller's limit.
func (b *MemoryBudget) Reserve(r SpendRequest) (Reservation, error) {
	usd := max(r.USD, 0)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	if b.capUSD > 0 {
		limit := b.capUSD
		if !r.Enforcing {
			limit = b.capUSD * (1 - b.enforcingReserve)
		}
		if b.spent+b.reserved+usd > limit+budgetEps || b.spent >= limit-budgetEps {
			return nil, ErrBudgetExhausted
		}
	}
	var ks *keySpend
	if b.perKeyCapUSD > 0 && r.KeyLabel != "" {
		ks = b.keys[r.KeyLabel]
		if ks == nil {
			ks = &keySpend{}
			b.keys[r.KeyLabel] = ks
		}
		if ks.spent+ks.reserved+usd > b.perKeyCapUSD+budgetEps {
			return nil, ErrBudgetExhausted
		}
		ks.reserved += usd
	}
	b.reserved += usd
	return &memReservation{b: b, usd: usd, key: ks}, nil
}

// rollover starts a new UTC day. Holds of running attempts stay reserved. Caller holds mu.
func (b *MemoryBudget) rollover() {
	day := b.now().UTC().Format(time.DateOnly)
	if day == b.day {
		return
	}
	b.day, b.spent = day, 0
	for _, k := range b.keys {
		k.spent = 0
	}
}

// Spend reports the day's spend.
func (b *MemoryBudget) Spend() SpendStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover()
	s := SpendStatus{Day: b.day, SpentUSD: b.spent, ReservedUSD: b.reserved, CapUSD: b.capUSD}
	if b.capUSD > 0 {
		s.SoftCapUSD = b.capUSD * (1 - b.enforcingReserve)
		s.Exhausted = b.spent >= s.SoftCapUSD-budgetEps
	}
	return s
}

type memReservation struct {
	b    *MemoryBudget
	usd  float64
	key  *keySpend
	once sync.Once
}

func (r *memReservation) Settle(actualUSD float64) {
	r.once.Do(func() {
		b := r.b
		b.mu.Lock()
		defer b.mu.Unlock()
		b.rollover()
		actual := max(actualUSD, 0)
		b.reserved = max(b.reserved-r.usd, 0)
		b.spent += actual
		if r.key != nil {
			r.key.reserved = max(r.key.reserved-r.usd, 0)
			r.key.spent += actual
		}
	})
}

// unlimitedBudget admits everything (a zero cap).
type unlimitedBudget struct{}

func (unlimitedBudget) Reserve(SpendRequest) (Reservation, error) { return noReservation{}, nil }

type noReservation struct{}

func (noReservation) Settle(float64) {}
