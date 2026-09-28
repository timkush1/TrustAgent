package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
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

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newServer wires the components and the routes. start runs the background loops.
func newServer(d serverDeps) (*server, error) {
	cfg, log := d.cfg, d.log
	if log == nil {
		log = slog.Default()
	}
	s := &server{cfg: cfg, log: log}

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

	sink := &traceSink{
		pub: s.hub, audits: s.pool, bodyRetention: cfg.BodyRetention, bodyMaxBytes: cfg.BodyMaxBytes,
		now: time.Now, log: log,
	}
	if d.store != nil {
		sink.store = d.store
	}
	// The policy services come first: the gateway reads the policy in effect and the evaluator
	// registry from them. newServer has no context, so loading is bounded here.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ps, err := newPolicyServices(ctx, policyWiring{
		cfg: cfg, pg: d.pg, hub: s.hub, runtime: s.policyRuntime, log: log,
		// Block and fallback stay refused until the /v1 handler enforces them (P3 handler work).
		enforcing: false,
		// A new policy version turns the decision model's keep-warm on or off (the gateway sets it
		// once at startup).
		onPolicy: s.policyChanged,
	})
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	s.policies = ps
	s.status.policies = ps.Engine

	gw, err := newGateway(cfg, sink, auth, ps, gatewayHooks{snapshotChanged: s.snapshotChanged}, log)
	if err != nil {
		return nil, err
	}
	s.gateway = gw
	s.status.jev, s.status.jevClient = gw.jev, gw.client
	s.auth = &apiAuth{keys: gw.auth, publicRead: cfg.PublicRead, demo: cfg.DemoMode}
	s.status.auth = s.auth
	if d.pg != nil {
		s.stats, s.status.figures = d.pg, d.pg
	}

	traces := &tracesAPI{now: time.Now, log: log}
	audits := &auditsAPI{audits: s.pool, maxTextChars: cfg.MaxTextChars, testMode: cfg.TestMode, log: log}
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
	upload := handleFileUpload(d.engine, cfg.MaxUploadBytes)
	uploadLimit := middleware.BodyLimit(cfg.MaxUploadBytes)
	api.POST("/kb/documents", writable, s.auth.require(authz.ScopeAdmin), uploadRateLimit, knowledge, uploadLimit, upload)
	api.POST("/upload", writable, ingest, uploadRateLimit, knowledge, uploadLimit, upload) // the v1 path
	api.GET("/kb/claims", read, rateLimit, knowledge, handleListKBClaims(d.engine))
	api.GET("/kb/conflicts", read, rateLimit, knowledge, handleListConflicts(d.engine))
	api.GET("/kb/stats", read, rateLimit, knowledge, handleKBStats(d.engine))
	// P2 authz core: setup, session, ws-ticket and keys endpoints (setup_api.go, session_api.go, keys_api.go).
	s.mountAuthz(api, rateLimit, bodyLimit, writable)
	// Policies and evaluators (policies_api.go, evaluators_api.go). The dry-run has only its own
	// limit: RateLimit keys on the route path, so the general limit would count it twice.
	s.policies.mount(api, apiGuards{read: read, admin: s.auth.require(authz.ScopeAdmin), writable: writable,
		rateLimit: rateLimit, dryRunLimit: rateLimitBy(6, time.Minute), bodyLimit: bodyLimit})
	// Overview, ROI and evidence coverage (stats_api.go).
	s.mountStats(api, read, rateLimit)

	r.GET("/ws", s.serveWS)

	// LLM API (SPEC §2.12): every /v1 response carries the trace id; ingest auth reads
	// X-TrustAgent-Key only, so Authorization stays the client's upstream credential (B1).
	h := s.gateway.handler
	v1 := r.Group("/v1", h.TraceIDs(), s.gateway.auth.Ingest(proxy.Deny), v1RateLimit)
	v1.POST("/chat/completions", bodyLimit, h.ChatCompletions)
	// Proxied but not gated yet: completions are gated in P3; /v1/responses stays ungated.
	v1.POST("/completions", bodyLimit, h.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/responses", bodyLimit, h.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/responses/*rest", bodyLimit, h.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/models", h.Passthrough(""))
	v1.Any("/models/*model", h.Passthrough(""))
	v1.Any("/embeddings", bodyLimit, h.Passthrough(""))

	// Unknown paths answer in the error shape of their family, not gin's text/plain 404.
	r.NoRoute(func(c *gin.Context) {
		if p := c.Request.URL.Path; p == "/v1" || strings.HasPrefix(p, "/v1/") {
			proxy.Deny(c, http.StatusNotFound, codeNotFound, "TrustAgent does not serve "+c.Request.Method+" "+p+".")
			c.Abort()
			return
		}
		abortAPI(c, http.StatusNotFound, codeNotFound, "No endpoint here: "+c.Request.Method+" "+c.Request.URL.Path+".")
	})
	return r
}

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
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		if cfg.UpstreamTimeout <= 0 {
			return 0
		}
		return cfg.UpstreamTimeout + 30*time.Second
	case (path == "/api/kb/documents" || path == "/api/upload") && ingest > 0:
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
// block readiness. Calibrations are looked up once the calibration store lands (P4).
func (s *server) policyRuntime() policy.Runtime {
	rt := policy.Runtime{Calibrations: policy.NoCalibration{}}
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

// serveWS serves GET /ws: WS v2 with protocol=2, v1 frames otherwise (SPEC §2.5). Browsers cannot
// set headers on the handshake: they connect with a single-use ?ticket= (POST /api/ws-ticket) or
// their session cookie; other clients send X-TrustAgent-Key. A failure before the upgrade is 401.
func (s *server) serveWS(c *gin.Context) {
	id, status, code, msg := s.auth.identify(c.Request, authz.ScopeRead, false)
	if id == nil {
		abortAPI(c, status, code, msg)
		return
	}
	s.hub.ServeWS(c.Writer, c.Request, websocket.ConnOptions{Content: id.Has(authz.ScopeContent), IP: c.ClientIP()})
}

// engineChanged logs an engine health transition and broadcasts it (WS status event).
func (s *server) engineChanged(h grpc.EngineHealth) {
	if h.Reachable() {
		s.log.Info("audit engine health", "state", h.State, "detail", h.Detail)
	} else {
		s.log.Warn("audit engine health", "state", h.State, "detail", h.Detail)
	}
	ev, err := websocket.NewEvent("status", "", engineStatusEvent(h), nil)
	if err != nil {
		return
	}
	s.hub.Publish(ev)
}

// snapshotChanged broadcasts a new decision-model snapshot (WS decision_model_changed, SPEC §2.5).
// Calibration lookups per snapshot arrive with calibration (P3), so the state is "none" until then.
// It runs on a jev attempt goroutine and must not block: Publish never does.
func (s *server) snapshotChanged(c jev.SnapshotChange) {
	s.log.Warn("decision model snapshot changed", "backend", c.Backend, "model", c.Model, "previous", c.Previous, "snapshot", c.Snapshot)
	ev, err := websocket.NewEvent("decision_model_changed", "", map[string]any{
		"backend": c.Backend, "previous_snapshot": c.Previous, "snapshot": c.Snapshot, "calibration": "none",
	}, nil)
	if err != nil {
		return
	}
	s.hub.Publish(ev)
}

// start runs the hub, the engine pinger, the policy reload loop, the audit workers and the
// decision model's startup probe until shutdown.
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
