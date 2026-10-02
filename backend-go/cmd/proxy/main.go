// Command proxy is the TrustAgent gateway: the /v1 LLM API proxy that gates answers, the REST API
// and WebSocket stream of the dashboard, and the deep-audit workers that call the audit engine.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/grpc"
	_ "github.com/truthtable/backend-go/internal/metrics" // registers the Prometheus metrics
	"github.com/truthtable/backend-go/internal/middleware"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/version"
)

func main() {
	// Command-line modes (-check-config, -dump-config-spec, -new-setup-token [-force]) and
	// TRUTHTABLE_STRICT_CONFIG (exit 2 on any configuration warning) run first (cli.go).
	cfg, code, exit := bootConfig(os.Args[1:], os.Stdout, os.Stderr)
	if exit {
		os.Exit(code)
	}
	redactor := cfg.Redactor()
	log := newLogger(cfg.LogLevel, redactor)
	slog.SetDefault(log)
	// gin's panic recovery dumps request headers (keys, the session cookie) in debug mode and on a
	// broken pipe; it captures this writer when the router is built.
	gin.DefaultErrorWriter = redactor.Writer(os.Stderr)
	if err := run(cfg, log, redactor); err != nil {
		log.Error("TrustAgent proxy stopped", "err", err)
		os.Exit(1)
	}
}

// newLogger logs text to stderr; redactor masks every loaded secret and credential pattern.
func newLogger(level string, redactor *config.Redactor) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l, ReplaceAttr: redactor.ReplaceAttr}))
}

// run starts the proxy and blocks until SIGINT or SIGTERM, then shuts down in order: stop
// accepting requests, let observe-mode gates and queued audits drain, stop the stream. redactor
// masks the secrets that only exist at run time (the generated internal token).
func run(cfg *config.Config, log *slog.Logger, redactor *config.Redactor) error {
	log.Info("starting the TrustAgent proxy", "version", version.Version, "port", cfg.ServerPort,
		"upstream", redactURL(cfg.UpstreamURL), "engine", cfg.GRPCAddress)
	if cfg.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}

	d := serverDeps{cfg: cfg, log: log, redisState: stateDisabled, redactor: redactor}
	// Admin audit diffs are scrubbed with the process Redactor, which learns every secret the
	// runtime configuration loads or receives (D42, D44).
	defer store.AddAuditRedactor(redactor)()
	// REST error messages pass through the same Redactor (D42).
	defer installRedaction(redactor)()

	if cfg.DatabaseURL == "" {
		log.Warn("TRUTHTABLE_DATABASE_URL is not set: traces and audits are not stored")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pg, err := store.NewPostgresStore(ctx, cfg.DatabaseURL)
		cancel()
		if err != nil {
			log.Error("persistence disabled: the database could not be opened", "err", err)
		} else {
			defer pg.Close()
			d.store, d.pg = pg, pg
			log.Info("persistence enabled (Postgres)")
		}
	}
	d.master = loadMasterKey(cfg, log) // signs dashboard sessions (P2 authz core)
	// The batched trace writer (TRUTHTABLE_TRACE_FLUSH=async, trace_writer.go) fronts the store;
	// its flush runs at shutdown after the drain.
	var flushTraces func(context.Context) error
	d.store, flushTraces = wrapTraceWriter(cfg, d.store, log)

	cfg.InternalToken = internalToken(cfg.InternalToken, cfg.InternalTokenFile, log)
	redactor.Add(cfg.InternalToken) // a generated token was not in the loaded configuration
	if cfg.GRPCAddress != "" {
		client, err := grpc.NewAuditClient(cfg.GRPCAddress, cfg.GRPCTimeout, cfg.InternalToken)
		if err != nil {
			log.Error("deep audits disabled: the audit engine address is not usable", "address", cfg.GRPCAddress, "err", err)
		} else {
			defer func() { _ = client.Close() }()
			d.engine = client
		}
	}

	if cfg.RedisURL != "" {
		if redis, err := middleware.NewRedisLimiter(cfg.RedisURL); err == nil {
			d.limiter, d.redisState = redis, stateUp
			log.Info("rate limiting backed by Redis")
		} else {
			d.redisState = stateDown
			log.Warn("Redis unavailable: rate limiting in memory", "err", err)
		}
	}

	srv, err := newServer(d)
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	logAuth(log, cfg)
	srv.announceSetup(context.Background(), os.Stdout) // "SETUP: open …" while no admin exists
	srv.start()

	httpServer := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.ServerPort), Handler: srv.router,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout,
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	// /metrics is not on the public router: Prometheus scrapes this port on the internal network.
	metricsServer := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.MetricsPort), Handler: metricsMux, ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}

	errc := make(chan error, 2)
	go func() { errc <- serve(httpServer) }()
	go func() {
		if err := serve(metricsServer); err != nil {
			log.Error("metrics server stopped", "err", err)
		}
	}()
	log.Info("TrustAgent proxy ready", "http", httpServer.Addr, "metrics", metricsServer.Addr)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	var runErr error
	select {
	case sig := <-quit:
		log.Info("shutting down", "signal", sig.String())
	case runErr = <-errc:
		log.Error("HTTP server failed", "err", runErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Warn("HTTP server shutdown", "err", err)
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.ShutdownDrainTimeout)
	defer drainCancel()
	if err := srv.shutdown(drainCtx); err != nil {
		log.Warn("work was still running at shutdown", "err", err)
	}
	if err := flushTraces(drainCtx); err != nil {
		log.Warn("trace writes were still pending at shutdown", "err", err)
	}
	if err := metricsServer.Shutdown(ctx); err != nil {
		log.Warn("metrics server shutdown", "err", err)
	}
	log.Info("TrustAgent proxy stopped")
	return runErr
}

// redactURL is a URL safe to log: a password becomes xxxxx and the query is dropped (a base URL
// may carry an api-key parameter).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.Redacted()
}

// serve runs an HTTP server; a graceful shutdown is not an error.
func serve(s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// logAuth reports how /api and /ws callers authenticate.
func logAuth(log *slog.Logger, cfg *config.Config) {
	switch {
	case cfg.PublicRead && cfg.DemoMode:
		log.Warn("TRUTHTABLE_PUBLIC_READ: /api reads and /ws need no key; demo mode shows keyless readers prompts and answers")
	case cfg.PublicRead:
		log.Warn("TRUTHTABLE_PUBLIC_READ: /api reads and /ws need no key (texts stay hidden without a key)")
	case len(cfg.APIKeys)+len(cfg.Auth.ReadKeys)+len(cfg.Auth.AdminKeys) == 0 && cfg.DatabaseURL == "":
		log.Warn("no env keys and no database: /api and /ws refuse every caller; set TRUTHTABLE_API_KEYS / _ADMIN_KEYS or TRUTHTABLE_PUBLIC_READ=true")
	}
}
