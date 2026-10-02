package studio

import "sync"

// ReserveFactor scales a call's estimated cost into the reservation a Spend cap checks: the token
// estimate (SPEC §2.9 est with cpt) can undercount, so the cap keeps a 2x margin.
const ReserveFactor = 2.0

// Spend is a hard cap on the decision-model spend of one run (gate-eval --max-cost-usd, a Studio
// dry-run's confirmed estimate): no call starts unless its reserved cost fits in what is left,
// and after the first refusal the run stops. A nil *Spend and a zero cap are unlimited. It is safe
// for concurrent use.
type Spend struct {
	mu        sync.Mutex
	cap       float64 // 0 = unlimited
	spent     float64
	reserved  float64
	exhausted bool
}

// NewSpend returns a cap of capUSD (0 = unlimited).
func NewSpend(capUSD float64) *Spend { return &Spend{cap: capUSD} }

// Reserve claims usd of the cap. It fails, and marks the cap exhausted, when the claim would take
// spent plus outstanding reservations above the cap.
func (s *Spend) Reserve(usd float64) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exhausted {
		return false
	}
	if s.cap > 0 && s.spent+s.reserved+usd > s.cap {
		s.exhausted = true
		return false
	}
	s.reserved += usd
	return true
}

// Settle replaces a reservation with the actual cost.
func (s *Spend) Settle(reserved, actual float64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved -= reserved
	s.spent += actual
}

// Stopped reports whether the cap has refused a call.
func (s *Spend) Stopped() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exhausted
}

// Total returns the actual cost so far.
func (s *Spend) Total() float64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spent
}

// Cap returns the cap in USD (0 = unlimited).
func (s *Spend) Cap() float64 {
	if s == nil {
		return 0
	}
	return s.cap
}
