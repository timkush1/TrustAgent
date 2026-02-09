# 🎯 TruthTable - Simple Project Plan

> **What are we building?** A smart proxy that watches AI answers and checks if they're making stuff up (hallucinating).

---

## 📋 Overview

TruthTable sits between your app and the AI (like ChatGPT). When the AI answers, TruthTable:
1. ✅ Sends the answer to your user immediately (no delay!)
2. 🔍 Quietly checks if the answer is factually correct
3. 📊 Shows you the results on a live dashboard

---

## 🏗️ The Three Parts We're Building

```
┌─────────────────────────────────────────────────────────────────┐
│                                                                 │
│   👤 User App          🔄 Go Proxy           🧠 Python Brain   │
│   (sends question) --> (forwards + captures) --> (checks facts) │
│                                                                 │
│                        📺 React Dashboard                       │
│                        (shows results live)                     │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

| Part | Language | What It Does |
|------|----------|--------------|
| **Go Proxy** | Go | The fast middleman - passes messages and captures them |
| **Python Brain** | Python | The smart checker - breaks down answers and verifies facts |
| **React Dashboard** | React/TypeScript | The display - shows what's happening in real-time |

---

## 📅 Implementation Phases

### Phase 0: Setup (Foundation)
**What we're doing:** Setting up the project structure and communication contract.

| Step | Task | Files Created |
|------|------|---------------|
| 0.1 | Create folder structure | `/backend-go`, `/backend-python`, `/frontend-react` |
| 0.2 | Define gRPC protocol | `proto/evaluator.proto` |
| 0.3 | Set up Docker Compose | `docker-compose.yml` |

**When done:** You'll have three empty project folders and a shared "language" (protobuf) for the services to talk.

---

### Phase 1: The Brain (Python Audit Engine)
**What we're doing:** Building the AI that checks if other AIs are lying.

| Step | Task | What You'll Learn |
|------|------|-------------------|
| 1.1 | Create LLM Provider interface | How to make swappable components |
| 1.2 | Implement Ollama provider | How to call local AI models |
| 1.3 | Build "Claim Decomposer" node | How LangGraph breaks problems into steps |
| 1.4 | Build "Fact Verifier" node | How NLI (Natural Language Inference) works |
| 1.5 | Build "Score Calculator" node | How to aggregate verification results |
| 1.6 | Wire up gRPC server | How Python talks to Go |

**When done:** You can send text to Python and it returns a "trust score" (0-100%).

---

### Phase 2: The Enforcer (Go Proxy)
**What we're doing:** Building the super-fast middleman that doesn't slow anyone down.

| Step | Task | What You'll Learn |
|------|------|-------------------|
| 2.1 | Set up Gin HTTP server | Go web framework basics |
| 2.2 | Create reverse proxy handler | How to forward requests transparently |
| 2.3 | Implement TeeWriter | How to copy data while streaming |
| 2.4 | Set up worker pool | Go concurrency with goroutines |
| 2.5 | Implement gRPC client | How Go talks to Python |
| 2.6 | Set up WebSocket hub | How to push live updates |

**When done:** Requests flow through, responses stream back instantly, and audits happen in the background.

---

### Phase 3: The Dashboard (React Frontend)
**What we're doing:** Building the cool monitoring screen.

| Step | Task | What You'll Learn |
|------|------|-------------------|
| 3.1 | Scaffold Vite + React + Tailwind | Modern React setup |
| 3.2 | Create WebSocket connection hook | Real-time data in React |
| 3.3 | Build AuditFeed component | Live scrolling list |
| 3.4 | Build TrustScoreGauge | Data visualization |
| 3.5 | Build detail view with highlighting | Showing what went wrong |
| 3.6 | Apply cyberpunk dark theme | Making it look awesome |

**When done:** A live dashboard showing every AI response and its trust score.

---

### Phase 4: Integration & Testing
**What we're doing:** Making sure everything works together.

| Step | Task | What You'll Learn |
|------|------|-------------------|
| 4.1 | End-to-end flow test | How all pieces connect |
| 4.2 | Unit tests for each component | Testing best practices |
| 4.3 | Integration tests with containers | Testcontainers usage |
| 4.4 | Performance testing | Latency measurement |

**When done:** Confidence that the system works and is fast.

---

## 🧪 Testing Strategy (Simple Version)

```
                 E2E Tests (full flow)
                    /          \
           Integration Tests    Integration Tests
           (Go + Redis)        (Python + Qdrant)
                |                    |
           Unit Tests            Unit Tests
           (Go handlers)         (Python nodes)
                |                    |
           Unit Tests            React Component Tests
           (Go providers)        (with mocks)
```

**Rule of thumb:**
- 70% unit tests (fast, test one thing)
- 20% integration tests (test connections)
- 10% E2E tests (test everything together)

---

## 📁 Where Things Go

### Go Proxy (`/backend-go`)
```
backend-go/
├── cmd/proxy/main.go          ← Start here (entry point)
├── internal/
│   ├── proxy/handler.go       ← HTTP request handling
│   ├── proxy/tee_writer.go    ← Stream capture magic
│   ├── grpc/client.go         ← Talks to Python
│   └── websocket/hub.go       ← Talks to Dashboard
└── pkg/llmprovider/           ← LLM interfaces (reusable)
```

### Python Brain (`/backend-python`)
```
backend-python/
├── src/truthtable/
│   ├── main.py                ← Start here (entry point)
│   ├── graphs/
│   │   ├── audit_graph.py     ← The workflow definition
│   │   └── nodes/             ← Individual steps
│   │       ├── decomposer.py  ← Break into claims
│   │       ├── verifier.py    ← Check each claim
│   │       └── scorer.py      ← Calculate score
│   └── providers/
│       ├── base.py            ← Interface definition
│       └── ollama.py          ← Local AI implementation
└── tests/                     ← Your tests go here
```

### React Dashboard (`/frontend-react`)
```
frontend-react/
├── src/
│   ├── App.tsx                ← Start here
│   ├── components/
│   │   ├── audit/
│   │   │   ├── AuditFeed.tsx  ← Live list
│   │   │   └── ClaimBreakdown.tsx
│   │   └── dashboard/
│   │       └── TrustScoreGauge.tsx
│   └── hooks/
│       └── useWebSocket.ts    ← Real-time connection
└── package.json
```

---

## 🔑 Key Concepts Explained

### What's a "Tee Writer"?
Imagine a water pipe with a T-junction. Water flows through to the destination, but some also goes to a side bucket. That's what we do with the AI's response - send it to the user AND capture a copy.

### What's gRPC?
A fast way for services to talk. Instead of slow JSON over HTTP, it uses compact binary messages. Think of it as the difference between mailing a letter vs. sending a telegram.

### What's LangGraph?
A way to build AI workflows as a series of steps (nodes) connected by arrows (edges). Each node does one thing, making the code modular and testable.

### What's NLI (Natural Language Inference)?
A technique where you ask an AI: "Given this context, does this claim follow logically?" It returns: ENTAILMENT (yes), CONTRADICTION (no), or NEUTRAL (can't tell).

---

## 🚀 Quick Start Commands

```bash
# 1. Start all infrastructure
docker-compose up -d redis qdrant ollama

# 2. Start Python brain (in one terminal)
cd backend-python
poetry install
poetry run python -m truthtable.main

# 3. Start Go proxy (in another terminal)
cd backend-go
go run ./cmd/proxy

# 4. Start React dashboard (in another terminal)
cd frontend-react
npm install
npm run dev

# 5. Open dashboard
open http://localhost:3000
```

---

## 📊 Success Metrics

| Metric | Target | Why It Matters |
|--------|--------|----------------|
| Proxy latency overhead | < 5ms | User shouldn't notice we're there |
| Audit completion time | < 2s | Results should feel real-time |
| Memory per connection | < 10KB | Handle thousands of users |
| Dashboard update delay | < 500ms | Feels instant |

---

## 🆘 Getting Help

1. **Stuck on Go?** Check `backend-go/README.md` (we'll create this)
2. **Stuck on Python?** Check `backend-python/README.md`
3. **Stuck on React?** Check `frontend-react/README.md`
4. **Architecture question?** Check `detailed_plan.md`

---

## 📝 Step Documentation

After each step is completed, a documentation file will be created in `/docs/steps/` explaining:
- What was done
- Why it was done that way
- Key code snippets with explanations
- How to test it
- Common issues and solutions

---

*Last updated: January 31, 2026*
