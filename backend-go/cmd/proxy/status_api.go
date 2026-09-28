package main

import (
	"context"
	"net/http"
	"net/url"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/version"
)

// Component states (ComponentStateName).
const (
	stateUp       = "up"
	stateDegraded = "degraded"
	stateDown     = "down"
	stateDisabled = "disabled"
	stateUnknown  = "unknown"
)

// dbStatus is the part of the store the status reads.
type dbStatus interface {
	Ping(ctx context.Context) error
	Migration(ctx context.Context) (string, error)
}

// engineHealth is the cached engine health (grpc.Pinger).
type engineHealth interface {
	Health() grpc.EngineHealth
}

// statusReporter answers GET /health, GET /api/status and the WS hello status summary from
// configuration and cached component state. Nothing here calls the engine: the pinger does (M4).
type statusReporter struct {
	cfg    *config.Config
	engine engineHealth // nil when no audit engine is configured
	db     dbStatus     // nil when persistence is disabled
	jev    jev.Info
	// jevClient reports the live decision-model state (jev.StatusReporter); nil falls back to jev.
	jevClient jev.Client
	policy    *policy.Policy
	auth      *apiAuth
	// redisState is the rate limiter's backend state at startup (up, down or disabled).
	redisState string
	now        func() time.Time
}

// healthStatus is api.ts HealthStatus (GET /health).
type healthStatus struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	Demo        bool   `json:"demo"`
	AuditEngine bool   `json:"audit_engine"`
}

// handleHealth serves GET /health: public liveness with the cached engine ping.
func (s *statusReporter) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, healthStatus{
		Status: "ok", Version: version.Version, Demo: s.cfg.DemoMode, AuditEngine: s.engineHealth().Reachable(),
	})
}

func (s *statusReporter) engineHealth() grpc.EngineHealth {
	if s.engine == nil {
		return grpc.EngineHealth{State: stateDisabled, Detail: "no audit engine is configured (TRUTHTABLE_GRPC_ADDRESS)"}
	}
	return s.engine.Health()
}

// componentState is api.ts ComponentState.
type componentState struct {
	State     string     `json:"state"`
	Detail    string     `json:"detail,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	LatencyMs *int64     `json:"latency_ms,omitempty"`
}

type statusAuth struct {
	Ingest        string   `json:"ingest"`
	Read          string   `json:"read"`
	SetupRequired bool     `json:"setup_required"`
	CallerScopes  []string `json:"caller_scopes"`
}

type statusLimits struct {
	MaxTextChars   int   `json:"max_text_chars"`
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	MaxBodyBytes   int64 `json:"max_body_bytes"`
}

type statusPersistence struct {
	componentState
	Configured bool    `json:"configured"`
	Migration  *string `json:"migration"`
}

type statusEngine struct {
	componentState
	Version              string `json:"version,omitempty"`
	RuntimeConfigVersion *int64 `json:"runtime_config_version,omitempty"`
	MaxAudits            *int32 `json:"max_audits,omitempty"`
}

type statusJudge struct {
	State            string   `json:"state"`
	Provider         *string  `json:"provider"`
	Model            *string  `json:"model"`
	CredentialSource string   `json:"credential_source"`
	FallbackActive   bool     `json:"fallback_active"`
	FallbackReason   *string  `json:"fallback_reason"`
	Warnings         []string `json:"warnings"`
}

type statusVerifier struct {
	Configured string `json:"configured"`
	Effective  string `json:"effective"`
	Reason     string `json:"reason"`
}

type decisionCredential struct {
	Source string  `json:"source"`
	Name   *string `json:"name"`
	Masked *string `json:"masked"`
}

// decisionModelError is DecisionModelStatus.last_error.
type decisionModelError struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Hint    string    `json:"hint"`
	At      time.Time `json:"at"`
}

// decisionModelStatus is api.ts DecisionModelStatus.
type decisionModelStatus struct {
	Provider          string              `json:"provider"`
	Backend           string              `json:"backend"`
	BackendReason     string              `json:"backend_reason"`
	EndpointHost      *string             `json:"endpoint_host"`
	ModelRequested    *string             `json:"model_requested"`
	Snapshot          *string             `json:"snapshot"`
	Credential        decisionCredential  `json:"credential"`
	State             string              `json:"state"`
	Breaker           string              `json:"breaker"`
	LastError         *decisionModelError `json:"last_error"`
	CalibrationState  string              `json:"calibration_state"`
	CalibratedTargets int                 `json:"calibrated_targets"`
	P50Ms             *float64            `json:"p50_ms"`
	P95Ms             *float64            `json:"p95_ms"`
	Calls24h          int                 `json:"calls_24h"`
	Cost24hUSD        float64             `json:"cost_24h_usd"`
	RPMLimit          int                 `json:"rpm_limit"`
	RPMUsed           int                 `json:"rpm_used"`
}

type statusKnowledge struct {
	componentState
	Enabled        bool   `json:"enabled"`
	KBClaims       *int64 `json:"kb_claims,omitempty"`
	EmbeddingModel string `json:"embedding_model,omitempty"`
}

type statusPolicy struct {
	Version    *int64  `json:"version"`
	Source     string  `json:"source"`
	Locked     bool    `json:"locked"`
	Persisted  bool    `json:"persisted"`
	Editable   bool    `json:"editable"`
	FileStatus string  `json:"file_status"`
	FileError  *string `json:"file_error"`
}

type statusBudgets struct {
	DecisionUSDDay        float64 `json:"decision_usd_day"`
	DecisionSpentTodayUSD float64 `json:"decision_spent_today_usd"`
	LLMUSDDay             float64 `json:"llm_usd_day"`
	LLMSpentTodayUSD      float64 `json:"llm_spent_today_usd"`
	DecisionExhausted     bool    `json:"decision_exhausted"`
	DecisionReserveOnly   bool    `json:"decision_reserve_only"`
	LLMExhausted          bool    `json:"llm_exhausted"`
}

type statusCredentials struct {
	Store         string   `json:"store"`
	Undecryptable []string `json:"undecryptable"`
}

type statusRedis struct {
	componentState
	Configured bool `json:"configured"`
}

// systemStatus is api.ts SystemStatus (GET /api/status).
type systemStatus struct {
	Version       string              `json:"version"`
	BuildSHA      string              `json:"build_sha"`
	Demo          bool                `json:"demo"`
	Readonly      bool                `json:"readonly"`
	TestMode      bool                `json:"test_mode"`
	Auth          statusAuth          `json:"auth"`
	InternalRPC   string              `json:"internal_rpc"`
	Limits        statusLimits        `json:"limits"`
	Persistence   statusPersistence   `json:"persistence"`
	Engine        statusEngine        `json:"engine"`
	Judge         statusJudge         `json:"judge"`
	Verifier      statusVerifier      `json:"verifier"`
	DecisionModel decisionModelStatus `json:"decision_model"`
	Knowledge     statusKnowledge     `json:"knowledge"`
	Policy        statusPolicy        `json:"policy"`
	Budgets       statusBudgets       `json:"budgets"`
	Credentials   statusCredentials   `json:"credentials"`
	Redis         statusRedis         `json:"redis"`
	Warnings      []string            `json:"warnings"`
}

// statusLite is api.ts SystemStatusLite (the WS hello summary).
type statusLite struct {
	Version       string `json:"version"`
	Demo          bool   `json:"demo"`
	Readonly      bool   `json:"readonly"`
	Engine        string `json:"engine"`
	DecisionModel string `json:"decision_model"`
	Judge         string `json:"judge"`
}

// handleStatus serves GET /api/status.
func (s *statusReporter) handleStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	st := s.system(ctx)
	st.Auth.CallerScopes = callerScopes(callerIdentity(c))
	c.JSON(http.StatusOK, st)
}

// lite is the hello status summary; it reads cached state only.
func (s *statusReporter) lite() statusLite {
	eng := s.engineHealth()
	return statusLite{
		Version: version.Version, Demo: s.cfg.DemoMode, Readonly: s.cfg.Readonly,
		Engine: eng.State, DecisionModel: s.decisionModel().State, Judge: judgeState(eng),
	}
}

// system builds the full status. Spend figures are zero until the cost ledger lands (P2), and the
// decision model's call statistics until the Jev client tracks them (P2).
func (s *statusReporter) system(ctx context.Context) systemStatus {
	cfg := s.cfg
	eng := s.engineHealth()
	st := systemStatus{
		Version: version.Version, BuildSHA: buildSHA(), Demo: cfg.DemoMode, Readonly: cfg.Readonly, TestMode: cfg.TestMode,
		Auth: statusAuth{Ingest: s.auth.keys.IngestMode(), Read: "keys", CallerScopes: []string{}},
		// Without a token the engine accepts only loopback callers (SPEC §2.2).
		InternalRPC: "loopback",
		Limits: statusLimits{
			MaxTextChars: cfg.MaxTextChars, MaxUploadBytes: cfg.MaxUploadBytes, MaxBodyBytes: cfg.MaxBodyBytes,
		},
		Persistence:   s.persistence(ctx),
		Engine:        statusEngine{componentState: engineComponent(eng)},
		Judge:         judgeStatus(eng),
		Verifier:      verifierStatus(eng),
		DecisionModel: s.decisionModel(),
		Knowledge:     knowledgeStatus(eng),
		Policy:        s.policyStatus(),
		Budgets:       s.budgets(),
		Credentials:   statusCredentials{Store: "unavailable", Undecryptable: []string{}},
		Redis:         statusRedis{componentState: componentState{State: s.redisState}, Configured: cfg.RedisURL != ""},
		Warnings:      append([]string{}, cfg.Warnings...),
	}
	if s.auth.publicRead {
		st.Auth.Read = "public"
	}
	if cfg.InternalToken != "" {
		st.InternalRPC = "token"
	}
	if s.db == nil {
		st.Credentials.Store = "no_database"
	}
	if r := eng.Response; r != nil {
		st.Engine.Version = r.GetVersion()
		if v := r.GetRuntimeConfigVersion(); v != 0 {
			st.Engine.RuntimeConfigVersion = &v
		}
	}
	if info := eng.Info; info != nil {
		if n := info.GetMaxAudits(); n > 0 {
			st.Engine.MaxAudits = &n
		}
		// The engine's own configuration notes (a judge fallback, an unknown key, a missing extra).
		for _, w := range info.GetWarnings() {
			st.Warnings = append(st.Warnings, "Engine: "+w)
		}
	}
	return st
}

func (s *statusReporter) persistence(ctx context.Context) statusPersistence {
	p := statusPersistence{Configured: s.cfg.DatabaseURL != ""}
	switch {
	case s.db == nil && p.Configured:
		p.State, p.Detail = stateDown, "the database could not be opened at startup; traces are not stored"
	case s.db == nil:
		p.State, p.Detail = stateDisabled, "TRUTHTABLE_DATABASE_URL is not set; traces are not stored"
	default:
		start := s.now()
		err := s.db.Ping(ctx)
		checked, ms := s.now().UTC(), s.now().Sub(start).Milliseconds()
		p.CheckedAt, p.LatencyMs = &checked, &ms
		if err != nil {
			p.State, p.Detail = stateDown, "the database does not answer"
			break
		}
		p.State = stateUp
		if m, err := s.db.Migration(ctx); err == nil && m != "" {
			p.Migration = &m
		}
	}
	return p
}

func engineComponent(h grpc.EngineHealth) componentState {
	cs := componentState{State: h.State, Detail: h.Detail}
	if !h.CheckedAt.IsZero() {
		at, ms := h.CheckedAt, h.LatencyMs
		cs.CheckedAt, cs.LatencyMs = &at, &ms
	}
	return cs
}

// judgeState is the deep-audit judge's state as the engine reports it. judge_provider "none" means
// the engine runs without a judge; an engine that leaves judge_provider empty (it does not name its
// judge, like the S0 engine) is judged by its judge dependency alone.
func judgeState(h grpc.EngineHealth) string {
	r := h.Response
	switch {
	case h.State == stateDisabled:
		return stateDisabled
	case r == nil:
		return stateUnknown
	case r.GetJudgeProvider() == "none":
		return stateDisabled
	}
	ok, known := r.GetDependencies()["judge"]
	switch {
	case !known:
		return stateUnknown
	case !ok:
		return stateDown
	case r.GetJudgeFallbackActive():
		return stateDegraded
	}
	return stateUp
}

// judgeStatus is SystemStatus.judge. Judge credentials come only from the engine's environment
// until dashboard credentials arrive (P4), so a cloud judge's source is env.
func judgeStatus(h grpc.EngineHealth) statusJudge {
	j := statusJudge{State: judgeState(h), CredentialSource: "none", Warnings: []string{}}
	r := h.Response
	if r == nil {
		return j
	}
	j.Provider, j.Model = nonEmpty(r.GetJudgeProvider()), nonEmpty(r.GetJudgeModel())
	j.FallbackActive = r.GetJudgeFallbackActive()
	switch r.GetJudgeProvider() {
	case "anthropic", "openai", "openrouter":
		j.CredentialSource = "env"
	}
	if j.FallbackActive {
		reason := "the configured judge is unavailable; the engine uses its fallback judge"
		j.FallbackReason = &reason
	}
	// GetEngineInfo says how the judge was chosen: where its key came from and why a fallback runs.
	if ji := h.Info.GetJudge(); ji != nil {
		if src := ji.GetCredentialSource(); src != "" {
			j.CredentialSource = src
		}
		if why := ji.GetFallbackReason(); why != "" && j.FallbackActive {
			j.FallbackReason = &why
		}
	}
	return j
}

// verifierStatus is SystemStatus.verifier: 2.0 verifies claims with the LLM judge only.
func verifierStatus(h grpc.EngineHealth) statusVerifier {
	v := statusVerifier{Configured: "auto", Effective: "llm", Reason: "deep audits verify claims with the LLM judge"}
	if c := h.Info.GetVerifierConfigured(); c != "" {
		v.Configured = c
	}
	if st := judgeState(h); st == stateDown || st == stateDisabled {
		v.Effective, v.Reason = "off", "no deep-audit judge is available"
	} else if why := h.Info.GetVerifierReason(); why != "" && h.Info.GetVerifierEffective() == "llm" {
		v.Reason = why
	}
	return v
}

func knowledgeStatus(h grpc.EngineHealth) statusKnowledge {
	k := statusKnowledge{componentState: componentState{State: stateDisabled}}
	r := h.Response
	if r == nil || !r.GetKnowledgeEnabled() {
		return k
	}
	k.Enabled = true
	k.State = stateUp
	k.EmbeddingModel = h.Info.GetKnowledge().GetEmbeddingModel()
	if ok, known := r.GetDependencies()["qdrant"]; known && !ok {
		k.State, k.Detail = stateDown, "the engine cannot reach its Qdrant store"
	}
	n := r.GetKbClaims()
	k.KBClaims = &n
	return k
}

// jevStatus is the live decision-model state, or the resolved backend alone when the client
// cannot report it.
func (s *statusReporter) jevStatus() jev.Status {
	if r, ok := s.jevClient.(jev.StatusReporter); ok {
		return r.Status()
	}
	st := jev.Status{Info: s.jev, State: "off", Breaker: jev.BreakerClosed}
	if s.jev.Backend == jev.BackendMock {
		st.State = "mock"
	}
	return st
}

// decisionModel is SystemStatus.decision_model, from the client's live status: state, breaker,
// last error with its hint, snapshot, rate use and the endpoint's host. Latency percentiles and
// 24-hour totals come from the cost ledger (P4); until then calls and cost count since start.
func (s *statusReporter) decisionModel() decisionModelStatus {
	st := s.jevStatus()
	d := decisionModelStatus{
		Provider: "jev", Backend: st.Backend, BackendReason: st.Reason, ModelRequested: nonEmpty(st.Model),
		Snapshot: nonEmpty(st.Snapshot), Credential: decisionCredential{Source: defaultString(st.CredentialSource, "none")},
		State: st.State, Breaker: defaultString(st.Breaker, jev.BreakerClosed), CalibrationState: "none",
		Calls24h: int(st.Calls), Cost24hUSD: st.CostUSD, RPMLimit: st.RPMLimit, RPMUsed: st.RPMUsed,
	}
	if d.State == "" {
		d.State = "off"
	}
	if st.Endpoint != "" {
		if u, err := url.Parse(st.Endpoint); err == nil && u.Hostname() != "" {
			d.EndpointHost = nonEmpty(u.Hostname())
		}
	}
	if e := st.LastError; e != nil {
		d.LastError = &decisionModelError{Kind: e.Kind, Message: e.Message, Hint: e.Hint, At: e.At.UTC()}
	}
	return d
}

// budgets is SystemStatus.budgets: the decision cap and today's spend from the decision-model
// client's budget (in memory until the persisted ledger, P4); the LLM cap is enforced by the
// engine, so its spend reads 0 here until the ledger lands.
func (s *statusReporter) budgets() statusBudgets {
	b := statusBudgets{DecisionUSDDay: s.cfg.SpendCapDecisionUSDDay, LLMUSDDay: s.cfg.SpendCapLLMUSDDay}
	if sp := s.jevStatus().Spend; sp != nil {
		b.DecisionSpentTodayUSD = sp.SpentUSD
		b.DecisionExhausted = sp.Exhausted
		b.DecisionReserveOnly = !sp.Exhausted && sp.SoftCapUSD > 0 && sp.SpentUSD+sp.ReservedUSD >= sp.SoftCapUSD
	}
	return b
}

func (s *statusReporter) policyStatus() statusPolicy {
	p := statusPolicy{Source: s.policy.Source, FileStatus: "ok"}
	v := s.policy.Version
	p.Version = &v
	if s.policy.Source == policy.SourceBuiltin {
		p.FileStatus = "missing"
	}
	return p
}

var buildSHAOnce = sync.OnceValue(func() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value
			}
		}
	}
	return "unknown"
})

func buildSHA() string { return buildSHAOnce() }

// engineStatusEvent is the data of the WS status event for an engine transition.
func engineStatusEvent(h grpc.EngineHealth) map[string]any {
	ev := map[string]any{"component": "engine", "state": h.State}
	if h.Detail != "" {
		ev["detail"] = h.Detail
	}
	return ev
}

// defaultString is s, or def when s is empty.
func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
