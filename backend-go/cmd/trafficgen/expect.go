package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
)

// Gateway response headers --expect checks (SPEC §2.12).
const (
	headerTraceID        = "X-TrustAgent-Trace-Id"
	headerRoute          = "X-TrustAgent-Route"
	headerMode           = "X-TrustAgent-Mode"
	headerAction         = "X-TrustAgent-Action"
	headerWouldAction    = "X-TrustAgent-Would-Action"
	headerStatus         = "X-TrustAgent-Status"
	headerVerdict        = "X-TrustAgent-Verdict"
	headerRisk           = "X-TrustAgent-Risk"
	headerReasons        = "X-TrustAgent-Reasons"
	headerCheckedAgainst = "X-TrustAgent-Checked-Against"
	headerAudit          = "X-TrustAgent-Audit"
)

// absent is how a missing header or value reads in a mismatch.
const absent = "(absent)"

// mismatch is one expectation that did not hold.
type mismatch struct {
	field, want, got string
}

func (m mismatch) String() string {
	return fmt.Sprintf("%s: want %s, got %s", m.field, m.want, m.got)
}

// expectation reads the raw expect members of a scenario.
type expectation map[string]json.RawMessage

// has reports whether member k is present, and whether it is null.
func (e expectation) has(k string) (present, null bool) {
	raw, ok := e[k]
	return ok, ok && string(raw) == "null"
}

func (e expectation) str(k string) string {
	var s string
	_ = json.Unmarshal(e[k], &s)
	return s
}

// checkHeaders compares the gate headers of a response with the scenario's expect (the header
// fields of scenarios.v1 expect). reasons is a subset match; Would-Action is sent only when it
// differs from Action; Risk has 2 decimals; X-TrustAgent-Route starts with "<route>@v".
func checkHeaders(s *scenario, h http.Header) []mismatch {
	e := expectation(s.Expect)
	var out []mismatch
	header := func(name string) (string, bool) {
		v := h.Values(name)
		if len(v) == 0 {
			return "", false
		}
		return v[0], true
	}
	show := func(v string, ok bool) string {
		if !ok {
			return absent
		}
		return fmt.Sprintf("%q", v)
	}
	// equal checks a string member against a header; null means the header must be absent.
	equal := func(member, name string) {
		present, null := e.has(member)
		if !present {
			return
		}
		got, ok := header(name)
		switch {
		case null && ok:
			out = append(out, mismatch{name, absent, show(got, ok)})
		case !null && (!ok || got != e.str(member)):
			out = append(out, mismatch{name, fmt.Sprintf("%q", e.str(member)), show(got, ok)})
		}
	}
	equal("mode", headerMode)
	equal("action", headerAction)
	equal("status", headerStatus)
	equal("verdict", headerVerdict)
	equal("checked_against", headerCheckedAgainst)
	equal("audit", headerAudit)

	if present, null := e.has("would_action"); present {
		want := ""
		if !null {
			want = e.str("would_action")
		}
		got, ok := header(headerWouldAction)
		switch {
		case want == "" || want == e.str("action"):
			if ok {
				out = append(out, mismatch{headerWouldAction, absent + " (equals the action)", show(got, ok)})
			}
		case !ok || got != want:
			out = append(out, mismatch{headerWouldAction, fmt.Sprintf("%q", want), show(got, ok)})
		}
	}

	if present, null := e.has("max_risk"); present {
		got, ok := header(headerRisk)
		if null {
			if ok {
				out = append(out, mismatch{headerRisk, absent, show(got, ok)})
			}
		} else {
			var risk float64
			_ = json.Unmarshal(e["max_risk"], &risk)
			if want := decision.FormatRisk(risk); !ok || got != want {
				out = append(out, mismatch{headerRisk, fmt.Sprintf("%q", want), show(got, ok)})
			}
		}
	}

	if present, _ := e.has("reasons"); present {
		var want []string
		_ = json.Unmarshal(e["reasons"], &want)
		got, ok := header(headerReasons)
		var have []string
		if ok {
			have = strings.Split(got, ",")
		}
		for _, r := range want {
			if !slices.Contains(have, r) {
				out = append(out, mismatch{headerReasons, "to include " + fmt.Sprintf("%q", r), show(got, ok)})
			}
		}
	}

	if s.Route != "" {
		if got, ok := header(headerRoute); !ok || !strings.HasPrefix(got, s.Route+"@v") {
			out = append(out, mismatch{headerRoute, fmt.Sprintf("%q", s.Route+"@v<version>"), show(got, ok)})
		}
	}
	return out
}

// traceView is the part of GET /api/traces/:id (TraceDetail) --expect reads.
type traceView struct {
	TraceID     string  `json:"trace_id"`
	GateStatus  string  `json:"gate_status"`
	Action      *string `json:"action"`
	WouldAction *string `json:"would_action"`
	Audit       struct {
		Status  string  `json:"status"`
		Verdict *string `json:"verdict"`
	} `json:"audit"`
	AuditDetail *struct {
		Status  string  `json:"status"`
		Verdict *string `json:"verdict"`
		Claims  []struct {
			Status string `json:"status"`
		} `json:"claims"`
	} `json:"audit_detail"`
}

// settled reports whether the trace is decided and its deep audit is no longer pending.
func (t *traceView) settled() bool {
	return t.GateStatus != store.GateStatusPending && t.Audit.Status != store.AuditQueued && t.Audit.Status != store.AuditRunning
}

// checkTrace compares a settled trace with the scenario's outcome, audit verdict and, when the
// scenario has a judge fixture, the claim verdicts in order.
func checkTrace(s *scenario, t *traceView) []mismatch {
	e := expectation(s.Expect)
	var out []mismatch
	if present, _ := e.has("outcome"); present {
		got := store.Outcome(store.OutcomeFields{GateStatus: t.GateStatus, Action: deref(t.Action), WouldAction: deref(t.WouldAction)})
		if want := e.str("outcome"); got != want {
			out = append(out, mismatch{"outcome", want, got})
		}
	}
	if present, null := e.has("audit_verdict"); present {
		got := absent
		if t.Audit.Verdict != nil {
			got = *t.Audit.Verdict
		}
		want := absent
		if !null {
			want = e.str("audit_verdict")
		}
		if got != want {
			out = append(out, mismatch{"audit verdict", want, got + " (audit status " + t.Audit.Status + ")"})
		}
	}
	if s.Judge != nil {
		want := make([]string, len(s.Judge.Claims))
		for i, c := range s.Judge.Claims {
			want[i] = c.Verdict
		}
		got := absent
		if t.AuditDetail != nil {
			statuses := make([]string, len(t.AuditDetail.Claims))
			for i, c := range t.AuditDetail.Claims {
				statuses[i] = c.Status
			}
			got = "[" + strings.Join(statuses, " ") + "]"
		}
		if w := "[" + strings.Join(want, " ") + "]"; got != w {
			out = append(out, mismatch{"audit claim verdicts", w, got})
		}
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
