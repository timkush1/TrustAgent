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
	"github.com/truthtable/backend-go/internal/costs"
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

	// The engine stops answering: nothing is known about its judge and knowledge store any more
	// (unknown, not disabled).
	gone := grpc.EngineHealth{State: "down", Detail: "the audit engine did not answer its health check"}
	s.engine.set(gone)
	s.srv.publishEngineStatus(gone)
	s.expect(t, "engine:down", "judge:unknown", "knowledge:unknown")
	s.srv.watchStatus()
	s.expect(t)
}

// TestStatusEventsWaitForTheEngine: before the engine's first answer, its judge and knowledge
// store are not announced (in particular not as disabled); the first answer announces them.
func TestStatusEventsWaitForTheEngine(t *testing.T) {
	s := newStatusStream(t)
	s.engine.set(grpc.EngineHealth{State: "unknown", Detail: "not checked yet"})
	s.srv.watchStatus()
	s.expect(t, "decision_model:ready", "budget:ok")
	up := grpc.EngineHealth{State: "up", Response: &pb.HealthResponse{Healthy: true, KnowledgeEnabled: true,
		Dependencies: map[string]bool{"judge": true, "qdrant": true}}}
	s.engine.set(up)
	s.srv.publishEngineStatus(up)
	s.expect(t, "engine:up", "judge:up", "knowledge:up")
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

// TestStatusEventDetailsAreRedacted: an engine or decision-model detail that echoes a loaded secret
// reaches the stream masked (D42), including a secret no credential pattern matches.
func TestStatusEventDetailsAreRedacted(t *testing.T) {
	const secret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"
	if got := config.NewRedactor().Redact("key " + secret); !strings.Contains(got, secret) {
		t.Fatal("the pattern set alone masks the no-pattern secret; the test would prove nothing")
	}
	s := newStatusStream(t)
	s.srv.redactor = config.NewRedactor(secret)
	s.jev.set(func(st *jev.Status) {
		st.State = "unauthorized"
		st.LastError = &jev.StatusError{Kind: "unauthorized", Message: "vendor said: bad key " + secret}
		st.Spend = nil
	})
	s.engine.set(grpc.EngineHealth{State: "down", Detail: "judge refused key " + secret})
	s.srv.watchStatus()
	// Read every frame the transition broadcast (judge and knowledge stay unknown while the
	// engine is down): the engine and the decision model each carry a masked detail.
	details := map[string]string{}
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			break
		}
		if strings.Contains(string(msg), secret) {
			t.Errorf("a status frame carries the secret")
		}
		var f struct {
			Data struct{ Component, Detail string } `json:"data"`
		}
		_ = json.Unmarshal(msg, &f)
		details[f.Data.Component] = f.Data.Detail
	}
	for _, c := range []string{"engine", "decision_model"} {
		if d := details[c]; d == "" || !strings.Contains(d, "key") {
			t.Errorf("%s detail %q, want the masked message", c, d)
		}
	}
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
	if _, ok := budgetState(nil); ok {
		t.Error("a client without a budget reports a budget state")
	}
	if _, _, ok := (&server{}).budgetStatus(nil); ok {
		t.Error("a server without a ledger or a client budget reports a budget status")
	}
	for _, c := range []struct {
		sp    jev.SpendStatus
		state string
	}{
		{jev.SpendStatus{}, budgetOK}, // no cap
		{jev.SpendStatus{Exhausted: true}, budgetExhausted},
		{jev.SpendStatus{CapUSD: 1, SoftCapUSD: 0.75, SpentUSD: 0.5, ReservedUSD: 0.3}, budgetReserveOnly}, // a held call
	} {
		if state, ok := budgetState(&c.sp); !ok || state != c.state {
			t.Errorf("%+v: %s, want %s", c.sp, state, c.state)
		}
	}

	b := jev.NewMemoryBudget(jev.BudgetConfig{CapUSD: 1, EnforcingReserve: 0.25})
	check := func(step, want string) {
		t.Helper()
		sp := b.Spend()
		// Without a ledger only the decision cap is known: a status that is not ok names it.
		wantDetail := ""
		if want != budgetOK {
			wantDetail = capDecision
		}
		if state, detail, ok := (&server{}).budgetStatus(&sp); !ok || state != want || detail != wantDetail {
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

// TestBudgetStatusNamesTheCaps: with the cost ledger the budget status is the worse of the
// decision and LLM caps, and its detail names the caps in that state (D49): the LLM cap running
// out is a transition of its own, and both caps exhausted name both. The fixture of an LLM-cap
// event is a valid WsEvent.
func TestBudgetStatusNamesTheCaps(t *testing.T) {
	s := newStatusStream(t)
	book, err := costs.LoadPriceBook("")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := costs.NewLedger(costs.LedgerConfig{DecisionCapUSD: 1, EnforcingReserve: 0.25, LLMCapUSD: 0.5, Prices: book})
	if err != nil {
		t.Fatal(err)
	}
	s.srv.costs.decision = ledger
	expectBudget := func(state, detail string) {
		t.Helper()
		s.srv.watchStatus()
		for {
			f := readFrame(t, s.conn)
			var d struct{ Component, State, Detail string }
			_ = json.Unmarshal(f.Data, &d)
			if d.Component != componentBudget {
				continue // the other components' first states
			}
			if d.State != state || d.Detail != detail {
				t.Fatalf("budget %s (%q), want %s (%q)", d.State, d.Detail, state, detail)
			}
			return
		}
	}
	spend := func(reserve func(float64) (func(float64), error), usd float64) {
		t.Helper()
		settle, err := reserve(usd)
		if err != nil {
			t.Fatal(err)
		}
		settle(usd)
	}
	llm := ledger.LLM()
	llmReserve := func(usd float64) (func(float64), error) { return llm.Reserve(usd, "deep_audit") }
	decisionReserve := func(enforcing bool) func(float64) (func(float64), error) {
		return func(usd float64) (func(float64), error) {
			h, err := ledger.Decision().Reserve(usd, enforcing, "")
			if err != nil {
				return nil, err
			}
			return h.Settle, nil
		}
	}

	expectBudget(budgetOK, "")
	spend(llmReserve, 0.5)
	expectBudget(budgetExhausted, capLLM)
	spend(decisionReserve(false), 0.75) // the observe line
	s.srv.watchStatus()
	s.expect(t)                        // the decision cap's reserve_only is better than the LLM cap's exhausted
	spend(decisionReserve(true), 0.25) // enforcing calls spend the reserve
	expectBudget(budgetExhausted, capDecision+","+capLLM)

	// The budget fixtures name the caps as the gateway sends them (D49).
	for name, detail := range map[string]string{"budget-llm": capLLM, "budget-decision": capDecision} {
		fixture, err := contracts.FS.ReadFile("fixtures/api/WsEvent.status." + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		contractstest.ValidateJSON(t, schemaWsEvent, fixture)
		var ev struct {
			Data struct{ Component, State, Detail string } `json:"data"`
		}
		if err := json.Unmarshal(fixture, &ev); err != nil || ev.Data.Component != componentBudget || ev.Data.Detail != detail {
			t.Errorf("fixture %s %+v %v", name, ev.Data, err)
		}
	}
}
