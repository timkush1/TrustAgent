package main

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// GET /api/traces/:id/decision-request (SPEC §2.4 row 12, P4-PLAN D63): the glass box. The
// decision-model requests of a trace's decision, recompiled from its stored body with the policy
// version and the evaluator versions it was gated with, the recorded chars-per-token and the
// recorded model, through the gate's own compile inputs (gate.CompileInputsOf, CompileRouteOf,
// RouteBudget). matches_recorded says whether the request hashes equal the ones the gate recorded;
// when they do not, mismatch_reason says why if it is known. session_id is never returned.

// Why a recompile may not match (DecisionRequestView.mismatch_reason).
const (
	mismatchBodyTruncated    = "body_truncated"
	mismatchPolicyMissing    = "policy_missing"
	mismatchEvaluatorMissing = "evaluator_missing"
)

// jevRequestView is api.ts JevRequest: one request body as compiled, without session_id.
type jevRequestView struct {
	Model     string         `json:"model"`
	State     map[string]any `json:"state"`
	Questions map[string]any `json:"questions"`
}

// decisionRequestView is api.ts DecisionRequestView.
type decisionRequestView struct {
	Requests        []jevRequestView `json:"requests"`
	RequestSHA256   []string         `json:"request_sha256"`
	MatchesRecorded bool             `json:"matches_recorded"`
	CPT             float64          `json:"cpt"`
	MismatchReason  *string          `json:"mismatch_reason"`
}

func (a *studioAPI) decisionRequest(c *gin.Context) {
	traceID := c.Param("id")
	attempt := 0
	switch c.DefaultQuery("attempt", "0") {
	case "0":
	case "1":
		attempt = 1
	default:
		badParam(c, "attempt", "must be 0 (the original answer) or 1 (the fallback answer)")
		return
	}
	if a.db == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "The decision request is rebuilt from stored traffic, and this gateway has no database.")
		return
	}
	ctx := c.Request.Context()
	// The trace's queued writes land before it is read (D83).
	if err := a.barrier(ctx, traceID); err != nil {
		// Not persistence_disabled: the database is there, the trace's writes just did not land.
		a.log.Error("decision request: the trace's queued writes did not land", "trace_id", traceID, "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The trace could not be written before it was read; try again in a moment.")
		return
	}
	rec, err := a.db.GetRecordedCompile(ctx, traceID, attempt)
	if errors.Is(err, store.ErrNotFound) {
		abortAPI(c, http.StatusNotFound, codeNotFound, "This trace has no recorded decision for that attempt.")
		return
	}
	if err != nil {
		a.log.Error("decision request: decision read failed", "trace_id", traceID, "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The decision could not be read.")
		return
	}
	body, err := a.db.GetTraceBody(ctx, traceID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.log.Error("decision request: body read failed", "trace_id", traceID, "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The trace body could not be read.")
		return
	}
	if body == nil || !a.now().Before(body.ExpiresAt) {
		abortAPI(c, http.StatusConflict, codeBodyExpired, "The trace's body is no longer kept, so its decision request cannot be rebuilt.")
		return
	}
	c.JSON(http.StatusOK, a.recompile(ctx, rec, body))
}

// recompile rebuilds the requests of a recorded decision from the body.
func (a *studioAPI) recompile(ctx context.Context, rec *store.RecordedCompile, body *store.BodyRecord) decisionRequestView {
	view := decisionRequestView{Requests: []jevRequestView{}, RequestSHA256: []string{}, CPT: a.budget.CPT}
	if rec.CPT != nil && *rec.CPT > 0 {
		view.CPT = *rec.CPT
	}
	recorded := rec.RequestSHA256
	if recorded == nil {
		recorded = []string{}
	}
	mismatch := func(reason string) decisionRequestView {
		view.MismatchReason = &reason
		return view
	}
	if rec.PolicyVersion == nil {
		return mismatch(mismatchPolicyMissing)
	}
	pol := a.recordedPolicy(ctx, *rec.PolicyVersion)
	if pol == nil {
		return mismatch(mismatchPolicyMissing)
	}
	route, ok := pol.Route(rec.Route)
	if !ok || route == nil {
		return mismatch(mismatchPolicyMissing)
	}
	for _, e := range route.Evaluators {
		if _, ok := a.registry.Get(e.Ref); e.IsEnabled() && !ok {
			return mismatch(mismatchEvaluatorMissing)
		}
	}

	text := body.ResponseText
	if rec.Attempt == 1 {
		text = body.FinalResponseText
	}
	if text != nil && *text != "" {
		model := ""
		switch {
		case rec.ModelRequested != nil && *rec.ModelRequested != "":
			model = *rec.ModelRequested
		case route.Decision.Model != "":
			model = route.Decision.Model
		default:
			if client := a.client(); client != nil {
				model = client.Info().Model
			}
		}
		if model == "" {
			model = "unset"
		}
		x := &gate.Exchange{Response: *text, Evidence: &evidence.Result{Items: bodyEvidence(body)}}
		if body.UserRequest != nil {
			x.Evidence.UserRequest = *body.UserRequest
		}
		if body.SystemInstructions != nil {
			x.Evidence.SystemInstructions = *body.SystemInstructions
		}
		// The body keeps the rules as the gate resolved them (request, else route, else default),
		// which compile() resolves to the same values whether or not client rules are honoured.
		compiled, err := decision.Compile(gate.CompileRouteOf(route, pol, model), a.registry.Specs(),
			gate.CompileInputsOf(x, body.Rules), gate.RouteBudget(a.budget, route, view.CPT))
		if err != nil {
			a.log.Warn("decision request: recompile failed", "trace_id", rec.TraceID, "err", err)
		} else {
			for _, r := range compiled.Requests {
				view.Requests = append(view.Requests, jevRequestView{Model: r.Model, State: r.State, Questions: r.Questions})
			}
			if compiled.RequestSHA256 != nil {
				view.RequestSHA256 = compiled.RequestSHA256
			}
		}
	}
	view.MatchesRecorded = slices.Equal(view.RequestSHA256, recorded)
	if !view.MatchesRecorded && body.Truncated {
		return mismatch(mismatchBodyTruncated)
	}
	return view
}

// recordedPolicy loads the policy version a trace was gated under, leniently (block, fallback,
// knowledge and the fallback upstream are accepted: the version was valid when it was active).
// nil when it is gone.
func (a *studioAPI) recordedPolicy(ctx context.Context, id int64) *policy.Policy {
	if p := a.policy(); p != nil && p.Version == id {
		return p
	}
	v, err := a.policyVersion(ctx, id)
	if err != nil || v == nil {
		return nil
	}
	env := a.policyEnv()
	env.Enforcing, env.Knowledge, env.FallbackUpstream = true, true, true
	return policy.Check([]byte(v.YAML), env).Policy
}
