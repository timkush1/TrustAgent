package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/proxy"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/backend-go/internal/worker"
)

func main() {
	cfg := config.Load()
	log.Printf("🚀 Starting TruthTable Proxy")
	log.Printf("   Server Port: %d", cfg.ServerPort)
	log.Printf("   WebSocket Port: %d", cfg.WSPort)
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

	workerPool := worker.NewPool(cfg.WorkerCount, cfg.QueueSize, auditClient, wsHub)
	go workerPool.Start()
	log.Printf("✓ Worker pool started (%d workers, queue size %d)", cfg.WorkerCount, cfg.QueueSize)

	proxyHandler := proxy.NewHandler(cfg.UpstreamURL, workerPool)
	log.Printf("✓ Proxy handler ready")

	if cfg.LogLevel != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(loggingMiddleware())
	router.Use(corsMiddleware())

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":       "healthy",
			"version":      "0.1.0",
			"audit_engine": auditClient != nil,
		})
	})

	router.GET("/metrics", func(c *gin.Context) {
		c.String(http.StatusOK, "# Metrics coming soon")
	})

	// Main LLM API endpoints (intercept and audit)
	router.POST("/v1/chat/completions", proxyHandler.HandleChatCompletion)
	router.POST("/v1/completions", proxyHandler.HandleCompletion)

	// Other v1 endpoints - forward as-is without auditing
	router.Any("/v1/models", proxyHandler.HandleGeneric)
	router.Any("/v1/models/*model", proxyHandler.HandleGeneric)
	router.Any("/v1/embeddings", proxyHandler.HandleGeneric)

	wsRouter := gin.New()
	wsRouter.Use(gin.Recovery())
	wsRouter.Use(corsMiddleware())
	wsRouter.GET("/ws", func(c *gin.Context) {
		websocket.ServeWS(wsHub, c.Writer, c.Request)
	})

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.ServerPort),
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	wsServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.WSPort),
		Handler: wsRouter,
	}

	go func() {
		log.Printf("🌐 HTTP server listening on :%d", cfg.ServerPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	go func() {
		log.Printf("🔌 WebSocket server listening on :%d", cfg.WSPort)
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("WebSocket server error: %v", err)
		}
	}()

	log.Printf("✅ TruthTable Proxy is ready!")
	log.Printf("   Send requests to: http://localhost:%d/v1/chat/completions", cfg.ServerPort)
	log.Printf("   Dashboard WebSocket: ws://localhost:%d/ws", cfg.WSPort)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Shutting down servers...")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	if err := wsServer.Shutdown(ctx); err != nil {
		log.Printf("WebSocket server shutdown error: %v", err)
	}

	workerPool.Stop()
	if auditClient != nil {
		auditClient.Close()
	}

	log.Println("✅ Servers stopped gracefully")
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

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
