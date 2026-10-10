package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/jev"
)

// statusView is the part of GET /api/status (api.ts SystemStatus) a recording run reads.
type statusView struct {
	TestMode      *bool `json:"test_mode"`
	DecisionModel struct {
		Backend      string  `json:"backend"`
		State        string  `json:"state"`
		EndpointHost *string `json:"endpoint_host"`
	} `json:"decision_model"`
	Judge struct {
		State          string  `json:"state"`
		Provider       *string `json:"provider"`
		FallbackActive bool    `json:"fallback_active"`
		FallbackReason *string `json:"fallback_reason"`
	} `json:"judge"`
}

// recordableStates are the decision-model states (api.ts DecisionModelStatus.state) a recording run
// accepts: the backend answers (ready, degraded) or its first check is still running (checking).
// Every other state (breaker_open, unauthorized, no_credits, budget_exhausted, misconfigured,
// unreachable, and mock or off) would leave the run's traces without a real decision.
var recordableStates = []string{"ready", "degraded", "checking"}

// recordable reads GET /api/status before a recording run (D121) and refuses a gateway whose
// traffic could not be labelled recorded: one not in test mode (scripted answers go out as
// test_response), a decision model that is the mock or off, answers from a local endpoint such as
// mock-upstream, or is in no state to answer, or a judge that is not up, is the mock (or none) or
// runs on its fallback. export-replay applies the same rules to the
// traces; checking first refuses the run before it spends anything.
func (g *generator) recordable(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.opts.baseURL+"/api/status", nil)
	if err != nil {
		return err
	}
	if g.opts.key != "" {
		req.Header.Set("X-TrustAgent-Key", g.opts.key)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET /api/status: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /api/status: HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var st statusView
	if err := json.Unmarshal(body, &st); err != nil || st.TestMode == nil || st.DecisionModel.Backend == "" || st.DecisionModel.State == "" {
		return errors.New("GET /api/status did not answer a gateway status")
	}
	return st.recordProblems()
}

// recordProblems lists why a gateway in state st cannot record, or returns nil.
func (st *statusView) recordProblems() error {
	var problems []string
	if !*st.TestMode {
		problems = append(problems, "the gateway is not in test mode (TRUTHTABLE_TEST_MODE=true sends scripted answers as test_response)")
	}
	dm := st.DecisionModel
	switch {
	case dm.Backend == "mock" || dm.Backend == "off" || dm.State == "mock" || dm.State == "off":
		problems = append(problems, fmt.Sprintf("the decision model is %s, not a real backend", dm.Backend))
	case dm.EndpointHost != nil && jev.LocalHost(*dm.EndpointHost):
		// In test mode the gateway forwards each scenario id to a local endpoint (D119), such as
		// mock-upstream, which answers by script.
		problems = append(problems, fmt.Sprintf("the decision model answers from the local endpoint %s, which gets each scenario id in test mode, so its answers are simulated", *dm.EndpointHost))
	case !slices.Contains(recordableStates, dm.State):
		problems = append(problems, fmt.Sprintf("the decision model is %s, so it cannot answer the run's checks", dm.State))
	}
	j := st.Judge
	provider := ""
	if j.Provider != nil {
		provider = *j.Provider
	}
	switch {
	case provider == "" || provider == "none":
		problems = append(problems, "the deep audit has no judge")
	case strings.EqualFold(provider, "mock"):
		problems = append(problems, "the judge is the mock, whose output is simulated")
	case j.FallbackActive:
		reason := "the judge runs on its fallback provider"
		if j.FallbackReason != nil && *j.FallbackReason != "" {
			reason += " (" + *j.FallbackReason + ")"
		}
		problems = append(problems, reason)
	case j.State != "up":
		problems = append(problems, fmt.Sprintf("the judge is %s, not up", j.State))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
