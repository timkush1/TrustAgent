// Package mockupstream is the in-process implementation of cmd/mock-upstream (SPEC §3.1): a fake
// of every vendor the gateway talks to, for e2e stacks, load tests and Go tests.
//
//   - OpenAI POST /v1/chat/completions and /v1/completions, Anthropic POST /v1/messages: JSON and
//     SSE, answering with the scenario's scripted response.
//   - OpenRouter POST /api/alpha/decisions and /api/v1/systemone, TypeSafe-style POST
//     /v1/systemone (the compatible e2e target), POST /api/v1/chat/completions, the PKCE pair
//     GET /auth → POST /api/v1/auth/keys, and GET /api/v1/key.
//
// Faults are keyed per request (SPEC §3.1): hang, html200, 402_inflight, 402_credits, 429, 529,
// 413 and more (see Faults). A fault comes from, in order: the X-Mock-Fault header, the fault
// query parameter, a /fault/<spec>/ path prefix, a scenario id of the form fault:<spec> (or a bare
// fault name), the mock_faults of the request's scenario, or Config.Fault. A spec "<name>@<n>"
// applies to the first n matching requests of one call (keyed by X-Mock-Key, which the gateway
// sets to the trace id, else the decision session_id, else globally) and then clears, so retries
// and hedges can be exercised.
//
// The scenarios are the demo pack and the faults pack (contracts/demo/faults.v1.json, D119). A
// faults-pack scenario's mock_faults.decision faults the decision requests that name it (the
// gateway forwards X-TrustAgent-Scenario to a local decision backend in test mode), and its
// mock_faults.upstream faults the LLM request of its conversation, counted per scenario (an LLM
// request carries no X-Mock-Key); the gateway's fallback request is never faulted.
//
// Scenario ids come from X-Mock-Scenario, X-TrustAgent-Scenario or the scenario query parameter.
// The gateway strips X-TrustAgent-* before it calls the LLM upstream, so LLM requests are matched
// on their conversation (every message, the Anthropic system and the prompt, as the gateway
// forwards the scenario's request), then on their last user message; they answer with the
// scenario's response, JSON or SSE as asked. The gateway's fallback request (its prompt carries the
// CONTEXT block) answers with the scenario's fallback.response. Decision requests answer each
// question from the scenario's mock_answers (fallback.mock_answers when the state's
// assistant_response is the fallback answer), else with a deterministic hash answer rounded to two
// decimals like the vendor (so choice probabilities can sum to 0.99 or 1.01).
package mockupstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truthtable/contracts"
)

// Config configures a Server.
type Config struct {
	// ScenariosPath is a trustagent.scenarios/v1 file; empty uses contracts/demo/scenarios.v1.json.
	// The embedded faults pack (contracts/demo/faults.v1.json) is always served as well.
	ScenariosPath string
	// Latency delays the first byte of every LLM response; JevLatency every decision response.
	Latency, JevLatency time.Duration
	// ChunkDelay separates SSE events.
	ChunkDelay time.Duration
	// Fault is the default fault spec for every request ("" = none).
	Fault string
	// Snapshot overrides the model id decision responses report (a snapshot change).
	Snapshot string
	// DefaultAnswer answers LLM requests that match no scenario.
	DefaultAnswer string
	// HangMax bounds the hang fault (default 10 minutes).
	HangMax time.Duration
	// MaxRecorded is the number of requests kept for Requests (default 200).
	MaxRecorded int
	Logger      *slog.Logger
}

// maxFaultKeys bounds the counters of counted fault specs: a load run with a counted default fault
// and a fresh session_id per request would otherwise grow them without limit.
const maxFaultKeys = 100_000

// Faults are the fault names a spec may use.
var Faults = []string{
	"hang", "html200", "402_inflight", "402_credits", "429", "529", "413",
	"400", "400_context", "401", "403", "403_moderation", "404", "404_data_policy", "422", "500", "502", "503", "524",
	"bad_json", "no_answers", "slow",
}

// Server is the mock upstream. It is safe for concurrent use.
type Server struct {
	cfg       Config
	log       *slog.Logger
	mux       *http.ServeMux
	scenarios map[string]*Scenario
	// byConversation, byPrompt and byFallbackPrompt match LLM requests that carry no scenario id
	// (the first scenario in file order wins a shared key).
	byConversation   map[string]*Scenario
	byPrompt         map[string]*Scenario
	byFallbackPrompt map[string]*Scenario

	mu       sync.Mutex
	counts   map[string]int
	codes    map[string]issuedCode // PKCE code → its challenge and mock key marker
	recorded []Recorded
	snapshot string
}

// Recorded is a request the server received.
type Recorded struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Query  string      `json:"query,omitempty"`
	Header http.Header `json:"header"`
	Body   string      `json:"body,omitempty"`
	At     time.Time   `json:"at"`
}

// New builds a Server.
func New(cfg Config) (*Server, error) {
	if cfg.HangMax <= 0 {
		cfg.HangMax = 10 * time.Minute
	}
	if cfg.MaxRecorded <= 0 {
		cfg.MaxRecorded = 200
	}
	if cfg.DefaultAnswer == "" {
		cfg.DefaultAnswer = "This is a mock answer from mock-upstream."
	}
	if cfg.Fault != "" {
		if _, _, err := parseFault(cfg.Fault); err != nil {
			return nil, err
		}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	var b []byte
	var err error
	if cfg.ScenariosPath != "" {
		b, err = os.ReadFile(cfg.ScenariosPath)
	} else {
		b, err = contracts.FS.ReadFile("demo/scenarios.v1.json")
	}
	if err != nil {
		return nil, fmt.Errorf("mock-upstream: scenarios: %w", err)
	}
	faults, err := contracts.FS.ReadFile("demo/faults.v1.json")
	if err != nil {
		return nil, fmt.Errorf("mock-upstream: faults pack: %w", err)
	}
	scenarios, err := parseScenarios(b, faults)
	if err != nil {
		return nil, fmt.Errorf("mock-upstream: scenarios: %w", err)
	}
	s := &Server{
		cfg: cfg, log: log, scenarios: map[string]*Scenario{},
		byConversation: map[string]*Scenario{}, byPrompt: map[string]*Scenario{}, byFallbackPrompt: map[string]*Scenario{},
		counts: map[string]int{}, codes: map[string]issuedCode{}, snapshot: cfg.Snapshot,
	}
	first := func(m map[string]*Scenario, key string, sc *Scenario) {
		if _, dup := m[key]; key != "" && !dup {
			m[key] = sc
		}
	}
	for _, sc := range scenarios {
		s.scenarios[sc.ID] = sc
		first(s.byConversation, sc.conversation(), sc)
		first(s.byPrompt, sc.prompt(), sc)
		if sc.fallbackText() != "" {
			first(s.byFallbackPrompt, sc.prompt(), sc)
		}
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	m.HandleFunc("GET /_mock/requests", s.handleRequests)
	m.HandleFunc("POST /_mock/reset", s.handleReset)
	m.HandleFunc("POST /v1/chat/completions", s.llm(formatOpenAIChat, false))
	m.HandleFunc("POST /v1/completions", s.llm(formatOpenAICompletions, false))
	m.HandleFunc("POST /v1/messages", s.llm(formatAnthropic, false))
	m.HandleFunc("POST /api/v1/chat/completions", s.llm(formatOpenAIChat, true))
	m.HandleFunc("POST /api/alpha/decisions", s.decisions(envOpenRouter, true))
	m.HandleFunc("POST /api/v1/systemone", s.decisions(envOpenRouter, true))
	m.HandleFunc("POST /v1/systemone", s.decisions(envTypeSafe, false))
	m.HandleFunc("GET /api/v1/key", s.handleKey)
	m.HandleFunc("POST /api/v1/auth/keys", s.handleAuthKeys)
	m.HandleFunc("GET /auth", s.handleAuthorize)
	s.mux = m
}

// ServeHTTP strips a /fault/<spec>/ prefix, records the request and routes it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if rest, ok := strings.CutPrefix(r.URL.Path, "/fault/"); ok {
		spec, path, _ := strings.Cut(rest, "/")
		spec, _ = url.PathUnescape(spec)
		r = r.Clone(r.Context())
		r.URL.Path = "/" + path
		r.URL.RawPath = ""
		r.Header.Set("X-Mock-Path-Fault", spec)
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 16<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	s.record(r, body)
	s.mux.ServeHTTP(w, r)
}

func (s *Server) record(r *http.Request, body []byte) {
	h := r.Header.Clone()
	for _, k := range []string{"Authorization", "X-Api-Key"} {
		if h.Get(k) != "" {
			h.Set(k, "[redacted]")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, Recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: h, Body: string(body), At: time.Now()})
	if n := len(s.recorded) - s.cfg.MaxRecorded; n > 0 {
		s.recorded = slices.Delete(s.recorded, 0, n)
	}
}

// Requests returns the recorded requests, oldest first (credentials redacted).
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.recorded)
}

// Reset clears recorded requests, fault counters and issued PKCE codes.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded, s.counts, s.codes = nil, map[string]int{}, map[string]issuedCode{}
}

// SetSnapshot changes the model id decision responses report ("" = derived from the request).
func (s *Server) SetSnapshot(snapshot string) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
}

func (s *Server) handleRequests(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"requests": s.Requests()})
}

func (s *Server) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.Reset()
	w.WriteHeader(http.StatusNoContent)
}

// scenarioID reads the scenario of a request.
func scenarioID(r *http.Request) string {
	for _, v := range []string{r.Header.Get("X-Mock-Scenario"), r.Header.Get("X-TrustAgent-Scenario"), r.URL.Query().Get("scenario")} {
		if v != "" {
			return v
		}
	}
	return ""
}

// fault resolves the fault spec of a request and consumes one use of a counted spec. packSpec is
// the mock_faults spec of the request's scenario ("" for none). countKey scopes counted specs to
// one call; X-Mock-Key overrides it.
func (s *Server) fault(r *http.Request, countKey, packSpec string) string {
	spec := ""
	for _, v := range []string{r.Header.Get("X-Mock-Fault"), r.URL.Query().Get("fault"), r.Header.Get("X-Mock-Path-Fault")} {
		if v != "" {
			spec = v
			break
		}
	}
	if spec == "" {
		sc := scenarioID(r)
		if f, ok := strings.CutPrefix(sc, "fault:"); ok {
			spec = f
		} else if name, _, err := parseFault(sc); err == nil && name != "" {
			spec = sc
		}
	}
	if spec == "" {
		spec = packSpec
	}
	if spec == "" {
		spec = s.cfg.Fault
	}
	if spec == "" {
		return ""
	}
	name, times, err := parseFault(spec)
	if err != nil {
		s.log.Warn("mock-upstream: unknown fault", "spec", spec)
		return ""
	}
	if times == 0 {
		return name
	}
	if k := r.Header.Get("X-Mock-Key"); k != "" {
		countKey = k
	}
	key := spec + "|" + countKey
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts[key] >= times {
		return ""
	}
	if len(s.counts) >= maxFaultKeys {
		clear(s.counts) // long finished calls; a load run keys every request anew
	}
	s.counts[key]++
	return name
}

// parseFault parses "<name>" or "<name>@<n>" (n ≥ 1).
func parseFault(spec string) (name string, times int, err error) {
	name, n, counted := strings.Cut(strings.TrimSpace(spec), "@")
	if !slices.Contains(Faults, name) {
		return "", 0, fmt.Errorf("mock-upstream: unknown fault %q (known: %s)", name, strings.Join(Faults, ", "))
	}
	if counted {
		if times, err = strconv.Atoi(n); err != nil || times < 1 {
			return "", 0, fmt.Errorf("mock-upstream: fault count in %q must be a positive integer", spec)
		}
	}
	return name, times, nil
}

// delay sleeps d or the X-Mock-Latency-Ms / latency_ms override, until the client goes away.
func delay(r *http.Request, d time.Duration) bool {
	if v := firstNonEmpty(r.Header.Get("X-Mock-Latency-Ms"), r.URL.Query().Get("latency_ms")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			d = time.Duration(ms) * time.Millisecond
		}
	}
	return sleepCtx(r, d)
}

func chunkDelay(r *http.Request, d time.Duration) time.Duration {
	if v := firstNonEmpty(r.Header.Get("X-Mock-Chunk-Delay-Ms"), r.URL.Query().Get("chunk_delay_ms")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return d
}

// sleepCtx sleeps d; false when the client went away first.
func sleepCtx(r *http.Request, d time.Duration) bool {
	if d <= 0 {
		return r.Context().Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// bearer returns the bearer token (or x-api-key) of a request.
func bearer(r *http.Request) string {
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	return r.Header.Get("X-Api-Key")
}
