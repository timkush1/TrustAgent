package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// mockUpstreamHost is the e2e and load stacks' fake vendor (cmd/mock-upstream): a decision backend
// on it is simulated, whatever its backend id says.
const mockUpstreamHost = "mock-upstream"

// recordable reads GET /api/status before a recording run (D121) and refuses a gateway whose
// traffic could not be labelled recorded: one not in test mode (scripted answers go out as
// test_response), a decision model that is the mock, off or mock-upstream, or a judge that is not
// up, is the mock (or none) or runs on its fallback. export-replay applies the same rules to the
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
	if err := json.Unmarshal(body, &st); err != nil || st.TestMode == nil || st.DecisionModel.Backend == "" {
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
	case dm.EndpointHost != nil && strings.EqualFold(*dm.EndpointHost, mockUpstreamHost):
		problems = append(problems, "the decision model answers from mock-upstream, whose answers are simulated")
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
