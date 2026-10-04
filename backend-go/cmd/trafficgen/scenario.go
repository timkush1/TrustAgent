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
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Sets         []string          `json:"sets"`
	Route        string            `json:"route"`
	APIFormat    string            `json:"api_format"`
	Stream       bool              `json:"stream"`
	AnswerSource string            `json:"answer_source"`
	Headers      map[string]string `json:"headers"`
	Request      json.RawMessage   `json:"request"`
	Response     *string           `json:"response"`
	Judge        *judgeFixture     `json:"judge"`
	// Expect keeps its raw members: an absent member is not checked, a null one is.
	Expect map[string]json.RawMessage `json:"expect"`
}

// Answer sources (scenarios.v1 answer_source).
const (
	answerScripted  = "scripted"
	answerGenerated = "generated"
)

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
		if s.AnswerSource == "" {
			s.AnswerSource = answerScripted
		}
	}
	return pack.Scenarios, nil
}

// splitList splits a comma-separated flag value, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// selectScenarios keeps the scenarios named by sel, a comma-separated list of set names (s0,
// demo, e2e, p3, ...), scenario ids, or all, minus those named by exclude (sets or ids). File
// order is kept.
func selectScenarios(all []scenario, sel, exclude string) ([]scenario, error) {
	names, drop := splitList(sel), splitList(exclude)
	named := func(s *scenario, list []string) bool {
		return slices.Contains(list, s.ID) || slices.ContainsFunc(s.Sets, func(set string) bool { return slices.Contains(list, set) })
	}
	var out []scenario
	for i := range all {
		s := &all[i]
		if (slices.Contains(names, "all") || named(s, names)) && !named(s, drop) {
			out = append(out, *s)
		}
	}
	if len(out) == 0 {
		msg := fmt.Sprintf("no scenario matches %q", sel)
		if exclude != "" {
			msg += fmt.Sprintf(" once %q is excluded", exclude)
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// Gateway phases, in delivery order (SPEC §4.2). A scenario that needs a later phase than the
// walking skeleton carries that phase as a set tag (p3: streaming gating, block and fallback bodies,
// /v1/messages, /v1/completions, routes in config/policies.pending.yaml); trafficgen sends it only
// when --phase reaches that phase.
const (
	phaseS0 = "s0"
	// defaultPhase is the phase this build's gateway implements: it adds knowledge on the hot path,
	// Playground streaming and the batched trace writer to P3's block and fallback enforcement,
	// stream gating, /v1/messages and /v1/completions. P4 added no traffic scenario of its own (its
	// work is the control plane), so no scenario is tagged p4; p5 is the next phase a tag may name.
	defaultPhase = "p4"
)

var phases = []string{phaseS0, "p2", "p3", "p4", "p5"}

// scenarioPhase is the latest phase tag among sets (the s0 selection set is the earliest phase).
func scenarioPhase(sets []string) string {
	phase := phaseS0
	for _, s := range sets {
		if i := slices.Index(phases, s); i > slices.Index(phases, phase) {
			phase = s
		}
	}
	return phase
}

// phaseAllows reports whether a gateway at phase serves a scenario of phase need.
func phaseAllows(phase, need string) bool {
	return slices.Index(phases, need) <= slices.Index(phases, phase)
}

// byPhase splits scenarios into those a gateway at phase serves and those it skips.
func byPhase(scenarios []scenario, phase string) (run, skipped []scenario) {
	for _, s := range scenarios {
		if phaseAllows(phase, scenarioPhase(s.Sets)) {
			run = append(run, s)
		} else {
			skipped = append(skipped, s)
		}
	}
	return run, skipped
}

// apiPaths maps a scenario api_format onto its gateway path.
var apiPaths = map[string]string{
	"openai_chat":        "/v1/chat/completions",
	"openai_completions": "/v1/completions",
	"anthropic_messages": "/v1/messages",
}

// body is the request trafficgen sends: the scenario request plus, unless the answer comes from
// the gateway's upstream, test_response, the scripted answer the gateway returns in test mode
// instead of calling the upstream.
func (s *scenario) body(fromUpstream bool) ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(s.Request, &req); err != nil {
		return nil, fmt.Errorf("scenario %s: request is not a JSON object: %w", s.ID, err)
	}
	if s.Response != nil && !fromUpstream {
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
