package websocket

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// WSMessage is the wrapper format expected by the frontend
type WSMessage struct {
	Type      string      `json:"type"`
	Timestamp string      `json:"timestamp"`
	Data      interface{} `json:"data,omitempty"`
}

// AuditResult matches the frontend's expected audit result format
type AuditResult struct {
	AuditID               string              `json:"audit_id"`
	RequestID             string              `json:"request_id"`
	UserQuery             string              `json:"user_query"`
	LLMResponse           string              `json:"llm_response"`
	FaithfulnessScore     float64             `json:"faithfulness_score"`
	RelevancyScore        float64             `json:"relevancy_score"`
	OverallScore          float64             `json:"overall_score"`
	HallucinationDetected bool                `json:"hallucination_detected"`
	Claims                []ClaimVerification `json:"claims"`
	ReasoningTrace        string              `json:"reasoning_trace"`
	ProcessingTimeMs      int64               `json:"processing_time_ms"`
	Timestamp             string              `json:"timestamp"`
	Provider              string              `json:"provider,omitempty"`
	Model                 string              `json:"model,omitempty"`
}

// ClaimVerification matches the frontend's claim format
type ClaimVerification struct {
	Claim      string   `json:"claim"`
	Status     string   `json:"status"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
}

// Legacy AuditEvent for internal use
type AuditEvent struct {
	Type       string      `json:"type"`
	RequestID  string      `json:"request_id"`
	Timestamp  time.Time   `json:"timestamp"`
	Model      string      `json:"model,omitempty"`
	Prompt     string      `json:"prompt,omitempty"`
	Response   string      `json:"response,omitempty"`
	TrustScore float64     `json:"trust_score,omitempty"`
	Claims     []ClaimInfo `json:"claims,omitempty"`
	Duration   int64       `json:"duration_ms,omitempty"`
	Error      string      `json:"error,omitempty"`
}

type ClaimInfo struct {
	Text       string  `json:"text"`
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	id   string
}

type Hub struct {
	clients          map[*Client]bool
	broadcast        chan *AuditEvent
	register         chan *Client
	unregister       chan *Client
	mu               sync.RWMutex
	totalConnections int
	totalBroadcasts  int
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *AuditEvent, 100),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.totalConnections++
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("WebSocket client connected (id: %s, total: %d)", client.id, count)

			welcome := &AuditEvent{
				Type:      "connected",
				RequestID: client.id,
				Timestamp: time.Now(),
			}
			if data, err := json.Marshal(welcome); err == nil {
				select {
				case client.send <- data:
				default:
				}
			}

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("WebSocket client disconnected (id: %s, remaining: %d)", client.id, count)

		case event := <-h.broadcast:
			h.mu.Lock()
			h.totalBroadcasts++
			h.mu.Unlock()

			data, err := json.Marshal(event)
			if err != nil {
				log.Printf("Failed to marshal broadcast event: %v", err)
				continue
			}

			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Broadcast(event *AuditEvent) {
	select {
	case h.broadcast <- event:
	default:
		log.Printf("Broadcast channel full, dropping event")
	}
}

// BroadcastAuditResult sends an audit result in the format expected by the frontend
func (h *Hub) BroadcastAuditResult(result *AuditResult) {
	msg := WSMessage{
		Type:      "audit_result",
		Timestamp: time.Now().Format(time.RFC3339),
		Data:      result,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal audit result: %v", err)
		return
	}

	h.mu.Lock()
	h.totalBroadcasts++
	h.mu.Unlock()

	h.mu.RLock()
	clientCount := len(h.clients)
	for client := range h.clients {
		select {
		case client.send <- data:
		default:
			// Client buffer full, will be cleaned up
		}
	}
	h.mu.RUnlock()

	log.Printf("Broadcast audit result %s to %d clients (score: %.2f)",
		result.AuditID, clientCount, result.OverallScore)
}

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) Stats() (connections, broadcasts int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.totalConnections, h.totalBroadcasts
}

func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	clientID := r.URL.Query().Get("client_id")
	if clientID == "" {
		clientID = generateClientID()
	}

	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
		id:   clientID,
	}

	hub.register <- client

	go client.writePump()
	go client.readPump()
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512 * 1024)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
		log.Printf("Received message from client %s: %s", c.id, string(message))
	}
}

func generateClientID() string {
	return time.Now().Format("20060102-150405.000")
}
