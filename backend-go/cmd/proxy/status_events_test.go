package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/contracts"
)

// mutableHealth is a cached engine health the test changes.
type mutableHealth struct {
	mu sync.Mutex
	h  grpc.EngineHealth
}

func (m *mutableHealth) Health() grpc.EngineHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.h
}

func (m *mutableHealth) set(h grpc.EngineHealth) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.h = h
}

// statusJev is a decision-model client that reports the status the test sets.
type statusJev struct {
	mu sync.Mutex
	st jev.Status
}

func (j *statusJev) Decide(context.Context, *jev.Request, jev.CallOptions) (*jev.Response, error) {
	return nil, errors.New("not in this test")
}
func (j *statusJev) Info() jev.Info { return jev.Info{Backend: "openrouter-decisions"} }
func (j *statusJev) Status() jev.Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}
func (j *statusJev) set(f func(*jev.Status)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f(&j.st)
}

// statusStream is a server with a live hub and one WS client reading its frames.
type statusStream struct {
	srv    *server
	engine *mutableHealth
	jev    *statusJev
	conn   *gws.Conn
}

func newStatusStream(t *testing.T) *statusStream {
	t.Helper()
	hub := websocket.NewHub(websocket.Config{Version: "1.0.0", Logger: slog.New(slog.DiscardHandler), Hello: func() websocket.HelloInfo {
		return websocket.HelloInfo{Status: map[string]any{"version": "1.0.0", "demo": false, "readonly": false, "engine": "up",
			"decision_model": "ready", "judge": "up"}}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r, websocket.ConnOptions{Content: true, IP: "127.0.0.1"})
	}))
	t.Cleanup(func() { cancel(); <-done; hs.Close() })
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+"/ws?protocol=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if f := readFrame(t, conn); f.Type != "hello" {
		t.Fatalf("first frame %s", f.raw)
	}
	up := grpc.EngineHealth{State: "up", Response: &pb.HealthResponse{Healthy: true, Dependencies: map[string]bool{"judge": true}}}
	st := &statusJev{st: jev.Status{State: "ready", Breaker: jev.BreakerClosed, Spend: &jev.SpendStatus{CapUSD: 1, SoftCapUSD: 0.75}}}
	s := &statusStream{engine: &mutableHealth{h: up}, jev: st, conn: conn}
	s.srv = &server{cfg: &config.Config{}, log: slog.New(slog.DiscardHandler), hub: hub,
		status: &statusReporter{engine: s.engine, jevClient: st, now: time.Now}}
	return s
}

// expect reads the next status frames and checks them against want ("component:state"), then
// checks that nothing else was broadcast.
func (s *statusStream) expect(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		f := readFrame(t, s.conn)
		var d struct{ Component, State, Detail string }
		_ = json.Unmarshal(f.Data, &d)
		if f.Type != "status" || d.Component+":"+d.State != w {
			t.Fatalf("frame %s, want %s", f.raw, w)
		}
	}
	// A marker on a component the watch never reports (redis) must be the next frame.
	s.srv.hub.PublishStatus("redis", "marker", "")
	if f := readFrame(t, s.conn); !strings.Contains(string(f.Data), `"marker"`) {
		t.Fatalf("an extra frame %s", f.raw)
	}
	s.srv.hub.PublishStatus("redis", "reset", "")
	readFrame(t, s.conn)
}

// TestStatusEventsFireOncePerTransition: every component's state is broadcast when it changes,
// once, whether the engine pinger or the 5 s watch notices it first.
func TestStatusEventsFireOncePerTransition(t *testing.T) {
	s := newStatusStream(t)
	s.srv.watchStatus()
	s.expect(t, "engine:up", "judge:up", "knowledge:disabled", "decision_model:ready", "budget:ok")
	s.srv.watchStatus()
	s.expect(t) // nothing changed

	// The judge falls back while the engine stays up: only the watch sees it.
	s.engine.set(grpc.EngineHealth{State: "up", Response: &pb.HealthResponse{Healthy: true, JudgeFallbackActive: true,
		Dependencies: map[string]bool{"judge": true}}})
	s.srv.watchStatus()
	s.expect(t, "judge:degraded")

	// The pinger reports the engine degraded by its judge: engine and judge change, once.
	down := grpc.EngineHealth{State: "degraded", Detail: "the audit engine reports judge unhealthy",
		Response: &pb.HealthResponse{Dependencies: map[string]bool{"judge": false}}}
	s.engine.set(down)
	s.srv.publishEngineStatus(down)
	s.expect(t, "engine:degraded", "judge:down")
	s.srv.watchStatus()
	s.expect(t)

	// The breaker opens, then the budget runs out.
	s.jev.set(func(st *jev.Status) {
		st.State, st.Breaker = "breaker_open", jev.BreakerOpen
		st.LastError = &jev.StatusError{Kind: "timeout", Message: "the decision model timed out 5 times"}
	})
	s.srv.watchStatus()
	s.expect(t, "decision_model:breaker_open")
	// As jev reports it: past the soft cap Exhausted is already true, yet enforcing routes still
	// spend the reserve.
	s.jev.set(func(st *jev.Status) {
		st.Spend = &jev.SpendStatus{CapUSD: 1, SoftCapUSD: 0.75, SpentUSD: 0.8, Exhausted: true}
	})
	s.srv.watchStatus()
	s.expect(t, "budget:reserve_only")
	s.jev.set(func(st *jev.Status) {
		st.Spend = &jev.SpendStatus{CapUSD: 1, SoftCapUSD: 0.75, SpentUSD: 1, Exhausted: true}
	})
	s.srv.watchStatus()
	s.expect(t, "budget:exhausted")
	s.srv.watchStatus()
	s.expect(t)

	// The knowledge add-on loses its store.
	s.engine.set(grpc.EngineHealth{State: "degraded", Response: &pb.HealthResponse{KnowledgeEnabled: true,
		Dependencies: map[string]bool{"judge": false, "qdrant": false}}})
	s.srv.watchStatus()
	s.expect(t, "knowledge:down")
}

func TestStatusEventDetails(t *testing.T) {
	s := newStatusStream(t)
	s.jev.set(func(st *jev.Status) {
		st.State = "unauthorized"
		st.LastError = &jev.StatusError{Kind: "unauthorized", Message: "the decision model refused the key"}
		st.Spend = nil // no budget kept: no budget component
	})
	s.engine.set(grpc.EngineHealth{State: "up", Response: &pb.HealthResponse{JudgeProvider: "none"}})
	s.srv.watchStatus()
	for _, want := range [][3]string{
		{"engine", "up", ""},
		{"judge", "disabled", "no deep-audit judge is available: audits come back not assessed"},
		{"knowledge", "disabled", ""},
		{"decision_model", "unauthorized", "the decision model refused the key"},
	} {
		f := readFrame(t, s.conn)
		var d struct{ Component, State, Detail string }
		_ = json.Unmarshal(f.Data, &d)
		if f.Type != "status" || [3]string{d.Component, d.State, d.Detail} != want {
			t.Errorf("frame %s, want %v", f.raw, want)
		}
	}
	s.expect(t)
}

// TestStatusFixture: the golden status event fixture is a valid WsEvent with a component and state.
func TestStatusFixture(t *testing.T) {
	for _, name := range []string{"WsEvent.status.json", "WsEvent.calibration_updated.json"} {
		b, err := contracts.FS.ReadFile("fixtures/api/" + name)
		if err != nil {
			t.Fatal(err)
		}
		contractstest.ValidateJSON(t, schemaWsEvent, b)
	}
}

// TestBudgetState maps the states a real decision budget reports: reserve_only once observe and
// flag calls stop at their share of the cap (jev's Exhausted is already true there), exhausted
// only once enforcing calls have spent the reserve too.
func TestBudgetState(t *testing.T) {
	if _, _, ok := budgetState(nil); ok {
		t.Error("a client without a budget reports a budget state")
	}
	for _, c := range []struct {
		sp    jev.SpendStatus
		state string
	}{
		{jev.SpendStatus{}, budgetOK}, // no cap
		{jev.SpendStatus{Exhausted: true}, budgetExhausted},
		{jev.SpendStatus{CapUSD: 1, SoftCapUSD: 0.75, SpentUSD: 0.5, ReservedUSD: 0.3}, budgetReserveOnly}, // a held call
	} {
		if state, _, ok := budgetState(&c.sp); !ok || state != c.state {
			t.Errorf("%+v: %s, want %s", c.sp, state, c.state)
		}
	}

	b := jev.NewMemoryBudget(jev.BudgetConfig{CapUSD: 1, EnforcingReserve: 0.25})
	check := func(step, want string) {
		t.Helper()
		sp := b.Spend()
		if state, detail, ok := budgetState(&sp); !ok || state != want || (want != budgetOK) == (detail == "") {
			t.Errorf("%s: %s (%q), want %s; spend %+v", step, state, detail, want, sp)
		}
	}
	spend := func(usd float64, enforcing bool) {
		t.Helper()
		r, err := b.Reserve(jev.SpendRequest{USD: usd, Enforcing: enforcing})
		if err != nil {
			t.Fatalf("reserve %v (enforcing %t): %v", usd, enforcing, err)
		}
		r.Settle(usd)
	}
	check("fresh", budgetOK)
	spend(0.5, false)
	check("half the share", budgetOK)
	held, err := b.Reserve(jev.SpendRequest{USD: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	check("a held call fills the share", budgetReserveOnly)
	held.Settle(0)
	check("the hold released", budgetOK)
	spend(0.3, true)
	check("past the share", budgetReserveOnly)
	if _, err := b.Reserve(jev.SpendRequest{USD: 0.01}); !errors.Is(err, jev.ErrBudgetExhausted) {
		t.Fatalf("an observe call past the share: %v", err)
	}
	spend(0.2, true)
	check("the cap", budgetExhausted)

	// Without a reserve the share is the cap: no reserve_only state.
	b = jev.NewMemoryBudget(jev.BudgetConfig{CapUSD: 1})
	spend(0.9, false)
	check("no reserve, 90%", budgetOK)
	spend(0.1, false)
	check("no reserve, the cap", budgetExhausted)
}
