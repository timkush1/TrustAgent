package jev

import (
	"sync"
	"time"
)

// Status is the live state of the decision-model backend (DecisionModelStatus core, SPEC §2.6).
type Status struct {
	Info
	// Snapshot answers the default model ("" until the first response).
	Snapshot string
	// State is off, mock, checking, ready, degraded, breaker_open, unauthorized, no_credits,
	// budget_exhausted, misconfigured or unreachable.
	State string
	// Breaker is closed, open or half_open; BreakerUntil ends an open period.
	Breaker      string
	BreakerUntil *time.Time
	LastError    *StatusError
	LastOK       *time.Time
	// RPMLimit is TRUTHTABLE_GATE_JEV_RPM; RPMUsed the attempts of the last minute.
	RPMLimit, RPMUsed, InFlight int
	// CPT is the runtime chars-per-token estimate.
	CPT float64
	// Calls, Attempts and CostUSD count since start; Calls24h and Cost24hUSD cover the last 24
	// hours (hourly buckets, so up to an hour more), for DecisionModelStatus.calls_24h/cost_24h_usd.
	Calls, Attempts int64
	CostUSD         float64
	Calls24h        int64
	Cost24hUSD      float64
	Spend           *SpendStatus
	KeepWarm        bool
}

// StatusError is the last error, with its plain-language hint.
type StatusError struct {
	Kind, Message, Hint string
	Status              int
	At                  time.Time
}

// statusTracker keeps the counters and the last outcome behind Status.
type statusTracker struct {
	mu          sync.Mutex
	lastErr     *StatusError
	lastOK      time.Time
	unreachable bool
	// transient marks a last error that a retry may cure (the in-flight 402 is kind credits but
	// says nothing about the balance).
	transient       bool
	calls, attempts int64
	costUSD         float64
	day             window24h
}

// window24h counts calls and cost in hourly buckets over the last 24 hours.
type window24h struct {
	hour  [24]int64 // the hour (Unix hours) each bucket holds
	calls [24]int64
	cost  [24]float64
}

func (w *window24h) bucket(at time.Time) int {
	h := at.Unix() / 3600
	i := int(h % 24)
	if w.hour[i] != h {
		w.hour[i], w.calls[i], w.cost[i] = h, 0, 0
	}
	return i
}

// totals sums the buckets of the 24 hours up to now.
func (w *window24h) totals(now time.Time) (calls int64, cost float64) {
	h := now.Unix() / 3600
	for i := range w.hour {
		if w.hour[i] > h-24 && w.hour[i] <= h {
			calls += w.calls[i]
			cost += w.cost[i]
		}
	}
	return calls, cost
}

func (s *statusTracker) call() {
	s.mu.Lock()
	s.calls++
	s.day.calls[s.day.bucket(time.Now())]++
	s.mu.Unlock()
}

// attempt records one finished attempt. Neutral outcomes (our cancellations) leave the last
// outcome alone.
func (s *statusTracker) attempt(at time.Time, costUSD float64, err *Error, neutral, network bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	s.costUSD += costUSD
	s.day.cost[s.day.bucket(at)] += costUSD
	switch {
	case neutral:
	case err == nil:
		s.lastOK, s.unreachable = at, false
	default:
		s.lastErr = &StatusError{Kind: err.Kind, Message: err.Message, Hint: err.Hint, Status: err.Status, At: at}
		s.unreachable, s.transient = network, err.Retryable
	}
}

// keyCheck records a key check or keep-warm outcome that says something about the credential.
func (s *statusTracker) keyCheck(at time.Time, err *Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		return
	}
	if (err.Kind == KindAuth || err.Kind == KindCredits) && !err.Retryable {
		s.lastErr = &StatusError{Kind: err.Kind, Message: err.Message, Hint: err.Hint, Status: err.Status, At: at}
		s.unreachable, s.transient = false, false
	}
}

// fill copies the tracked fields into st and derives st.State.
func (s *statusTracker) fill(st *Status, b breakerView, spend *SpendStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.Calls, st.Attempts, st.CostUSD = s.calls, s.attempts, s.costUSD
	st.Calls24h, st.Cost24hUSD = s.day.totals(time.Now())
	if !s.lastOK.IsZero() {
		t := s.lastOK
		st.LastOK = &t
	}
	if s.lastErr != nil {
		e := *s.lastErr
		st.LastError = &e
	}
	errAfterOK := s.lastErr != nil && s.lastErr.At.After(s.lastOK)
	kind := ""
	if errAfterOK {
		kind = s.lastErr.Kind
		if kind == KindCredits && s.transient {
			kind = KindRetryable // OpenRouter's in-flight budget: busy, not out of credits
		}
	}
	// An open period a refused key or account started is named by its own cause: a success that
	// was in flight before it (the startup probe, a slower answer) can land after the refusal.
	switch {
	case b.keyCause == KindAuth, b.state == BreakerOpen && kind == KindAuth:
		st.State = "unauthorized"
	case b.keyCause == KindCredits, b.state == BreakerOpen && kind == KindCredits:
		st.State = "no_credits"
	case b.state != BreakerClosed:
		st.State = "breaker_open"
	case spend != nil && spend.Exhausted:
		st.State = "budget_exhausted"
	case kind == KindAuth:
		st.State = "unauthorized"
	case kind == KindCredits:
		st.State = "no_credits"
	case kind == KindBadContentType || kind == KindNotFound || kind == KindBadRequest:
		st.State = "misconfigured"
	case errAfterOK && s.unreachable:
		st.State = "unreachable"
	case errAfterOK:
		st.State = "degraded"
	case s.lastOK.IsZero():
		st.State = "checking"
	default:
		st.State = "ready"
	}
}
