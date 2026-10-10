package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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
	headerDegraded       = "X-TrustAgent-Degraded"
)

// statusDeferred is X-TrustAgent-Status on an observe-mode response: the gate runs after it.
const statusDeferred = "deferred"

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

// degraded is expect.degraded, and whether it is asserted.
func (e expectation) degraded() (want, ok bool) {
	raw, present := e["degraded"]
	if !present || json.Unmarshal(raw, &want) != nil {
		return false, false
	}
	return want, true
}

// decisionModelExpect is scenarios.v1 expect.decision_model: what the decision-model client did
// (D119). A nil member is not asserted.
type decisionModelExpect struct {
	Attempts *int  `json:"attempts"`
	Hedged   *bool `json:"hedged"`
	Late     *bool `json:"late"`
}

// decisionModel is expect.decision_model, or nil when it is not asserted.
func (e expectation) decisionModel() *decisionModelExpect {
	raw, ok := e["decision_model"]
	if !ok || string(raw) == "null" {
		return nil
	}
	var dm decisionModelExpect
	if json.Unmarshal(raw, &dm) != nil {
		return nil
	}
	return &dm
}

// checkHeaders compares the gate headers of a response with the scenario's expect.
func checkHeaders(s *scenario, h http.Header) []mismatch {
	return checkDecision(s, newDecisionView(&answer{header: h}), false)
}

// checkDecision compares the decision a response exposes (headers, trailers and the stream
// comment) with the header fields of the scenario's expect. reasons is a subset match;
// Would-Action is sent only when it differs from Action; Risk has 2 decimals;
// X-TrustAgent-Route starts with "<route>@v". An observe-mode response says Status: deferred and
// carries no decision yet: its decision fields are checked on the stored trace (checkTrace). A
// stream may leave Checked-Against and Audit out (its comment carries neither): when streamed is
// set, those two are checked only when present, and the stored trace is checked too.
func checkDecision(s *scenario, v *decisionView, streamed bool) []mismatch {
	e := expectation(s.Expect)
	var out []mismatch
	show := func(val string, ok bool) string {
		if !ok {
			return absent
		}
		return fmt.Sprintf("%q", val)
	}
	// equal checks a string member against a header; null means the header must be absent.
	equal := func(member, name string) {
		present, null := e.has(member)
		if !present {
			return
		}
		got, ok := v.get(name)
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
	if v.deferred() && e.str("status") == statusDeferred {
		return append(out, checkRoute(s, v)...)
	}
	equal("verdict", headerVerdict)
	for member, name := range map[string]string{"checked_against": headerCheckedAgainst, "audit": headerAudit} {
		if _, ok := v.get(name); ok || !streamed {
			equal(member, name)
		}
	}
	// expect.degraded (D119): X-TrustAgent-Degraded names the degradation of a decision that did
	// not get a full answer, and is absent otherwise. A stream's comment does not carry it, so a
	// stream is checked on the stored decision only.
	if want, ok := e.degraded(); ok && !streamed {
		got, has := v.get(headerDegraded)
		switch {
		case want && !has:
			out = append(out, mismatch{headerDegraded, "a degradation (the check did not complete)", absent})
		case !want && has:
			out = append(out, mismatch{headerDegraded, absent, show(got, has)})
		}
	}

	if present, null := e.has("would_action"); present {
		want := ""
		if !null {
			want = e.str("would_action")
		}
		got, ok := v.get(headerWouldAction)
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
		got, ok := v.get(headerRisk)
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
		got, ok := v.get(headerReasons)
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
	return append(out, checkRoute(s, v)...)
}

func checkRoute(s *scenario, v *decisionView) []mismatch {
	if s.Route == "" {
		return nil
	}
	if got, ok := v.get(headerRoute); !ok || !strings.HasPrefix(got, s.Route+"@v") {
		show := absent
		if ok {
			show = fmt.Sprintf("%q", got)
		}
		return []mismatch{{headerRoute, fmt.Sprintf("%q", s.Route+"@v<version>"), show}}
	}
	return nil
}

// streamDecision is scenarios.v1 expect.stream_decision.
type streamDecision struct {
	Comment  bool     `json:"comment"`
	Trailers []string `json:"trailers"`
}

// checkAnswer compares what the client received with the scenario: the HTTP status
// (expect.http_status, default 200), the delivered text (expect.delivered_text, default response;
// unchecked when checkText is false) and, for a stream, how it carried the decision
// (expect.stream_decision: the trustagent comment and the trailers).
func checkAnswer(s *scenario, a *answer, checkText bool) []mismatch {
	e := expectation(s.Expect)
	var out []mismatch
	wantStatus := http.StatusOK
	if present, null := e.has("http_status"); present && !null {
		_ = json.Unmarshal(e["http_status"], &wantStatus)
	}
	if a.status != wantStatus {
		out = append(out, mismatch{"HTTP status", strconv.Itoa(wantStatus), strconv.Itoa(a.status)})
	}
	if s.Stream != a.stream {
		want, got := "a JSON body", "an event stream"
		if s.Stream {
			want, got = got, want
		}
		out = append(out, mismatch{"response body", want, got})
	}
	want, textKnown := "", false
	if present, null := e.has("delivered_text"); present && !null {
		want, textKnown = e.str("delivered_text"), true
	} else if s.Response != nil {
		want, textKnown = *s.Response, true
	}
	if checkText && textKnown && wantStatus >= 200 && wantStatus <= 299 {
		switch {
		case !a.ok:
			out = append(out, mismatch{"delivered text", fmt.Sprintf("%q", want), "no assistant text in the response"})
		case a.text != want:
			out = append(out, mismatch{"delivered text", fmt.Sprintf("%q", want), fmt.Sprintf("%q", a.text)})
		}
	}
	if present, null := e.has("stream_decision"); present && !null {
		var sd streamDecision
		_ = json.Unmarshal(e["stream_decision"], &sd)
		if sd.Comment && a.comment == nil {
			out = append(out, mismatch{"stream decision comment", "a `" + strings.TrimSpace(annotationPrefix) + " {...}` comment", absent})
		}
		for _, name := range sd.Trailers {
			if a.trailer.Get(name) == "" {
				got := absent
				if a.header.Get(name) != "" {
					got = "(sent as a header, not a trailer)"
				}
				out = append(out, mismatch{name, "an HTTP trailer", got})
			}
		}
	}
	return out
}

// traceView is the part of GET /api/traces/:id (TraceDetail) --expect reads.
type traceView struct {
	TraceID        string   `json:"trace_id"`
	GateStatus     string   `json:"gate_status"`
	Action         *string  `json:"action"`
	WouldAction    *string  `json:"would_action"`
	Verdict        *string  `json:"verdict"`
	MaxRisk        *float64 `json:"max_risk"`
	Reasons        []string `json:"reasons"`
	CheckedAgainst *struct {
		Used []struct {
			Source string `json:"source"`
			Items  int    `json:"items"`
		} `json:"used"`
	} `json:"checked_against"`
	Audit struct {
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
	// Decisions are the stored decision/v1 documents, one per attempt (0 the answer, 1 a fallback
	// re-gate); a late decision replaces its attempt's document and says late.
	Decisions []storedDecision `json:"decisions"`
}

// storedDecision is the part of a stored decision/v1 document --expect reads.
type storedDecision struct {
	Attempt       int     `json:"attempt"`
	Late          bool    `json:"late"`
	Degraded      *string `json:"degraded"`
	DecisionModel *struct {
		Attempts int  `json:"attempts"`
		Hedged   bool `json:"hedged"`
	} `json:"decision_model"`
}

// decision is the stored decision of attempt n, or nil.
func (t *traceView) decision(n int) *storedDecision {
	for i := range t.Decisions {
		if t.Decisions[i].Attempt == n {
			return &t.Decisions[i]
		}
	}
	return nil
}

// settled reports whether the trace is decided and its deep audit is no longer pending.
func (t *traceView) settled() bool {
	return t.GateStatus != store.GateStatusPending && t.Audit.Status != store.AuditQueued && t.Audit.Status != store.AuditRunning
}

// settledFor reports whether the trace is settled for s: decided, its deep audit no longer
// pending and, when s expects a late decision (expect.decision_model.late), that decision
// recorded. A late decision arrives after the response, often after the audit is queued.
func (t *traceView) settledFor(s *scenario) bool {
	if !t.settled() {
		return false
	}
	if dm := expectation(s.Expect).decisionModel(); dm != nil && dm.Late != nil && *dm.Late {
		d := t.decision(0)
		return d != nil && d.Late
	}
	return true
}

// checkedAgainst formats the trace's evidence summary as X-TrustAgent-Checked-Against does.
func (t *traceView) checkedAgainst() string {
	if t.CheckedAgainst == nil || len(t.CheckedAgainst.Used) == 0 {
		return "none"
	}
	parts := make([]string, len(t.CheckedAgainst.Used))
	for i, u := range t.CheckedAgainst.Used {
		parts[i] = u.Source + ":" + strconv.Itoa(u.Items)
	}
	return strings.Join(parts, ",")
}

// checkTrace compares a settled trace with the scenario's outcome, audit verdict and, when the
// scenario has a judge fixture, the claim verdicts in order. For a response that did not carry the
// whole decision (a deferred observe-mode response, or a stream), storedDecision also checks the
// stored decision.
func checkTrace(s *scenario, t *traceView, storedDecision bool) []mismatch {
	e := expectation(s.Expect)
	var out []mismatch
	if present, _ := e.has("outcome"); present {
		got := store.Outcome(store.OutcomeFields{GateStatus: t.GateStatus, Action: deref(t.Action), WouldAction: deref(t.WouldAction)})
		if want := e.str("outcome"); got != want {
			out = append(out, mismatch{"outcome", want, got})
		}
	}
	if storedDecision {
		out = append(out, checkStoredDecision(e, t)...)
	}
	out = append(out, checkDecisionModel(e, t)...)
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

// checkStoredDecision checks the decision fields of expect on a trace.
func checkStoredDecision(e expectation, t *traceView) []mismatch {
	var out []mismatch
	str := func(field, got string) {
		if present, null := e.has(field); present {
			want := absent
			if !null {
				want = e.str(field)
			}
			if got == "" {
				got = absent
			}
			if got != want {
				out = append(out, mismatch{"stored " + field, want, got})
			}
		}
	}
	str("would_action", deref(t.WouldAction))
	str("verdict", deref(t.Verdict))
	str("checked_against", t.checkedAgainst())
	if present, null := e.has("max_risk"); present {
		want, got := absent, absent
		if !null {
			var risk float64
			_ = json.Unmarshal(e["max_risk"], &risk)
			want = decision.FormatRisk(risk)
		}
		if t.MaxRisk != nil {
			got = decision.FormatRisk(*t.MaxRisk)
		}
		if got != want {
			out = append(out, mismatch{"stored max_risk", want, got})
		}
	}
	if present, _ := e.has("reasons"); present {
		var want []string
		_ = json.Unmarshal(e["reasons"], &want)
		for _, r := range want {
			if !slices.Contains(t.Reasons, r) {
				out = append(out, mismatch{"stored reasons", "to include " + fmt.Sprintf("%q", r), fmt.Sprintf("%q", strings.Join(t.Reasons, ","))})
			}
		}
	}
	return out
}

// checkDecisionModel checks expect.degraded and expect.decision_model (D119) on the stored decision
// of the answer (attempt 0). attempts and hedged count the vendor requests of the decision the
// gate enforced. A late decision replaces that document with the decision made from the late
// answer, so it is checked for late only: the response headers carried the degradation.
func checkDecisionModel(e expectation, t *traceView) []mismatch {
	wantDegraded, checkDegraded := e.degraded()
	dm := e.decisionModel()
	if !checkDegraded && dm == nil {
		return nil
	}
	d := t.decision(0)
	if d == nil {
		return []mismatch{{"stored decision", "the decision of attempt 0", absent}}
	}
	var out []mismatch
	if dm != nil && dm.Late != nil && *dm.Late != d.Late {
		out = append(out, mismatch{"stored decision late", strconv.FormatBool(*dm.Late), strconv.FormatBool(d.Late)})
	}
	if d.Late {
		return out
	}
	if checkDegraded && wantDegraded != (d.Degraded != nil) {
		got := absent
		if d.Degraded != nil {
			got = *d.Degraded
		}
		want := absent
		if wantDegraded {
			want = "a degradation (the check did not complete)"
		}
		out = append(out, mismatch{"stored degraded", want, got})
	}
	if dm == nil {
		return out
	}
	attempts, hedged := 0, false
	if d.DecisionModel != nil {
		attempts, hedged = d.DecisionModel.Attempts, d.DecisionModel.Hedged
	}
	if dm.Attempts != nil && *dm.Attempts != attempts {
		out = append(out, mismatch{"decision_model attempts", strconv.Itoa(*dm.Attempts), strconv.Itoa(attempts)})
	}
	if dm.Hedged != nil && *dm.Hedged != hedged {
		out = append(out, mismatch{"decision_model hedged", strconv.FormatBool(*dm.Hedged), strconv.FormatBool(hedged)})
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
