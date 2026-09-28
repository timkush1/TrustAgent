// Command proxy is the TrustAgent gateway: the /v1 LLM API proxy that gates answers, the REST API
// and WebSocket stream of the dashboard, and the deep-audit workers that call the audit engine.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	cfg := config.Load()
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)
	if err := run(cfg, log); err != nil {
		log.Error("TrustAgent proxy stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

// run starts the proxy and blocks until SIGINT or SIGTERM, then shuts down in order: stop
// accepting requests, let observe-mode gates and queued audits drain, stop the stream.
func run(cfg *config.Config, log *slog.Logger) error {
	log.Info("starting the TrustAgent proxy", "version", version.Version, "port", cfg.ServerPort,
		"upstream", cfg.UpstreamURL, "engine", cfg.GRPCAddress)
	if cfg.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}

	d := serverDeps{cfg: cfg, log: log, redisState: stateDisabled}

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
			d.store = pg
			log.Info("persistence enabled (Postgres)")
		}
	}
	if cfg.TraceFlush != "sync" {
		log.Info("trace writes run inline; the batched TRUTHTABLE_TRACE_FLUSH=async writer lands in P2")
	}

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
	if err := metricsServer.Shutdown(ctx); err != nil {
		log.Warn("metrics server shutdown", "err", err)
	}
	log.Info("TrustAgent proxy stopped")
	return runErr
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
	case len(cfg.APIKeys) == 0:
		log.Warn("no TRUTHTABLE_API_KEYS: /api and /ws refuse every caller; set keys or TRUTHTABLE_PUBLIC_READ=true")
	}
}
