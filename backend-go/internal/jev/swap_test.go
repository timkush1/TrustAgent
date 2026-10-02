package jev

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
)

// fakeClient records the capability calls Swappable delegates.
type fakeClient struct {
	name string

	mu      sync.Mutex
	warm    []bool
	closed  int
	decides int
}

func (f *fakeClient) Decide(context.Context, *Request, CallOptions) (*Response, error) {
	f.mu.Lock()
	f.decides++
	f.mu.Unlock()
	return &Response{Backend: f.name}, nil
}
func (f *fakeClient) Info() Info { return Info{Backend: f.name} }
func (f *fakeClient) SetKeepWarm(active bool) {
	f.mu.Lock()
	f.warm = append(f.warm, active)
	f.mu.Unlock()
}
func (f *fakeClient) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) state() (warm []bool, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.warm...), f.closed
}

// TestSwappableSwapUnderConcurrentDecide swaps mock clients while callers decide (run with
// -race): every call is answered by one whole client, and the last swap is the one in effect.
func TestSwappableSwapUnderConcurrentDecide(t *testing.T) {
	clients := make([]*mockClient, 4)
	for i := range clients {
		m, err := newMock(MockConfig{Mode: MockHash}, fmt.Sprintf("mock/model-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = m
	}
	s := NewSwappable(clients[0])
	req, err := ProbeRequest("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls, failures atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				resp, err := s.Decide(ctx, req, CallOptions{Class: ClassSync})
				if err != nil && ctx.Err() == nil {
					failures.Add(1)
					continue
				}
				if resp != nil && resp.Answers[ProbeQuestionID] == nil {
					failures.Add(1)
				}
				calls.Add(1)
				_ = s.Info().Model
				_ = s.Status().State
				_ = s.CPT()
			}
		}()
	}
	for i := range 200 {
		s.Swap(clients[(i+1)%len(clients)])
		if i%20 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d calls failed across swaps", failures.Load())
	}
	if calls.Load() == 0 {
		t.Fatal("no call ran during the swaps")
	}
	if got, want := s.Info().Model, clients[200%len(clients)].Info().Model; got != want {
		t.Fatalf("model in effect = %q, want the last swapped %q", got, want)
	}
}

// TestSwappableCarriesKeepWarmOver: the keep-warm wish moves to the new client at once and the
// replaced one stops pinging; a client swapped in before any wish is left alone.
func TestSwappableCarriesKeepWarmOver(t *testing.T) {
	a, b, c := &fakeClient{name: "a"}, &fakeClient{name: "b"}, &fakeClient{name: "c"}
	s := NewSwappable(a)
	s.grace = time.Hour
	t.Cleanup(func() { _ = s.Close() })

	s.Swap(b) // no wish yet: b is not told anything
	if warm, _ := b.state(); len(warm) != 0 {
		t.Fatalf("b got keep-warm calls %v before any wish", warm)
	}
	s.SetKeepWarm(true)
	if warm, _ := b.state(); len(warm) != 1 || !warm[0] {
		t.Fatalf("b keep-warm calls = %v, want [true]", warm)
	}
	s.Swap(c)
	if warm, _ := c.state(); len(warm) != 1 || !warm[0] {
		t.Fatalf("c keep-warm calls = %v, want [true] carried over", warm)
	}
	if warm, _ := b.state(); len(warm) != 2 || warm[1] {
		t.Fatalf("b keep-warm calls = %v, want it stopped on the swap", warm)
	}
	s.SetKeepWarm(false)
	if warm, _ := c.state(); len(warm) != 2 || warm[1] {
		t.Fatalf("c keep-warm calls = %v, want [true false]", warm)
	}
}

// TestSwappableClosesTheReplacedClientAfterTheGrace: a replaced client serves its running calls
// for the grace period and is closed after it; Close ends the waiting at once.
func TestSwappableClosesTheReplacedClientAfterTheGrace(t *testing.T) {
	a, b, c := &fakeClient{name: "a"}, &fakeClient{name: "b"}, &fakeClient{name: "c"}
	s := NewSwappable(a)
	s.grace = 20 * time.Millisecond
	if prev := s.Swap(b); prev != a {
		t.Fatalf("Swap returned %v, want the replaced client", prev)
	}
	if _, closed := a.state(); closed != 0 {
		t.Fatal("the replaced client was closed before its grace period ended")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, closed := a.state(); closed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replaced client was not closed after the grace period")
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.grace = time.Hour
	s.Swap(c)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, closed := b.state(); closed != 1 {
		t.Fatalf("Close left the retired client open (closed %d times)", closed)
	}
	if _, closed := c.state(); closed != 1 {
		t.Fatalf("Close left the current client open (closed %d times)", closed)
	}
	// After Close a swap closes the new client instead of putting it in effect.
	d := &fakeClient{name: "d"}
	s.Swap(d)
	if _, closed := d.state(); closed != 1 || s.Info().Backend != "c" {
		t.Fatalf("swap after Close: d closed %d times, in effect %q", closed, s.Info().Backend)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, closed := c.state(); closed != 1 {
		t.Fatal("a second Close closed the client again")
	}
}

// TestSwappableSwapBackCancelsThePendingClose: a client swapped back in during its grace period
// serves again and is never closed by its old timer; the one it replaced is.
func TestSwappableSwapBackCancelsThePendingClose(t *testing.T) {
	a, b := &fakeClient{name: "a"}, &fakeClient{name: "b"}
	s := NewSwappable(a)
	s.grace = 20 * time.Millisecond
	s.Swap(b)
	s.Swap(a)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, closed := b.state(); closed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replaced client was not closed after the grace period")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(4 * s.grace) // well past a's old timer
	if _, closed := a.state(); closed != 0 || s.Info().Backend != "a" {
		t.Fatalf("the client swapped back in was closed %d times while in effect (%q)", closed, s.Info().Backend)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, closed := a.state(); closed != 1 {
		t.Fatalf("Close closed the client in effect %d times, want once", closed)
	}
}

// TestSwappableSameClientIsANoOp: swapping in the client in effect neither stops its keep-warm
// nor schedules it for closing.
func TestSwappableSameClientIsANoOp(t *testing.T) {
	a := &fakeClient{name: "a"}
	s := NewSwappable(a)
	s.grace = time.Millisecond
	s.SetKeepWarm(true)
	s.Swap(a)
	time.Sleep(20 * time.Millisecond)
	if warm, closed := a.state(); len(warm) != 1 || closed != 0 {
		t.Fatalf("same-client swap: keep-warm %v, closed %d", warm, closed)
	}
}

// TestSwappableResetBreakerAfterAKeyChange: a rejected key opens the breaker for the auth
// cooldown. A replaced key is noticed by the KeyFunc only on the next call, so a secret-only
// change also calls ResetBreaker: the status shows the breaker closed at once, and the next call
// goes through with the new key.
func TestSwappableResetBreakerAfterAKeyChange(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	var key atomic.Value
	key.Store("sk-or-v1-invalid-0123456789")
	var origins []string
	var mu sync.Mutex
	keyFn := func(_ context.Context, origin string) (string, error) {
		mu.Lock()
		origins = append(origins, origin)
		mu.Unlock()
		return key.Load().(string), nil
	}
	c := h.client(BackendOpenRouterDecisions, func(cfg *Config) {
		cfg.OpenRouterKey = ""
		cfg.Binding = &Binding{Backend: BackendOpenRouterDecisions, Name: "openrouter", Key: keyFn}
	})
	s := NewSwappable(c)
	req, err := ProbeRequest(c.Info().Model)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = s.Decide(ctx, req, CallOptions{Class: ClassSync})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindAuth {
		t.Fatalf("first call: %v, want an auth error", err)
	}
	if st := s.Status(); st.Breaker != BreakerOpen {
		t.Fatalf("breaker after a 401 = %q, want open", st.Breaker)
	}
	// The new key is only seen by the next call: until then the status (and the policy's block
	// readiness, which reads it) still shows the breaker open.
	key.Store("sk-or-v1-good-0123456789")
	if st := s.Status(); st.Breaker != BreakerOpen {
		t.Fatalf("breaker after the key change, before any call = %q, want still open", st.Breaker)
	}
	s.ResetBreaker()
	if st := s.Status(); st.Breaker != BreakerClosed {
		t.Fatalf("breaker after ResetBreaker = %q, want closed", st.Breaker)
	}
	resp, err := s.Decide(ctx, req, CallOptions{Class: ClassSync})
	if err != nil || resp.Answers[ProbeQuestionID] == nil {
		t.Fatalf("call after the reset: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, o := range origins {
		if o != CanonicalOrigin(mustParse(t, h.srv.URL)) {
			t.Fatalf("KeyFunc asked for origin %q, want the endpoint's canonical origin", o)
		}
	}
}

// TestSwappableWithoutCapabilities: a client lacking the optional capabilities answers as one
// without them would.
func TestSwappableWithoutCapabilities(t *testing.T) {
	type bare struct{ Client }
	off, err := New(Config{Backend: BackendOff})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSwappable(bare{off})
	if st := s.Status(); st.State != "off" || st.Breaker != BreakerClosed {
		t.Fatalf("status = %q/%q, want off/closed", st.State, st.Breaker)
	}
	if s.CPT() != 0 {
		t.Fatalf("CPT = %v, want 0 (compile's default)", s.CPT())
	}
	if _, err := s.CheckKey(context.Background()); !errors.Is(err, ErrKeyCheckUnsupported) {
		t.Fatalf("CheckKey: %v, want ErrKeyCheckUnsupported", err)
	}
	s.ResetBreaker() // no breaker: a no-op
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
