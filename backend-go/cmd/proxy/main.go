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
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/grpc"
	_ "github.com/truthtable/backend-go/internal/metrics" // Register Prometheus metrics
	"github.com/truthtable/backend-go/internal/proxy"
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
			"version":      version.Version,
			"audit_engine": auditClient != nil,
		})
	})

	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Main LLM API endpoints (intercept and audit)
	router.POST("/v1/chat/completions", proxyHandler.HandleChatCompletion)
	router.POST("/v1/completions", proxyHandler.HandleCompletion)

	// Direct audit endpoint (submit query + response for auditing from the dashboard)
	router.POST("/api/audit", handleDirectAudit(workerPool))

	// File upload endpoint (ingest documents into RAG knowledge base)
	router.POST("/api/upload", handleFileUpload(auditClient))

	// Other v1 endpoints - forward as-is without auditing
	router.Any("/v1/models", proxyHandler.HandleGeneric)
	router.Any("/v1/models/*model", proxyHandler.HandleGeneric)
	router.Any("/v1/embeddings", proxyHandler.HandleGeneric)

	// WebSocket endpoint on the same HTTP server
	router.GET("/ws", func(c *gin.Context) {
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
	metricsServer := &http.Server{Addr: fmt.Sprintf(":%d", metricsPort), Handler: metricsMux}
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

// handleDirectAudit returns a handler that accepts a query + response for auditing.
func handleDirectAudit(pool *worker.Pool) gin.HandlerFunc {
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
func handleFileUpload(auditClient *grpc.AuditClient) gin.HandlerFunc {
	type uploadDocument struct {
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata"`
	}

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

		// Limit to 10MB
		if header.Size > 10*1024*1024 {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File too large (max 10MB)"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read file"})
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

		// Convert to gRPC IngestDocument format
		ingestDocs := make([]grpc.IngestDocument, len(docs))
		for i, doc := range docs {
			ingestDocs[i] = grpc.IngestDocument{
				Content:  doc.Content,
				Metadata: doc.Metadata,
			}
		}

		count, err := auditClient.IngestDocuments(c.Request.Context(), ingestDocs)
		if err != nil {
			log.Printf("Document ingestion failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ingest documents"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"documents_ingested": count,
			"status":             "success",
		})
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
