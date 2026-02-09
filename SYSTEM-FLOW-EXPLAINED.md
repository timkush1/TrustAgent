# TrustAgent System Flow - Complete Guide for Junior Engineers

## Table of Contents
1. [System Overview](#system-overview)
2. [Key Concepts Explained](#key-concepts-explained)
3. [File-by-File Breakdown](#file-by-file-breakdown)
4. [Complete Request Flow](#complete-request-flow)
5. [Real-World Example](#real-world-example)

---

## System Overview

### What Does This System Do?

TrustAgent is a **proxy server** that sits between a client (like a web app) and an LLM (Large Language Model) API. It intercepts LLM requests, captures the responses, and audits them for truthfulness in the background.

```
┌─────────┐         ┌──────────────┐         ┌─────────────┐
│ Client  │ ──────> │  TrustAgent  │ ──────> │ LLM API     │
│ (Web)   │         │  (Go Proxy)  │         │ (Ollama)    │
└─────────┘         └──────────────┘         └─────────────┘
     ^                     │                        
     │                     v                        
     │              ┌──────────────┐               
     │              │ Audit Engine │               
     │              │ (Python)     │               
     │              └──────────────┘               
     │                     │                        
     └─────────────────────┘                        
        (WebSocket - real-time results)
```

### The Four Main Components

1. **main.go** - The application starter (entry point)
2. **handler.go** - The request interceptor (proxy logic)
3. **pool.go** - The background worker manager (async processing)
4. **hub.go** - The real-time broadcaster (WebSocket manager)

---

## Key Concepts Explained

### 1. What is a Proxy?

A **proxy** is a middleman server that sits between a client and a destination server.

**Without Proxy:**
```
Client ──────────> LLM API
```

**With Proxy:**
```
Client ──────> Proxy ──────> LLM API
              (intercepts)
```

**Why use a proxy?**
- Capture and inspect requests/responses
- Add extra processing (auditing)
- Monitor and log traffic
- Don't need to modify the client code

### 2. What is a WebSocket?

**Traditional HTTP:**
- Client asks, server responds, connection closes
- One request = one response
- Client must keep asking for updates (polling)

```
Client: "Any updates?" → Server: "No"
Client: "Any updates?" → Server: "No"
Client: "Any updates?" → Server: "Yes, here's data!"
```

**WebSocket:**
- Persistent two-way connection
- Server can push data anytime
- Real-time communication

```
Client ←──────────→ Server
     (connection stays open)

Server can send: "Here's an update!"
Client receives: Immediately!
```

**Our Use Case:**
When an audit completes (which takes seconds), we push the result to the dashboard immediately via WebSocket instead of the dashboard asking "Is it done? Is it done? Is it done?"

### 3. What is a Worker Pool?

Imagine a restaurant:
- **Without worker pool**: One chef makes all orders one by one (slow!)
- **With worker pool**: 5 chefs work simultaneously on different orders (fast!)

**Worker Pool Pattern:**
```
┌─────────────────────────────────────┐
│         Job Queue (Channel)         │
│  [Job 1] [Job 2] [Job 3] [Job 4]   │
└─────────────────────────────────────┘
           │        │        │
           v        v        v
      Worker-1  Worker-2  Worker-3
      (running) (running) (running)
```

**Benefits:**
- **Concurrency**: Multiple jobs processed simultaneously
- **Bounded resources**: Limit how many jobs run at once
- **Async processing**: Don't block the main request
- **Queue management**: Handle bursts of work

### 4. Go Channels

Think of a channel as a **pipe** or **conveyor belt** for passing data between goroutines (lightweight threads).

```go
// Create a pipe
queue := make(chan *Job, 100)

// Someone puts a job in the pipe
queue <- myJob

// Someone else takes it out
job := <-queue
```

**Key behaviors:**
- **Blocking**: If pipe is empty, reading waits. If pipe is full, writing waits.
- **Thread-safe**: Multiple goroutines can safely use the same channel
- **Closing**: `close(queue)` signals "no more data coming"

---

## File-by-File Breakdown

## 1. main.go - The Application Starter

### Purpose
This is the **entry point** of the application. When you run the program, this is what executes first. It sets up all the components and starts all the servers.

### Step-by-Step Breakdown

#### Step 1: Load Configuration
```go
cfg := config.Load()
```
- Reads settings (ports, URLs, timeouts) from environment variables or defaults
- Example: Server port (8080), WebSocket port (8081), upstream URL

#### Step 2: Connect to Audit Engine (gRPC)
```go
auditClient, err := grpc.NewAuditClient(cfg.GRPCAddress, cfg.GRPCTimeout)
```
- **gRPC** is a way for Go code to call Python code (the audit engine)
- Creates a connection to the Python service
- If it fails, proxy still works but without auditing

**Why this design?**
- Audit engine is a separate Python service (uses LangGraph, AI models)
- Go handles fast networking, Python handles AI logic
- They communicate via gRPC (fast binary protocol)

#### Step 3: Create WebSocket Hub
```go
wsHub := websocket.NewHub()
go wsHub.Run()
```
- Creates the WebSocket manager
- `go wsHub.Run()` starts it in a **goroutine** (background thread)
- This runs forever, managing WebSocket connections

#### Step 4: Create Worker Pool
```go
workerPool := worker.NewPool(cfg.WorkerCount, cfg.QueueSize, auditClient, wsHub)
go workerPool.Start()
```
- Creates a pool of workers (default: 5 workers)
- Workers will process audit jobs in the background
- Needs access to:
  - `auditClient` - to call the audit engine
  - `wsHub` - to broadcast results via WebSocket

#### Step 5: Create Proxy Handler
```go
proxyHandler := proxy.NewHandler(cfg.UpstreamURL, workerPool)
```
- Creates the request interceptor
- Knows where to forward requests (upstream URL)
- Can submit jobs to the worker pool

#### Step 6: Setup HTTP Routes
```go
router := gin.New()
router.POST("/v1/chat/completions", proxyHandler.HandleChatCompletion)
router.GET("/ws", func(c *gin.Context) {
    websocket.ServeWS(wsHub, c.Writer, c.Request)
})
```

**Routes defined:**
- `POST /v1/chat/completions` - Main LLM endpoint (with auditing)
- `POST /v1/completions` - Legacy LLM endpoint
- `GET /v1/models` - Pass-through (no auditing needed)
- `GET /health` - Health check
- `GET /ws` - WebSocket connection endpoint

#### Step 7: Start Servers
```go
go func() {
    httpServer.ListenAndServe()  // Port 8080 - HTTP API
}()

go func() {
    wsServer.ListenAndServe()    // Port 8081 - WebSocket
}()
```
- Starts two servers simultaneously using goroutines
- HTTP server handles API requests
- WebSocket server handles real-time connections

#### Step 8: Wait for Shutdown Signal
```go
quit := make(chan os.Signal, 1)
signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
<-quit  // Blocks until Ctrl+C
```
- Program runs forever until you press Ctrl+C
- Then gracefully shuts down all components

**Graceful Shutdown:**
- Finish processing current requests
- Close worker pool
- Close gRPC connection
- Stop servers cleanly

---

## 2. handler.go - The Request Interceptor

### Purpose
This is the **core proxy logic**. It intercepts LLM requests, forwards them to the real LLM API, captures the response, and submits it for auditing.

### Key Structures

#### ChatCompletionRequest
```go
type ChatCompletionRequest struct {
    Model       string        `json:"model"`        // e.g., "gpt-4"
    Messages    []ChatMessage `json:"messages"`     // Conversation history
    Stream      bool          `json:"stream"`       // Streaming mode?
    Temperature float64       `json:"temperature"`  // Randomness
}
```
This matches the OpenAI API format, so it's compatible with any OpenAI-compatible LLM.

#### ChatMessage
```go
type ChatMessage struct {
    Role    string `json:"role"`     // "system", "user", "assistant"
    Content string `json:"content"`  // The actual text
}
```

### Main Flow: HandleChatCompletion()

This is called when a client sends `POST /v1/chat/completions`.

#### Phase 1: Receive and Parse Request
```go
// Generate request ID for tracking
requestID := c.GetHeader("X-Request-ID")
if requestID == "" {
    requestID = uuid.New().String()
}

// Read the request body
bodyBytes, err := io.ReadAll(c.Request.Body)

// Parse JSON to understand what's being asked
var chatReq ChatCompletionRequest
json.Unmarshal(bodyBytes, &chatReq)
```

**Why read the body?**
- We need to inspect the prompt (what the user asked)
- We'll audit it later against the response
- But we also need to forward it to the LLM, so we keep the raw bytes

#### Phase 2: Extract the Prompt
```go
prompt := extractPrompt(chatReq.Messages)
```

**Example:**
```
Messages:
  [system]: You are a helpful assistant
  [user]: What is the capital of France?

Extracted prompt:
  "[system]: You are a helpful assistant\n[user]: What is the capital of France?"
```

#### Phase 3: Test Mode (Optional)
```go
if chatReq.TestResponse != "" {
    h.handleTestResponse(c, requestID, prompt, chatReq)
    return
}
```
- Allows testing without calling real LLM API
- Useful for development and demos

#### Phase 4: Forward to Upstream LLM
```go
// Build upstream URL (e.g., http://localhost:11434/v1/chat/completions)
upstreamURL := *h.upstreamURL
upstreamURL.Path = c.Request.URL.Path

// Create new request to forward
proxyReq, err := http.NewRequest("POST", upstreamURL.String(), bytes.NewReader(bodyBytes))

// Copy all headers (important: API keys!)
for key, values := range c.Request.Header {
    proxyReq.Header.Add(key, value)
}

// Send to LLM
resp, err := h.httpClient.Do(proxyReq)
```

**What's happening:**
1. Client sends request to us (proxy)
2. We create a new request to the real LLM
3. Copy everything (body, headers) so LLM thinks it's the original client
4. Send it and get response

#### Phase 5: Handle Response (Two Modes)

**Mode 1: Non-Streaming (Simple JSON Response)**
```go
if !chatReq.Stream {
    h.handleNonStreamingResponse(c, resp, requestID, prompt, chatReq)
}
```

**Flow:**
1. Read entire response body
2. Parse JSON to extract assistant's message
3. Submit audit job with prompt + response
4. Return response to client

**Code:**
```go
func (h *Handler) handleNonStreamingResponse(...) {
    // Read full response
    bodyBytes, err := io.ReadAll(resp.Body)
    
    // Parse to get assistant message
    var chatResp ChatCompletionResponse
    json.Unmarshal(bodyBytes, &chatResp)
    responseContent := chatResp.Choices[0].Message.Content
    
    // Submit for audit
    job := &worker.AuditJob{
        RequestID: requestID,
        Prompt:    prompt,
        Response:  responseContent,
        Model:     req.Model,
        Timestamp: time.Now(),
    }
    h.workerPool.Submit(job)
    
    // Send to client
    c.Data(resp.StatusCode, "application/json", bodyBytes)
}
```

**Mode 2: Streaming (Server-Sent Events)**
```go
if chatReq.Stream {
    h.handleStreamingResponse(c, resp, requestID, prompt, chatReq)
}
```

**Why streaming is harder:**
Response comes in chunks like this:
```
data: {"choices":[{"delta":{"content":"The"}}]}
data: {"choices":[{"delta":{"content":" capital"}}]}
data: {"choices":[{"delta":{"content":" is"}}]}
data: {"choices":[{"delta":{"content":" Paris"}}]}
data: [DONE]
```

**TeeWriter Pattern:**
```go
tee := NewTeeWriter()

c.Stream(func(w io.Writer) bool {
    buf := make([]byte, 1024)
    n, err := resp.Body.Read(buf)
    if n > 0 {
        w.Write(buf[:n])     // Send to client
        tee.Write(buf[:n])   // Capture for audit
    }
    return err == nil
})

// After streaming complete
fullResponse := tee.String()
extractedResponse := extractStreamingContent(fullResponse)
h.workerPool.Submit(job)
```

**What's happening:**
1. Read chunk from upstream LLM
2. Immediately send to client (real-time!)
3. Also save to TeeWriter (for later audit)
4. Repeat until `[DONE]`
5. Extract content from all chunks
6. Submit complete response for audit

---

## 3. pool.go - The Worker Pool Manager

### Purpose
Manages a pool of **workers** that process audit jobs in the background without blocking the main request flow.

### Why Do We Need This?

**Problem:**
```go
// BAD: This blocks the HTTP response for 5 seconds!
func HandleRequest(c *gin.Context) {
    response := callLLM()
    auditResult := doAudit(response)  // Takes 5 seconds!
    c.JSON(200, response)
}
```

**Solution:**
```go
// GOOD: Return immediately, audit in background
func HandleRequest(c *gin.Context) {
    response := callLLM()
    workerPool.Submit(auditJob)  // Non-blocking!
    c.JSON(200, response)        // Returns immediately
}
```

### Structure

```go
type Pool struct {
    workers     int                    // How many workers? (e.g., 5)
    queue       chan *AuditJob        // Job queue (buffered channel)
    auditClient *grpc.AuditClient     // How to call audit engine
    wsHub       *websocket.Hub        // Where to send results
    wg          sync.WaitGroup        // Wait for workers to finish
    ctx         context.Context       // Cancellation signal
    cancel      context.CancelFunc    // Trigger cancellation
}
```

### Creating the Pool

```go
func NewPool(numWorkers, queueSize int, client *grpc.AuditClient, hub *websocket.Hub) *Pool {
    ctx, cancel := context.WithCancel(context.Background())
    return &Pool{
        workers:     numWorkers,                      // e.g., 5
        queue:       make(chan *AuditJob, queueSize), // e.g., 100 job buffer
        auditClient: client,
        wsHub:       hub,
        ctx:         ctx,
        cancel:      cancel,
    }
}
```

**Buffered Channel:**
```go
queue := make(chan *AuditJob, 100)
```
- Can hold up to 100 jobs before blocking
- Like a waiting room with 100 seats
- If full, new submissions are dropped (logged)

### Starting the Workers

```go
func (p *Pool) Start() {
    for i := 0; i < p.workers; i++ {
        p.wg.Add(1)
        go p.worker(i)  // Start worker goroutine
    }
}
```

**What happens:**
1. Creates 5 goroutines (if workers=5)
2. Each runs `p.worker(i)` function
3. All run concurrently (in parallel)

### Worker Loop

```go
func (p *Pool) worker(id int) {
    defer p.wg.Done()  // Signal completion on exit
    
    for {
        select {
        case <-p.ctx.Done():
            // Shutdown signal received
            return
            
        case job, ok := <-p.queue:
            if !ok {
                // Queue closed
                return
            }
            p.processJob(id, job)  // Do the work!
        }
    }
}
```

**How it works:**

1. **Wait for a job**: `job := <-p.queue`
   - Worker blocks here until a job arrives
   - When job arrives, worker grabs it

2. **Process the job**: `p.processJob(id, job)`
   - Call audit engine
   - Get result
   - Broadcast via WebSocket

3. **Repeat**: Go back to waiting

**Multiple workers compete for jobs:**
```
Queue: [Job1] [Job2] [Job3] [Job4] [Job5]
          │      │      │      │      │
          v      v      v      v      v
       Worker1 Worker2 Worker3 Worker4 Worker5
```

### Submitting Jobs

```go
func (p *Pool) Submit(job *AuditJob) {
    select {
    case p.queue <- job:
        log.Printf("Job submitted")
    default:
        log.Printf("Queue full, dropping job")
    }
}
```

**Non-blocking submission:**
- If queue has space: Job added
- If queue full: Job dropped (logged)
- Never blocks the HTTP handler

### Processing Jobs

```go
func (p *Pool) processJob(workerID int, job *AuditJob) {
    startTime := time.Now()
    
    // Call the Python audit engine via gRPC
    result, err := p.auditClient.Evaluate(
        p.ctx,
        job.RequestID,
        job.Prompt,
        job.Response,
    )
    
    if err != nil {
        // Broadcast error
        p.wsHub.Broadcast(&websocket.AuditEvent{
            Type:  "audit_error",
            Error: err.Error(),
        })
        return
    }
    
    duration := time.Since(startTime)
    
    // Convert to frontend format
    auditResult := &websocket.AuditResult{
        AuditID:           job.RequestID,
        FaithfulnessScore: result.TrustScore,
        Claims:            convertClaims(result.Claims),
        ProcessingTimeMs:  duration.Milliseconds(),
        // ... more fields
    }
    
    // Broadcast to all connected WebSocket clients
    p.wsHub.BroadcastAuditResult(auditResult)
}
```

**What happens:**
1. Call audit engine (Python service via gRPC)
2. Wait for result (may take 1-5 seconds)
3. Convert result to frontend format
4. Broadcast to WebSocket clients (dashboard sees it!)

### Stopping the Pool

```go
func (p *Pool) Stop() {
    p.cancel()       // Signal all workers to stop
    close(p.queue)   // Close the queue
    p.wg.Wait()      // Wait for all workers to finish
}
```

**Graceful shutdown:**
1. `p.cancel()` - All workers see `ctx.Done()` signal
2. `close(p.queue)` - No new jobs accepted
3. `p.wg.Wait()` - Wait for current jobs to complete

---

## 4. hub.go - The WebSocket Manager

### Purpose
Manages WebSocket connections and broadcasts audit results to connected clients (the dashboard) in real-time.

### What Problem Does It Solve?

**Without WebSocket:**
```
Dashboard: "Any audit results?" (HTTP poll every 2 seconds)
Server:    "No"
Dashboard: "Any audit results?"
Server:    "No"
Dashboard: "Any audit results?"
Server:    "Yes! Here's the result"
```
- Wastes bandwidth
- Delayed updates
- Server overhead

**With WebSocket:**
```
Dashboard ←──────────→ Server (persistent connection)

[5 seconds later]
Server:    "New audit result!" (pushed instantly)
Dashboard: Receives and displays immediately
```

### Structure

```go
type Hub struct {
    clients    map[*Client]bool     // All connected clients
    broadcast  chan *AuditEvent     // Channel for broadcast messages
    register   chan *Client         // Channel for new connections
    unregister chan *Client         // Channel for disconnections
    mu         sync.RWMutex         // Thread-safe access to clients
}
```

### The Client

```go
type Client struct {
    hub  *Hub                  // Reference to hub
    conn *websocket.Conn      // WebSocket connection
    send chan []byte          // Outbound message queue
    id   string               // Client identifier
}
```

Each connected dashboard has a Client instance.

### Creating the Hub

```go
func NewHub() *Hub {
    return &Hub{
        clients:    make(map[*Client]bool),
        broadcast:  make(chan *AuditEvent, 100),
        register:   make(chan *Client),
        unregister: make(chan *Client),
    }
}
```

### The Hub Loop

This is the **heart** of the WebSocket system. It runs forever, handling three types of events:

```go
func (h *Hub) Run() {
    for {
        select {
        case client := <-h.register:
            // New client connected
            h.clients[client] = true
            log.Printf("Client connected (total: %d)", len(h.clients))
            
            // Send welcome message
            welcome := &AuditEvent{Type: "connected"}
            client.send <- marshalJSON(welcome)
            
        case client := <-h.unregister:
            // Client disconnected
            if _, ok := h.clients[client]; ok {
                delete(h.clients, client)
                close(client.send)
            }
            log.Printf("Client disconnected (remaining: %d)", len(h.clients))
            
        case event := <-h.broadcast:
            // Broadcast message to all clients
            data := marshalJSON(event)
            for client := range h.clients {
                select {
                case client.send <- data:
                    // Sent successfully
                default:
                    // Client buffer full, close it
                    close(client.send)
                    delete(h.clients, client)
                }
            }
        }
    }
}
```

**Channel-based coordination:**
- All operations go through channels
- Thread-safe without manual locking
- Clean separation of concerns

### Connecting a Client

When dashboard opens WebSocket connection:

```go
func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
    // Upgrade HTTP connection to WebSocket
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        return
    }
    
    // Create client
    client := &Client{
        hub:  hub,
        conn: conn,
        send: make(chan []byte, 256),  // Buffered channel
        id:   generateClientID(),
    }
    
    // Register with hub
    hub.register <- client
    
    // Start two goroutines for this client
    go client.writePump()  // Sends messages to client
    go client.readPump()   // Receives messages from client
}
```

**Two pumps per client:**

1. **writePump**: Sends messages from server to client
2. **readPump**: Receives messages from client to server

### Write Pump

Sends messages to the client:

```go
func (c *Client) writePump() {
    ticker := time.NewTicker(30 * time.Second)
    defer func() {
        ticker.Stop()
        c.conn.Close()
    }()
    
    for {
        select {
        case message, ok := <-c.send:
            if !ok {
                // Channel closed, client disconnected
                c.conn.WriteMessage(websocket.CloseMessage, []byte{})
                return
            }
            
            // Write message to WebSocket
            w, err := c.conn.NextWriter(websocket.TextMessage)
            if err != nil {
                return
            }
            w.Write(message)
            
            // Batch multiple messages if available
            n := len(c.send)
            for i := 0; i < n; i++ {
                w.Write([]byte{'\n'})
                w.Write(<-c.send)
            }
            
            w.Close()
            
        case <-ticker.C:
            // Send ping to keep connection alive
            c.conn.WriteMessage(websocket.PingMessage, nil)
        }
    }
}
```

**Key points:**
- Reads from `c.send` channel
- Writes to WebSocket connection
- Sends ping every 30 seconds (keep-alive)
- Batches multiple messages if available

### Read Pump

Receives messages from the client:

```go
func (c *Client) readPump() {
    defer func() {
        c.hub.unregister <- c
        c.conn.Close()
    }()
    
    c.conn.SetReadLimit(512 * 1024)  // Max message size
    c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
    c.conn.SetPongHandler(func(string) error {
        c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
        return nil
    })
    
    for {
        _, message, err := c.conn.ReadMessage()
        if err != nil {
            break  // Connection closed
        }
        log.Printf("Received: %s", string(message))
        // In this system, we mostly ignore client messages
    }
}
```

**Purpose:**
- Detects when client disconnects
- Responds to pings
- Could handle client commands (not used much here)

### Broadcasting Audit Results

When worker completes an audit:

```go
func (h *Hub) BroadcastAuditResult(result *AuditResult) {
    // Wrap in WebSocket message format
    msg := WSMessage{
        Type:      "audit_result",
        Timestamp: time.Now().Format(time.RFC3339),
        Data:      result,
    }
    
    data, _ := json.Marshal(msg)
    
    // Send to all connected clients
    for client := range h.clients {
        select {
        case client.send <- data:
            // Queued successfully
        default:
            // Client buffer full, skip
        }
    }
    
    log.Printf("Broadcast audit result to %d clients", len(h.clients))
}
```

**Flow:**
1. Worker calls `hub.BroadcastAuditResult(result)`
2. Hub marshals to JSON
3. Sends to all connected clients' `send` channels
4. Each client's `writePump` sends it over WebSocket
5. Dashboard receives and displays instantly!

---

## Complete Request Flow

Let's trace a complete request from start to finish.

### Scenario
User asks ChatGPT (via our proxy): "What is the capital of France?"

### Step-by-Step Flow

#### 1. Client Sends Request
```http
POST http://localhost:8080/v1/chat/completions
Content-Type: application/json

{
  "model": "gpt-4",
  "messages": [
    {"role": "user", "content": "What is the capital of France?"}
  ]
}
```

#### 2. Proxy Receives (handler.go)
```go
// HandleChatCompletion() called
requestID := "abc-123"
bodyBytes := read request body
chatReq := parse JSON

prompt := "What is the capital of France?"
```

#### 3. Proxy Forwards to LLM
```go
// Build request to real LLM (Ollama)
proxyReq := new request to http://localhost:11434/v1/chat/completions
copy all headers and body

// Send it
resp := httpClient.Do(proxyReq)
```

#### 4. LLM Responds
```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "The capital of France is Paris."
    }
  }]
}
```

#### 5. Proxy Captures Response
```go
// Read response
bodyBytes := read response body
chatResp := parse JSON

responseContent := "The capital of France is Paris."
```

#### 6. Proxy Submits Audit Job
```go
job := &AuditJob{
    RequestID: "abc-123",
    Prompt:    "What is the capital of France?",
    Response:  "The capital of France is Paris.",
    Model:     "gpt-4",
    Timestamp: time.Now(),
}

workerPool.Submit(job)
// ✓ Non-blocking! Returns immediately
```

#### 7. Proxy Returns Response to Client
```go
c.JSON(200, chatResp)
// Client gets response in ~500ms
```

**Client is happy! Got fast response.**

#### 8. Worker Picks Up Job (pool.go)
```go
// Worker 3 was idle, grabs the job from queue
worker(3): job := <-p.queue
worker(3): processJob(job)
```

#### 9. Worker Calls Audit Engine (gRPC to Python)
```go
result, err := auditClient.Evaluate(
    ctx,
    "abc-123",
    "What is the capital of France?",
    "The capital of France is Paris.",
)
```

**In Python (backend-python/src/truthtable/grpc/server.py):**
```python
# Python service receives gRPC call
audit_id = str(uuid.uuid4())

# Run the audit graph (LangGraph workflow)
result = await run_audit(
    graph=audit_graph,
    request_id="abc-123",
    user_query="What is the capital of France?",
    llm_response="The capital of France is Paris.",
    context_docs=[]
)

# Returns:
# {
#   "faithfulness_score": 0.95,
#   "claims": [
#     {"claim": "Capital is Paris", "verdict": "supported"}
#   ]
# }
```

#### 10. Worker Receives Result (pool.go)
```go
// Result received after ~2 seconds
result := {
    TrustScore: 0.95,
    Claims: [
        {Text: "Capital is Paris", Verdict: "supported", Confidence: 0.95}
    ]
}

duration := 2.3 seconds
```

#### 11. Worker Broadcasts via WebSocket
```go
auditResult := &websocket.AuditResult{
    AuditID:           "abc-123",
    FaithfulnessScore: 0.95,
    OverallScore:      0.95,
    Claims: [
        {Claim: "Capital is Paris", Status: "SUPPORTED", Confidence: 0.95}
    ],
    ProcessingTimeMs: 2300,
}

wsHub.BroadcastAuditResult(auditResult)
```

#### 12. WebSocket Hub Broadcasts (hub.go)
```go
// Hub sends to all connected dashboard clients
msg := {
    "type": "audit_result",
    "timestamp": "2024-02-07T10:30:45Z",
    "data": auditResult
}

for client := range clients {
    client.send <- marshal(msg)
}
```

#### 13. Dashboard Receives Update
```javascript
// Frontend WebSocket listener
ws.onmessage = (event) => {
    const msg = JSON.parse(event.data)
    if (msg.type === "audit_result") {
        displayAuditResult(msg.data)
        // Shows: ✓ Truthful (95% confidence)
    }
}
```

### Timeline

```
t=0ms      Client sends request
t=50ms     Proxy forwards to LLM
t=500ms    LLM responds
t=510ms    Proxy returns to client ✓ (client happy!)
t=510ms    Audit job queued
t=511ms    Worker picks up job
t=2800ms   Audit completes
t=2801ms   WebSocket broadcast
t=2802ms   Dashboard updates ✓ (shows audit result!)
```

---

## Real-World Example

### Scenario: Detecting Hallucination

**User asks:** "What is the capital of Australia?"

**LLM responds (incorrectly):** "The capital of Australia is Sydney."

### Flow

1. **Client Request** → Proxy
2. **Proxy** → LLM: "What is capital?"
3. **LLM** → Proxy: "Sydney" (wrong!)
4. **Proxy** → Client: "Sydney" (returns immediately)
5. **Proxy** → Worker Pool: Submit audit job
6. **Worker** → Python Audit Engine:
   ```python
   # Audit engine checks
   claim = "Capital of Australia is Sydney"
   
   # Uses RAG, searches knowledge base
   # Finds: "Capital of Australia is Canberra"
   
   verdict = "unsupported"
   confidence = 0.85
   ```
7. **Worker** → WebSocket Hub:
   ```go
   result := AuditResult{
       FaithfulnessScore: 0.2,  // Low score!
       HallucinationDetected: true,
       Claims: [
           {
               Claim: "Capital is Sydney",
               Status: "UNSUPPORTED",
               Confidence: 0.85
           }
       ]
   }
   ```
8. **Hub** → Dashboard: Broadcast in real-time
9. **Dashboard shows:**
   ```
   ⚠️ Hallucination Detected!
   Score: 20%
   Claim: "Capital is Sydney" - UNSUPPORTED
   Correct: "Capital is Canberra"
   ```

### Why This Architecture Works

1. **Fast Response**: Client gets LLM response in ~500ms
2. **Async Audit**: Audit happens in background (~2-5s)
3. **Real-time Updates**: Dashboard sees results instantly
4. **Scalable**: Worker pool handles bursts (100 requests/sec)
5. **Resilient**: If audit fails, client still gets response

---

## Key Takeaways

### For Understanding the System:

1. **Proxy Pattern**: Sit in the middle, don't change client/server
2. **Worker Pool**: Background processing, don't block requests
3. **WebSocket**: Real-time push, no polling needed
4. **Channels**: Thread-safe communication in Go
5. **Goroutines**: Lightweight threads, run things concurrently

### Component Interactions:

```
┌────────────────────────────────────────────────────────────┐
│                         main.go                            │
│  (Orchestrates everything, starts all components)          │
└────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
              v               v               v
       ┌──────────┐    ┌──────────┐   ┌──────────┐
       │ handler  │───>│   pool   │──>│   hub    │
       │ (proxy)  │    │(workers) │   │(WebSocket│
       └──────────┘    └──────────┘   └──────────┘
            │               │               │
            v               v               v
        Forward         Call Audit     Broadcast
        to LLM          Engine          Results
```

### Request Journey:

```
Client Request
      │
      v
[handler.go] Intercept & Forward
      │
      ├──> LLM API (forward)
      │         │
      │         v
      │    LLM Response
      │         │
      └─────────┴──> Extract prompt & response
                │
                v
         [pool.go] Submit job to queue
                │
                v
         Worker picks up job
                │
                v
         Call Audit Engine (Python)
                │
                v
         Get audit result
                │
                v
         [hub.go] Broadcast via WebSocket
                │
                v
         Dashboard displays result
```

---

## Glossary

- **Proxy**: Middleman server that intercepts requests
- **WebSocket**: Persistent two-way connection for real-time communication
- **Worker Pool**: Group of background workers processing jobs concurrently
- **Channel**: Go's way of passing data between goroutines (thread-safe pipe)
- **Goroutine**: Lightweight thread in Go
- **gRPC**: High-performance RPC framework (Go ↔ Python communication)
- **TeeWriter**: Captures data while passing it through (like a "T" pipe fitting)
- **SSE**: Server-Sent Events (streaming format)
- **Buffered Channel**: Channel with a queue/waiting room
- **Context**: Cancellation and deadline mechanism in Go
- **Hub**: Central manager for WebSocket connections
- **Audit Engine**: Python service that checks LLM responses for truthfulness

---

This should give you a complete understanding of how the system works! Feel free to ask questions about any specific part.
