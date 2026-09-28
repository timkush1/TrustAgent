package jev

import (
	"sync"
	"time"
)

// Breaker states (DecisionModelStatus.breaker).
const (
	BreakerClosed   = "closed"
	BreakerOpen     = "open"
	BreakerHalfOpen = "half_open"
)

// breaker is the circuit breaker of one backend. It counts vendor faults only: our own deadline
// cancellations, hedge losers and requests the vendor refused on their merits are neutral.
//
// closed → open after `failures` consecutive faults (cooldown), at once on a 200 text/html
// (cooldown) or on auth and credit failures (authCooldown). open → half_open when the cooldown
// ends: one trial call is let through, and its outcome closes or reopens the breaker.
type breaker struct {
	failures               int
	cooldown, authCooldown time.Duration
	now                    func() time.Time
	onChange               func(state string)

	mu          sync.Mutex
	state       string
	consecutive int
	openUntil   time.Time
	trial       bool // the half-open trial is in flight
	lastCause   string
}

func newBreaker(failures int, cooldown, authCooldown time.Duration, now func() time.Time, onChange func(string)) *breaker {
	return &breaker{failures: max(failures, 1), cooldown: cooldown, authCooldown: authCooldown, now: now, onChange: onChange, state: BreakerClosed}
}

// allow reports whether a call may go out. In half_open it admits a single trial (trial=true),
// whose outcome must be recorded with the trial flag; hedges are never trials.
func (b *breaker) allow(hedge bool) (ok, trial bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advance()
	switch b.state {
	case BreakerClosed:
		return true, false
	case BreakerHalfOpen:
		if hedge || b.trial {
			return false, false
		}
		b.trial = true
		return true, true
	}
	return false, false
}

// advance moves open → half_open once the cooldown has passed. Caller holds mu.
func (b *breaker) advance() {
	if b.state == BreakerOpen && !b.now().Before(b.openUntil) {
		b.set(BreakerHalfOpen)
		b.trial = false
	}
}

// record applies one attempt outcome. trial is the flag allow returned for the attempt.
func (b *breaker) record(effect breakerEffect, trial bool, cause string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if trial {
		b.trial = false
	}
	switch effect {
	case effectSuccess:
		b.consecutive = 0
		// A late success from before the breaker opened does not close it; the trial does.
		if b.state == BreakerHalfOpen && trial || b.state == BreakerClosed {
			b.set(BreakerClosed)
		}
	case effectFault:
		b.consecutive++
		switch {
		case b.state == BreakerHalfOpen && trial:
			b.open(b.cooldown, cause)
		case b.state == BreakerClosed && b.consecutive >= b.failures:
			b.open(b.cooldown, cause)
		}
	case effectTrip:
		b.consecutive++
		b.open(b.cooldown, cause)
	case effectAuth:
		b.consecutive++
		b.open(b.authCooldown, cause)
	}
}

// open opens the breaker for d (never shortening an open period). Caller holds mu.
func (b *breaker) open(d time.Duration, cause string) {
	until := b.now().Add(d)
	if b.state == BreakerOpen && until.Before(b.openUntil) {
		return
	}
	b.openUntil, b.lastCause, b.trial = until, cause, false
	b.set(BreakerOpen)
}

func (b *breaker) set(state string) {
	if b.state == state {
		return
	}
	b.state = state
	if b.onChange != nil {
		b.onChange(state)
	}
}

// reset closes the breaker (a credential change).
func (b *breaker) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive, b.trial, b.lastCause = 0, false, ""
	b.set(BreakerClosed)
}

// breakerView is a consistent read of the breaker.
type breakerView struct {
	state       string
	until       time.Time
	consecutive int
	cause       string
}

func (b *breaker) view() breakerView {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advance()
	v := breakerView{state: b.state, consecutive: b.consecutive, cause: b.lastCause}
	if b.state == BreakerOpen {
		v.until = b.openUntil
	}
	return v
}
