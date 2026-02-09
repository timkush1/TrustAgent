# Step 2.1: Gin Server Setup

## 🎯 Goal

Set up the **Go Gin web server** that will serve as the foundation for the TruthTable proxy. This server will:

- Handle incoming RAG requests
- Route requests to the upstream LLM
- Return responses immediately to clients
- Trigger async audits in the background

---

## 📚 Prerequisites

- Go 1.22+ installed
- Basic understanding of HTTP servers
- Completed Phase 0 (project structure)

---

## 🧠 Concepts Explained

### Why Gin Framework?

Gin is one of the most popular Go web frameworks:

| Feature | net/http | Gin |
|---------|----------|-----|
| Routing | Manual | Powerful router |
| Middleware | Manual | Built-in chain |
| JSON | Manual | Automatic |
| Validation | None | Built-in |
| Performance | Good | Excellent |

### Project Layout

Go projects follow a specific layout:

```
backend-go/
├── cmd/
│   └── proxy/
│       └── main.go         # Entry point
├── internal/
│   ├── config/             # Configuration
│   ├── handlers/           # HTTP handlers
│   ├── middleware/         # Gin middleware
│   ├── proxy/              # Reverse proxy logic
│   └── worker/             # Background workers
├── pkg/
│   └── client/             # Reusable clients
├── go.mod
└── go.sum
```

### Gin Middleware Flow

```
Request → Logger → Recovery → Auth → Handler → Response
                                        ↓
                              TeeWriter → Audit Queue
```

---

## 💻 Implementation

### Step 1: Initialize Go Module

```bash
cd backend-go
go mod init github.com/yourorg/truthtable-proxy

# Install dependencies
go get github.com/gin-gonic/gin@v1.9.1
go get github.com/gin-contrib/cors
go get github.com/spf13/viper
go get go.uber.org/zap
```

### Step 2: Create Configuration

Create `internal/config/config.go`:

```go
package config

import (
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the proxy
type Config struct {
	Server   ServerConfig
	Upstream UpstreamConfig
	Audit    AuditConfig
	Redis    RedisConfig
}

// ServerConfig for the Gin server
type ServerConfig struct {
	Port            int           `mapstructure:"port"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	Mode            string        `mapstructure:"mode"` // debug, release, test
}

// UpstreamConfig for the RAG backend
type UpstreamConfig struct {
	URL            string        `mapstructure:"url"`
	Timeout        time.Duration `mapstructure:"timeout"`
	MaxIdleConns   int           `mapstructure:"max_idle_conns"`
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
}

// AuditConfig for the Python audit service
type AuditConfig struct {
	GRPCAddress     string        `mapstructure:"grpc_address"`
	Timeout         time.Duration `mapstructure:"timeout"`
	Enabled         bool          `mapstructure:"enabled"`
	WorkerCount     int           `mapstructure:"worker_count"`
	QueueSize       int           `mapstructure:"queue_size"`
	RetryAttempts   int           `mapstructure:"retry_attempts"`
	RetryBackoff    time.Duration `mapstructure:"retry_backoff"`
}

// RedisConfig for the message queue
type RedisConfig struct {
	Address  string `mapstructure:"address"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// Load reads configuration from environment and files
func Load() (*Config, error) {
	v := viper.New()

	// Set defaults
	setDefaults(v)

	// Read from environment
	v.SetEnvPrefix("TRUTHTABLE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Try to read config file
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	
	// Ignore if config file doesn't exist
	_ = v.ReadInConfig()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	// Server defaults
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", "30s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.shutdown_timeout", "10s")
	v.SetDefault("server.mode", "release")

	// Upstream defaults
	v.SetDefault("upstream.url", "http://localhost:8000")
	v.SetDefault("upstream.timeout", "60s")
	v.SetDefault("upstream.max_idle_conns", 100)
	v.SetDefault("upstream.request_timeout", "120s")

	// Audit defaults
	v.SetDefault("audit.grpc_address", "localhost:50051")
	v.SetDefault("audit.timeout", "30s")
	v.SetDefault("audit.enabled", true)
	v.SetDefault("audit.worker_count", 5)
	v.SetDefault("audit.queue_size", 1000)
	v.SetDefault("audit.retry_attempts", 3)
	v.SetDefault("audit.retry_backoff", "1s")

	// Redis defaults
	v.SetDefault("redis.address", "localhost:6379")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)
}
```

### Step 3: Create Logger

Create `internal/logger/logger.go`:

```go
package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var log *zap.Logger

// Init initializes the global logger
func Init(mode string) {
	var config zap.Config

	if mode == "debug" {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		config = zap.NewProductionConfig()
	}

	var err error
	log, err = config.Build()
	if err != nil {
		panic(err)
	}
}

// Get returns the global logger
func Get() *zap.Logger {
	if log == nil {
		// Default to development if not initialized
		log, _ = zap.NewDevelopment()
	}
	return log
}

// Sugar returns a sugared logger for convenience
func Sugar() *zap.SugaredLogger {
	return Get().Sugar()
}

// Sync flushes any buffered log entries
func Sync() {
	if log != nil {
		_ = log.Sync()
	}
}
```

### Step 4: Create Gin Server

Create `internal/server/server.go`:

```go
package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/logger"
)

// Server wraps the Gin engine with lifecycle management
type Server struct {
	config     *config.ServerConfig
	engine     *gin.Engine
	httpServer *http.Server
}

// New creates a new server instance
func New(cfg *config.ServerConfig) *Server {
	// Set Gin mode
	gin.SetMode(cfg.Mode)

	// Create engine with default middleware
	engine := gin.New()

	// Add core middleware
	engine.Use(gin.Recovery())
	engine.Use(requestLogger())
	engine.Use(corsMiddleware())

	return &Server{
		config: cfg,
		engine: engine,
	}
}

// Engine returns the underlying Gin engine for route registration
func (s *Server) Engine() *gin.Engine {
	return s.engine
}

// Start begins listening for HTTP requests
func (s *Server) Start() error {
	log := logger.Get()
	
	addr := fmt.Sprintf(":%d", s.config.Port)
	
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      s.engine,
		ReadTimeout:  s.config.ReadTimeout,
		WriteTimeout: s.config.WriteTimeout,
	}

	log.Info("Starting HTTP server",
		zap.String("addr", addr),
		zap.String("mode", s.config.Mode),
	)

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server
func (s *Server) Shutdown(ctx context.Context) error {
	log := logger.Get()
	log.Info("Shutting down server...")
	
	return s.httpServer.Shutdown(ctx)
}

// requestLogger is Gin middleware for logging requests
func requestLogger() gin.HandlerFunc {
	log := logger.Get()

	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		// Process request
		c.Next()

		// Log after request completes
		latency := time.Since(start)
		status := c.Writer.Status()

		log.Info("Request",
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", query),
			zap.Int("status", status),
			zap.Duration("latency", latency),
			zap.String("client_ip", c.ClientIP()),
		)
	}
}

// corsMiddleware configures CORS for the API
func corsMiddleware() gin.HandlerFunc {
	return cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	})
}
```

### Step 5: Create Health Handler

Create `internal/handlers/health.go`:

```go
package handlers

import (
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler provides health check endpoints
type HealthHandler struct {
	startTime time.Time
	version   string
}

// NewHealthHandler creates a new health handler
func NewHealthHandler(version string) *HealthHandler {
	return &HealthHandler{
		startTime: time.Now(),
		version:   version,
	}
}

// HealthResponse is the health check response
type HealthResponse struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	Uptime    string `json:"uptime"`
	GoVersion string `json:"go_version"`
	Timestamp string `json:"timestamp"`
}

// Liveness returns OK if the server is running
func (h *HealthHandler) Liveness(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "alive"})
}

// Readiness returns OK if the server is ready to accept traffic
func (h *HealthHandler) Readiness(c *gin.Context) {
	// TODO: Check dependencies (gRPC, Redis, etc.)
	
	response := HealthResponse{
		Status:    "ready",
		Version:   h.version,
		Uptime:    time.Since(h.startTime).String(),
		GoVersion: runtime.Version(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	c.JSON(http.StatusOK, response)
}

// RegisterRoutes registers health check routes
func (h *HealthHandler) RegisterRoutes(r *gin.Engine) {
	r.GET("/health", h.Liveness)
	r.GET("/health/live", h.Liveness)
	r.GET("/health/ready", h.Readiness)
}
```

### Step 6: Create Main Entry Point

Create `cmd/proxy/main.go`:

```go
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/handlers"
	"github.com/yourorg/truthtable-proxy/internal/logger"
	"github.com/yourorg/truthtable-proxy/internal/server"
)

const version = "0.1.0"

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		panic("Failed to load config: " + err.Error())
	}

	// Initialize logger
	logger.Init(cfg.Server.Mode)
	defer logger.Sync()

	log := logger.Get()
	log.Info("Starting TruthTable Proxy",
		zap.String("version", version),
	)

	// Create server
	srv := server.New(&cfg.Server)

	// Register routes
	healthHandler := handlers.NewHealthHandler(version)
	healthHandler.RegisterRoutes(srv.Engine())

	// Start server in goroutine
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("Server error", zap.Error(err))
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Received shutdown signal")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown", zap.Error(err))
	}

	log.Info("Server stopped")
}
```

---

## ✅ Testing

### Test 1: Build and Run

```bash
cd backend-go

# Build
go build -o bin/proxy ./cmd/proxy

# Run
./bin/proxy
```

Expected output:
```
2024-XX-XX INFO Starting TruthTable Proxy {"version": "0.1.0"}
2024-XX-XX INFO Starting HTTP server {"addr": ":8080", "mode": "release"}
```

### Test 2: Health Check

```bash
# Liveness
curl http://localhost:8080/health
# {"status":"alive"}

# Readiness
curl http://localhost:8080/health/ready
# {"status":"ready","version":"0.1.0","uptime":"5s","go_version":"go1.22.0",...}
```

### Test 3: Unit Tests

Create `internal/server/server_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/yourorg/truthtable-proxy/internal/config"
)

func TestNew(t *testing.T) {
	gin.SetMode(gin.TestMode)
	
	cfg := &config.ServerConfig{
		Port: 8080,
		Mode: "test",
	}

	srv := New(cfg)

	assert.NotNil(t, srv)
	assert.NotNil(t, srv.Engine())
}

func TestHealthEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	
	cfg := &config.ServerConfig{
		Port: 8080,
		Mode: "test",
	}

	srv := New(cfg)
	
	// Register a test health endpoint
	srv.Engine().GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "alive"})
	})

	// Test the endpoint
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	srv.Engine().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "alive")
}
```

Run tests:
```bash
go test ./internal/... -v
```

---

## 📁 Directory Structure So Far

```
backend-go/
├── cmd/
│   └── proxy/
│       └── main.go
├── internal/
│   ├── config/
│   │   └── config.go
│   ├── handlers/
│   │   └── health.go
│   ├── logger/
│   │   └── logger.go
│   └── server/
│       ├── server.go
│       └── server_test.go
├── go.mod
└── go.sum
```

---

## 🐛 Common Issues

### Issue: "package not found"

**Solution:** Run `go mod tidy` to sync dependencies.

### Issue: Port already in use

**Solution:** Change port via environment:
```bash
TRUTHTABLE_SERVER_PORT=9090 ./bin/proxy
```

### Issue: Permission denied on port 80

**Solution:** Use a port > 1024 or run with sudo (not recommended).

---

## ⏭️ Next Step

Continue to [Step 2.2: Reverse Proxy](step-2.2-reverse-proxy.md) to forward requests to the upstream LLM.
