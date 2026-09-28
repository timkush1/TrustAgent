// Package jev is the gateway's decision-model client (SPEC §1, §3.1): one call asks every attached
// evaluator's question about one answer and returns a probability per question.
//
// Backends sit behind the vendor-neutral Client interface:
//
//   - openrouter-decisions: POST {OpenRouter}/api/alpha/decisions (the default with an OpenRouter key)
//   - openrouter-systemone: POST {OpenRouter}/api/v1/systemone (TypeSafe-compatible fallback)
//   - typesafe: POST https://api.typesafe.ai/v1/systemone
//   - compatible: any gateway that speaks the TypeSafe body at a custom base URL and path
//   - mock: scenario-keyed answers from contracts/demo/scenarios.v1.json with a hash fallback
//   - off: every call fails with KindOff and the gate skips it
//
// The HTTP backends share one resilient client (http.go): Content-Type check, the vendor error
// taxonomy (classify.go), retries that honour Retry-After, hedging, a breaker that counts vendor
// faults only, a token-bucket limiter with a sync reserve and an in-flight cap, spend reservations
// against the daily cap, one cost event per attempt, snapshot tracking, the chars-per-token EWMA,
// a dedicated HTTP/2 transport and keep-warm.
package jev

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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

// Default endpoints (SPEC §6, TRUTHTABLE_OPENROUTER_BASE_URL, TRUTHTABLE_JEV_BASE_URL / _PATH).
const (
	DefaultOpenRouterBaseURL = "https://openrouter.ai"
	DefaultTypeSafeBaseURL   = "https://api.typesafe.ai"
	PathDecisions            = "/alpha/decisions" // under {OpenRouter}/api
	PathSystemOne            = "/v1/systemone"    // under {OpenRouter}/api, TypeSafe and compatible gateways
	PathKey                  = "/v1/key"          // under {OpenRouter}/api: key check and keep-warm
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

// NewRequest builds the vendor request of one compiled request (SPEC §2.9 compile, §2.12): the
// compiled model, state and questions, plus the trace id as session_id, which OpenRouter
// backends send (TRUTHTABLE_JEV_SEND_SESSION_ID) and which never enters request_sha256.
func NewRequest(r *decision.Request, traceID string) *Request {
	return &Request{Model: r.Model, SessionID: traceID, State: r.State, Questions: r.Questions}
}

// Response is a decision-model response with the gateway's call accounting.
type Response struct {
	// ID is the vendor response id; Snapshot is the model snapshot that answered.
	ID       string
	Snapshot string
	// Answers maps question ids to raw answers. Probabilities are passed through as the vendor
	// sent them (2-decimal rounding, sums near 1); decision.Normalize renormalizes them.
	Answers map[string]*decision.RawAnswer
	// InputTokens is the reported (or, for the mock, estimated) input token count.
	InputTokens *int
	// CostUSD and CostSource describe the call's cost: the winning attempt plus every attempt
	// that finished before it. Hedge losers that finish later are only in the cost ledger.
	CostUSD    *float64
	CostSource string
	// Backend, LatencyMs, ConnectMs, Attempts and Hedged describe the call.
	Backend   string
	LatencyMs int64
	ConnectMs *int64
	Attempts  int
	Hedged    bool
	// Late is set on a response delivered through CallOptions.OnLate after the caller stopped
	// waiting; it must be recorded but never enforced.
	Late bool
}

// Class says who waits for a call: sync calls gate a response in flight (flag, block, fallback),
// async calls do not (observe). The class picks the retry budget and the limiter reserve.
type Class int

// Call classes.
const (
	ClassSync Class = iota
	ClassAsync
)

func (c Class) String() string {
	if c == ClassAsync {
		return "async"
	}
	return "sync"
}

// Cost-ledger components of a decision call (CostRecord.component).
const (
	ComponentGate       = "gate"
	ComponentProbe      = "probe"
	ComponentStudioTest = "studio_test"
	ComponentDryRun     = "dry_run"
	ComponentPlayground = "playground"
)

// CallOptions carry per-call context. Every field is optional.
type CallOptions struct {
	Class Class
	// Scenario is the X-TrustAgent-Scenario id, honoured by the mock backend in test mode and
	// forwarded to an HTTP backend only with HTTPConfig.ForwardScenario (mock-upstream faults).
	Scenario string
	// HedgeAfter starts a second attempt when the first has not answered after this long
	// (route hedge_after_ms). 0 disables hedging.
	HedgeAfter time.Duration
	// Enforcing marks a call from a block or fallback route: it may spend the enforcing reserve
	// of the daily decision cap (TRUTHTABLE_SPEND_ENFORCING_RESERVE).
	Enforcing bool
	// TraceID, Route and KeyLabel label the cost events; Component defaults to gate.
	TraceID, Route, KeyLabel, Component string
	// OnLate receives the outcome of attempts that were still running when ctx ended, if the
	// in-flight count allowed them to continue (SPEC §2.9 late decisions). It is called at most
	// once, from another goroutine. Without it, running attempts are cancelled when ctx ends.
	OnLate func(*Response, error)
}

func (o CallOptions) component() string {
	if o.Component == "" {
		return ComponentGate
	}
	return o.Component
}

// Error kinds (SPEC §3.1 Jev error taxonomy, plus the gateway's own). They appear in
// X-TrustAgent-Status as error:<kind> and in decide() request statuses. The kinds before
// KindTimeout mean no vendor request was made; SkipReason maps them to gate skip reasons.
const (
	KindOff                    = "off"
	KindBreakerOpen            = "breaker_open"
	KindRateLimited            = "rate_limited"
	KindOverload               = "overload"
	KindBudget                 = "budget"
	KindCredentialsUnavailable = "credentials_unavailable"
	KindTimeout                = "timeout"
	KindAuth                   = "auth"
	KindCredits                = "credits"
	KindDataPolicy             = "data_policy"
	KindNotFound               = "not_found"
	KindTooLarge               = "too_large"
	KindBadRequest             = "bad_request"
	KindRetryable              = "retryable"
	KindBadContentType         = "bad_content_type"
	KindBadResponse            = "bad_response"
	KindMockFixtureMissing     = "mock_fixture_missing"
)

// Error is a failed decision-model call.
type Error struct {
	Kind    string
	Status  int // HTTP status, 0 when there was none
	Message string
	Hint    string
	Err     error
	// Retryable is set for faults a retry may cure: 429, 5xx, network errors, attempt timeouts
	// and the transient 402 (OpenRouter in-flight budget).
	Retryable bool
	// RetryAfter is the vendor's Retry-After, capped at TRUTHTABLE_JEV_RETRY_AFTER_CAP.
	RetryAfter time.Duration
	// Attempts is the number of vendor requests the call made before failing.
	Attempts int
}

func (e *Error) Error() string {
	msg := "jev " + e.Kind
	if e.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
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

// SkipReason maps an error that stopped a call before any vendor request to the gate skip reason
// (SPEC §2.6 SkipReason): breaker_open, rate_limited, overload, budget, decision_model_off or
// credentials_unavailable. ok is false for every other error (a request status error:<kind>).
func SkipReason(err error) (reason string, ok bool) {
	switch ErrorKind(err) {
	case KindBreakerOpen:
		return "breaker_open", true
	case KindRateLimited:
		return "rate_limited", true
	case KindOverload:
		return "overload", true
	case KindBudget:
		return "budget", true
	case KindOff:
		return "decision_model_off", true
	case KindCredentialsUnavailable:
		return "credentials_unavailable", true
	}
	return "", false
}

// Info describes the resolved backend (the core of DecisionModelStatus).
type Info struct {
	Backend string
	// Reason explains how the backend was chosen, for status pages, doctor and jevcheck.
	Reason string
	// Model is the decision model requested when a route pins none.
	Model string
	// Endpoint is the decision endpoint URL of an HTTP backend ("" for mock and off).
	Endpoint string
	// CredentialSource is env, dashboard or none.
	CredentialSource string
	// Misconfigured is set when an explicitly chosen backend could not be built, so it runs off.
	Misconfigured bool
	// Warnings are non-fatal configuration notes (a latest model, an ignored base URL).
	Warnings []string
}

// Client is a decision-model backend.
type Client interface {
	Decide(ctx context.Context, req *Request, opt CallOptions) (*Response, error)
	Info() Info
}

// Optional capabilities of a Client. The HTTP backends implement all of them; the mock and off
// backends implement StatusReporter.
type (
	// StatusReporter reports the live backend state for GET /api/status and doctor.
	StatusReporter interface{ Status() Status }
	// KeepWarmer keeps the vendor connection warm while a synchronous route exists.
	KeepWarmer interface{ SetKeepWarm(active bool) }
	// CPTSource is the runtime chars-per-token estimate for compile().
	CPTSource interface{ CPT() float64 }
	// KeyChecker validates the credential without a paid call (OpenRouter GET /api/v1/key).
	KeyChecker interface {
		CheckKey(ctx context.Context) (*KeyInfo, error)
	}
)

// KeyFunc returns the current credential for an outbound call to host. The credentials store
// supplies it for dashboard keys and asserts the credential's allowed_hosts (SPEC §7.5); a changed
// key resets the breaker.
type KeyFunc func(ctx context.Context, host string) (string, error)

// Binding is a dashboard credential bound to the decision model (settings.bindings); auto
// resolution uses it when no environment credential applies.
type Binding struct {
	// Backend is the bound backend id (openrouter-decisions, openrouter-systemone, typesafe or
	// compatible).
	Backend string
	// Name names the credential in reasons and status.
	Name string
	// BaseURL is the compatible gateway's base URL (the host fixed at creation).
	BaseURL string
	Key     KeyFunc
}

// Config selects and configures the backend.
type Config struct {
	// Backend is TRUTHTABLE_JEV_BACKEND (auto by default).
	Backend string
	// Model is TRUTHTABLE_JEV_MODEL ("" = the backend default; required for compatible).
	Model string
	// DemoMode is TRUTHTABLE_DEMO_MODE: it forces the mock backend.
	DemoMode bool
	// OpenRouterKey is OPENROUTER_API_KEY, TypeSafeKey TYPESAFE_API_KEY and JevAPIKey
	// TRUTHTABLE_JEV_API_KEY, which applies only to an explicit backend or compatible.
	OpenRouterKey, TypeSafeKey, JevAPIKey string
	// BaseURL and Path are TRUTHTABLE_JEV_BASE_URL and _PATH: used for compatible, and for a
	// vendor backend chosen explicitly.
	BaseURL, Path string
	// OpenRouterBaseURL is TRUTHTABLE_OPENROUTER_BASE_URL (default https://openrouter.ai).
	OpenRouterBaseURL string
	// Binding is the dashboard credential bound to the decision model, if any.
	Binding *Binding
	Mock    MockConfig
	// HTTP configures the HTTP backends; nil uses DefaultHTTPConfig().
	HTTP *HTTPConfig
	// Costs receives one cost event per attempt (per call for the mock); nil discards them.
	Costs CostSink
	// Budget admits spend against the daily decision cap; nil builds a MemoryBudget from
	// HTTP.SpendCapUSDDay and HTTP.EnforcingReserve.
	Budget Budget
	// OnSnapshotChange is called when the snapshot answering a model differs from the last one
	// seen (or from KnownSnapshot for the default model), e.g. to broadcast
	// decision_model_changed.
	OnSnapshotChange func(SnapshotChange)
	// KnownSnapshot is the last snapshot recorded for the default model, if persisted.
	KnownSnapshot string
	Logger        *slog.Logger
}

// New resolves the backend (SPEC §3.1 auto resolution) and builds its client.
func New(cfg Config) (Client, error) {
	r := resolve(cfg)
	switch r.backend {
	case BackendMock:
		m, err := newMock(cfg.Mock, cfg.Model)
		if err != nil {
			return nil, err
		}
		m.info.Reason = r.reason
		m.info.CredentialSource = "none"
		m.costs = cfg.Costs
		if cfg.Mock.Mode == MockHeuristic {
			m.info.Reason += "; the heuristic mock mode is not implemented yet, so questions get hash answers"
		}
		return m, nil
	case BackendOff:
		return &offClient{info: Info{
			Backend: BackendOff, Reason: r.reason, Model: cfg.Model, CredentialSource: "none",
			Misconfigured: r.misconfigured, Warnings: r.warnings,
		}}, nil
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne, BackendTypeSafe, BackendCompatible:
		return newHTTPClient(cfg, r)
	}
	return nil, fmt.Errorf("jev: unknown backend %q", r.backend)
}

// resolution is the outcome of backend resolution.
type resolution struct {
	backend, reason string
	// explicit is set when TRUTHTABLE_JEV_BACKEND names the backend.
	explicit bool
	// key and keySource: the environment credential ("" when key comes from the binding).
	key, keySource string
	binding        *Binding
	model          string
	ep             endpoint
	misconfigured  bool
	warnings       []string
}

// Resolve reports the backend New would build and why, without building it (doctor, jevcheck).
func Resolve(cfg Config) Info {
	r := resolve(cfg)
	info := Info{Backend: r.backend, Reason: r.reason, Model: r.model, Endpoint: r.ep.url,
		Misconfigured: r.misconfigured, Warnings: r.warnings, CredentialSource: "none"}
	switch {
	case r.binding != nil:
		info.CredentialSource = "dashboard"
	case r.key != "":
		info.CredentialSource = "env"
	}
	if r.backend == BackendMock && info.Model == "" {
		info.Model = DefaultMockModel
	}
	return info
}

func resolve(cfg Config) resolution {
	if cfg.DemoMode {
		return resolution{backend: BackendMock, reason: "demo mode (TRUTHTABLE_DEMO_MODE) uses the mock decision model", model: cfg.Model}
	}
	want := strings.TrimSpace(cfg.Backend)
	if want == "" {
		want = BackendAuto
	}
	r := resolution{backend: want, explicit: want != BackendAuto}
	switch want {
	case BackendMock:
		r.reason, r.model = "TRUTHTABLE_JEV_BACKEND=mock", cfg.Model
		return r
	case BackendOff:
		r.reason = "TRUTHTABLE_JEV_BACKEND=off"
		return r
	case BackendAuto:
		if !autoPick(cfg, &r) {
			return r
		}
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne, BackendTypeSafe, BackendCompatible:
		if !explicitKey(cfg, &r) {
			return r
		}
	default:
		return offBecause(fmt.Sprintf("unknown TRUTHTABLE_JEV_BACKEND %q; use auto, %s", want, strings.Join(allBackends, ", ")), true)
	}

	r.model = cfg.Model
	if r.model == "" {
		switch r.backend {
		case BackendOpenRouterDecisions, BackendOpenRouterSystemOne:
			r.model = DefaultOpenRouterModel
		case BackendTypeSafe:
			r.model = DefaultTypeSafeModel
		default:
			return offBecause("the compatible backend needs TRUTHTABLE_JEV_MODEL (for Vercel AI Gateway: typesafe-ai/jev)", true)
		}
	}
	if strings.Contains(r.model, "latest") {
		r.warnings = append(r.warnings, fmt.Sprintf("TRUTHTABLE_JEV_MODEL=%s follows the newest snapshot; thresholds are tuned per version, so recalibrate after each change", r.model))
	}
	ep, warn, err := resolveEndpoint(r.backend, r.explicit, cfg, r.binding)
	if err != nil {
		return offBecause(err.Error(), true)
	}
	r.ep = ep
	r.warnings = append(r.warnings, warn...)
	return r
}

var allBackends = []string{BackendOpenRouterDecisions, BackendOpenRouterSystemOne, BackendTypeSafe, BackendCompatible, BackendMock, BackendOff}

func offBecause(reason string, misconfigured bool) resolution {
	return resolution{backend: BackendOff, reason: reason + "; fast checks are off", misconfigured: misconfigured}
}

// autoPick applies the auto order: BASE_URL+JEV_API_KEY → compatible; OpenRouter key →
// openrouter-decisions; TypeSafe key → typesafe; a dashboard binding; else off.
func autoPick(cfg Config, r *resolution) bool {
	switch {
	case cfg.BaseURL != "" && cfg.JevAPIKey != "":
		r.backend, r.key, r.keySource = BackendCompatible, cfg.JevAPIKey, "TRUTHTABLE_JEV_API_KEY"
		r.reason = "auto: TRUTHTABLE_JEV_BASE_URL and TRUTHTABLE_JEV_API_KEY are set, so the compatible backend calls that gateway"
		return true
	case cfg.OpenRouterKey != "":
		r.backend, r.key, r.keySource = BackendOpenRouterDecisions, cfg.OpenRouterKey, "OPENROUTER_API_KEY"
		r.reason = "auto: OPENROUTER_API_KEY is set, so fast checks use OpenRouter's Decisions endpoint"
	case cfg.TypeSafeKey != "":
		r.backend, r.key, r.keySource = BackendTypeSafe, cfg.TypeSafeKey, "TYPESAFE_API_KEY"
		r.reason = "auto: TYPESAFE_API_KEY is set (and no OpenRouter key), so fast checks call TypeSafe directly"
	case cfg.Binding != nil && cfg.Binding.Key != nil && isHTTPBackend(cfg.Binding.Backend):
		r.backend, r.binding = cfg.Binding.Backend, cfg.Binding
		r.reason = fmt.Sprintf("auto: the dashboard credential %q is bound to the decision model (%s)", cfg.Binding.Name, cfg.Binding.Backend)
		return true
	default:
		*r = offBecause("no decision-model credentials: set OPENROUTER_API_KEY, or TRUTHTABLE_JEV_BACKEND=mock for simulated checks", false)
		return false
	}
	if cfg.BaseURL != "" {
		r.warnings = append(r.warnings, "TRUTHTABLE_JEV_BASE_URL is ignored: auto uses it only together with TRUTHTABLE_JEV_API_KEY (the compatible backend)")
	}
	return true
}

// explicitKey picks the credential of an explicitly chosen HTTP backend: TRUTHTABLE_JEV_API_KEY,
// else the vendor key (OpenRouter or TypeSafe), else a binding for the same backend.
func explicitKey(cfg Config, r *resolution) bool {
	name := "TRUTHTABLE_JEV_BACKEND=" + r.backend
	vendorKey, vendorEnv := "", ""
	switch r.backend {
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne:
		vendorKey, vendorEnv = cfg.OpenRouterKey, "OPENROUTER_API_KEY"
	case BackendTypeSafe:
		vendorKey, vendorEnv = cfg.TypeSafeKey, "TYPESAFE_API_KEY"
	}
	switch {
	case cfg.JevAPIKey != "":
		r.key, r.keySource = cfg.JevAPIKey, "TRUTHTABLE_JEV_API_KEY"
	case vendorKey != "":
		r.key, r.keySource = vendorKey, vendorEnv
	case cfg.Binding != nil && cfg.Binding.Key != nil && cfg.Binding.Backend == r.backend:
		r.binding = cfg.Binding
		r.reason = fmt.Sprintf("%s with the dashboard credential %q", name, cfg.Binding.Name)
		return true
	default:
		need := "TRUTHTABLE_JEV_API_KEY"
		if vendorEnv != "" {
			need = vendorEnv + " or " + need
		}
		*r = offBecause(fmt.Sprintf("%s has no credential: set %s", name, need), true)
		return false
	}
	r.reason = fmt.Sprintf("%s with the key from %s", name, r.keySource)
	return true
}

func isHTTPBackend(b string) bool {
	switch b {
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne, BackendTypeSafe, BackendCompatible:
		return true
	}
	return false
}

// offClient is the off backend: every call fails with KindOff, and the gate skips it as
// decision_model_off without calling.
type offClient struct{ info Info }

func (o *offClient) Decide(context.Context, *Request, CallOptions) (*Response, error) {
	return nil, &Error{Kind: KindOff, Message: o.info.Reason, Hint: Hint(KindOff)}
}

func (o *offClient) Info() Info { return o.info }

func (o *offClient) Status() Status {
	state := "off"
	if o.info.Misconfigured {
		state = "misconfigured"
	}
	return Status{Info: o.info, State: state, Breaker: BreakerClosed}
}
