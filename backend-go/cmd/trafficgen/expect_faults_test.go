package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// faultExpect is a scenario expecting a degraded decision and what the decision-model client did.
func faultExpect(degraded bool, dm string) *scenario {
	e := map[string]json.RawMessage{"action": json.RawMessage(`"pass"`), "degraded": json.RawMessage(fmt.Sprint(degraded))}
	if dm != "" {
		e["decision_model"] = json.RawMessage(dm)
	}
	return &scenario{ID: "f", Expect: e}
}

// TestDegradedHeader: expect.degraded true needs X-TrustAgent-Degraded on the response, false
// needs it absent, and an absent member asserts nothing (D119).
func TestDegradedHeader(t *testing.T) {
	h := http.Header{}
	h.Set(headerAction, "pass")
	if m := fmt.Sprint(checkHeaders(faultExpect(true, ""), h)); !strings.Contains(m, "X-TrustAgent-Degraded: want a degradation") {
		t.Errorf("a missing degradation passed: %s", m)
	}
	h.Set(headerDegraded, "timeout")
	if m := checkHeaders(faultExpect(true, ""), h); len(m) != 0 {
		t.Errorf("mismatches %v", m)
	}
	if m := fmt.Sprint(checkHeaders(faultExpect(false, ""), h)); !strings.Contains(m, `X-TrustAgent-Degraded: want (absent), got "timeout"`) {
		t.Errorf("an unexpected degradation passed: %s", m)
	}
	if m := checkHeaders(&scenario{Expect: map[string]json.RawMessage{"action": json.RawMessage(`"pass"`)}}, h); len(m) != 0 {
		t.Errorf("without expect.degraded nothing is asserted: %v", m)
	}
}

// traceWith is a settled trace whose attempt-0 decision is raw.
func traceWith(t *testing.T, raw string) *traceView {
	t.Helper()
	var tv traceView
	if err := json.Unmarshal([]byte(`{"gate_status":"error","audit":{"status":"completed"},"decisions":[`+raw+`]}`), &tv); err != nil {
		t.Fatal(err)
	}
	return &tv
}

// TestDecisionModelOnTheTrace: degraded and decision_model are read from the stored decision of
// attempt 0; a late decision replaces that document, so only late is checked on it.
func TestDecisionModelOnTheTrace(t *testing.T) {
	failed := traceWith(t, `{"attempt":0,"late":false,"degraded":"timeout","decision_model":{"attempts":2,"hedged":true}}`)
	for _, c := range []struct {
		name string
		s    *scenario
		want []string
	}{
		{"matches", faultExpect(true, `{"attempts":2,"hedged":true,"late":false}`), nil},
		{"attempts", faultExpect(true, `{"attempts":1}`), []string{"decision_model attempts: want 1, got 2"}},
		{"hedged", faultExpect(true, `{"hedged":false}`), []string{"decision_model hedged: want false, got true"}},
		{"late", faultExpect(true, `{"late":true}`), []string{"stored decision late: want true, got false"}},
		{"degraded", faultExpect(false, ""), []string{"stored degraded: want (absent), got timeout"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := fmt.Sprint(checkTrace(c.s, failed, false))
			if len(c.want) == 0 && got != "[]" {
				t.Errorf("mismatches %s", got)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q missing from %s", w, got)
				}
			}
		})
	}

	late := traceWith(t, `{"attempt":0,"late":true,"degraded":null,"decision_model":{"attempts":1,"hedged":false}}`)
	if m := checkTrace(faultExpect(true, `{"late":true}`), late, false); len(m) != 0 {
		t.Errorf("a late decision is checked for late only: %v", m)
	}
	if m := fmt.Sprint(checkTrace(faultExpect(true, ""), traceWith(t, `{"attempt":1,"late":false}`), false)); !strings.Contains(m, "stored decision: want the decision of attempt 0") {
		t.Errorf("a trace without its attempt-0 decision passed: %s", m)
	}
	// Nothing asserted, nothing read: a main-pack scenario needs no stored decision.
	if m := checkTrace(&scenario{Expect: map[string]json.RawMessage{}}, &traceView{}, false); len(m) != 0 {
		t.Errorf("mismatches %v", m)
	}
}

// TestSettledWaitsForAnExpectedLateDecision: a trace whose scenario expects a late decision is not
// settled until that decision is recorded, however settled its gate and audit are.
func TestSettledWaitsForAnExpectedLateDecision(t *testing.T) {
	s := faultExpect(true, `{"late":true}`)
	if tv := traceWith(t, `{"attempt":0,"late":false}`); !tv.settled() || tv.settledFor(s) {
		t.Error("a trace still waiting for its late decision counts as settled")
	}
	if tv := traceWith(t, `{"attempt":0,"late":true}`); !tv.settledFor(s) {
		t.Error("a trace with its late decision is not settled")
	}
	if tv := traceWith(t, `{"attempt":0,"late":false}`); !tv.settledFor(faultExpect(true, `{"late":false}`)) {
		t.Error("a scenario that expects no late decision waits for one")
	}
}
