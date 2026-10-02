package jev

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// SwapGrace is how long a replaced client keeps serving the calls it already started before it
// is closed (P4-PLAN D35: a constant, no registry key).
const SwapGrace = 30 * time.Second

// BreakerResetter closes a backend's circuit breaker: a credential that changed outside the
// client's KeyFunc deserves a fresh try at once instead of waiting out an auth cooldown.
type BreakerResetter interface{ ResetBreaker() }

// Swappable is a decision-model Client whose backend is replaced at run time (P4-PLAN CN-1): a
// changed decision_model setting or binding builds a new client and swaps it in without a
// restart. Every call goes to the client in effect when it starts; a replaced client finishes the
// calls it already started and is closed after SwapGrace. The keep-warm wish survives a swap.
//
// Swappable implements every optional capability (StatusReporter, KeepWarmer, CPTSource,
// KeyChecker, BreakerResetter and io.Closer) and delegates to the current client, answering
// as a client without that capability would when the current one lacks it.
type Swappable struct {
	cur   atomic.Pointer[swapSlot]
	grace time.Duration

	mu      sync.Mutex // serializes Swap, SetKeepWarm and Close
	warm    bool       // the last SetKeepWarm wish
	warmSet bool
	closed  bool
	retired map[*time.Timer]Client // replaced clients waiting out their grace period
}

// swapSlot boxes the interface value for atomic.Pointer.
type swapSlot struct{ c Client }

// Interface checks: the gateway, the status page and jevcheck discover these by assertion.
var (
	_ Client          = (*Swappable)(nil)
	_ StatusReporter  = (*Swappable)(nil)
	_ KeepWarmer      = (*Swappable)(nil)
	_ CPTSource       = (*Swappable)(nil)
	_ KeyChecker      = (*Swappable)(nil)
	_ BreakerResetter = (*Swappable)(nil)
	_ io.Closer       = (*Swappable)(nil)
)

// NewSwappable wraps c, the client in effect until the first Swap.
func NewSwappable(c Client) *Swappable {
	s := &Swappable{grace: SwapGrace, retired: map[*time.Timer]Client{}}
	s.cur.Store(&swapSlot{c: c})
	return s
}

// Current is the client in effect.
func (s *Swappable) Current() Client { return s.cur.Load().c }

// Swap puts next in effect and returns the client it replaced. The keep-warm wish moves to next
// at once; the replaced client stops keeping warm, finishes its running calls and is closed after
// the grace period. After Close, next is closed instead of being put in effect.
func (s *Swappable) Swap(next Client) (prev Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		closeClient(next)
		return nil
	}
	prev = s.cur.Swap(&swapSlot{c: next}).c
	if sameClient(prev, next) {
		return prev
	}
	// A client swapped back in before its grace period ended is in effect again: its pending
	// close is cancelled, or it would be closed while it serves.
	for t, c := range s.retired {
		if sameClient(c, next) {
			t.Stop()
			delete(s.retired, t)
		}
	}
	if s.warmSet {
		if kw, ok := next.(KeepWarmer); ok {
			kw.SetKeepWarm(s.warm)
		}
	}
	if kw, ok := prev.(KeepWarmer); ok {
		kw.SetKeepWarm(false)
	}
	if s.grace <= 0 {
		closeClient(prev)
		return prev
	}
	var t *time.Timer
	t = time.AfterFunc(s.grace, func() {
		s.mu.Lock()
		c, ok := s.retired[t]
		delete(s.retired, t)
		s.mu.Unlock()
		if ok {
			closeClient(c)
		}
	})
	s.retired[t] = prev
	return prev
}

// sameClient reports whether a and b are the same client value (pointers in practice; a value
// of an uncomparable type is never the same).
func sameClient(a, b Client) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta := reflect.TypeOf(a)
	return ta == reflect.TypeOf(b) && ta.Comparable() && a == b
}

// closeClient closes c when it holds resources (an HTTP backend's keep-warm loop and connections).
func closeClient(c Client) {
	if cl, ok := c.(io.Closer); ok {
		_ = cl.Close()
	}
}

// Decide sends the call to the client in effect.
func (s *Swappable) Decide(ctx context.Context, req *Request, opt CallOptions) (*Response, error) {
	return s.Current().Decide(ctx, req, opt)
}

// Info describes the client in effect.
func (s *Swappable) Info() Info { return s.Current().Info() }

// Status reports the client in effect; a client without a live status reports off (or mock).
func (s *Swappable) Status() Status {
	c := s.Current()
	if r, ok := c.(StatusReporter); ok {
		return r.Status()
	}
	info := c.Info()
	st := Status{Info: info, State: "off", Breaker: BreakerClosed}
	if info.Backend == BackendMock {
		st.State = "mock"
	}
	return st
}

// SetKeepWarm records the wish and applies it to the client in effect; a later Swap carries it
// over to the new client.
func (s *Swappable) SetKeepWarm(active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warm, s.warmSet = active, true
	if s.closed {
		return
	}
	if kw, ok := s.Current().(KeepWarmer); ok {
		kw.SetKeepWarm(active)
	}
}

// CPT is the current client's chars-per-token estimate; 0 (compile's default) without one.
func (s *Swappable) CPT() float64 {
	if src, ok := s.Current().(CPTSource); ok {
		return src.CPT()
	}
	return 0
}

// CheckKey runs the current client's key check; ErrKeyCheckUnsupported without one.
func (s *Swappable) CheckKey(ctx context.Context) (*KeyInfo, error) {
	if kc, ok := s.Current().(KeyChecker); ok {
		return kc.CheckKey(ctx)
	}
	return nil, ErrKeyCheckUnsupported
}

// ResetBreaker closes the current client's breaker, if it has one.
func (s *Swappable) ResetBreaker() {
	if r, ok := s.Current().(BreakerResetter); ok {
		r.ResetBreaker()
	}
}

// Close closes the client in effect and every replaced client still in its grace period. Later
// swaps close their client at once.
func (s *Swappable) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pending := make([]Client, 0, len(s.retired)+1)
	for t, c := range s.retired {
		t.Stop()
		pending = append(pending, c)
	}
	clear(s.retired)
	pending = append(pending, s.Current())
	s.mu.Unlock()
	var errs []error
	for _, c := range pending {
		if cl, ok := c.(io.Closer); ok {
			errs = append(errs, cl.Close())
		}
	}
	return errors.Join(errs...)
}
