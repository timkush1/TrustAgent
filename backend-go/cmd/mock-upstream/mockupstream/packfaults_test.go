package mockupstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/contracts"
)

// readPack reads an embedded scenario pack's scenarios.
func readPack(t *testing.T, name string) []*Scenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/" + name)
	if err != nil {
		t.Fatal(err)
	}
	all, err := parseScenarios(b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return all
}

// packSets reads each scenario's sets from an embedded pack (the mock does not keep them).
func packSets(t *testing.T, name string) map[string][]string {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []struct {
			ID   string   `json:"id"`
			Sets []string `json:"sets"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, s := range pack.Scenarios {
		out[s.ID] = s.Sets
	}
	return out
}

// TestPackFaultSpecsKnown keeps the two packs servable (D119): every mock_faults value is a fault
// this mock knows, in the shape the scenarios schema and the gateway's forward-only fault:<spec>
// value accept; only the faults pack holds fault scenarios, each in set faults; ids are unique
// across both packs; a scenario that faults its LLM upstream has a conversation of its own, so the
// mock can find it without the scenario header the gateway strips; and the mock serves every
// faults-pack scenario.
func TestPackFaultSpecsKnown(t *testing.T) {
	schema, err := contracts.FS.ReadFile("schema/scenarios.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Items struct {
			Properties struct {
				MockFaults struct {
					Properties map[string]struct {
						Pattern string `json:"pattern"`
					} `json:"properties"`
				} `json:"mock_faults"`
			} `json:"properties"`
		} `json:"items"`
	}
	var root struct {
		Properties struct {
			Scenarios json.RawMessage `json:"scenarios"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &root); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(root.Properties.Scenarios, &doc); err != nil {
		t.Fatal(err)
	}
	pattern := doc.Items.Properties.MockFaults.Properties["decision"].Pattern
	if pattern == "" || doc.Items.Properties.MockFaults.Properties["upstream"].Pattern != pattern {
		t.Fatalf("scenarios.v1 mock_faults patterns: %+v", doc.Items.Properties.MockFaults.Properties)
	}
	spec := regexp.MustCompile(pattern)
	// The gateway accepts X-TrustAgent-Scenario fault:<spec> with the same shape
	// (internal/proxy faultScenario), so every fault this mock knows can be sent both ways.
	for _, name := range Faults {
		if !spec.MatchString(name) || !spec.MatchString(name+"@2") {
			t.Errorf("fault %q does not fit the mock_faults pattern %s", name, pattern)
		}
	}

	main, faults := readPack(t, "scenarios.v1.json"), readPack(t, "faults.v1.json")
	mainSets, faultSets := packSets(t, "scenarios.v1.json"), packSets(t, "faults.v1.json")
	if _, err := parseScenarios(mustRead(t, "demo/scenarios.v1.json"), mustRead(t, "demo/faults.v1.json")); err != nil {
		t.Fatalf("the two packs together: %v", err)
	}
	for _, s := range main {
		if s.MockFaults != nil || slices.Contains(mainSets[s.ID], "faults") {
			t.Errorf("%s: the main pack never holds a fault scenario (D119)", s.ID)
		}
	}
	conversations := map[string][]string{}
	for _, s := range append(slices.Clone(main), faults...) {
		if k := s.conversation(); k != "" {
			conversations[k] = append(conversations[k], s.ID)
		}
	}
	for _, s := range faults {
		if !slices.Contains(faultSets[s.ID], "faults") {
			t.Errorf("%s: every faults-pack scenario carries set faults", s.ID)
		}
		if s.MockFaults == nil {
			t.Errorf("%s: a faults-pack scenario sets mock_faults", s.ID)
			continue
		}
		for field, v := range map[string]string{"decision": s.MockFaults.Decision, "upstream": s.MockFaults.Upstream} {
			if v == "" {
				continue
			}
			if _, _, err := parseFault(v); err != nil || !spec.MatchString(v) {
				t.Errorf("%s: mock_faults.%s %q: %v", s.ID, field, v, err)
			}
		}
		if ids := conversations[s.conversation()]; s.MockFaults.Upstream != "" && len(ids) != 1 {
			t.Errorf("%s faults its upstream but shares its conversation with %v", s.ID, ids)
		}
	}
	srv, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range faults {
		if srv.scenarios[s.ID] == nil {
			t.Errorf("mock-upstream does not serve faults-pack scenario %s", s.ID)
		}
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := contracts.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// faultsPack is a pack in the faults pack's shape: a decision fault counted per call, a decision
// fault on every call, and an upstream fault on a scenario with a scripted fallback.
const faultsPack = `{"schema":"trustagent.scenarios/v1","scenarios":[
 {"id":"pf-decision-429","request":{"model":"m","messages":[{"role":"user","content":"Is the shop open on Sunday?"}]},
  "response":"Yes, from 10:00 to 16:00.","mock_answers":{"groundedness__v1":{"noul":0.97}},
  "mock_faults":{"decision":"429@1"}},
 {"id":"pf-decision-badjson","request":{"model":"m","messages":[{"role":"user","content":"Do you ship to Norway?"}]},
  "response":"Yes, within five days.","mock_faults":{"decision":"bad_json"}},
 {"id":"pf-upstream-503","request":{"model":"m","messages":[{"role":"system","content":"Refunds take 5 days."},{"role":"user","content":"How long do refunds take?"}]},
  "response":"Refunds take 5 days.","fallback":{"response":"Refunds take five business days."},
  "mock_faults":{"upstream":"503"}},
 {"id":"pf-upstream-html","request":{"model":"m","messages":[{"role":"user","content":"Where is my parcel?"}]},
  "response":"It left the depot today.","mock_faults":{"upstream":"html200@1"}}
]}`

func newFaultsPackServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(path, []byte(faultsPack), 0o600); err != nil {
		t.Fatal(err)
	}
	return newTestServer(t, Config{ScenariosPath: path})
}

// TestPackFaultsByScenario: a decision request that names a scenario answers with its
// mock_faults.decision (a counted spec per X-Mock-Key, so one call's retry passes while the next
// call faults again); an explicit fault overrides the pack; an LLM request matched by its
// conversation answers with mock_faults.upstream, counted per scenario; the fallback request of a
// faulted scenario is never faulted.
func TestPackFaultsByScenario(t *testing.T) {
	s, srv := newFaultsPackServer(t)
	decide := func(scenario, key string, extra map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer k", "X-TrustAgent-Scenario": scenario, "X-Mock-Key": key}
		for k, v := range extra {
			h[k] = v
		}
		return do(t, srv, call{path: "/v1/systemone", header: h, body: decisionBody("typesafe/jev-1.13", "sess")})
	}
	if resp, b := decide("pf-decision-429", "trace-1", nil); resp.StatusCode != http.StatusTooManyRequests ||
		resp.Header.Get("Retry-After") != "1" || !strings.Contains(string(b), "rate_limit_error") {
		t.Fatalf("first attempt: %d %v %s", resp.StatusCode, resp.Header, b)
	}
	resp, b := decide("pf-decision-429", "trace-1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the retry of the same call: %d %s", resp.StatusCode, b)
	}
	if got := decodeJSON(t, b)["answers"].(map[string]any)["groundedness__v1"].(map[string]any)["noul"]; got != 0.97 {
		t.Errorf("after the fault the scenario's mock answers apply: %s", b)
	}
	if resp, _ := decide("pf-decision-429", "trace-2", nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("the next call faults again: %d", resp.StatusCode)
	}
	for range 2 {
		if resp, b := decide("pf-decision-badjson", "trace-3", nil); resp.StatusCode != http.StatusOK || json.Valid(b) {
			t.Errorf("an uncounted fault answers every attempt: %d %s", resp.StatusCode, b)
		}
	}
	if resp, _ := decide("pf-decision-429", "trace-4", map[string]string{"X-Mock-Fault": "403"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("an explicit fault overrides the pack: %d", resp.StatusCode)
	}
	if resp, _ := decide("grounded-pass", "trace-5", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("a scenario without mock_faults is not faulted: %d", resp.StatusCode)
	}

	llm := func(body map[string]json.RawMessage) (*http.Response, []byte) {
		b, _ := json.Marshal(body)
		return do(t, srv, call{path: "/v1/chat/completions", body: string(b)})
	}
	up := s.scenarios["pf-upstream-503"]
	for range 2 {
		resp, b := llm(forwarded(t, up, false))
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(b), `"type":"api_error"`) {
			t.Fatalf("upstream fault by conversation: %d %s", resp.StatusCode, b)
		}
	}
	resp, b = llm(fallbackRequest(t, up))
	if resp.StatusCode != http.StatusOK || answerText(t, "openai_chat", b, false) != up.Fallback.Response {
		t.Errorf("the fallback request is not faulted: %d %s", resp.StatusCode, b)
	}
	html := s.scenarios["pf-upstream-html"]
	if resp, b := llm(forwarded(t, html, false)); resp.StatusCode != http.StatusOK ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(b), "<html") {
		t.Errorf("counted upstream fault, first request: %d %s", resp.StatusCode, b)
	}
	if resp, b := llm(forwarded(t, html, false)); resp.StatusCode != http.StatusOK || answerText(t, "openai_chat", b, false) != html.Response {
		t.Errorf("counted upstream fault, second request: %d %s", resp.StatusCode, b)
	}
	s.Reset()
	if resp, _ := llm(forwarded(t, html, false)); !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("a reset counts the upstream fault afresh: %v", resp.Header)
	}
}

// TestPackFaultsRefused: a pack with an unknown fault, or an id the faults pack also defines, is
// refused.
func TestPackFaultsRefused(t *testing.T) {
	for name, pack := range map[string]string{
		"unknown fault": `{"scenarios":[{"id":"x","mock_faults":{"decision":"teapot"}}]}`,
		"bad count":     `{"scenarios":[{"id":"x","mock_faults":{"upstream":"503@0"}}]}`,
	} {
		if _, err := parseScenarios([]byte(pack)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := parseScenarios([]byte(`{"scenarios":[{"id":"a"}]}`), []byte(`{"scenarios":[{"id":"a"}]}`)); err == nil {
		t.Error("an id defined in both packs is refused")
	}
}
