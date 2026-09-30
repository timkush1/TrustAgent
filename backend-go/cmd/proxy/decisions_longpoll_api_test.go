package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// pollDecision long-polls a trace's decision with testKey.
func pollDecision(t *testing.T, e *testEnv, traceID, waitMs string) (int, decisionPoll, []byte) {
	t.Helper()
	path := "/v1/trustagent/decisions/" + traceID
	if waitMs != "" {
		path += "?wait_ms=" + waitMs
	}
	code, body := e.get(path, testKey)
	var p decisionPoll
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
	}
	return code, p, body
}

func TestDecisionLongPollAnswersTheDecision(t *testing.T) {
	e := newTestEnv(t, envOpts{})
	sc := loadDemoScenarios(t)
	// A flag route decides before it answers: the poll finds the decision at once.
	resp := e.send(sc["refund-contradiction-flag"])
	id := resp.Header.Get("X-TrustAgent-Trace-Id")
	code, p, body := pollDecision(t, e, id, "")
	if code != http.StatusOK || p.State != decisionDecided || p.TraceID != id {
		t.Fatalf("HTTP %d %s", code, body)
	}
	var d struct {
		Spec    string `json:"spec"`
		Attempt int    `json:"attempt"`
		Action  string `json:"action"`
	}
	if err := json.Unmarshal(p.Decision, &d); err != nil || d.Spec != "decision/v1" || d.Attempt != 0 || d.Action != "flag" {
		t.Errorf("decision %s (%v)", p.Decision, err)
	}

	// An observe route answers first and decides after: the poll waits for the decision.
	resp = e.send(sc["observe-would-block"])
	id = resp.Header.Get("X-TrustAgent-Trace-Id")
	start := time.Now()
	code, p, body = pollDecision(t, e, id, "5000")
	if code != http.StatusOK || p.State != decisionDecided || time.Since(start) > 4*time.Second {
		t.Fatalf("observe: HTTP %d after %v: %s", code, time.Since(start), body)
	}
	if err := json.Unmarshal(p.Decision, &d); err != nil || d.Action != "observe" {
		t.Errorf("observe decision %s (%v)", p.Decision, err)
	}
}

func TestDecisionLongPollWaitsAndWakes(t *testing.T) {
	e := newTestEnv(t, envOpts{})
	w := e.srv.waiters
	pending, woken := store.NewID(), store.NewID()
	w.started(pending, "trafficgen")
	w.started(woken, "trafficgen")

	// A pending trace answers pending, with a null decision, once wait_ms runs out.
	start := time.Now()
	code, p, body := pollDecision(t, e, pending, "60")
	if code != http.StatusOK || p.State != decisionPending || string(p.Decision) != "null" || time.Since(start) < 50*time.Millisecond {
		t.Errorf("pending: HTTP %d after %v: %s", code, time.Since(start), body)
	}
	// Without wait_ms it answers at once.
	if code, p, _ := pollDecision(t, e, pending, ""); code != http.StatusOK || p.State != decisionPending {
		t.Errorf("no wait: HTTP %d %s", code, p.State)
	}

	// The decision wakes a waiting poll.
	done := make(chan decisionPoll, 1)
	go func() {
		_, p, _ := pollDecision(t, e, woken, "5000")
		done <- p
	}()
	time.Sleep(50 * time.Millisecond)
	w.decided(woken, "trafficgen", policy.HeadersFull,
		json.RawMessage(`{"spec":"decision/v1","trace_id":"`+woken+`","attempt":0,"action":"flag","status":"ok","late":false}`))
	select {
	case p := <-done:
		if p.State != decisionDecided || !strings.Contains(string(p.Decision), `"action":"flag"`) {
			t.Errorf("woken poll %+v", p)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the decision did not wake the poll")
	}
}

func TestDecisionLongPollRefusals(t *testing.T) {
	e := newTestEnv(t, envOpts{})
	w := e.srv.waiters
	other := store.NewID()
	w.decided(other, "support-app", policy.HeadersFull, json.RawMessage(`{"action":"pass","status":"ok"}`))

	// Another key's trace and an unknown trace are the same 404, in the /v1 error shape.
	for _, id := range []string{other, store.NewID()} {
		code, _, body := pollDecision(t, e, id, "")
		var b struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if code != http.StatusNotFound || json.Unmarshal(body, &b) != nil || b.Error.Code != codeNotFound {
			t.Errorf("%s: HTTP %d %s", id, code, body)
		}
	}
	// A bad wait_ms is refused; one above the cap is clamped, not refused.
	for _, v := range []string{"abc", "-1", "1.5"} {
		if code, _, body := pollDecision(t, e, other, v); code != http.StatusBadRequest || !strings.Contains(string(body), "wait_ms") {
			t.Errorf("wait_ms=%s: HTTP %d %s", v, code, body)
		}
	}
	// Without the ingest key there is no answer at all.
	if code, _ := e.get("/v1/trustagent/decisions/"+other, ""); code != http.StatusUnauthorized {
		t.Errorf("no key: HTTP %d", code)
	}
}

func TestDecisionLongPollActionOnly(t *testing.T) {
	e := newTestEnv(t, envOpts{})
	id := store.NewID()
	e.srv.waiters.decided(id, "trafficgen", policy.HeadersActionOnly, json.RawMessage(
		`{"spec":"decision/v1","trace_id":"`+id+`","attempt":0,"action":"block","status":"ok","reasons":["groundedness:critical"],"max_risk":0.97}`))
	code, p, body := pollDecision(t, e, id, "")
	if code != http.StatusOK || p.State != decisionDecided {
		t.Fatalf("HTTP %d %s", code, body)
	}
	var d map[string]any
	if err := json.Unmarshal(p.Decision, &d); err != nil || len(d) != 2 || d["action"] != "block" || d["status"] != "ok" {
		t.Errorf("action_only decision %s (%v)", p.Decision, err)
	}
}

func TestDecisionLongPollReadsTheDatabase(t *testing.T) {
	// Two servers share one store: the second never saw the trace, so it reads it back.
	st := newMemStore()
	first := newTestEnv(t, envOpts{store: st})
	second := newTestEnv(t, envOpts{store: st})
	resp := first.send(loadDemoScenarios(t)["refund-contradiction-flag"])
	id := resp.Header.Get("X-TrustAgent-Trace-Id")
	code, p, body := pollDecision(t, second, id, "")
	if code != http.StatusOK || p.State != decisionDecided || !strings.Contains(string(p.Decision), `"decision/v1"`) {
		t.Fatalf("decided in the database: HTTP %d %s", code, body)
	}

	// A pending trace in the database waits, and another key's trace is not found.
	now := time.Now().UTC()
	label, otherLabel := "trafficgen", "support-app"
	pending := &store.TraceRecord{TraceID: store.NewID(), CreatedAt: now, Source: "proxy", APIFormat: "openai_chat",
		Path: "/v1/chat/completions", Route: "default", Mode: "observe", KeyLabel: &label, GateStatus: store.GateStatusPending,
		AuditStatus: store.TraceAuditNone, Reasons: []string{}, Flags: []string{}}
	foreign := *pending
	foreign.TraceID, foreign.KeyLabel = store.NewID(), &otherLabel
	for _, tr := range []*store.TraceRecord{pending, &foreign} {
		if err := st.StartTrace(context.Background(), tr); err != nil {
			t.Fatal(err)
		}
	}
	if code, p, body := pollDecision(t, second, pending.TraceID, "30"); code != http.StatusOK || p.State != decisionPending {
		t.Errorf("pending in the database: HTTP %d %s", code, body)
	}
	if code, _, _ := pollDecision(t, second, foreign.TraceID, ""); code != http.StatusNotFound {
		t.Errorf("another key's trace in the database: HTTP %d", code)
	}
	// Without a database only the traces this process saw are known.
	noDB := newTestEnv(t, envOpts{noStore: true})
	if code, _, _ := pollDecision(t, noDB, id, ""); code != http.StatusNotFound {
		t.Errorf("no database: HTTP %d", code)
	}
}

func TestDecisionWaitersRememberABoundedRecentSet(t *testing.T) {
	w := newDecisionWaiters()
	now := time.Now()
	w.now = func() time.Time { return now }
	w.started("a", "k")
	if e, ok := w.get("a"); !ok || e.doc != nil || e.keyLabel != "k" {
		t.Fatalf("pending entry %+v %v", e, ok)
	}
	w.decided("a", "k", policy.HeadersFull, json.RawMessage(`{"action":"pass"}`))
	// A late decision replaces the entry's decision; registering again keeps it.
	w.decided("a", "k", policy.HeadersFull, json.RawMessage(`{"action":"flag"}`))
	w.started("a", "k")
	if e, _ := w.get("a"); string(e.doc) != `{"action":"flag"}` {
		t.Errorf("entry %s", e.doc)
	}
	// Past decisionTTL an entry is forgotten.
	now = now.Add(decisionTTL + time.Second)
	if _, ok := w.get("a"); ok {
		t.Error("an expired entry was served")
	}
	// Past decisionEntries the least recently touched go first.
	for i := range decisionEntries + 5 {
		w.started(store.NewID()+string(rune('a'+i%26)), "k")
	}
	if w.order.Len() != decisionEntries || len(w.entries) != decisionEntries {
		t.Errorf("%d entries, want %d", w.order.Len(), decisionEntries)
	}
	var nilW *decisionWaiters
	nilW.started("x", "k")
	nilW.decided("x", "k", "", json.RawMessage(`{}`))
}

func TestParseWaitMs(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 0, "0": 0, "250": 250 * time.Millisecond, "10000": maxDecisionWait,
		"20000": maxDecisionWait, "9223372036854775807": maxDecisionWait} {
		if got, ok := parseWaitMs(v); !ok || got != want {
			t.Errorf("wait_ms=%q: %v %v, want %v", v, got, ok, want)
		}
	}
	for _, v := range []string{"x", "-5", "2e3"} {
		if _, ok := parseWaitMs(v); ok {
			t.Errorf("wait_ms=%q accepted", v)
		}
	}
}
