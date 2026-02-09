# Getting Started with TruthTable

> **Audience**: New developers joining the project  
> **Time to complete**: 15-30 minutes  
> **Prerequisites**: Docker, Go 1.21+, Python 3.11+, Node.js 18+

---

## Table of Contents

1. [What is TruthTable?](#1-what-is-truthtable)
2. [Prerequisites](#2-prerequisites)
3. [Clone and Setup](#3-clone-and-setup)
4. [Start All Services](#4-start-all-services)
5. [Verify Everything Works](#5-verify-everything-works)
6. [Send Your First Audit](#6-send-your-first-audit)
7. [Understanding the Output](#7-understanding-the-output)
8. [Stopping Services](#8-stopping-services)
9. [Troubleshooting](#9-troubleshooting)

---

## 1. What is TruthTable?

TruthTable is an **AI hallucination detection system**. It acts as a proxy between your application and LLM APIs (like OpenAI), intercepting responses and checking them for factual accuracy.

### The Problem It Solves

When you ask an LLM "What is 2+2?", it might respond:
> "2+2 equals 4. This was first discovered by Albert Einstein in 1905."

The first part is correct, but the second part is a **hallucination** - Einstein didn't discover basic arithmetic!

TruthTable catches these errors automatically.

### How It Works (30-second version)

```
Your App → Go Proxy → LLM (OpenAI/Ollama)
              ↓ (async)
         Python Engine
              ↓
         Dashboard shows: "50% accuracy - 1 hallucination detected"
```

---

## 2. Prerequisites

### Required Software

| Software | Version | Check Command | Install |
|----------|---------|---------------|---------|
| Docker | 20.10+ | `docker --version` | [docker.com](https://docker.com) |
| Go | 1.21+ | `go version` | [go.dev](https://go.dev) |
| Python | 3.11+ | `python3 --version` | [python.org](https://python.org) |
| Node.js | 18+ | `node --version` | [nodejs.org](https://nodejs.org) |

### Verify Prerequisites

```bash
# Run these commands - all should succeed
docker --version          # Docker version 24.0.0 or higher
go version               # go version go1.21 or higher
python3 --version        # Python 3.11 or higher
node --version           # v18.0.0 or higher
```

---

## 3. Clone and Setup

### Step 3.1: Clone the Repository

```bash
git clone <repository-url> trustAgent
cd trustAgent
```

### Step 3.2: Start Infrastructure (Docker)

```bash
# Start Redis, Qdrant, and Ollama
docker-compose up -d

# Verify containers are running
docker-compose ps
```

Expected output:
```
NAME                STATUS
trustagent-redis    running (healthy)
trustagent-qdrant   running (healthy)
trustagent-ollama   running
```

### Step 3.3: Pull Ollama Model

```bash
# This downloads the LLM model (~2GB, takes a few minutes)
docker exec -it trustagent-ollama ollama pull llama3.2
```

### Step 3.4: Setup Python Environment

```bash
cd backend-python

# Create virtual environment
python3 -m venv .venv

# Activate it
source .venv/bin/activate  # On Windows: .venv\Scripts\activate

# Install dependencies
pip install -e .

# Verify installation
python -c "import truthtable; print('✅ Python package installed')"
```

### Step 3.5: Setup Go Proxy

```bash
cd ../backend-go

# Download dependencies
go mod download

# Verify build
go build ./...
echo "✅ Go proxy builds successfully"
```

### Step 3.6: Setup React Dashboard

```bash
cd ../frontend-react

# Install dependencies
npm install

# Verify installation
npm run build
echo "✅ React dashboard builds successfully"
```

---

## 4. Start All Services

You need **4 terminal windows**. Here's exactly what to run in each:

### Terminal 1: Docker Services (already running)

```bash
# If not already running:
cd trustAgent
docker-compose up -d
```

### Terminal 2: Python Audit Engine

```bash
cd trustAgent/backend-python
source .venv/bin/activate
python -m truthtable.main
```

Expected output:
```
INFO: Starting TruthTable Audit Engine
INFO: Connecting to Ollama at http://localhost:11434
INFO: gRPC server listening on port 50051
```

### Terminal 3: Go Proxy

```bash
cd trustAgent/backend-go
go run ./cmd/proxy
```

Expected output:
```
🚀 Starting TruthTable Proxy
   Server Port: 8080
   WebSocket Port: 8081
✓ Connected to audit engine at localhost:50051
✅ TruthTable Proxy is ready!
```

### Terminal 4: React Dashboard

```bash
cd trustAgent/frontend-react
npm run dev
```

Expected output:
```
VITE v7.3.1  ready in 274 ms
➜  Local:   http://localhost:5173/
```

---

## 5. Verify Everything Works

### Check 1: Go Proxy Health

```bash
curl http://localhost:8080/health
```

Expected: `{"status":"ok"}`

### Check 2: Python Engine

The Go proxy logs should show:
```
✓ Connected to audit engine at localhost:50051
```

### Check 3: Dashboard

Open http://localhost:5173 in your browser. You should see:
- A dark-themed dashboard
- A green "Connected" indicator
- "Waiting for audit events..." message

### Check 4: WebSocket Connection

In the dashboard, the connection status (top right) should show green.

---

## 6. Send Your First Audit

### Option A: Using curl (recommended for learning)

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "What is the capital of France?"}],
    "test_response": "Paris is the capital of France. It was founded in 508 AD by Clovis I."
  }'
```

### Option B: Using the test script

```bash
./test_full_flow.sh
```

### What Happens Behind the Scenes

1. **Go Proxy** receives your request
2. **Go Proxy** uses the `test_response` instead of calling OpenAI
3. **Go Proxy** sends the prompt + response to **Python Engine** via gRPC
4. **Python Engine** extracts claims:
   - "Paris is the capital of France"
   - "It was founded in 508 AD by Clovis I"
5. **Python Engine** verifies each claim:
   - Claim 1: ✅ SUPPORTED (true fact)
   - Claim 2: ❌ UNSUPPORTED (Paris wasn't "founded" in 508 AD!)
6. **Python Engine** calculates score: 50% (1/2 claims true)
7. **Go Proxy** broadcasts result via WebSocket
8. **Dashboard** displays the audit result

---

## 7. Understanding the Output

### In the Terminal (Go Proxy)

```
[abc123] Intercepted chat completion request (model: gpt-4)
[abc123] TEST MODE: Using provided test_response
[abc123] Worker 1 processing job
[abc123] Audit complete in 15.2s (score: 0.50, claims: 2)
Broadcast audit result abc123 to 1 clients (score: 0.50)
```

### In the Dashboard

You'll see a new audit entry with:
- **Trust Score**: 50% (yellow/orange gauge)
- **Hallucination Badge**: Red "Hallucination Detected"
- **Claims**: 
  - ✅ "Paris is the capital of France" - SUPPORTED
  - ❌ "It was founded in 508 AD..." - UNSUPPORTED

### Score Interpretation

| Score | Color | Meaning |
|-------|-------|---------|
| 90-100% | 🟢 Green | Highly trustworthy |
| 70-89% | 🟡 Yellow | Minor issues |
| 50-69% | 🟠 Orange | Significant concerns |
| <50% | 🔴 Red | Major hallucinations |

---

## 8. Stopping Services

### Stop in Reverse Order

```bash
# Terminal 4: Stop React (Ctrl+C)
# Terminal 3: Stop Go Proxy (Ctrl+C)
# Terminal 2: Stop Python Engine (Ctrl+C)

# Terminal 1: Stop Docker
docker-compose down
```

### Stop Everything at Once

```bash
# Kill all TruthTable processes
pkill -f "python -m truthtable"
pkill -f "go run ./cmd/proxy"
pkill -f "npm run dev"
docker-compose down
```

---

## 9. Troubleshooting

### Problem: "Connection refused" on port 50051

**Cause**: Python engine not running

**Solution**:
```bash
cd backend-python
source .venv/bin/activate
python -m truthtable.main
```

### Problem: "Could not connect to audit engine"

**Cause**: Python engine started after Go proxy

**Solution**: Restart Go proxy after Python is running

### Problem: Dashboard shows "Disconnected"

**Cause**: Go proxy not running or WebSocket port blocked

**Solution**:
```bash
# Check if Go proxy is running
lsof -i:8081

# Restart if needed
cd backend-go && go run ./cmd/proxy
```

### Problem: "Port 5173 is in use"

**Cause**: Another process using that port

**Solution**: Vite will automatically try 5174, 5175, etc. Check the terminal output for the actual URL.

### Problem: Audit takes forever / times out

**Cause**: Ollama model not downloaded or slow hardware

**Solution**:
```bash
# Verify Ollama has the model
docker exec -it trustagent-ollama ollama list

# If not, pull it
docker exec -it trustagent-ollama ollama pull llama3.2
```

### Problem: "No module named truthtable"

**Cause**: Virtual environment not activated or package not installed

**Solution**:
```bash
cd backend-python
source .venv/bin/activate
pip install -e .
```

---

## Next Steps

Now that you have TruthTable running:

1. **Read the architecture docs**: Start with [INDEX.md](INDEX.md)
2. **Understand the Python Engine**: [PHASE-1-PYTHON-ENGINE.md](PHASE-1-PYTHON-ENGINE.md)
3. **Understand the Go Proxy**: [PHASE-2-GO-PROXY.md](PHASE-2-GO-PROXY.md)
4. **Customize the Dashboard**: [PHASE-3-REACT-DASHBOARD.md](PHASE-3-REACT-DASHBOARD.md)

---

*Document version: 1.0 | Last updated: January 31, 2026*
