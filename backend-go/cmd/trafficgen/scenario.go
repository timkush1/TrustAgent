package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/truthtable/contracts"
)

// scenarioPackSchema is the schema marker of contracts/demo/scenarios.v1.json.
const scenarioPackSchema = "trustagent.scenarios/v1"

// scenarioPack is a trustagent.scenarios/v1 document (contracts/schema/scenarios.v1.schema.json).
type scenarioPack struct {
	Schema    string     `json:"schema"`
	Scenarios []scenario `json:"scenarios"`
}

// scenario is one scripted exchange. Request stays raw so that fields trafficgen does not know
// reach the gateway unchanged.
type scenario struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Sets      []string          `json:"sets"`
	Route     string            `json:"route"`
	APIFormat string            `json:"api_format"`
	Stream    bool              `json:"stream"`
	Headers   map[string]string `json:"headers"`
	Request   json.RawMessage   `json:"request"`
	Response  *string           `json:"response"`
	Judge     *judgeFixture     `json:"judge"`
	// Expect keeps its raw members: an absent member is not checked, a null one is.
	Expect map[string]json.RawMessage `json:"expect"`
}

// judgeFixture is the mock judge's output for a scenario: the claims the deep audit reports.
type judgeFixture struct {
	Claims []struct {
		Claim   string `json:"claim"`
		Verdict string `json:"verdict"`
	} `json:"claims"`
}

// loadScenarios reads the pack at path, or the embedded contracts/demo/scenarios.v1.json.
func loadScenarios(path string) ([]scenario, error) {
	var (
		b   []byte
		err error
	)
	if path == "" {
		b, err = contracts.FS.ReadFile("demo/scenarios.v1.json")
		path = "contracts/demo/scenarios.v1.json"
	} else {
		b, err = os.ReadFile(path) // #nosec G304 -- the operator names the scenario pack (--file)
	}
	if err != nil {
		return nil, err
	}
	var pack scenarioPack
	if err := json.Unmarshal(b, &pack); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if pack.Schema != scenarioPackSchema {
		return nil, fmt.Errorf("%s: schema is %q, want %q", path, pack.Schema, scenarioPackSchema)
	}
	for i := range pack.Scenarios {
		s := &pack.Scenarios[i]
		if s.ID == "" || len(s.Request) == 0 {
			return nil, fmt.Errorf("%s: scenario %d needs an id and a request", path, i)
		}
		if s.APIFormat == "" {
			s.APIFormat = "openai_chat"
		}
	}
	return pack.Scenarios, nil
}

// selectScenarios keeps the scenarios named by sel: a comma-separated list of set names (s0,
// demo, e2e, ...), scenario ids, or all. File order is kept.
func selectScenarios(all []scenario, sel string) ([]scenario, error) {
	names := strings.Split(sel, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}
	var out []scenario
	for _, s := range all {
		if slices.Contains(names, "all") || slices.Contains(names, s.ID) ||
			slices.ContainsFunc(s.Sets, func(set string) bool { return slices.Contains(names, set) }) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scenario matches %q", sel)
	}
	return out, nil
}

// apiPaths maps a scenario api_format onto its gateway path.
var apiPaths = map[string]string{
	"openai_chat":        "/v1/chat/completions",
	"openai_completions": "/v1/completions",
	"anthropic_messages": "/v1/messages",
}

// body is the request trafficgen sends: the scenario request plus test_response, the scripted
// upstream answer the gateway returns in test mode instead of calling the upstream.
func (s *scenario) body() ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(s.Request, &req); err != nil {
		return nil, fmt.Errorf("scenario %s: request is not a JSON object: %w", s.ID, err)
	}
	if s.Response != nil {
		b, err := json.Marshal(*s.Response)
		if err != nil {
			return nil, err
		}
		req["test_response"] = b
	}
	if s.Stream {
		req["stream"] = json.RawMessage("true")
	}
	return json.Marshal(req)
}

func (s *scenario) path() (string, error) {
	p, ok := apiPaths[s.APIFormat]
	if !ok {
		return "", errors.New("unknown api_format " + s.APIFormat)
	}
	return p, nil
}
