package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
)

// scenarioSpy records the scenario every decision-model call names, and answers as the mock
// decision model does (not strict: a fault value names no scripted scenario).
type scenarioSpy struct {
	jev.Client
	mu    sync.Mutex
	named []string
}

func newScenarioSpy(t *testing.T) *scenarioSpy {
	t.Helper()
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario}})
	if err != nil {
		t.Fatal(err)
	}
	return &scenarioSpy{Client: cl}
}

func (s *scenarioSpy) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	s.mu.Lock()
	s.named = append(s.named, opt.Scenario)
	s.mu.Unlock()
	return s.Client.Decide(ctx, req, opt)
}

func (s *scenarioSpy) scenarios() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.named)
}

// packScenario is a demo-pack scenario's request, answer source and scripted answer.
func packScenario(t *testing.T, id string) (request map[string]any, answerSource, response string) {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []struct {
			ID           string         `json:"id"`
			AnswerSource string         `json:"answer_source"`
			Request      map[string]any `json:"request"`
			Response     string         `json:"response"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	for _, s := range pack.Scenarios {
		if s.ID == id {
			return s.Request, s.AnswerSource, s.Response
		}
	}
	t.Fatalf("no scenario %s in the demo pack", id)
	return nil, "", ""
}

// sendScenario posts a demo scenario's request to /v1/chat/completions with X-TrustAgent-Scenario
// set to header, and test_response when scripted is set, and returns the trace it started.
func sendScenario(t *testing.T, e *env, id, header string, scripted bool) *gate.TraceStart {
	t.Helper()
	req, _, response := packScenario(t, id)
	if scripted {
		req["test_response"] = response
	}
	body, _ := json.Marshal(req)
	before := len(e.sink.traces())
	w := e.do(t, "/v1/chat/completions", string(body), map[string]string{HeaderScenario: header})
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status %d %s", id, w.Code, w.Body.String())
	}
	e.sink.result(t)
	traces := e.sink.traces()
	if len(traces) != before+1 {
		t.Fatalf("%s: %d traces started, want %d", id, len(traces), before+1)
	}
	return traces[len(traces)-1]
}

// TestFaultScenarioIsForwardOnly: in test mode an X-TrustAgent-Scenario value fault:<spec> (the
// load test's fault:hang@1, D119) reaches the decision-model call, which the test-header seam
// forwards to a local backend, but is never stored: the trace keeps no scenario. A value of any
// other shape, or any value outside test mode, reaches nothing; a scenario id is still stored and
// forwarded.
func TestFaultScenarioIsForwardOnly(t *testing.T) {
	for _, c := range []struct {
		name, header string
		testMode     bool
		forwarded    string
		stored       string
	}{
		{"fault spec", "fault:hang@1", true, "fault:hang@1", ""},
		{"uncounted fault spec", "fault:402_credits", true, "fault:402_credits", ""},
		{"scenario id", "grounded-pass", true, "grounded-pass", "grounded-pass"},
		{"outside test mode", "fault:hang@1", false, "", ""},
		{"upper case", "fault:Hang@1", true, "", ""},
		{"no count", "fault:hang@", true, "", ""},
		{"empty spec", "fault:", true, "", ""},
		{"trailing junk", "fault:hang@1x", true, "", ""},
		{"path", "fault:../hang", true, "", ""},
		{"no prefix", "hang@1", true, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			spy := newScenarioSpy(t)
			e := newEnv(t, envOpts{testMode: c.testMode, jev: spy})
			tr := sendScenario(t, e, "grounded-pass", c.header, c.testMode)
			if tr.Scenario != c.stored {
				t.Errorf("trace scenario %q, want %q", tr.Scenario, c.stored)
			}
			named := spy.scenarios()
			if len(named) == 0 {
				t.Fatal("the decision model was not called")
			}
			for _, got := range named {
				if got != c.forwarded {
					t.Errorf("the decision-model call names %q, want %q", got, c.forwarded)
				}
			}
			// Nothing the sink records carries a forward-only value.
			if b, err := json.Marshal(tr); err != nil || c.stored == "" && slices.Contains(jsonStrings(b), c.header) {
				t.Errorf("the recorded trace holds %q (%v): %s", c.header, err, b)
			}
		})
	}
}

// jsonStrings is every string value of a JSON document.
func jsonStrings(b []byte) []string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}
