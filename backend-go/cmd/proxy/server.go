package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/middleware"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/proxy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/version"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/backend-go/internal/worker"
)

// headerScenario is the test-mode scenario header (SPEC §2.12), honoured on POST /api/audit too.
const headerScenario = proxy.HeaderScenario

// scenarioPattern is the shape of a scenario id (contracts/schema/scenarios.v1.schema.json).
var scenarioPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// serverDeps are the external components of a server; nil ones are disabled.
type serverDeps struct {
	cfg *config.Config
	// store persists traces and audits; nil disables persistence.
	store store.Store
	// engine is the audit engine client; nil disables deep audits.
	engine *grpc.AuditClient
	// limiter backs rate limiting; redisState is its backend state for /api/status.
	limiter    middleware.Limiter
	redisState string
	log        *slog.Logger
	// master signs dashboard sessions; nil means an ephemeral key (sessions end at restart).
	master *authz.MasterKey
	// pg is the Postgres store behind store, nil without a database: policy versions, the
	// evaluator tables, the dry-run replay and the overview aggregates need it. Without it the
	// policy is file-managed.
	pg *store.PostgresStore
	// redactor masks every loaded secret in logs and errors (config.Redactor); nil in tests that
	// do not check redaction. Received and decrypted secrets join it (P4-PLAN D42).
	redactor *config.Redactor
}

// apiGuards are the middleware of the policy, evaluator, P3 and P4 routes.
type apiGuards struct {
	// read, content and admin authenticate callers with those scopes; writable refuses writes in
	// read-only mode; rateLimit is the general per-caller limit; dryRunLimit is the policy
	// dry-run limit (6/min), used instead of rateLimit; bodyLimit caps request bodies.
	read, content, admin, writable, rateLimit, dryRunLimit, bodyLimit gin.HandlerFunc
	// limitBy builds an endpoint's own per-caller limit of n requests per window, used instead
	// of rateLimit (the Playground's 10/min).
	limitBy func(n int, window time.Duration) gin.HandlerFunc
}

// costHooks are the cost ledger's hooks into the gateway, the audit pool, Studio and the engine
// push (P4, GCOST fills them in initCosts). The zero value is no ledger: every field is optional
// and the methods below are the nil-safe way to call the function fields.
type costHooks struct {
	// decision receives the decision model's cost events; budget admits decision spend against
	// the daily cap; prices resolves computed decision prices (nil: in-memory budget, embedded
	// prices, events discarded).
	decision jev.CostSink
	budget   jev.Budget
	prices   jev.PriceFunc
	// knownSnapshot is the last decision-model snapshot recorded; uniformSeed today's persisted
	// uniform-sample count.
	knownSnapshot string
	uniformSeed   int
	// reserveLLM reserves usd of the LLM cap for component before an LLM call and returns the
	// settle function for the actual cost; an error means the cap is exhausted (D48).
	reserveLLM func(ctx context.Context, usd float64, component string) (settle func(actual float64), err error)
	// recordLLM books the engine's cost records (deep audits, the Studio drafter, ingest).
	recordLLM func(ctx context.Context, records []*pb.CostRecord, link CostLink)
	// effectivePriceJSON is the effective price table pushed to the engine (D53).
	effectivePriceJSON func(overrides []decision.PriceEntry) []byte
	// decisionHeadroom is the decision spend still allowed today: up to the observe line when
	// enforcing is false, up to the cap when true (D82).
	decisionHeadroom func(enforcing bool) float64
}

// reserve reserves LLM spend (reserveLLM); without a ledger nothing is reserved.
func (h costHooks) reserve(ctx context.Context, usd float64, component string) (func(actual float64), error) {
	if h.reserveLLM == nil {
		return func(float64) {}, nil
	}
	return h.reserveLLM(ctx, usd, component)
}

// record books LLM cost records (recordLLM); without a ledger they are dropped.
func (h costHooks) record(ctx context.Context, records []*pb.CostRecord, link CostLink) {
	if h.recordLLM != nil {
		h.recordLLM(ctx, records, link)
	}
}

// priceJSON is the effective price table for the engine; nil without a ledger (the engine keeps
// its own table).
func (h costHooks) priceJSON(overrides []decision.PriceEntry) []byte {
	if h.effectivePriceJSON == nil {
		return nil
	}
	return h.effectivePriceJSON(overrides)
}

// headroom is the decision spend still allowed today; without a ledger it is unlimited.
func (h costHooks) headroom(enforcing bool) float64 {
	if h.decisionHeadroom == nil {
		return math.Inf(1)
	}
	return h.decisionHeadroom(enforcing)
}

// server is the proxy: the /v1 gateway, the REST API, the WS hub and the deep-audit workers.
type server struct {
	cfg     *config.Config
	log     *slog.Logger
	router  *gin.Engine
	gateway *gatewayParts
	hub     *websocket.Hub
	pool    *worker.Pool
	pinger  *grpc.Pinger // nil without an engine
	status  *statusReporter
	auth    *apiAuth
	// policies are the policy engine and the evaluator registry (hot reload, REST APIs).
	policies *policyServices
	// stats serves the overview, ROI and evidence-coverage aggregates; nil without a database.
	stats statsStore
	// pg is the Postgres store (serverDeps.pg), nil without a database.
	pg *store.PostgresStore
	// calib is the calibration lookup of the gate and the policy readiness checks
	// (decision_wiring.go); waiters wake the /v1 decision long-polls (decisions_longpoll_api.go).
	calib   policy.CalibrationLookup
	waiters *decisionWaiters

	// P4 seams (docs/v2/P4-PLAN.md §2.3a). redactor masks secrets (nil: identity); engine is the
	// audit engine client (nil without one); notify fans config notifications out (D84).
	redactor *config.Redactor
	engine   *grpc.AuditClient
	notify   *notifyHub
	// rt is the runtime configuration (runtime_wiring.go), apply its hot apply
	// (apply_wiring.go), costs the cost ledger's hooks (costs_wiring.go), eval the evaluator,
	// label and calibration services (eval_wiring.go).
	rt    *runtimeParts
	apply *applyParts
	costs costHooks
	eval  *evalParts
	// sink records traces and decisions (sink.go); the P4 init steps may wire hooks into it.
	sink *traceSink
	// traceBarrier flushes a trace's pending writes before a later read or write of it (D83);
	// nil while trace writes are inline (trace_writer.go sets it).
	traceBarrier func(ctx context.Context, traceID string) error

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// redact masks secrets in text that leaves the process (D42): the identity while there is no
// redactor.
func (s *server) redact(text string) string {
	if s.redactor == nil {
		return text
	}
	return s.redactor.Redact(text)
}

// barrier runs traceBarrier when it is set (D83).
func (s *server) barrier(ctx context.Context, traceID string) error {
	if s.traceBarrier == nil {
		return nil
	}
	return s.traceBarrier(ctx, traceID)
}

// newServer wires the components and the routes. start runs the background loops.
func newServer(d serverDeps) (*server, error) {
	cfg, log := d.cfg, d.log
	if log == nil {
		log = slog.Default()
	}
	s := &server{cfg: cfg, log: log, pg: d.pg, redactor: d.redactor, engine: d.engine}

	s.status = &statusReporter{cfg: cfg, redisState: d.redisState, now: time.Now}
	if s.status.redisState == "" {
		s.status.redisState = stateDisabled
	}
	if d.store != nil {
		s.status.db = d.store
	}
	auth := newAuthenticator(cfg, d.store, d.master, log)
	s.hub = websocket.NewHub(websocket.Config{
		Version: version.Version, ReplayEvents: cfg.WS.ReplayBuffer, ReplayMaxBytes: cfg.WS.ReplayMaxBytes,
		MaxClients: cfg.WS.MaxClients, MaxPerIP: cfg.WS.MaxPerIP, Heartbeat: cfg.WS.Heartbeat,
		// The dashboard's own origin (TRUTHTABLE_PUBLIC_URL) is allowed as in the CSRF check.
		AllowedOrigins: cfg.AllowedOrigins, OriginAllowed: auth.AllowsOrigin, Logger: log,
		Hello: func() websocket.HelloInfo {
			return websocket.HelloInfo{Demo: cfg.DemoMode, Readonly: cfg.Readonly, Status: s.status.lite()}
		},
	})

	poolCfg := worker.Config{
		Workers: cfg.WorkerCount, LowQueue: cfg.QueueSize, HighQueue: cfg.AuditQueueHigh, HighPerMin: cfg.AuditHighPerMin,
		Publisher: s.hub, TextRetention: cfg.BodyRetention, Logger: log,
	}
	if d.store != nil {
		poolCfg.Store = d.store
	}
	if d.engine != nil {
		poolCfg.Engine = d.engine
		s.pinger = grpc.NewPinger(d.engine, cfg.EngineHealthInterval, s.engineChanged)
		s.status.engine = s.pinger
	}
	s.pool = worker.New(poolCfg)

	s.waiters = newDecisionWaiters()
	sink := &traceSink{
		pub: s.hub, audits: s.pool, bodyRetention: cfg.BodyRetention, bodyMaxBytes: cfg.BodyMaxBytes,
		now: time.Now, log: log, waiters: s.waiters,
	}
	if d.store != nil {
		sink.store = d.store
	}
	s.sink = sink
	// The calibration lookup comes before the policy services: block readiness reads it.
	s.initCalibration()
	// P4: the notification hub, the runtime configuration and the cost ledger come before the
	// policy services (the hub carries their notifications; the gateway reads the other two).
	s.notify = newNotifyHub()
	s.initRuntime(d)
	s.initCosts(d)
	// The policy services come first: the gateway reads the policy in effect and the evaluator
	// registry from them. newServer has no context, so loading is bounded here.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ps, err := newPolicyServices(ctx, policyWiring{
		cfg: cfg, pg: d.pg, hub: s.hub, runtime: s.policyRuntime, notify: s.notify, log: log,
		// Block and fallback stay refused until the /v1 handler enforces them (v1_routes.go).
		enforcing: gatewayEnforces,
		// A new policy version turns the decision model's keep-warm on or off (the gateway sets it
		// once at startup).
		onPolicy: s.policyChanged,
	})
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	s.policies = ps
	s.status.policies = ps.Engine
	s.status.evaluatorWarnings = ps.Registry.Warnings // excluded versions (D54)
	s.initEval(d)

	gw, err := newGateway(cfg, sink, auth, ps, gatewayHooks{
		snapshotChanged: s.snapshotChanged, calibrations: s.calib,
		runtime: s.rt.manager, costs: s.costs, retriever: s.knowledgeRetriever(d), redact: s.redact,
	}, log)
	if err != nil {
		return nil, err
	}
	s.gateway = gw
	s.initApply(d)
	s.status.jev, s.status.jevClient = gw.jev, gw.client
	s.auth = &apiAuth{keys: gw.auth, publicRead: cfg.PublicRead, demo: cfg.DemoMode}
	s.status.auth = s.auth
	if d.pg != nil {
		s.stats, s.status.figures = d.pg, d.pg
	}

	traces := &tracesAPI{now: time.Now, log: log, labels: s.traceLabels}
	audits := &auditsAPI{audits: s.pool, pub: s.hub, maxTextChars: cfg.MaxTextChars, testMode: cfg.TestMode, log: log}
	if d.store != nil {
		traces.store, audits.store = d.store, d.store
	}
	s.router = s.routes(d, traces, audits)
	return s, nil
}

// routes builds the router: /health, /api, /ws and the /v1 gateway.
func (s *server) routes(d serverDeps, traces *tracesAPI, audits *auditsAPI) *gin.Engine {
	cfg := s.cfg
	r := gin.New()
	// Client IPs (rate limits, the WS per-IP cap) come from the connection, or from X-Forwarded-For
	// only when the connection is from TRUTHTABLE_TRUSTED_PROXIES (the dashboard's nginx); by
	// default no proxy is trusted.
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		s.log.Warn("TRUTHTABLE_TRUSTED_PROXIES is not usable; trusting no proxy", "err", err)
		_ = r.SetTrustedProxies(nil)
	}
	r.Use(gin.Recovery(), requestLog(s.log), middleware.CORS(cfg.AllowedOrigins))
	if cfg.WriteTimeout == 0 {
		// TRUTHTABLE_WRITE_TIMEOUT=0: the server has no write timeout; each route family gets
		// its own deadline instead (SPEC M12).
		ingest := time.Duration(0)
		if d.engine != nil {
			ingest = d.engine.IngestTimeout()
		}
		r.Use(middleware.WriteDeadline(func(c *gin.Context) time.Duration { return writeDeadline(cfg, c.Request.URL.Path, ingest) }))
	}

	limiter := d.limiter
	if limiter == nil {
		limiter = middleware.NewMemoryLimiter()
	}
	// /api answers 429 as an ApiErrorBody, /v1 in the upstream API's error shape (SPEC §2.12).
	apiDeny := func(c *gin.Context, status int, code, message string) { abortAPI(c, status, code, message) }
	// Buckets are per key id, or per session id for cookie auth, else per IP (SPEC §2.4, M8): the
	// limiters run after the authentication middleware, which stores the identity.
	rateLimitBy := func(limit int, window time.Duration) gin.HandlerFunc {
		return middleware.RateLimitBy(limiter, limit, window, apiDeny, rateKey)
	}
	rateLimit := rateLimitBy(cfg.RateLimitPerMinute, time.Minute)
	uploadRateLimit := rateLimitBy(cfg.UploadLimitPerMin, time.Minute)
	v1RateLimit := middleware.RateLimitBy(limiter, cfg.RateLimitPerMinute, time.Minute, proxy.Deny, rateKey)
	bodyLimit := middleware.BodyLimit(cfg.MaxBodyBytes)
	read, ingest := s.auth.require(authz.ScopeRead), s.auth.require(authz.ScopeIngest)
	writable := s.writable()

	r.GET("/health", s.status.handleHealth)

	api := r.Group("/api")
	api.GET("/status", read, rateLimit, s.status.handleStatus)
	api.GET("/traces", read, rateLimit, traces.list)
	api.GET("/traces/:id", read, rateLimit, traces.get)
	api.GET("/audits", read, rateLimit, audits.list)
	api.GET("/audits/:id", read, rateLimit, audits.get)
	api.POST("/audit", writable, ingest, rateLimit, bodyLimit, audits.direct)
	// Knowledge base (SPEC §2.4 rows 62-65): 409 knowledge_disabled while the add-on is off.
	knowledge := s.knowledgeOn(d.engine != nil)
	upload := s.handleFileUpload(cfg.MaxUploadBytes)
	uploadLimit := middleware.BodyLimit(cfg.MaxUploadBytes)
	api.POST("/kb/documents", writable, s.auth.require(authz.ScopeAdmin), uploadRateLimit, knowledge, uploadLimit, upload)
	api.GET("/kb/claims", read, rateLimit, knowledge, handleListKBClaims(d.engine))
	api.GET("/kb/conflicts", read, rateLimit, knowledge, handleListConflicts(d.engine))
	api.GET("/kb/stats", read, rateLimit, knowledge, handleKBStats(d.engine))
	// P2 authz core: setup, session, ws-ticket and keys endpoints (setup_api.go, session_api.go, keys_api.go).
	s.mountAuthz(api, rateLimit, bodyLimit, writable)
	// Policies and evaluators (policies_api.go, evaluators_api.go). The dry-run has only its own
	// limit: RateLimit keys on the route path, so the general limit would count it twice. limitBy
	// builds an endpoint's own per-caller limit.
	guards := apiGuards{read: read, content: s.auth.require(authz.ScopeContent), admin: s.auth.require(authz.ScopeAdmin), writable: writable,
		rateLimit: rateLimit, dryRunLimit: rateLimitBy(6, time.Minute), bodyLimit: bodyLimit, limitBy: rateLimitBy}
	s.policies.mount(api, guards)
	// Overview, ROI and evidence coverage (stats_api.go).
	s.mountStats(api, read, rateLimit)
	// The P3 REST endpoints (p3_api.go).
	s.mountP3API(r, api, guards)
	// The P4 REST endpoints, one mount per owner: settings (runtime_wiring.go), credentials and
	// Connect OpenRouter (apply_wiring.go), ROI time series (costs_wiring.go), evaluator writes,
	// labels and calibration (eval_wiring.go), Studio, the decision request and evidence
	// suggestions (studio_api.go).
	s.mountSecurityAPI(api, guards)
	s.mountConnectAPI(api, guards)
	s.mountCostAPI(api, guards)
	s.mountEvalAPI(api, guards)
	s.mountStudioAPI(api, guards)

	r.GET("/ws", s.serveWS)

	// LLM API (SPEC §2.12): the /v1 gateway (v1_routes.go).
	s.mountV1(r, v1RateLimit, bodyLimit)

	// Unknown paths answer in the error shape of their family, not gin's text/plain 404. An unknown
	// /v1 path carries the trace and request ids like every /v1 response (SPEC §2.12: "Always").
	v1TraceIDs := s.gateway.handler.TraceIDs()
	r.NoRoute(func(c *gin.Context) {
		if isV1Path(c.Request.URL.Path) {
			v1TraceIDs(c) // sets the ids, then runs the next handler
			return
		}
		c.Next()
	}, func(c *gin.Context) {
		if p := c.Request.URL.Path; isV1Path(p) {
			proxy.Deny(c, http.StatusNotFound, codeNotFound, "TrustAgent does not serve "+c.Request.Method+" "+p+".")
			c.Abort()
			return
		}
		abortAPI(c, http.StatusNotFound, codeNotFound, "No endpoint here: "+c.Request.Method+" "+c.Request.URL.Path+".")
	})
	return r
}

// isV1Path reports whether path belongs to the LLM API (/v1 and its sub-paths).
func isV1Path(path string) bool { return path == "/v1" || strings.HasPrefix(path, "/v1/") }

// apiWriteDeadline is the write deadline of /api, /health and unknown paths when
// TRUTHTABLE_WRITE_TIMEOUT=0 (SPEC M12).
const apiWriteDeadline = 60 * time.Second

// writeDeadline is the per-route write deadline of path (SPEC M12): /v1 the upstream timeout plus
// 30 s (the gate and the trace write follow the answer; none when the upstream timeout is off),
// the knowledge upload the ingest call's bound plus 30 s, /ws none (the hub sets a deadline per
// frame), everything else 60 s. Handlers that stream longer extend it with
// http.ResponseController. 0 means no deadline.
func writeDeadline(cfg *config.Config, path string, ingest time.Duration) time.Duration {
	switch {
	case path == "/ws":
		return 0
	case isV1Path(path):
		if cfg.UpstreamTimeout <= 0 {
			return 0
		}
		return cfg.UpstreamTimeout + 30*time.Second
	case path == "/api/kb/documents" && ingest > 0:
		return ingest + 30*time.Second
	}
	return apiWriteDeadline
}

// rateKey is the rate-limit bucket of the authenticated caller: the /api identity apiAuth stored,
// else the /v1 ingest identity; "" (public or open callers) falls back to the key or IP.
func rateKey(c *gin.Context) string {
	if id := callerIdentity(c); id != nil {
		return id.RateKey()
	}
	return authz.FromContext(c).RateKey()
}

// policyRuntime is the decision-model state the policy engine reads for effective routes and
// block readiness, with the calibration lookup (decision_wiring.go).
func (s *server) policyRuntime() policy.Runtime {
	rt := policy.Runtime{Calibrations: s.calibrationLookup()}
	if s.gateway == nil { // still loading: the gateway is built after the policy services
		return rt
	}
	st := s.status.jevStatus()
	rt.Backend, rt.Snapshot = st.Backend, st.Snapshot
	rt.BreakerOpen = st.Breaker == jev.BreakerOpen
	rt.BudgetExhausted = st.Spend != nil && st.Spend.Exhausted
	return rt
}

// policyChanged keeps the decision model warm only while a route gates answers in flight.
func (s *server) policyChanged(p *policy.Policy) {
	if s.gateway == nil || p == nil {
		return
	}
	if kw, ok := s.gateway.client.(jev.KeepWarmer); ok {
		kw.SetKeepWarm(hasSyncRoute(p))
	}
}

// knowledgeOn refuses knowledge-base calls the engine cannot serve: 503 engine_unavailable without
// an engine, 409 knowledge_disabled when the engine reports the knowledge add-on off (from its
// cached health; an engine not checked yet is let through and answers for itself).
func (s *server) knowledgeOn(engine bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !engine {
			abortAPI(c, http.StatusServiceUnavailable, codeEngineUnavailable, "No audit engine is configured (TRUTHTABLE_GRPC_ADDRESS).")
			return
		}
		if r := s.status.engineHealth().Response; r != nil && !r.GetKnowledgeEnabled() {
			abortAPI(c, http.StatusConflict, "knowledge_disabled",
				"The knowledge add-on is off: start the knowledge compose profile with TRUTHTABLE_KNOWLEDGE_ENABLED=true.")
			return
		}
		c.Next()
	}
}

// writable refuses writes in read-only mode (TRUTHTABLE_READONLY; SPEC §2.4).
func (s *server) writable() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.cfg.Readonly {
			abortAPI(c, http.StatusForbidden, "readonly", "This TrustAgent instance is read-only (TRUTHTABLE_READONLY).")
			return
		}
		c.Next()
	}
}

// serveWS serves GET /ws: WS v2 only (SPEC §2.5). A handshake without protocol=2 gets 400
// bad_request before the upgrade, because WS v1 was removed (D13). Browsers cannot set headers on
// the handshake: they connect with a single-use ?ticket= (POST /api/ws-ticket) or their session
// cookie; other clients send X-TrustAgent-Key. An authentication failure before the upgrade is 401.
func (s *server) serveWS(c *gin.Context) {
	id, status, code, msg := s.auth.identify(c.Request, authz.ScopeRead, false)
	if id == nil {
		abortAPI(c, status, code, msg)
		return
	}
	s.hub.ServeWS(c.Writer, c.Request, websocket.ConnOptions{Content: id.Has(authz.ScopeContent), IP: c.ClientIP()})
}

// engineChanged receives an engine health transition from the pinger (status_events.go).
func (s *server) engineChanged(h grpc.EngineHealth) { s.publishEngineStatus(h) }

// snapshotChanged broadcasts a new decision-model snapshot (WS decision_model_changed, SPEC §2.5)
// with its calibration state (decision_wiring.go). It runs on a jev attempt goroutine and must not
// block: Publish never does.
func (s *server) snapshotChanged(c jev.SnapshotChange) {
	s.log.Warn("decision model snapshot changed", "backend", c.Backend, "model", c.Model, "previous", c.Previous, "snapshot", c.Snapshot)
	ev, err := websocket.NewEvent("decision_model_changed", "", map[string]any{
		"backend": c.Backend, "previous_snapshot": c.Previous, "snapshot": c.Snapshot, "calibration": s.calibrationState(c.Snapshot),
	}, nil)
	if err != nil {
		return
	}
	s.hub.Publish(ev)
}

// start runs the hub, the engine pinger, the policy reload loop, the audit workers, the decision
// model's startup probe, and the decision and status loops until shutdown.
func (s *server) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.hub.Run(ctx) }()
	if s.pinger != nil {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.pinger.Run(ctx) }()
	}
	// Policy and evaluator hot reload; shutdown joins it before main closes the store.
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.policies.run(ctx) }()
	if s.cfg.Jev.StartupProbe {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.gateway.probe(ctx, max(s.cfg.Jev.AttemptTimeout, 10*time.Second), s.log) }()
	}
	// Calibration reload and retention (decision_wiring.go), status transitions (status_events.go).
	s.startDecisionLoops(ctx, &s.wg)
	s.startStatusWatch(ctx, &s.wg)
	// P4: the runtime configuration reload, the engine push and the cost ledger.
	s.startRuntimeLoops(ctx, &s.wg)
	s.startApplyLoops(ctx, &s.wg)
	s.startCostLoops(ctx, &s.wg)
	s.pool.Start()
}

// shutdown lets observe-mode gates and queued audits finish within ctx, then stops the hub and
// the pinger. Call it after the HTTP server stopped accepting requests.
func (s *server) shutdown(ctx context.Context) error {
	var errs []error
	if err := s.gateway.handler.Drain(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.pool.Drain(ctx); err != nil {
		errs = append(errs, err)
	}
	// The drained audits booked their costs: flush the ledger.
	if err := s.stopCosts(ctx); err != nil {
		errs = append(errs, err)
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.gateway.close()
	s.gateway.auth.Wait() // flush the api_keys last_used_at updates
	return errors.Join(errs...)
}

// requestLog logs one line per request (never bodies or query strings, which may hold keys).
func requestLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("request", "method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(), "trace_id", c.Writer.Header().Get(proxy.HeaderTraceID))
	}
}
