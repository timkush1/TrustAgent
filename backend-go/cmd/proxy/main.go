package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
	_ "github.com/truthtable/backend-go/internal/metrics" // Register Prometheus metrics
	"github.com/truthtable/backend-go/internal/middleware"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/proxy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/upstream"
	"github.com/truthtable/backend-go/internal/version"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/backend-go/internal/worker"
)

func main() {
	cfg := config.Load()
	log.Printf("🚀 Starting TruthTable Proxy")
	log.Printf("   Server Port: %d (HTTP + WebSocket)", cfg.ServerPort)
	log.Printf("   Upstream URL: %s", cfg.UpstreamURL)
	log.Printf("   gRPC Address: %s", cfg.GRPCAddress)

	auditClient, err := grpc.NewAuditClient(cfg.GRPCAddress, cfg.GRPCTimeout)
	if err != nil {
		log.Printf("⚠️  Warning: Could not connect to audit engine: %v", err)
		log.Printf("   Proxy will still work, but audits will be skipped")
	} else {
		log.Printf("✓ Connected to audit engine at %s", cfg.GRPCAddress)
	}

	wsHub := websocket.NewHub()
	go wsHub.Run()
	log.Printf("✓ WebSocket hub started")

	// Audit persistence (optional: enabled when TRUTHTABLE_DATABASE_URL is set)
	var auditStore store.Store
	if cfg.DatabaseURL != "" {
		pgStore, err := store.NewPostgresStore(context.Background(), cfg.DatabaseURL)
		if err != nil {
			log.Printf("⚠️  Audit persistence disabled: %v", err)
		} else {
			auditStore = pgStore
			defer pgStore.Close()
			log.Printf("✓ Audit persistence enabled (Postgres)")
		}
	} else {
		log.Printf("⚠️  TRUTHTABLE_DATABASE_URL not set — audit history will not be persisted")
	}

	workerPool := worker.NewPool(cfg.WorkerCount, cfg.QueueSize, auditClient, wsHub)
	if auditStore != nil {
		workerPool.AttachStore(auditStore)
	}
	go workerPool.Start()
	log.Printf("✓ Worker pool started (%d workers, queue size %d)", cfg.WorkerCount, cfg.QueueSize)

	gateway, err := newGateway(cfg, gate.NopSink{})
	if err != nil {
		log.Fatalf("TrustAgent gateway: %v", err)
	}
	log.Printf("✓ Gateway ready")

	if cfg.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}

	// Security wiring
	apiKeys := cfg.APIKeySecrets()
	if len(apiKeys) == 0 {
		log.Printf("⚠️  TRUTHTABLE_API_KEYS not set — /api authentication is DISABLED (dev mode)")
	} else {
		log.Printf("✓ API-key auth enabled (%d keys)", len(apiKeys))
	}
	websocket.ConfigureUpgrader(cfg.AllowedOrigins)

	var limiter middleware.Limiter
	if cfg.RedisURL != "" {
		if redisLimiter, err := middleware.NewRedisLimiter(cfg.RedisURL); err == nil {
			limiter = redisLimiter
			log.Printf("✓ Rate limiting backed by Redis")
		} else {
			log.Printf("⚠️  Redis unavailable (%v) — using in-memory rate limiter", err)
		}
	}
	if limiter == nil {
		limiter = middleware.NewMemoryLimiter()
	}

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(loggingMiddleware())
	router.Use(middleware.CORS(cfg.AllowedOrigins))

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":       "healthy",
			"version":      version.Version,
			"audit_engine": auditClient != nil,
		})
	})

	// NOTE: /metrics is intentionally NOT on this router. Prometheus scrapes
	// the internal metrics server on :8002, which is not published to the host.

	auth := middleware.APIKeyAuth(apiKeys)
	rateLimit := middleware.RateLimit(limiter, cfg.RateLimitPerMinute, time.Minute)
	uploadRateLimit := middleware.RateLimit(limiter, cfg.UploadLimitPerMin, time.Minute)
	bodyLimit := middleware.BodyLimit(cfg.MaxBodyBytes)

	// LLM API (SPEC §2.12): every /v1 response carries the trace id; ingest auth reads
	// X-TrustAgent-Key only, so Authorization stays the client's upstream credential (B1).
	v1 := router.Group("/v1", gateway.handler.TraceIDs(), gateway.auth.Ingest(proxy.Deny), rateLimit)
	v1.POST("/chat/completions", bodyLimit, gateway.handler.ChatCompletions)
	// Proxied but not gated yet: completions are gated in P3; /v1/responses stays ungated.
	v1.POST("/completions", bodyLimit, gateway.handler.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/responses", bodyLimit, gateway.handler.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/responses/*rest", bodyLimit, gateway.handler.Passthrough(proxy.StatusSkippedUnsupportedAPI))
	v1.Any("/models", gateway.handler.Passthrough(""))
	v1.Any("/models/*model", gateway.handler.Passthrough(""))
	v1.Any("/embeddings", bodyLimit, gateway.handler.Passthrough(""))

	// Direct audit endpoint (submit query + response for auditing from the dashboard)
	router.POST("/api/audit", auth, rateLimit, bodyLimit, handleDirectAudit(workerPool, cfg.MaxTextChars))

	// File upload endpoint (ingest documents into RAG knowledge base)
	router.POST("/api/upload", auth, uploadRateLimit, middleware.BodyLimit(cfg.MaxUploadBytes),
		handleFileUpload(auditClient, cfg.MaxUploadBytes))

	// Audit history (requires persistence; 503 when disabled)
	router.GET("/api/audits", auth, rateLimit, handleListAudits(auditStore))
	router.GET("/api/audits/:id", auth, rateLimit, handleGetAudit(auditStore))

	// Knowledge base (VERITAS-lite): claims, conflicts, stats
	router.GET("/api/kb/claims", auth, rateLimit, handleListKBClaims(auditClient))
	router.GET("/api/kb/conflicts", auth, rateLimit, handleListConflicts(auditClient))
	router.GET("/api/kb/stats", auth, rateLimit, handleKBStats(auditClient))

	// WebSocket endpoint. Browsers cannot set headers on the WS handshake,
	// so when auth is enabled the key is accepted via ?api_key= instead.
	router.GET("/ws", func(c *gin.Context) {
		if len(apiKeys) > 0 && !middleware.KeyAllowed(apiKeys, c.Query("api_key")) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid API key"})
			return
		}
		websocket.ServeWS(wsHub, c.Writer, c.Request)
	})

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.ServerPort),
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	go func() {
		log.Printf("🌐 HTTP/WebSocket server listening on :%d", cfg.ServerPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Metrics server on port 8002 for Prometheus scraping
	metricsPort := 8002
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", metricsPort),
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second, // Slowloris defense (gosec G112)
	}
	go func() {
		log.Printf("📊 Metrics server listening on :%d", metricsPort)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Metrics server error: %v", err)
		}
	}()

	log.Printf("✅ TruthTable Proxy is ready!")
	log.Printf("   Send requests to: http://localhost:%d/v1/chat/completions", cfg.ServerPort)
	log.Printf("   Dashboard WebSocket: ws://localhost:%d/ws", cfg.ServerPort)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Shutting down servers...")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	if err := metricsServer.Shutdown(ctx); err != nil {
		log.Printf("Metrics server shutdown error: %v", err)
	}
	if err := gateway.handler.Drain(ctx); err != nil {
		log.Printf("Observe-mode gates still running at shutdown: %v", err)
	}

	workerPool.Stop()
	if auditClient != nil {
		auditClient.Close()
	}

	log.Println("✅ Servers stopped gracefully")
}

// gatewayParts are the /v1 components main wires into the router.
type gatewayParts struct {
	handler *proxy.Handler
	auth    *authz.Authenticator
}

// newGateway builds the gate hot path: ingest auth, the policy, the decision-model client, the
// gate and the /v1 handler. The sink persists and broadcasts traces and decisions and submits
// deep audits.
func newGateway(cfg *config.Config, sink gate.Sink) (*gatewayParts, error) {
	for _, w := range cfg.Warnings {
		log.Printf("⚠️  config: %s", w)
	}

	keys := make([]authz.Key, 0, len(cfg.APIKeys))
	for _, k := range cfg.APIKeys {
		keys = append(keys, authz.Key{Label: k.Label, Secret: k.Key, Scopes: authz.APIKeyScopes})
	}
	auth := authz.New(keys, cfg.IngestAuth)
	switch {
	case auth.IngestMode() == authz.IngestOpen:
		log.Printf("⚠️  TRUTHTABLE_INGEST_AUTH=open: /v1 accepts requests without an ingest key")
	case !auth.HasIngestKeys():
		log.Printf("⚠️  No ingest keys: /v1 answers 401 until TRUTHTABLE_API_KEYS (label=key) is set, or TRUTHTABLE_INGEST_AUTH=open")
	default:
		log.Printf("✓ /v1 ingest auth: X-TrustAgent-Key (%d keys)", len(keys))
	}

	registry, err := evaluators.Builtins()
	if err != nil {
		return nil, fmt.Errorf("evaluator templates: %w", err)
	}
	pol, err := policy.LoadFile(cfg.PolicyFile, policy.Seeds{
		Mode: cfg.GateDefaultMode, Fail: cfg.GateFailMode, MaxResponseTokens: cfg.Jev.MaxResponseTokens,
	}, registry)
	if err != nil {
		return nil, err
	}
	if pol.Source == policy.SourceBuiltin {
		log.Printf("⚠️  Policy file %s not found: gating every request on the built-in defaults (%s mode)", cfg.PolicyFile, pol.Default.Mode)
	} else {
		log.Printf("✓ Policy %s: %d route(s) plus default (%s mode)", pol.Path, len(pol.Routes), pol.Default.Mode)
	}

	jevClient, err := jev.New(jev.Config{
		Backend: cfg.Jev.Backend, Model: cfg.Jev.Model, DemoMode: cfg.DemoMode,
		OpenRouterKey: cfg.Jev.OpenRouterKey, TypeSafeKey: cfg.Jev.TypeSafeKey, JevAPIKey: cfg.Jev.APIKey, BaseURL: cfg.Jev.BaseURL,
		Mock: jev.MockConfig{
			Mode: cfg.Jev.MockMode, Strict: cfg.Jev.MockStrict, FixturesPath: cfg.Jev.MockFixtures,
			Seed: cfg.Jev.MockSeed, Latency: cfg.Jev.MockLatency,
		},
	})
	if err != nil {
		return nil, err
	}
	info := jevClient.Info()
	log.Printf("✓ Decision model: %s (%s)", info.Backend, info.Reason)

	g, err := gate.New(gate.Config{
		Jev: jevClient, Evaluators: registry,
		Budget: decision.Budget{
			Tokens: cfg.Jev.TokenBudget, CPT: cfg.Jev.CharsPerToken, MaxQuestions: cfg.Jev.MaxQuestionsPerCall,
			Overhead: decision.Overhead, MaxResponseTokens: cfg.Jev.MaxResponseTokens, EvidenceMaxTokens: 8000,
		},
		AttemptTimeout: cfg.Jev.AttemptTimeout, AsyncDeadline: cfg.Jev.AsyncDeadline,
		UniformAuditsPerDay: cfg.UniformAuditsPerDay,
	})
	if err != nil {
		return nil, err
	}

	up, err := upstream.New(upstream.Config{
		BaseURL: cfg.UpstreamURL, HeaderTimeout: cfg.UpstreamHeaderTimeout, Timeout: cfg.UpstreamTimeout,
	}, auth.IsProxyKey)
	if err != nil {
		return nil, err
	}
	log.Printf("✓ Upstream: %s (passthrough credentials)", up.Base())
	if cfg.TestMode {
		log.Printf("⚠️  TRUTHTABLE_TEST_MODE: test_response and X-TrustAgent-Scenario are honoured")
	}

	h := proxy.New(proxy.Options{
		Upstream: up, Policy: pol, Gate: g, Sink: sink, TestMode: cfg.TestMode,
		MaxUpstreamResponseBytes: cfg.MaxUpstreamResponseBytes, GateMaxConcurrency: cfg.GateMaxConcurrency,
	})
	return &gatewayParts{handler: h, auth: auth}, nil
}

func loggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		latency := time.Since(start)
		status := c.Writer.Status()
		log.Printf("%s %s %d %v", c.Request.Method, path, status, latency)
	}
}

// handleDirectAudit returns a handler that accepts a query + response for auditing.
func handleDirectAudit(pool *worker.Pool, maxTextChars int) gin.HandlerFunc {
	type directAuditRequest struct {
		Query    string `json:"query" binding:"required"`
		Response string `json:"response" binding:"required"`
		Model    string `json:"model"`
	}

	return func(c *gin.Context) {
		var req directAuditRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Both 'query' and 'response' fields are required"})
			return
		}

		if len(req.Query) > maxTextChars || len(req.Response) > maxTextChars {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("'query' and 'response' must each be at most %d characters", maxTextChars),
			})
			return
		}
		if len(req.Model) > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "'model' name too long"})
			return
		}

		requestID := uuid.New().String()

		if pool != nil {
			pool.Submit(&worker.AuditJob{
				RequestID:   requestID,
				Prompt:      req.Query,
				Response:    req.Response,
				Model:       req.Model,
				Timestamp:   time.Now(),
				RequestPath: "/api/audit",
			})
		}

		c.JSON(http.StatusAccepted, gin.H{
			"request_id": requestID,
			"status":     "submitted",
		})
	}
}

// handleFileUpload returns a handler that accepts JSON document uploads for RAG ingestion.
func handleFileUpload(auditClient *grpc.AuditClient, maxUploadBytes int64) gin.HandlerFunc {
	type uploadDocument struct {
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata"`
	}
	const maxDocuments = 1000
	const maxDocumentChars = 50000

	return func(c *gin.Context) {
		if auditClient == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Audit engine not connected"})
			return
		}

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No file provided. Use multipart form field 'file'"})
			return
		}
		defer file.Close()

		if header.Size > maxUploadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("File too large (max %d bytes)", maxUploadBytes),
			})
			return
		}

		data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read file"})
			return
		}
		if int64(len(data)) > maxUploadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("File too large (max %d bytes)", maxUploadBytes),
			})
			return
		}

		var docs []uploadDocument
		if err := json.Unmarshal(data, &docs); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON. Expected array of {\"content\": \"...\", \"metadata\": {...}}"})
			return
		}

		if len(docs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No documents in file"})
			return
		}
		if len(docs) > maxDocuments {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("Too many documents (max %d per upload)", maxDocuments),
			})
			return
		}
		for i, doc := range docs {
			if len(doc.Content) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("Document %d has empty 'content'", i),
				})
				return
			}
			if len(doc.Content) > maxDocumentChars {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{
					"error": fmt.Sprintf("Document %d exceeds %d characters", i, maxDocumentChars),
				})
				return
			}
		}

		// Convert to gRPC IngestDocument format
		ingestDocs := make([]grpc.IngestDocument, len(docs))
		for i, doc := range docs {
			ingestDocs[i] = grpc.IngestDocument{
				Content:  doc.Content,
				Metadata: doc.Metadata,
			}
		}

		result, err := auditClient.IngestDocuments(c.Request.Context(), ingestDocs)
		if err != nil {
			log.Printf("Document ingestion failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ingest documents"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"documents_ingested": result.DocumentsIngested,
			"claims_accepted":    result.ClaimsAccepted,
			"claims_quarantined": result.ClaimsQuarantined,
			"conflicts_detected": result.ConflictsDetected,
			"claim_results":      result.ClaimResults,
			"status":             "success",
		})
	}
}
