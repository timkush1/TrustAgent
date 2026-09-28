package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/truthtable/backend-go/internal/contractstest"
)

const wsEventSchema = "api.v1.schema.json#/$defs/WsEvent"

var liteStatus = map[string]any{
	"version": "test", "demo": true, "readonly": false, "engine": "up", "decision_model": "mock", "judge": "up",
}

type testHub struct {
	*Hub
	url    string
	cancel context.CancelFunc
}

// startHub runs a hub behind an httptest server. content decides the scope of every client; the
// client IP is the X-Test-IP header (for the per-IP cap).
func startHub(t *testing.T, cfg Config, content bool) *testHub {
	t.Helper()
	cfg.Logger = slog.New(slog.DiscardHandler)
	if cfg.Hello == nil {
		cfg.Hello = func() HelloInfo { return HelloInfo{Demo: true, Status: liteStatus} }
	}
	h := NewHub(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeWS(w, r, ConnOptions{Content: content, IP: r.Header.Get("X-Test-IP")})
	}))
	t.Cleanup(func() {
		cancel()
		<-done
		srv.Close()
	})
	return &testHub{Hub: h, url: "ws" + strings.TrimPrefix(srv.URL, "http"), cancel: cancel}
}

func dial(t *testing.T, h *testHub, query string, header http.Header) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(h.url+"/ws?"+query, header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// frame is a received v2 frame.
type frame struct {
	V       int             `json:"v"`
	Seq     *int64          `json:"seq"`
	Epoch   string          `json:"epoch"`
	Type    string          `json:"type"`
	TS      string          `json:"ts"`
	TraceID string          `json:"trace_id"`
	Data    json.RawMessage `json:"data"`
	raw     []byte
}

// next reads one message and checks that it is exactly one JSON document (B5).
func next(t *testing.T, conn *websocket.Conn) frame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != websocket.TextMessage {
		t.Fatalf("message type %d", kind)
	}
	dec := json.NewDecoder(strings.NewReader(string(msg)))
	var f frame
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("frame %q: %v", msg, err)
	}
	if dec.More() {
		t.Fatalf("one frame carried more than one JSON document: %q", msg)
	}
	f.raw = msg
	return f
}

// nextEvent skips heartbeats.
func nextEvent(t *testing.T, conn *websocket.Conn) frame {
	t.Helper()
	for {
		if f := next(t, conn); f.Type != "hb" {
			return f
		}
	}
}

func event(t *testing.T, typ, traceID string, data, redacted any) Event {
	t.Helper()
	ev, err := NewEvent(typ, traceID, data, redacted)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// waitClients waits until the hub has n clients.
func waitClients(t *testing.T, h *Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.ClientCount() != n {
		if time.Now().After(deadline) {
			t.Fatalf("clients: %d, want %d", h.ClientCount(), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func policyEvent(t *testing.T, i int) Event {
	return event(t, "policy_updated", "", map[string]any{"version": i, "source": "file", "author": "test", "message": "m"}, nil)
}

func TestHelloAndOneFramePerEvent(t *testing.T) {
	h := startHub(t, Config{Version: "9.9.9"}, true)
	conn := dial(t, h, "protocol=2", nil)
	hello := next(t, conn)
	contractstest.ValidateJSON(t, wsEventSchema, hello.raw)
	var hd struct {
		Protocol     int    `json:"protocol"`
		Epoch        string `json:"epoch"`
		LastSeq      int64  `json:"last_seq"`
		ReplayedFrom *int64 `json:"replayed_from"`
		Server       string `json:"server_version"`
	}
	if err := json.Unmarshal(hello.Data, &hd); err != nil {
		t.Fatal(err)
	}
	if hello.Type != "hello" || hd.Protocol != 2 || hd.Epoch != h.Epoch() || hd.LastSeq != 0 || hd.ReplayedFrom != nil || hd.Server != "9.9.9" {
		t.Fatalf("hello %s", hello.raw)
	}

	// Published back to back, every event still arrives as its own message (B5), in seq order.
	const n = 60
	for i := 1; i <= n; i++ {
		h.Publish(policyEvent(t, i))
	}
	for i := 1; i <= n; i++ {
		f := nextEvent(t, conn)
		if f.Type != "policy_updated" || f.Seq == nil || *f.Seq != int64(i) || f.Epoch != h.Epoch() || f.V != 2 {
			t.Fatalf("event %d: %s", i, f.raw)
		}
		contractstest.ValidateJSON(t, wsEventSchema, f.raw)
	}
}

func TestHeartbeat(t *testing.T) {
	h := startHub(t, Config{Heartbeat: 20 * time.Millisecond}, true)
	conn := dial(t, h, "protocol=2", nil)
	next(t, conn) // hello
	hb := next(t, conn)
	if hb.Type != "hb" || hb.Seq != nil || hb.Data != nil || hb.Epoch != "" {
		t.Fatalf("hb %s", hb.raw)
	}
	contractstest.ValidateJSON(t, wsEventSchema, hb.raw)
}

func TestRedactedVariant(t *testing.T) {
	full := startHub(t, Config{}, true)
	red := startHub(t, Config{}, false)
	for _, h := range []*testHub{full, red} {
		conn := dial(t, h, "protocol=2", nil)
		next(t, conn)
		h.Publish(event(t, "trace_started", "t1", map[string]any{"q": "secret question"}, map[string]any{"q": nil}))
		f := nextEvent(t, conn)
		got := string(f.Data)
		if h == full && got != `{"q":"secret question"}` || h == red && got != `{"q":null}` {
			t.Errorf("content=%v got %s", h == full, got)
		}
		if f.TraceID != "t1" {
			t.Errorf("trace_id %q", f.TraceID)
		}
	}
}

func TestReplayFromSinceSeq(t *testing.T) {
	h := startHub(t, Config{}, true)
	first := dial(t, h, "protocol=2", nil)
	next(t, first)
	for i := 1; i <= 3; i++ {
		h.Publish(policyEvent(t, i))
	}
	for i := 1; i <= 3; i++ {
		nextEvent(t, first)
	}

	conn := dial(t, h, "protocol=2&since_seq=1&epoch="+url.QueryEscape(h.Epoch()), nil)
	hello := next(t, conn)
	var hd struct {
		LastSeq      int64  `json:"last_seq"`
		ReplayedFrom *int64 `json:"replayed_from"`
	}
	_ = json.Unmarshal(hello.Data, &hd)
	if hd.LastSeq != 3 || hd.ReplayedFrom == nil || *hd.ReplayedFrom != 2 {
		t.Fatalf("hello %s", hello.raw)
	}
	for want := int64(2); want <= 3; want++ {
		if f := nextEvent(t, conn); *f.Seq != want || f.Type != "policy_updated" {
			t.Fatalf("replayed %s, want seq %d", f.raw, want)
		}
	}
	// Up to date: nothing is replayed.
	upToDate := dial(t, h, "protocol=2&since_seq=3&epoch="+h.Epoch(), nil)
	next(t, upToDate)
	h.Publish(policyEvent(t, 4))
	if f := nextEvent(t, upToDate); *f.Seq != 4 {
		t.Fatalf("after an up-to-date hello: %s", f.raw)
	}
}

func TestGapWhenReplayIsImpossible(t *testing.T) {
	h := startHub(t, Config{ReplayEvents: 2}, true)
	for i := 1; i <= 5; i++ {
		h.Publish(policyEvent(t, i))
	}
	// The publishes are processed before a later registration (one channel, in order) only once
	// Run has read them; a first client syncs on that.
	sync := dial(t, h, "protocol=2", nil)
	for {
		var hd struct {
			LastSeq int64 `json:"last_seq"`
		}
		f := next(t, sync)
		_ = json.Unmarshal(f.Data, &hd)
		if hd.LastSeq == 5 {
			break
		}
		_ = sync.Close()
		sync = dial(t, h, "protocol=2", nil)
	}

	evicted := dial(t, h, "protocol=2&since_seq=1&epoch="+h.Epoch(), nil)
	next(t, evicted)
	gap := next(t, evicted)
	contractstest.ValidateJSON(t, wsEventSchema, gap.raw)
	var g struct {
		From   int64  `json:"from_seq"`
		To     int64  `json:"to_seq"`
		Reason string `json:"reason"`
		Resync struct {
			Traces string `json:"traces"`
		} `json:"resync"`
	}
	_ = json.Unmarshal(gap.Data, &g)
	if gap.Type != "gap" || g.Reason != GapBufferEvicted || g.From != 2 || g.To != 5 || *gap.Seq != 5 ||
		!strings.HasPrefix(g.Resync.Traces, "/api/traces?updated_since=") {
		t.Fatalf("gap %s", gap.raw)
	}

	other := dial(t, h, "protocol=2&since_seq=4&epoch=another-epoch", nil)
	next(t, other)
	gap = next(t, other)
	_ = json.Unmarshal(gap.Data, &g)
	if g.Reason != GapEpochChanged || g.To != 5 {
		t.Fatalf("epoch gap %s", gap.raw)
	}
}

func TestV1ClientsGetV1FramesOnly(t *testing.T) {
	h := startHub(t, Config{}, true)
	conn := dial(t, h, "", nil)
	waitClients(t, h.Hub, 1)
	h.Publish(policyEvent(t, 1)) // no v1 variant: not sent
	ev := event(t, "audit_completed", "t1", map[string]any{"audit": 1}, nil)
	ev.V1Type, ev.V1 = "audit_result", json.RawMessage(`{"audit_id":"a1"}`)
	h.Publish(ev)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Data      json.RawMessage `json:"data"`
		V         *int            `json:"v"`
	}
	if err := json.Unmarshal(msg, &f); err != nil || f.Type != "audit_result" || string(f.Data) != `{"audit_id":"a1"}` || f.V != nil {
		t.Fatalf("v1 frame %s (%v)", msg, err)
	}
}

func TestPerIPCapClosesWith4429(t *testing.T) {
	h := startHub(t, Config{MaxPerIP: 1}, true)
	ip := http.Header{"X-Test-Ip": {"10.0.0.1"}}
	first := dial(t, h, "protocol=2", ip)
	next(t, first)
	second := dial(t, h, "protocol=2", ip)
	_ = second.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := second.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != CloseTooMany {
		t.Fatalf("want close %d, got %v", CloseTooMany, err)
	}
	// Another IP is fine.
	third := dial(t, h, "protocol=2", http.Header{"X-Test-Ip": {"10.0.0.2"}})
	next(t, third)
}

func TestStalledClientIsClosed(t *testing.T) {
	h := startHub(t, Config{Heartbeat: time.Hour, WriteStall: 20 * time.Millisecond}, true)
	conn := dial(t, h, "protocol=2", nil)
	next(t, conn)
	waitClients(t, h.Hub, 1)
	// No event and no heartbeat: no write succeeds within the stall window, so Run closes it.
	waitClients(t, h.Hub, 0)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("the stalled connection is still open")
	}
}

// B6: concurrent publishers, connects and disconnects. Run the package with -race.
func TestConcurrentPublishConnectAndDisconnect(t *testing.T) {
	h := startHub(t, Config{ClientBuffer: 8, MaxPerIP: 100}, false)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				h.Publish(event(t, "trace_started", fmt.Sprint(i), map[string]int{"i": i}, map[string]int{"i": -1}))
			}
		}()
	}
	for c := 0; c < 8; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				conn, _, err := websocket.DefaultDialer.Dial(h.url+"/ws?protocol=2", nil)
				if err != nil {
					t.Errorf("dial: %v", err)
					return
				}
				for k := 0; k < 3; k++ {
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					if _, msg, err := conn.ReadMessage(); err != nil {
						t.Errorf("read: %v", err)
						break
					} else if strings.Contains(string(msg), `"i":`) && !strings.Contains(string(msg), `"i":-1`) {
						t.Errorf("a client without content got the full variant: %s", msg)
					}
				}
				_ = conn.Close()
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
	waitClients(t, h.Hub, 0)
}

// The slow-consumer path, driven directly on Run's state (single goroutine).
func TestSlowConsumerGetsOneGapBeforeTheNextEvent(t *testing.T) {
	h := NewHub(Config{Logger: slog.New(slog.DiscardHandler)})
	c := &client{send: make(chan []byte, 3), protocol: ProtocolV2, content: true}
	h.clients[c] = struct{}{}
	for i := 1; i <= 5; i++ { // 3 fit, 4 and 5 are dropped
		h.dispatch(policyEvent(t, i))
	}
	if len(c.send) != 3 || c.gapFrom != 4 || c.gapTo != 5 {
		t.Fatalf("buffered %d, gap %d-%d", len(c.send), c.gapFrom, c.gapTo)
	}
	for len(c.send) > 0 { // the client catches up
		<-c.send
	}
	h.dispatch(policyEvent(t, 6))
	gap, ev := <-c.send, <-c.send
	var g struct {
		Type string `json:"type"`
		Seq  int64  `json:"seq"`
		Data struct {
			From   int64  `json:"from_seq"`
			To     int64  `json:"to_seq"`
			Reason string `json:"reason"`
		} `json:"data"`
	}
	_ = json.Unmarshal(gap, &g)
	if g.Type != "gap" || g.Data.Reason != GapSlowConsumer || g.Data.From != 4 || g.Data.To != 5 || g.Seq != 5 {
		t.Fatalf("gap %s", gap)
	}
	if !strings.Contains(string(ev), `"seq":6`) {
		t.Fatalf("event after the gap %s", ev)
	}
	contractstest.ValidateJSON(t, wsEventSchema, gap)
}

func TestProducerOverflowBecomesAGap(t *testing.T) {
	h := NewHub(Config{ProducerBuffer: 1, Logger: slog.New(slog.DiscardHandler)})
	c := &client{send: make(chan []byte, 8), protocol: ProtocolV2, content: true}
	h.clients[c] = struct{}{}
	h.Publish(policyEvent(t, 1))
	h.Publish(policyEvent(t, 2)) // dropped
	h.Publish(policyEvent(t, 3)) // dropped
	h.flushOverflow()
	h.dispatch(<-h.events)
	gap, ev := string(<-c.send), string(<-c.send)
	if !strings.Contains(gap, `"reason":"hub_overflow"`) || !strings.Contains(gap, `"from_seq":1,"to_seq":2`) {
		t.Fatalf("gap %s", gap)
	}
	if !strings.Contains(ev, `"seq":3`) {
		t.Fatalf("the next event follows the gap's seqs: %s", ev)
	}
}

// A reconnect whose next event was dropped at Publish (a hub_overflow hole in the seqs) cannot be
// replayed: it gets a buffer_evicted gap, not the events after the hole.
func TestReplayAcrossAnOverflowHoleIsAGap(t *testing.T) {
	h := NewHub(Config{ProducerBuffer: 1, Logger: slog.New(slog.DiscardHandler)})
	h.Publish(policyEvent(t, 1))
	h.dispatch(<-h.events) // seq 1
	h.Publish(policyEvent(t, 2))
	h.Publish(policyEvent(t, 3)) // dropped: seq 2
	h.flushOverflow()
	h.dispatch(<-h.events) // seq 3
	if h.ring.has(2) || !h.ring.has(1) || !h.ring.has(3) {
		t.Fatalf("ring seqs: has(1)=%v has(2)=%v has(3)=%v", h.ring.has(1), h.ring.has(2), h.ring.has(3))
	}

	since := int64(1)
	c := &client{send: make(chan []byte, 8), protocol: ProtocolV2, content: true}
	if err := h.add(&registration{c: c, sinceSeq: &since, epoch: h.Epoch()}); err != nil {
		t.Fatal(err)
	}
	hello, gap := string(<-c.send), string(<-c.send)
	if !strings.Contains(hello, `"replayed_from":null`) {
		t.Errorf("hello %s", hello)
	}
	if !strings.Contains(gap, `"reason":"buffer_evicted"`) || !strings.Contains(gap, `"from_seq":2,"to_seq":3`) {
		t.Errorf("gap %s", gap)
	}
	if len(c.send) != 0 {
		t.Errorf("%d events replayed across the hole", len(c.send))
	}
}

func TestRingCaps(t *testing.T) {
	r := newRing(3, 100)
	for i := int64(1); i <= 5; i++ {
		r.push(ringEntry{seq: i, size: 10})
	}
	if r.n != 3 || r.at(0).seq != 3 || !r.has(4) || r.has(2) || len(r.after(3)) != 2 {
		t.Fatalf("count cap: n=%d oldest=%d", r.n, r.at(0).seq)
	}
	r.push(ringEntry{seq: 6, size: 80}) // 10+10+80 = 100 fits the byte cap exactly; seq 4 made room
	if r.n != 3 || r.at(0).seq != 4 || r.bytes != 100 {
		t.Fatalf("count and byte caps: n=%d oldest=%d bytes=%d", r.n, r.at(0).seq, r.bytes)
	}
	r.push(ringEntry{seq: 7, size: 60}) // evicts 4 (count), then 5 and 6 (bytes)
	if r.n != 1 || r.at(0).seq != 7 || r.bytes != 60 {
		t.Fatalf("byte cap: n=%d bytes=%d", r.n, r.bytes)
	}
	r.push(ringEntry{seq: 7, size: 200})
	if r.n != 0 || r.bytes != 0 || r.has(7) {
		t.Fatalf("an event over the byte cap is not kept: n=%d", r.n)
	}
	if off := newRing(0, 100); off.has(1) {
		t.Fatal("a disabled ring holds nothing")
	}
}

// A replay longer than the client buffer is delivered whole, not cut short by a slow_consumer gap.
func TestReplayLargerThanTheClientBuffer(t *testing.T) {
	h := startHub(t, Config{ClientBuffer: 4}, true)
	const n = 20
	for i := 1; i <= n; i++ {
		h.Publish(policyEvent(t, i))
	}
	waitLastSeq(t, h, n)
	conn := dial(t, h, "protocol=2&since_seq=0&epoch="+h.Epoch(), nil)
	if hello := next(t, conn); hello.Type != "hello" {
		t.Fatalf("hello %s", hello.raw)
	}
	for want := int64(1); want <= n; want++ {
		if f := nextEvent(t, conn); f.Type != "policy_updated" || *f.Seq != want {
			t.Fatalf("replayed %s, want seq %d", f.raw, want)
		}
	}
}

// The ring keeps the ring variant for every client: an event without a redacted variant replays
// its ring data to clients without the content scope too.
func TestRingVariantServesRedactedClients(t *testing.T) {
	h := NewHub(Config{Logger: slog.New(slog.DiscardHandler)})
	ev := event(t, "audit_completed", "t1", map[string]any{"audit": map[string]any{"context_used": []string{"doc"}}}, nil)
	ev.Ring = json.RawMessage(`{"audit":{"context_used":[]}}`)
	h.dispatch(ev)
	entries := h.ring.after(0)
	if len(entries) != 1 || !strings.Contains(string(entries[0].full), `"context_used":[]`) ||
		!strings.Contains(string(entries[0].redacted), `"context_used":[]`) {
		t.Fatalf("ring %+v", entries)
	}
}

// waitLastSeq dials until a hello reports last_seq n, so every published event is in the ring.
func waitLastSeq(t *testing.T, h *testHub, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn := dial(t, h, "protocol=2", nil)
		var hd struct {
			LastSeq int64 `json:"last_seq"`
		}
		_ = json.Unmarshal(next(t, conn).Data, &hd)
		_ = conn.Close()
		if hd.LastSeq == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("last_seq %d, want %d", hd.LastSeq, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestOriginCheck: the allow-list and the OriginAllowed hook admit an origin, anything else is
// refused before the upgrade, and a request without an Origin (a non-browser client) connects.
func TestOriginCheck(t *testing.T) {
	h := startHub(t, Config{
		AllowedOrigins: []string{"http://allowed.example"},
		OriginAllowed:  func(o string) bool { return o == "https://dashboard.example" },
	}, true)
	for origin, ok := range map[string]bool{
		"":                          true,
		"http://allowed.example":    true,
		"https://dashboard.example": true,
		"https://evil.example":      false,
	} {
		header := http.Header{}
		if origin != "" {
			header.Set("Origin", origin)
		}
		conn, resp, err := websocket.DefaultDialer.Dial(h.url+"/ws?protocol=2", header)
		if ok {
			if err != nil {
				t.Errorf("origin %q refused: %v", origin, err)
				continue
			}
			_ = conn.Close()
			continue
		}
		if err == nil {
			_ = conn.Close()
			t.Errorf("origin %q connected", origin)
		} else if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: want 403, got %v", origin, resp)
		}
	}
}
