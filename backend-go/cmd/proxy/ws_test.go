package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/truthtable/backend-go/internal/contractstest"
)

const schemaWsEvent = "api.v1.schema.json#/$defs/WsEvent"

type wsFrame struct {
	Type    string          `json:"type"`
	Seq     int64           `json:"seq"`
	Epoch   string          `json:"epoch"`
	TraceID string          `json:"trace_id"`
	Data    json.RawMessage `json:"data"`
	raw     []byte
}

func dialWS(t *testing.T, env *testEnv, query string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(env.http.URL, "http")+"/ws?"+query, header)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, resp, err
}

// readFrame reads one message and checks it is one JSON document valid as a WsEvent (B5).
func readFrame(t *testing.T, conn *websocket.Conn) wsFrame {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(msg)))
	var f wsFrame
	if err := dec.Decode(&f); err != nil || dec.More() {
		t.Fatalf("frame %q is not exactly one JSON document (%v)", msg, err)
	}
	contractstest.ValidateJSON(t, schemaWsEvent, msg)
	f.raw = msg
	return f
}

// TestStreamCarriesTheTraceLifecycle follows the flagged scenario over WS v2: trace_started,
// gate_decision, audit_queued, audit_started and audit_completed, in seq order, one event per
// frame.
func TestStreamCarriesTheTraceLifecycle(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	conn, _, err := dialWS(t, env, "protocol=2", http.Header{"X-TrustAgent-Key": {testKey}})
	if err != nil {
		t.Fatal(err)
	}
	hello := readFrame(t, conn)
	var hd struct {
		Protocol int            `json:"protocol"`
		Status   map[string]any `json:"status"`
	}
	_ = json.Unmarshal(hello.Data, &hd)
	if hello.Type != "hello" || hd.Protocol != 2 || hd.Status["engine"] != "up" || hd.Status["decision_model"] != "mock" {
		t.Fatalf("hello %s", hello.raw)
	}

	traceID := env.send(loadDemoScenarios(t)["refund-contradiction-flag"]).Header.Get("X-TrustAgent-Trace-Id")
	var types []string
	last := hello.Seq
	for len(types) < 5 {
		f := readFrame(t, conn)
		if f.Type == "hb" {
			continue
		}
		if f.Type == "status" { // the engine pinger's first transition may land here
			last = f.Seq
			continue
		}
		if f.TraceID != traceID || f.Seq != last+1 || f.Epoch != hello.Epoch {
			t.Fatalf("frame %s (trace %s, previous seq %d)", f.raw, traceID, last)
		}
		last = f.Seq
		types = append(types, f.Type)
		switch f.Type {
		case "trace_started":
			if !strings.Contains(string(f.Data), `"gate_status":"pending"`) || !strings.Contains(string(f.Data), `"action":null`) {
				t.Errorf("trace_started %s", f.Data)
			}
		case "gate_decision":
			if !strings.Contains(string(f.Data), `"spec":"decision/v1"`) || !strings.Contains(string(f.Data), `"action":"flag"`) {
				t.Errorf("gate_decision %s", f.Data)
			}
		case "audit_queued":
			if !strings.Contains(string(f.Data), `"lane":"high"`) || !strings.Contains(string(f.Data), `"queue_depth":`) {
				t.Errorf("audit_queued %s", f.Data)
			}
		case "audit_started":
			if !strings.Contains(string(f.Data), `"queue_wait_ms":`) {
				t.Errorf("audit_started %s", f.Data)
			}
		case "audit_completed":
			if !strings.Contains(string(f.Data), `"verdict":"HALLUCINATED"`) || !strings.Contains(string(f.Data), `"status":"CONTRADICTED"`) {
				t.Errorf("audit_completed %s", f.Data)
			}
		}
	}
	if strings.Join(types, ",") != "trace_started,gate_decision,audit_queued,audit_started,audit_completed" {
		t.Errorf("events %v", types)
	}

	// A reconnect with the epoch and last seq replays nothing new; one from before the trace
	// replays its five events.
	replay, _, err := dialWS(t, env, "protocol=2&since_seq=1&epoch="+hello.Epoch, http.Header{"X-TrustAgent-Key": {testKey}})
	if err != nil {
		t.Fatal(err)
	}
	if f := readFrame(t, replay); f.Type != "hello" {
		t.Fatalf("hello %s", f.raw)
	}
	for f := readFrame(t, replay); f.Seq < last; f = readFrame(t, replay) {
		if f.Seq <= 1 {
			t.Fatalf("replayed an event the client had: %s", f.raw)
		}
	}
}

func TestStreamAuth(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	for _, key := range []string{"", "wrong"} {
		h := http.Header{}
		if key != "" {
			h.Set("X-TrustAgent-Key", key)
		}
		_, resp, err := dialWS(t, env, "protocol=2", h)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("key %q: %v %v", key, resp, err)
		}
	}
	// A key in the query string is not an auth source.
	if _, resp, err := dialWS(t, env, "protocol=2&api_key="+testKey, nil); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("api_key query: %v %v", resp, err)
	}

	// Public read: browsers connect without a key and get the redacted variants.
	public := newTestEnv(t, envOpts{publicRead: true})
	conn, _, err := dialWS(t, public, "protocol=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	readFrame(t, conn)
	public.send(loadDemoScenarios(t)["refund-contradiction-flag"])
	for {
		f := readFrame(t, conn)
		if f.Type == "hb" || f.Type == "status" {
			continue
		}
		if strings.Contains(string(f.Data), "60 days") || strings.Contains(string(f.Data), "bought it") {
			t.Fatalf("a keyless client got text: %s", f.raw)
		}
		if f.Type == "audit_completed" {
			break
		}
	}
}

// TestStreamRefusesV1: WS v1 was removed (D13). An authenticated handshake without protocol=2 gets
// 400 bad_request before the upgrade; an unauthenticated one still gets 401 first.
func TestStreamRefusesV1(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	for _, query := range []string{"", "protocol=1"} {
		_, resp, err := dialWS(t, env, query, http.Header{"X-TrustAgent-Key": {testKey}})
		if err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q: want 400, got %v %v", query, resp, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		contractstest.ValidateJSON(t, schemaError, body)
		if !strings.Contains(string(body), `"code":"bad_request"`) || !strings.Contains(string(body), "protocol=2") {
			t.Errorf("%q: body %s", query, body)
		}
	}
	if _, resp, err := dialWS(t, env, "", nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: want 401, got %v %v", resp, err)
	}
}
