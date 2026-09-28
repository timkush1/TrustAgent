// Package jev is the gateway's decision-model client (SPEC §1, §3.1): one call asks every attached
// evaluator's question about one answer and returns a probability per question.
//
// Backends sit behind the vendor-neutral Client interface. S0 ships two: mock (scenario-keyed
// answers from contracts/demo/scenarios.v1.json with a deterministic hash fallback) and off. Auto
// resolution picks mock in demo mode; the HTTP backends (openrouter-decisions,
// openrouter-systemone, typesafe, compatible) with retry, hedging, breaker, limiter and spend caps
// arrive in P2, and until then they resolve to off with a reason.
package jev

import (
	"context"
	"errors"
	"fmt"

	"github.com/truthtable/backend-go/internal/decision"
)

// Backend ids (DecisionBackend) and the auto setting.
const (
	BackendAuto                = "auto"
	BackendOpenRouterDecisions = "openrouter-decisions"
	BackendOpenRouterSystemOne = "openrouter-systemone"
	BackendTypeSafe            = "typesafe"
	BackendCompatible          = "compatible"
	BackendMock                = "mock"
	BackendOff                 = "off"
)

// Default decision models per backend (TRUTHTABLE_JEV_MODEL default_note).
const (
	DefaultMockModel       = "mock/jev-heuristic-v1"
	DefaultOpenRouterModel = "typesafe/jev-1.13"
	DefaultTypeSafeModel   = "jev-1.13.0"
)

// DefaultPricePerMTok is the list price per million input tokens used when a backend reports no
// cost (SPEC §1: $0.042/M input, output free [V]); the mock reports it as simulated cost.
const DefaultPricePerMTok = 0.042

// Cost sources (DecisionModelUsage.cost_source).
const (
	CostReported  = "reported"
	CostComputed  = "computed"
	CostSimulated = "simulated"
	CostUnknown   = "unknown"
)

// Request is one decision-model request body. State and Questions come from decision.Compile; the
// request hash covers {model, state, questions} only.
type Request struct {
	Model     string         `json:"model"`
	SessionID string         `json:"session_id,omitempty"`
	State     map[string]any `json:"state"`
	Questions map[string]any `json:"questions"`
}

// Response is a decision-model response with the gateway's call accounting.
type Response struct {
	// ID is the vendor response id; Snapshot is the model snapshot that answered.
	ID       string
	Snapshot string
	// Answers maps question ids to raw answers.
	Answers map[string]*decision.RawAnswer
	// InputTokens is the reported (or, for the mock, estimated) input token count.
	InputTokens *int
	// CostUSD and CostSource describe the call's cost.
	CostUSD    *float64
	CostSource string
	// Backend, LatencyMs, ConnectMs, Attempts and Hedged describe the call.
	Backend   string
	LatencyMs int64
	ConnectMs *int64
	Attempts  int
	Hedged    bool
}

// Class says who waits for a call: sync calls gate a response in flight (flag), async calls do
// not (observe). The class picks retry budgets and limiter reserves in P2.
type Class int

// Call classes.
const (
	ClassSync Class = iota
	ClassAsync
)

// CallOptions carry per-call context.
type CallOptions struct {
	Class Class
	// Scenario is the X-TrustAgent-Scenario id, honoured by the mock backend in test mode.
	Scenario string
}

// Error kinds (SPEC §3.1 Jev error taxonomy, plus the gateway's own). They appear in
// X-TrustAgent-Status as error:<kind> and in decide() request statuses.
const (
	KindOff                = "off"
	KindTimeout            = "timeout"
	KindAuth               = "auth"
	KindCredits            = "credits"
	KindDataPolicy         = "data_policy"
	KindNotFound           = "not_found"
	KindTooLarge           = "too_large"
	KindRetryable          = "retryable"
	KindBadContentType     = "bad_content_type"
	KindBadResponse        = "bad_response"
	KindMockFixtureMissing = "mock_fixture_missing"
)

// Error is a failed decision-model call.
type Error struct {
	Kind    string
	Status  int // HTTP status, 0 when there was none
	Message string
	Hint    string
	Err     error
}

func (e *Error) Error() string {
	msg := "jev " + e.Kind
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// ErrorKind returns the error kind of err: its *Error kind, timeout for a context deadline, or
// bad_response for anything else.
func ErrorKind(err error) string {
	var e *Error
	switch {
	case errors.As(err, &e):
		return e.Kind
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	}
	return KindBadResponse
}

// Info describes the resolved backend (the core of DecisionModelStatus).
type Info struct {
	Backend string
	// Reason explains how the backend was chosen, for status pages and logs.
	Reason string
	// Model is the decision model requested when a route pins none.
	Model string
}

// Client is a decision-model backend.
type Client interface {
	Decide(ctx context.Context, req *Request, opt CallOptions) (*Response, error)
	Info() Info
}

// Config selects and configures the backend.
type Config struct {
	// Backend is TRUTHTABLE_JEV_BACKEND (auto by default).
	Backend string
	// Model is TRUTHTABLE_JEV_MODEL ("" = the backend default).
	Model string
	// DemoMode is TRUTHTABLE_DEMO_MODE: it forces the mock backend.
	DemoMode bool
	// Credentials and endpoint, read only to explain auto resolution in S0.
	OpenRouterKey, TypeSafeKey, JevAPIKey, BaseURL string
	Mock                                           MockConfig
}

// New resolves the backend (SPEC §3.1 auto resolution) and builds its client.
func New(cfg Config) (Client, error) {
	backend, reason := resolve(cfg)
	switch backend {
	case BackendMock:
		m, err := newMock(cfg.Mock, cfg.Model)
		if err != nil {
			return nil, err
		}
		m.info.Reason = reason
		if cfg.Mock.Mode == MockHeuristic {
			m.info.Reason += "; the heuristic mock mode lands in P2, so questions get hash answers"
		}
		return m, nil
	case BackendOff:
		return &offClient{info: Info{Backend: BackendOff, Reason: reason, Model: cfg.Model}}, nil
	}
	return nil, fmt.Errorf("jev: unknown backend %q", backend)
}

func resolve(cfg Config) (backend, reason string) {
	if cfg.DemoMode {
		return BackendMock, "demo mode (TRUTHTABLE_DEMO_MODE) uses the mock decision model"
	}
	want := cfg.Backend
	if want == "" {
		want = BackendAuto
	}
	switch want {
	case BackendMock:
		return BackendMock, "TRUTHTABLE_JEV_BACKEND=mock"
	case BackendOff:
		return BackendOff, "TRUTHTABLE_JEV_BACKEND=off"
	case BackendAuto:
		switch {
		case cfg.BaseURL != "" && cfg.JevAPIKey != "":
			want = BackendCompatible
		case cfg.OpenRouterKey != "":
			want = BackendOpenRouterDecisions
		case cfg.TypeSafeKey != "":
			want = BackendTypeSafe
		default:
			return BackendOff, "no decision-model credentials: set OPENROUTER_API_KEY, or TRUTHTABLE_JEV_BACKEND=mock for simulated checks"
		}
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne, BackendTypeSafe, BackendCompatible:
	default:
		return BackendOff, fmt.Sprintf("unknown TRUTHTABLE_JEV_BACKEND %q", want)
	}
	return BackendOff, fmt.Sprintf("the %s backend is not available in this build yet (it lands in P2); fast checks are off", want)
}

// offClient is the off backend: every call fails with KindOff, and the gate skips it as
// decision_model_off without calling.
type offClient struct{ info Info }

func (o *offClient) Decide(context.Context, *Request, CallOptions) (*Response, error) {
	return nil, &Error{Kind: KindOff, Message: o.info.Reason}
}

func (o *offClient) Info() Info { return o.info }
