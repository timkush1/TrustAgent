# Step 2.6: WebSocket Hub

## 🎯 Goal

Implement a **WebSocket Hub** that broadcasts audit results to connected dashboard clients in real-time. This enables:

- Live updates as audits complete
- Multiple simultaneous dashboard connections
- Efficient pub/sub pattern

---

## 📚 Prerequisites

- Completed Step 2.5 (gRPC Client)
- Understanding of WebSocket protocol
- Familiarity with Go channels

---

## 🧠 Concepts Explained

### WebSocket vs HTTP

| HTTP | WebSocket |
|------|-----------|
| Request-Response | Bidirectional |
| Client initiates | Either side can send |
| Short-lived | Long-lived connection |
| Overhead per request | One-time handshake |

### The Hub Pattern

The Hub manages all WebSocket connections:

```
┌─────────────────────────────────────────────────────────┐
│                        Hub                              │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────┐   ┌─────────┐   ┌─────────┐               │
│  │Client 1 │   │Client 2 │   │Client 3 │   ...         │
│  └────┬────┘   └────┬────┘   └────┬────┘               │
│       │             │             │                     │
│       └─────────────┼─────────────┘                     │
│                     │                                   │
│              ┌──────▼──────┐                           │
│              │  Broadcast  │                           │
│              │   Channel   │                           │
│              └──────┬──────┘                           │
│                     │                                   │
│       ┌─────────────┼─────────────┐                    │
│       ↓             ↓             ↓                    │
│   Client 1      Client 2      Client 3                 │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Gorilla WebSocket

We use the `gorilla/websocket` library:
- Production-ready implementation
- Proper ping/pong handling
- Clean connection management

---

## 💻 Implementation

### Step 1: Install Dependencies

```bash
cd backend-go
go get github.com/gorilla/websocket
```

### Step 2: Create the Hub

Create `internal/websocket/hub.go`:

```go
package websocket

import (
	"encoding/json"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/logger"
)

// Hub manages WebSocket connections and broadcasts
type Hub struct {
	// Registered clients
	clients map[*Client]bool
	
	// Inbound messages from clients
	broadcast chan *Message
	
	// Register requests from clients
	register chan *Client
	
	// Unregister requests from clients
	unregister chan *Client
	
	// Mutex for clients map
	mu sync.RWMutex
	
	// Running state
	running bool
}

// Message represents a WebSocket message
type Message struct {
	Type      string      `json:"type"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
}

// NewHub creates a new Hub instance
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *Message, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's main loop
func (h *Hub) Run() {
	log := logger.Get()
	log.Info("Starting WebSocket hub")
	
	h.running = true
	
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			
			log.Debug("Client registered",
				zap.String("client_id", client.ID),
				zap.Int("total_clients", len(h.clients)),
			)
			
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			
			log.Debug("Client unregistered",
				zap.String("client_id", client.ID),
				zap.Int("total_clients", len(h.clients)),
			)
			
		case message := <-h.broadcast:
			h.broadcastMessage(message)
		}
	}
}

// broadcastMessage sends a message to all connected clients
func (h *Hub) broadcastMessage(message *Message) {
	log := logger.Get()
	
	// Serialize message once
	data, err := json.Marshal(message)
	if err != nil {
		log.Error("Failed to marshal message", zap.Error(err))
		return
	}
	
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()
	
	log.Debug("Broadcasting message",
		zap.String("type", message.Type),
		zap.Int("clients", len(clients)),
	)
	
	for _, client := range clients {
		select {
		case client.send <- data:
		default:
			// Client's buffer is full, close and remove
			h.mu.Lock()
			delete(h.clients, client)
			close(client.send)
			h.mu.Unlock()
		}
	}
}

// Broadcast sends a message to all clients
func (h *Hub) Broadcast(msgType string, payload interface{}) {
	message := &Message{
		Type:      msgType,
		Payload:   payload,
		Timestamp: time.Now(),
	}
	
	select {
	case h.broadcast <- message:
	default:
		logger.Get().Warn("Broadcast channel full, dropping message")
	}
}

// Register adds a client to the hub
func (h *Hub) Register(client *Client) {
	h.register <- client
}

// Unregister removes a client from the hub
func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

// ClientCount returns the number of connected clients
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// IsRunning returns true if the hub is running
func (h *Hub) IsRunning() bool {
	return h.running
}
```

### Step 3: Create the Client

Create `internal/websocket/client.go`:

```go
package websocket

import (
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/logger"
)

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second
	
	// Time allowed to read the next pong message from the peer
	pongWait = 60 * time.Second
	
	// Send pings to peer with this period (must be less than pongWait)
	pingPeriod = (pongWait * 9) / 10
	
	// Maximum message size allowed from peer
	maxMessageSize = 512 * 1024 // 512KB
	
	// Size of the send buffer
	sendBufferSize = 256
)

// Client represents a WebSocket connection
type Client struct {
	ID   string
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// NewClient creates a new WebSocket client
func NewClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		ID:   uuid.New().String(),
		hub:  hub,
		conn: conn,
		send: make(chan []byte, sendBufferSize),
	}
}

// ReadPump pumps messages from the WebSocket connection to the hub
func (c *Client) ReadPump() {
	log := logger.Get()
	
	defer func() {
		c.hub.Unregister(c)
		c.conn.Close()
	}()
	
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error("WebSocket read error",
					zap.String("client_id", c.ID),
					zap.Error(err),
				)
			}
			break
		}
		
		// Handle incoming messages (for future features like filtering)
		c.handleMessage(message)
	}
}

// WritePump pumps messages from the hub to the WebSocket connection
func (c *Client) WritePump() {
	log := logger.Get()
	ticker := time.NewTicker(pingPeriod)
	
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			
			if !ok {
				// Hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)
			
			// Add queued messages to the current WebSocket frame
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}
			
			if err := w.Close(); err != nil {
				return
			}
			
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Debug("Ping failed", zap.String("client_id", c.ID))
				return
			}
		}
	}
}

// handleMessage processes incoming messages from the client
func (c *Client) handleMessage(message []byte) {
	log := logger.Get()
	
	// For now, just log incoming messages
	log.Debug("Received client message",
		zap.String("client_id", c.ID),
		zap.Int("size", len(message)),
	)
	
	// Future: Handle subscription changes, filters, etc.
}
```

### Step 4: Create the Handler

Create `internal/websocket/handler.go`:

```go
package websocket

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/logger"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins in development
		// TODO: Restrict in production
		return true
	},
}

// Handler handles WebSocket upgrade requests
type Handler struct {
	hub *Hub
}

// NewHandler creates a new WebSocket handler
func NewHandler(hub *Hub) *Handler {
	return &Handler{hub: hub}
}

// HandleWebSocket upgrades HTTP to WebSocket
func (h *Handler) HandleWebSocket(c *gin.Context) {
	log := logger.Get()

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Error("WebSocket upgrade failed", zap.Error(err))
		return
	}

	client := NewClient(h.hub, conn)
	h.hub.Register(client)

	log.Info("New WebSocket client connected",
		zap.String("client_id", client.ID),
		zap.String("remote_addr", c.ClientIP()),
	)

	// Start client goroutines
	go client.WritePump()
	go client.ReadPump()
}

// RegisterRoutes registers WebSocket routes
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.GET("/ws", h.HandleWebSocket)
	r.GET("/api/ws", h.HandleWebSocket)
}
```

### Step 5: Define Audit Event Types

Create `internal/websocket/events.go`:

```go
package websocket

// Event types for WebSocket messages
const (
	EventAuditComplete = "audit_complete"
	EventAuditStarted  = "audit_started"
	EventAuditFailed   = "audit_failed"
	EventHealthUpdate  = "health_update"
	EventStatsUpdate   = "stats_update"
)

// AuditCompleteEvent is sent when an audit finishes
type AuditCompleteEvent struct {
	RequestID         string              `json:"request_id"`
	TrustScore        float64             `json:"trust_score"`
	HallucinationRate float64             `json:"hallucination_rate"`
	Grade             string              `json:"grade"`
	Verdict           string              `json:"verdict"`
	TotalClaims       int                 `json:"total_claims"`
	SupportedClaims   int                 `json:"supported_claims"`
	UnsupportedClaims int                 `json:"unsupported_claims"`
	Verifications     []ClaimVerification `json:"verifications"`
	ProcessingTimeMs  int                 `json:"processing_time_ms"`
	Timestamp         int64               `json:"timestamp"`
}

// ClaimVerification represents a verified claim
type ClaimVerification struct {
	Claim      string   `json:"claim"`
	Supported  bool     `json:"supported"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
	Reasoning  string   `json:"reasoning"`
}

// AuditStartedEvent is sent when an audit begins
type AuditStartedEvent struct {
	RequestID    string `json:"request_id"`
	ResponseSize int    `json:"response_size"`
	Timestamp    int64  `json:"timestamp"`
}

// AuditFailedEvent is sent when an audit fails
type AuditFailedEvent struct {
	RequestID string `json:"request_id"`
	Error     string `json:"error"`
	Timestamp int64  `json:"timestamp"`
}

// HealthUpdateEvent provides system health status
type HealthUpdateEvent struct {
	AuditService string `json:"audit_service"`
	QueueDepth   int    `json:"queue_depth"`
	Connections  int    `json:"connections"`
}

// StatsUpdateEvent provides runtime statistics
type StatsUpdateEvent struct {
	TotalAudits     int64   `json:"total_audits"`
	SuccessfulAudits int64  `json:"successful_audits"`
	FailedAudits     int64  `json:"failed_audits"`
	AverageScore     float64 `json:"average_score"`
	AverageTimeMs    int64   `json:"average_time_ms"`
}
```

### Step 6: Integrate with Main

Update `cmd/proxy/main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/client"
	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/handlers"
	"github.com/yourorg/truthtable-proxy/internal/logger"
	"github.com/yourorg/truthtable-proxy/internal/proxy"
	"github.com/yourorg/truthtable-proxy/internal/server"
	"github.com/yourorg/truthtable-proxy/internal/websocket"
	"github.com/yourorg/truthtable-proxy/internal/worker"
)

const version = "0.1.0"

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic("Failed to load config: " + err.Error())
	}

	logger.Init(cfg.Server.Mode)
	defer logger.Sync()

	log := logger.Get()
	log.Info("Starting TruthTable Proxy", zap.String("version", version))

	// Create WebSocket hub
	wsHub := websocket.NewHub()
	go wsHub.Run()

	// Create gRPC client
	var auditClient *client.AuditClient
	if cfg.Audit.Enabled {
		auditClient, err = client.NewAuditClient(&cfg.Audit)
		if err != nil {
			log.Warn("Failed to connect to audit service", zap.Error(err))
		} else {
			defer auditClient.Close()
		}
	}

	// Create audit handler
	auditHandler := worker.NewAuditHandler(auditClient, cfg.Audit.Timeout)

	// Create worker pool
	auditPool := worker.NewPool(worker.Config{
		Name:      "audit",
		Workers:   cfg.Audit.WorkerCount,
		QueueSize: cfg.Audit.QueueSize,
		Handler:   auditHandler.Handle,
	})
	auditPool.Start()

	// Process audit results and broadcast via WebSocket
	go processAuditResults(auditPool.Results(), wsHub)

	// Create server
	srv := server.New(&cfg.Server)

	// Register routes
	healthHandler := handlers.NewHealthHandler(version)
	healthHandler.RegisterRoutes(srv.Engine())

	wsHandler := websocket.NewHandler(wsHub)
	wsHandler.RegisterRoutes(srv.Engine())

	// Create proxy handler
	proxyHandler, err := proxy.NewHandler(&cfg.Upstream)
	if err != nil {
		log.Fatal("Failed to create proxy handler", zap.Error(err))
	}
	defer proxyHandler.Close()

	// Set response callback
	proxyHandler.SetResponseCallback(func(ctx context.Context, req *proxy.CapturedRequest, resp *proxy.CapturedResponse) {
		if auditClient == nil {
			return
		}

		// Notify WebSocket clients that audit started
		wsHub.Broadcast(websocket.EventAuditStarted, websocket.AuditStartedEvent{
			RequestID:    req.ID,
			ResponseSize: len(resp.Body),
			Timestamp:    time.Now().UnixMilli(),
		})

		auditJob := worker.AuditJob{
			RequestID:   req.ID,
			UserQuery:   extractUserQuery(req.Body),
			LLMResponse: string(resp.Body),
			Timestamp:   req.Timestamp,
		}

		job := worker.Job{
			ID:        req.ID,
			Payload:   auditJob,
			CreatedAt: time.Now(),
		}

		if !auditPool.Submit(job) {
			log.Warn("Failed to submit audit job", zap.String("request_id", req.ID))
		}
	})

	srv.Engine().NoRoute(proxyHandler.Handle)

	// Start server
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("Server error", zap.Error(err))
		}
	}()

	// Wait for shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down...")
	auditPool.Stop(cfg.Audit.Timeout)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Server shutdown error", zap.Error(err))
	}

	log.Info("Server stopped")
}

// processAuditResults broadcasts results via WebSocket
func processAuditResults(results <-chan worker.Result, hub *websocket.Hub) {
	log := logger.Get()

	for result := range results {
		if !result.Success {
			hub.Broadcast(websocket.EventAuditFailed, websocket.AuditFailedEvent{
				RequestID: result.JobID,
				Error:     result.Error.Error(),
				Timestamp: time.Now().UnixMilli(),
			})
			continue
		}

		auditResult, ok := result.Data.(*client.AuditResult)
		if !ok {
			continue
		}

		// Convert to WebSocket event
		verifications := make([]websocket.ClaimVerification, len(auditResult.Verifications))
		for i, v := range auditResult.Verifications {
			verifications[i] = websocket.ClaimVerification{
				Claim:      v.Claim,
				Supported:  v.Supported,
				Confidence: v.Confidence,
				Evidence:   v.Evidence,
				Reasoning:  v.Reasoning,
			}
		}

		event := websocket.AuditCompleteEvent{
			RequestID:         auditResult.RequestID,
			TrustScore:        auditResult.TrustScore,
			HallucinationRate: auditResult.HallucinationRate,
			Grade:             auditResult.Grade,
			Verdict:           auditResult.Verdict,
			TotalClaims:       auditResult.TotalClaims,
			SupportedClaims:   auditResult.SupportedClaims,
			UnsupportedClaims: auditResult.UnsupportedClaims,
			Verifications:     verifications,
			ProcessingTimeMs:  auditResult.ProcessingTimeMs,
			Timestamp:         time.Now().UnixMilli(),
		}

		hub.Broadcast(websocket.EventAuditComplete, event)

		log.Info("Audit result broadcast",
			zap.String("request_id", auditResult.RequestID),
			zap.Float64("trust_score", auditResult.TrustScore),
			zap.Int("clients", hub.ClientCount()),
		)
	}
}

func extractUserQuery(body []byte) string {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}
	if query, ok := data["query"].(string); ok {
		return query
	}
	if prompt, ok := data["prompt"].(string); ok {
		return prompt
	}
	return ""
}
```

---

## ✅ Testing

### Test 1: WebSocket Connection Test

Create a simple HTML test page:

```html
<!-- test-websocket.html -->
<!DOCTYPE html>
<html>
<head><title>WebSocket Test</title></head>
<body>
<h1>WebSocket Test</h1>
<div id="messages"></div>
<script>
const ws = new WebSocket('ws://localhost:8080/ws');

ws.onopen = () => {
    console.log('Connected');
    document.getElementById('messages').innerHTML += '<p>Connected!</p>';
};

ws.onmessage = (event) => {
    console.log('Message:', event.data);
    const msg = JSON.parse(event.data);
    document.getElementById('messages').innerHTML += 
        `<p><b>${msg.type}</b>: ${JSON.stringify(msg.payload)}</p>`;
};

ws.onerror = (error) => {
    console.error('WebSocket error:', error);
};
</script>
</body>
</html>
```

Open in browser and check console.

### Test 2: Unit Tests

Create `internal/websocket/hub_test.go`:

```go
package websocket

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHubBroadcast(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Wait for hub to start
	time.Sleep(50 * time.Millisecond)

	assert.True(t, hub.IsRunning())
	assert.Equal(t, 0, hub.ClientCount())

	// Test broadcast with no clients (should not panic)
	hub.Broadcast("test", map[string]string{"key": "value"})
}

func TestHubClientCount(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	time.Sleep(50 * time.Millisecond)

	// Mock clients
	client1 := &Client{
		ID:   "client-1",
		hub:  hub,
		send: make(chan []byte, 256),
	}
	client2 := &Client{
		ID:   "client-2",
		hub:  hub,
		send: make(chan []byte, 256),
	}

	hub.Register(client1)
	hub.Register(client2)

	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 2, hub.ClientCount())

	hub.Unregister(client1)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, hub.ClientCount())
}
```

---

## 🎉 Phase 2 Complete!

You've now built the complete Go Interceptor Proxy:

- ✅ Gin HTTP server with middleware
- ✅ Reverse proxy with response capture
- ✅ TeeWriter for streaming support
- ✅ Worker pool for async processing
- ✅ gRPC client to Python audit service
- ✅ WebSocket hub for real-time updates

---

## ⏭️ Next Phase

Continue to [Phase 3: React Dashboard](../phase-3/step-3.1-vite-setup.md) to build the real-time visualization interface.
