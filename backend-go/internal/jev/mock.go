package jev

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
)

// Mock modes (TRUTHTABLE_JEV_MOCK_MODE).
const (
	MockScenario  = "scenario"
	MockHash      = "hash"
	MockHeuristic = "heuristic"
)

// MockConfig configures the mock backend.
type MockConfig struct {
	// Mode is scenario (answers from the scenario pack, keyed by scenario id and question id), hash
	// (deterministic answers derived from the request) or heuristic (P2; answered as hash here).
	Mode string
	// Strict makes a scenario without an answer fail with mock_fixture_missing instead of falling
	// back to a hash answer.
	Strict bool
	// FixturesPath is a scenario file; empty uses the embedded contracts/demo/scenarios.v1.json.
	FixturesPath string
	// Seed feeds the hash answers.
	Seed int64
	// Latency is the simulated call latency.
	Latency time.Duration
}

// mockClient answers from scripted scenario answers, or from a hash of the request.
type mockClient struct {
	cfg       MockConfig
	info      Info
	scenarios map[string]map[string]*decision.RawAnswer
}

func newMock(cfg MockConfig, model string) (*mockClient, error) {
	switch cfg.Mode {
	case "":
		cfg.Mode = MockScenario
	case MockScenario, MockHash, MockHeuristic:
	default:
		return nil, fmt.Errorf("jev: unknown mock mode %q", cfg.Mode)
	}
	if model == "" {
		model = DefaultMockModel
	}
	m := &mockClient{cfg: cfg, info: Info{Backend: BackendMock, Model: model}}
	var b []byte
	var err error
	if cfg.FixturesPath != "" {
		b, err = os.ReadFile(cfg.FixturesPath)
	} else {
		b, err = contracts.FS.ReadFile("demo/scenarios.v1.json")
	}
	if err != nil {
		return nil, fmt.Errorf("jev: mock fixtures: %w", err)
	}
	if m.scenarios, err = parseScenarios(b); err != nil {
		return nil, fmt.Errorf("jev: mock fixtures: %w", err)
	}
	return m, nil
}

// parseScenarios reads the mock answers of a trustagent.scenarios/v1 pack.
func parseScenarios(b []byte) (map[string]map[string]*decision.RawAnswer, error) {
	var pack struct {
		Scenarios []struct {
			ID          string                         `json:"id"`
			MockAnswers map[string]*decision.RawAnswer `json:"mock_answers"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]*decision.RawAnswer, len(pack.Scenarios))
	for _, s := range pack.Scenarios {
		if s.ID == "" {
			return nil, fmt.Errorf("a scenario has no id")
		}
		if _, dup := out[s.ID]; dup {
			return nil, fmt.Errorf("scenario %q is defined twice", s.ID)
		}
		out[s.ID] = s.MockAnswers
	}
	return out, nil
}

func (m *mockClient) Info() Info { return m.info }

// Decide answers every question of req. In scenario mode a question the scenario scripts gets that
// answer; any other question fails the call when strict and gets a hash answer otherwise.
func (m *mockClient) Decide(ctx context.Context, req *Request, opt CallOptions) (*Response, error) {
	start := time.Now()
	if m.cfg.Latency > 0 {
		t := time.NewTimer(m.cfg.Latency)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, &Error{Kind: KindTimeout, Err: ctx.Err()}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Kind: KindTimeout, Err: err}
	}

	stateJCS, err := decision.Canonicalize(req.State)
	if err != nil {
		return nil, &Error{Kind: KindBadResponse, Message: "mock: state is not canonicalizable", Err: err}
	}
	scripted := m.scenarios[opt.Scenario]
	qids := make([]string, 0, len(req.Questions))
	for qid := range req.Questions {
		qids = append(qids, qid)
	}
	slices.Sort(qids)
	answers := make(map[string]*decision.RawAnswer, len(qids))
	for _, qid := range qids {
		if m.cfg.Mode == MockScenario {
			if a, ok := scripted[qid]; ok && a != nil {
				answers[qid] = cloneAnswer(a)
				continue
			}
			if m.cfg.Strict {
				return nil, &Error{Kind: KindMockFixtureMissing, Message: missingMessage(opt.Scenario, scripted != nil, qid)}
			}
		}
		answers[qid] = m.hashAnswer(qid, req.Questions[qid], stateJCS)
	}

	tokens := decision.Overhead
	if est, err := decision.Est(map[string]any{"model": req.Model, "state": req.State, "questions": req.Questions}, decision.DefaultCPT); err == nil {
		tokens += est
	}
	cost := float64(tokens) * DefaultPricePerMTok / 1e6
	snapshot := req.Model
	if !strings.HasPrefix(snapshot, "mock/") {
		snapshot = DefaultMockModel
	}
	return &Response{
		ID:          "mock-" + hex8(sha256.Sum256(stateJCS)),
		Snapshot:    snapshot,
		Answers:     answers,
		InputTokens: &tokens,
		CostUSD:     &cost,
		CostSource:  CostSimulated,
		Backend:     BackendMock,
		LatencyMs:   time.Since(start).Milliseconds(),
		Attempts:    1,
	}, nil
}

func missingMessage(scenario string, known bool, qid string) string {
	switch {
	case scenario == "":
		return "no X-TrustAgent-Scenario on the request, and TRUTHTABLE_JEV_MOCK_STRICT forbids hash answers"
	case !known:
		return fmt.Sprintf("unknown scenario %q", scenario)
	}
	return fmt.Sprintf("scenario %q has no mock answer for %s", scenario, qid)
}

// hashAnswer derives a deterministic answer from the seed, the question id and the state. Risk
// skews low (u^3), so unscripted demo traffic mostly passes.
func (m *mockClient) hashAnswer(qid string, question any, stateJCS []byte) *decision.RawAnswer {
	h := sha256.New()
	// The seed's two's-complement big-endian bytes.
	_ = binary.Write(h, binary.BigEndian, m.cfg.Seed)
	h.Write([]byte(qid))
	h.Write([]byte{0})
	h.Write(stateJCS)
	sum := h.Sum(nil)
	u := func(i int) float64 { return float64(binary.BigEndian.Uint64(sum[8*i:8*i+8])>>11) / (1 << 53) }

	q, _ := question.(map[string]any)
	typ, _ := q["type"].(string)
	switch typ {
	case decision.QuestionChoice:
		criteria, _ := q["criteria"].(map[string]any)
		options := make([]string, 0, len(criteria))
		for k := range criteria {
			options = append(options, k)
		}
		slices.Sort(options)
		if len(options) == 0 {
			break
		}
		top := options[int(u(1)*float64(len(options)))%len(options)]
		return &decision.RawAnswer{Type: typ, Choice: top, Probabilities: spread(options, top, u(2))}
	case decision.QuestionScore:
		levels, _ := q["criteria"].([]any)
		if len(levels) == 0 {
			break
		}
		keys := make([]string, len(levels))
		for i := range levels {
			keys[i] = strconv.Itoa(i)
		}
		top := keys[int(u(1)*float64(len(keys)))%len(keys)]
		return &decision.RawAnswer{Type: typ, Probabilities: spread(keys, top, u(2))}
	}
	p := decision.R2(math.Pow(u(0), 3))
	return &decision.RawAnswer{Type: decision.QuestionNoul, Noul: &p}
}

// spread puts most of the probability on top and shares the rest evenly.
func spread(keys []string, top string, u float64) map[string]float64 {
	pTop := decision.R2(0.55 + 0.4*u)
	if len(keys) == 1 {
		pTop = 1
	}
	rest := (1 - pTop) / float64(max(len(keys)-1, 1))
	out := make(map[string]float64, len(keys))
	for _, k := range keys {
		out[k] = rest
	}
	out[top] = pTop
	return out
}

// cloneAnswer copies a scripted answer, so a decision never shares memory with the fixtures.
func cloneAnswer(a *decision.RawAnswer) *decision.RawAnswer {
	c := *a
	c.Noul, c.Score, c.Confidence = clonePtr(a.Noul), clonePtr(a.Score), clonePtr(a.Confidence)
	if a.Probabilities != nil {
		c.Probabilities = make(map[string]float64, len(a.Probabilities))
		for k, v := range a.Probabilities {
			c.Probabilities[k] = v
		}
	}
	if a.Legend != nil {
		c.Legend = make(map[string]any, len(a.Legend))
		for k, v := range a.Legend {
			c.Legend[k] = v
		}
	}
	return &c
}

func clonePtr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func hex8(sum [32]byte) string {
	return strconv.FormatUint(binary.BigEndian.Uint64(sum[:8]), 16)
}
