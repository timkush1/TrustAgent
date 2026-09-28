package jev

import (
	"math"
	"sync"
	"time"
)

// limiter admits decision calls: a token bucket of RPM requests per minute and a cap on calls in
// flight. A share of both (TRUTHTABLE_GATE_JEV_SYNC_RESERVE) is reserved for synchronous calls:
// async (observe) calls stop while fewer tokens or slots than the reserve remain, so a burst of
// observe traffic cannot starve the routes that hold a response. Admission never blocks.
type limiter struct {
	rate        float64 // tokens per second
	burst       float64
	reserve     float64 // share reserved for sync calls
	maxInFlight int
	now         func() time.Time

	mu       sync.Mutex
	tokens   float64
	last     time.Time
	inFlight int
	window   rpmWindow
}

// newLimiter builds a limiter of rpm requests per minute. The bucket holds a 10-second burst
// (rpm/6), so no 60-second window exceeds 7/6·rpm: 1,167 at the default 1,000, below the vendor's
// 1,200 RPM.
func newLimiter(rpm int, reserve float64, maxInFlight int, now func() time.Time) *limiter {
	rpm = max(rpm, 1)
	burst := math.Max(1, math.Ceil(float64(rpm)/6))
	return &limiter{
		rate: float64(rpm) / 60, burst: burst, reserve: math.Min(math.Max(reserve, 0), 1),
		maxInFlight: max(maxInFlight, 1), now: now, tokens: burst, last: now(),
	}
}

// acquire takes a token and an in-flight slot for one attempt. It returns the slot's release
// function, or the error kind: overload (in-flight cap) or rate_limited (bucket).
func (l *limiter) acquire(class Class) (release func(), kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.refill(now)
	slotCap, tokenFloor := l.maxInFlight, 0.0
	if class == ClassAsync {
		slotCap = l.maxInFlight - int(math.Ceil(l.reserve*float64(l.maxInFlight)-1e-9))
		tokenFloor = l.reserve * l.burst
		if l.reserve < 1 {
			// A tiny limit still admits async calls: at least one slot and the last token.
			slotCap, tokenFloor = max(slotCap, 1), math.Min(tokenFloor, l.burst-1)
		}
	}
	if l.inFlight >= slotCap {
		return nil, KindOverload
	}
	if l.tokens-1 < tokenFloor-1e-9 {
		return nil, KindRateLimited
	}
	l.tokens--
	l.inFlight++
	l.window.add(now)
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.inFlight--
			l.mu.Unlock()
		})
	}, ""
}

func (l *limiter) refill(now time.Time) {
	if el := now.Sub(l.last).Seconds(); el > 0 {
		l.tokens = math.Min(l.burst, l.tokens+el*l.rate)
		l.last = now
	}
}

// load is the in-flight count and the requests admitted in the last minute.
func (l *limiter) load() (inFlight, lastMinute int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight, l.window.count(l.now())
}

// rpmWindow counts admissions over the last 60 seconds in one-second buckets.
type rpmWindow struct {
	buckets [60]int
	stamps  [60]int64
}

func (w *rpmWindow) add(t time.Time) {
	s := t.Unix()
	i := int(s % 60)
	if w.stamps[i] != s {
		w.stamps[i], w.buckets[i] = s, 0
	}
	w.buckets[i]++
}

func (w *rpmWindow) count(t time.Time) int {
	now, n := t.Unix(), 0
	for i := range w.buckets {
		if now-w.stamps[i] < 60 {
			n += w.buckets[i]
		}
	}
	return n
}
