package main

// The vendor-fault pack against the reference (D119). A faults-pack scenario names a mock-upstream
// fault (mock_faults); faultModel turns it into what the decision-model client makes of it on the
// scenario's route, from the route's deadline and hedge, the client's retry defaults and the vendor
// fixtures' Retry-After, so runReference can confirm each row's status, action, degraded and
// decision_model against decide(). A disagreement is never fixed by editing an expectation
// silently: SPEC §2.12 and GATEWAY.md §6 say what each fault must do.

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/contracts"
)

// refFault is what the decision-model client makes of a scenario's vendor fault.
type refFault struct {
	// answered: the backend answered from mock_answers in the end (after a retry, through the
	// hedge, or to the smaller compile of a too_large retry); otherwise every request has status.
	answered bool
	status   string // timeout or error:<kind>, when nothing answered
	// halve: the first request was too large, and the gate compiled again with half the evidence cap.
	halve bool
	// attempts, hedged and late are the decision's decision_model (late: an answer arrived after the
	// deadline, within the attempt timeout, and is recorded as the late decision).
	attempts     int
	hedged, late bool
	// skip is set for an upstream fault: the answer never reaches the gate (the gate's skip
	// reason), and upstreamStatus is a non-2xx status the client receives as it is.
	skip           string
	upstreamStatus int
}

// attemptWindow is the least time an attempt needs before the deadline (internal/jev
// minAttemptWindow): a retry or hedge is sent only if its wait leaves that much.
const attemptWindow = 20 * time.Millisecond

// maxBackoff is the longest first-retry backoff without a Retry-After (50 ms with jitter below it).
const maxBackoff = 50 * time.Millisecond

// vendorRetryAfter is the Retry-After of a vendor fixture (contracts/fixtures/vendor/openrouter),
// which vendor_fixtures_test.go keeps equal to mock-upstream's faultTable.
func vendorRetryAfter(t *testing.T, fixture string) time.Duration {
	t.Helper()
	b, err := contracts.FS.ReadFile("fixtures/vendor/openrouter/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	secs, err := strconv.Atoi(f.Headers["retry-after"])
	if err != nil {
		t.Fatalf("%s: retry-after %q", fixture, f.Headers["retry-after"])
	}
	return time.Duration(secs) * time.Second
}

// faultModel is the client's handling of s's mock_faults on route r, on the e2e stack (the
// compatible backend on mock-upstream's TypeSafe-shaped /v1/systemone; nil without a fault).
func faultModel(t *testing.T, s *refScenario, r *policy.Route) *refFault {
	t.Helper()
	if s.MockFaults == nil {
		return nil
	}
	if up := s.MockFaults.Upstream; up != "" {
		if s.MockFaults.Decision != "" {
			t.Fatal("an upstream fault leaves nothing to gate, so a decision fault next to it never applies")
		}
		f := &refFault{skip: gate.SkipUpstreamError}
		switch up {
		case "html200", "bad_json":
			// A 2xx that is not JSON is gated as a skip; the client gets the gate's outcome.
		default:
			code, err := strconv.Atoi(up)
			if err != nil || code < 400 {
				t.Fatalf("upstream fault %q: the reference knows non-2xx statuses, html200 and bad_json", up)
			}
			f.upstreamStatus = code
		}
		return f
	}

	name, n, counted := strings.Cut(s.MockFaults.Decision, "@")
	times := 0 // 0: every request of the call is faulted
	if counted {
		var err error
		if times, err = strconv.Atoi(n); err != nil || times < 1 {
			t.Fatalf("mock_faults.decision %q", s.MockFaults.Decision)
		}
	}
	faulted := func(attempt int) bool { return times == 0 || attempt <= times }
	cfg := jev.DefaultHTTPConfig()
	deadline := time.Duration(r.DeadlineMs) * time.Millisecond
	hedgeAfter := time.Duration(r.HedgeAfterMs) * time.Millisecond
	fits := func(at time.Duration) bool { return deadline-at > attemptWindow }
	hedges := hedgeAfter > 0 && fits(hedgeAfter)

	// errorFault: the vendor answers at once with an error; a retryable one is retried after its
	// wait while the wait fits the deadline and retries are left (MaxRetries for a sync call).
	errorFault := func(kind string, retryable bool, wait time.Duration) *refFault {
		f := &refFault{attempts: 1}
		at, retries := time.Duration(0), cfg.MaxRetries
		for faulted(f.attempts) {
			if !retryable || retries == 0 || !fits(at+wait) {
				f.status = "error:" + kind
				return f
			}
			at += wait
			retries--
			f.attempts++
		}
		f.answered = true
		return f
	}

	switch name {
	case "429":
		return errorFault(jev.KindRetryable, true, vendorRetryAfter(t, "decisions.429.json"))
	case "503", "502", "500", "529":
		return errorFault(jev.KindRetryable, true, maxBackoff)
	case "402_inflight", "402_credits":
		// The TypeSafe envelope has no metadata, so no 402 there says limit_source: it is an
		// account out of credits, never retried (and the breaker opens for the auth cooldown).
		return errorFault(jev.KindCredits, false, 0)
	case "bad_json":
		return errorFault(jev.KindBadResponse, false, 0)
	case "413":
		// too_large is not retried as such: the gate compiles again with half the evidence cap and
		// sends that once (a new call, so attempt 2 of the trace's X-Mock-Key count).
		if !faulted(1) || faulted(2) {
			t.Fatalf("the reference models 413@1: the smaller compile answers")
		}
		return &refFault{answered: true, halve: true, attempts: 2}
	case "hang":
		// The first request never answers; the hedge, when the route hedges in time, is the
		// second request of the call.
		if hedges && !faulted(2) {
			return &refFault{answered: true, attempts: 2, hedged: true}
		}
		f := &refFault{status: decision.RequestTimeout, attempts: 1}
		if hedges {
			f.attempts, f.hedged = 2, true
		}
		// A hanging request ends at the attempt timeout with no answer: no late decision.
		return f
	case "slow":
		// Every request answers after SlowDelay; the first answer wins.
		f := &refFault{attempts: 1}
		if hedges && hedgeAfter < mockupstream.SlowDelay {
			f.attempts, f.hedged = 2, true
		}
		if fits(mockupstream.SlowDelay) {
			f.answered = true
			return f
		}
		f.status = decision.RequestTimeout
		f.late = mockupstream.SlowDelay < cfg.AttemptTimeout
		return f
	}
	t.Fatalf("mock_faults.decision %q: the reference does not model fault %s", s.MockFaults.Decision, name)
	return nil
}

// refStatus is X-TrustAgent-Status for a decision (SPEC §2.12): skipped:<reason>, the request
// status error:<kind>, or the decision status.
func refStatus(core *decision.Core, f *refFault) string {
	switch core.Status {
	case decision.StatusSkipped:
		if core.SkipReason != nil {
			return "skipped:" + *core.SkipReason
		}
		return "skipped"
	case decision.StatusError:
		if f != nil && strings.HasPrefix(f.status, "error:") {
			return f.status
		}
		return "error"
	}
	return core.Status
}

// notRunDetectors is the detector results of an exchange without an answer: every detector, no
// findings (internal/gate notRun).
func notRunDetectors() []decision.DetectorResult {
	out := make([]decision.DetectorResult, len(decision.DetectorIDs))
	for i, id := range decision.DetectorIDs {
		out[i] = decision.DetectorResult{ID: id, Findings: []decision.DetectorFinding{}}
	}
	return out
}

// checkFaultExpect checks expect.degraded and expect.decision_model against the reference. A
// faults-pack scenario states degraded; without a fault, the backend answered the first request.
func checkFaultExpect(t *testing.T, s *refScenario, ref *reference) {
	t.Helper()
	x, f := s.Expect, ref.fault
	if f != nil && x.Degraded == nil {
		t.Error("expect.degraded is missing (every faults-pack scenario states whether its check completed)")
	}
	if s.MockFaults == nil && slices.Contains(s.Sets, "faults") {
		t.Error("a scenario of set faults names its fault in mock_faults")
	}
	if x.Degraded != nil && *x.Degraded != (ref.core.Degraded != nil) {
		t.Errorf("degraded: expect %v, reference %s", *x.Degraded, fmtDegraded(ref.core.Degraded))
	}
	dm := x.DecisionModel
	if dm == nil {
		return
	}
	attempts, hedged, late := 1, false, false
	if f != nil {
		attempts, hedged, late = f.attempts, f.hedged, f.late
		if f.skip != "" {
			attempts = 0 // nothing was asked
		}
	}
	if dm.Attempts != nil && *dm.Attempts != attempts {
		t.Errorf("decision_model.attempts: expect %d, reference %d", *dm.Attempts, attempts)
	}
	if dm.Hedged != nil && *dm.Hedged != hedged {
		t.Errorf("decision_model.hedged: expect %v, reference %v", *dm.Hedged, hedged)
	}
	if dm.Late != nil && *dm.Late != late {
		t.Errorf("decision_model.late: expect %v, reference %v", *dm.Late, late)
	}
}

func fmtDegraded(d *string) string {
	if d == nil {
		return "not degraded"
	}
	return "degraded " + *d
}
