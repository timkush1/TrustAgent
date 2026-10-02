package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/jev"
)

// decisionModelTestTimeout bounds one POST /api/decision-model/test: the probe's attempts and
// their retry.
const decisionModelTestTimeout = 15 * time.Second

// maxTestMessage is the longest error text the test result carries, in characters (D40, D42).
const maxTestMessage = 300

// decisionModelTestResult is api.ts DecisionModelTestResult.
type decisionModelTestResult struct {
	OK             bool                    `json:"ok"`
	Backend        string                  `json:"backend"`
	EndpointHost   *string                 `json:"endpoint_host"`
	ModelRequested *string                 `json:"model_requested"`
	Snapshot       *string                 `json:"snapshot"`
	LatencyMs      *int64                  `json:"latency_ms"`
	InputTokens    *int                    `json:"input_tokens"`
	CostUSD        *float64                `json:"cost_usd"`
	CostSource     *string                 `json:"cost_source"`
	Error          *decisionModelTestError `json:"error"`
}

// decisionModelTestError is DecisionModelTestResult.error.
type decisionModelTestError struct {
	Kind    string `json:"kind"`
	Status  *int   `json:"status"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// handleDecisionModelTest serves POST /api/decision-model/test (SPEC §2.4 row 60): one
// connectivity probe through the decision model in effect (≈ $0.00001). The probe is a
// non-enforcing call (D82), so it never spends the enforcing reserve and stops at the observe
// line of the daily cap. Its answer carries no meaning and is never shown; the result says
// whether the key, the endpoint and the model work, and what it cost. Error text passes through
// the redactor (D42).
//
// A breaker the key or the credits opened (the auth cooldown) is closed first, so "I added
// credits — check again" really checks instead of answering breaker_open for the rest of the
// cooldown; a rejected key or an empty account opens it again at once. A breaker that vendor
// faults opened stays open: closing it would send live traffic back to a failing vendor, so the
// test then answers breaker_open without a call. The per-caller limit (one per 10 s) keeps this
// from hammering the vendor.
func (s *server) handleDecisionModelTest(c *gin.Context) {
	client := s.gateway.client
	if pausedByKey(client) {
		if r, ok := client.(jev.BreakerResetter); ok {
			r.ResetBreaker()
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), decisionModelTestTimeout)
	defer cancel()
	start := time.Now()
	resp, err := jev.Probe(ctx, client)
	c.JSON(http.StatusOK, s.decisionModelTestResult(client.Info(), resp, err, time.Since(start)))
}

// pausedByKey reports a decision model whose breaker is open because the vendor rejected the key
// or found no credits (jev status unauthorized or no_credits): something the admin fixes outside
// the gateway, and the only pause an admin's test may lift.
func pausedByKey(client jev.Client) bool {
	r, ok := client.(jev.StatusReporter)
	if !ok {
		return false
	}
	st := r.Status()
	return st.Breaker != jev.BreakerClosed && (st.State == "unauthorized" || st.State == "no_credits")
}

// decisionModelTestResult maps a probe outcome to the wire result.
func (s *server) decisionModelTestResult(info jev.Info, resp *jev.Response, err error, elapsed time.Duration) decisionModelTestResult {
	out := decisionModelTestResult{
		OK: err == nil, Backend: info.Backend,
		EndpointHost: nonEmpty(endpointHost(info.Endpoint)), ModelRequested: nonEmpty(info.Model),
	}
	// Latency is the vendor round trip; a probe that never left the gateway (off, breaker,
	// budget) has none.
	if _, local := jev.SkipReason(err); !local {
		latency := elapsed.Milliseconds()
		out.LatencyMs = &latency
	}
	if resp != nil {
		out.Snapshot, out.InputTokens, out.CostUSD = nonEmpty(resp.Snapshot), resp.InputTokens, resp.CostUSD
		out.CostSource = nonEmpty(resp.CostSource)
		if resp.LatencyMs > 0 {
			latency := resp.LatencyMs
			out.LatencyMs = &latency
		}
	}
	if err != nil {
		e := &decisionModelTestError{Kind: jev.ErrorKind(err), Message: err.Error(), Hint: jev.Hint(jev.ErrorKind(err))}
		var je *jev.Error
		if errors.As(err, &je) {
			if je.Message != "" {
				e.Message = je.Message
			}
			if je.Hint != "" {
				e.Hint = je.Hint
			}
			if je.Status != 0 {
				status := je.Status
				e.Status = &status
			}
		}
		e.Message = cutChars(s.redact(dropCutWord(e.Message)), maxTestMessage)
		out.Error = e
	}
	return out
}

// endpointHost is the host of a decision endpoint URL ("" for mock and off).
func endpointHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// dropCutWord removes the word a cut left at the end of a decision-model message, before its
// "…". The decision client cuts a vendor message to 300 characters before it masks the key it sent
// (jev scrub, then redactSecret), so a key the vendor echoed across the cut arrives as a bare
// prefix that no exact-secret redaction recognises (D42). The cut word carries nothing worth
// showing, so it goes whatever it was.
func dropCutWord(msg string) string {
	body, cut := strings.CutSuffix(msg, "…")
	if !cut {
		return msg
	}
	body = body[:strings.LastIndexFunc(body, unicode.IsSpace)+1]
	return strings.TrimRightFunc(body, unicode.IsSpace) + "…"
}

// cutChars cuts s to at most n characters, ending with "…" when it cut.
func cutChars(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
