# 🎓 TruthTable Project - Complete Guide for Junior Engineers

> **Created:** January 31, 2026  
> **Purpose:** Explain everything about this project in simple terms

---

## Table of Contents

1. [What Are We Building?](#1-what-are-we-building)
2. [Project Structure Explained](#2-project-structure-explained)
3. [Technologies We Use (and Why)](#3-technologies-we-use-and-why)
4. [How Everything Is Installed](#4-how-everything-is-installed)
5. [Every File Explained](#5-every-file-explained)
6. [How The System Works](#6-how-the-system-works)
7. [What's Done vs What's Left](#7-whats-done-vs-whats-left)
8. [Common Commands](#8-common-commands)

---

## 1. What Are We Building?

### The Problem
When you use AI assistants like ChatGPT, sometimes they make up information that sounds correct but is actually false. This is called **"hallucination"**.

Example:
- You ask: "When was the Eiffel Tower built?"
- AI answers: "The Eiffel Tower was built in 1889 by Gustave Eiffel for the World's Fair, and it was originally painted bright red."
- Reality: The first part is true, but it was never bright red (it was originally reddish-brown, then yellow, now bronze).

### The Solution: TruthTable
TruthTable is a system that:
1. **Intercepts** AI responses before they reach the user
2. **Breaks down** the response into individual claims
3. **Checks** each claim against source documents
4. **Scores** how truthful the response is
5. **Shows** the results on a dashboard

### The Architecture (3 Parts)

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  Go Proxy    │────▶│ Python Brain │────▶│    React     │
│  (Fast)      │     │  (Smart)     │     │  Dashboard   │
└──────────────┘     └──────────────┘     └──────────────┘
       │                    │                    │
       │                    │                    │
       ▼                    ▼                    ▼
   Intercepts          Checks facts        Shows results
   AI traffic          using AI             visually
```

| Part | Language | Job |
|------|----------|-----|
| **Go Proxy** | Go | Super fast middleman - catches AI responses |
| **Python Brain** | Python | The smart part - analyzes if claims are true |
| **React Dashboard** | TypeScript | Pretty interface to see results |

---

## 2. Project Structure Explained

Here's what every folder in the project does:

```
trustAgent/                          ← Root project folder
│
├── backend-python/                  ← THE BRAIN (fact checking)
│   ├── .venv/                       ← Python's isolated environment (like a sandbox)
│   ├── src/truthtable/              ← Our actual code
│   ├── tests/                       ← Tests to verify code works
│   ├── pyproject.toml               ← Python dependencies list
│   └── test_e2e.py                  ← Full system test
│
├── backend-go/                      ← THE INTERCEPTOR (not built yet)
│   ├── api/audit/v1/                ← Generated Go code from proto
│   ├── cmd/                         ← Entry points (main.go will go here)
│   ├── internal/                    ← Private Go code
│   └── go.mod                       ← Go dependencies list
│
├── frontend-react/                  ← THE DASHBOARD (not built yet)
│   └── src/                         ← React components (empty now)
│
├── proto/                           ← SHARED CONTRACT
│   └── evaluator.proto              ← Defines how Python & Go communicate
│
├── config/                          ← Configuration files
│   ├── prometheus.yml               ← Metrics collection config
│   └── grafana/                     ← Dashboard config
│
├── docker-compose.yml               ← Starts all infrastructure
└── docs/                            ← Documentation (you're reading this)
```

---

## 3. Technologies We Use (and Why)

### 3.1 Python Technologies

| Technology | What It Is | Why We Use It |
|------------|-----------|---------------|
| **Python 3.12** | Programming language | Good for AI/ML, lots of libraries |
| **Poetry** | Dependency manager | Better than pip, locks versions |
| **venv** | Virtual environment | Isolates our packages from your other projects |
| **LangGraph** | AI workflow tool | Lets us create step-by-step AI pipelines |
| **gRPC** | Communication protocol | Fast binary protocol (faster than REST) |
| **Pydantic** | Data validation | Ensures our data structures are correct |
| **httpx** | HTTP client | Async HTTP requests to Ollama |
| **pytest** | Testing framework | Runs our unit tests |

### 3.2 Infrastructure (Docker)

| Service | What It Is | Why We Use It | Port |
|---------|-----------|---------------|------|
| **Redis** | In-memory database | Fast message queue between services | 6379 |
| **Qdrant** | Vector database | Stores document embeddings for search | 6333 |
| **Ollama** | Local AI runner | Runs AI models on your computer (free!) | 11434 |
| **Prometheus** | Metrics collector | Collects performance metrics | 9090 |
| **Grafana** | Dashboards | Visualizes metrics | 3001 |

### 3.3 Communication

| Technology | What It Is | Why We Use It |
|------------|-----------|---------------|
| **Protocol Buffers (protobuf)** | Data format | Defines message structures in a language-agnostic way |
| **gRPC** | RPC framework | Python and Go can call each other's functions |

---

## 4. How Everything Is Installed

### 4.1 Python Environment (Isolated!)

**Where:** `backend-python/.venv/`

**What it is:** A "sandbox" for Python packages. Everything installed here ONLY affects this project.

```
backend-python/
└── .venv/                          ← Virtual environment folder
    ├── bin/                        ← Executables (python, pip, poetry)
    ├── lib/python3.12/             ← Installed packages
    │   └── site-packages/          ← All 55+ packages live here
    └── pyvenv.cfg                  ← Config file
```

**How to activate it:**
```bash
cd backend-python
source .venv/bin/activate    # Now using isolated Python
```

**How to deactivate:**
```bash
deactivate                   # Back to system Python
```

### 4.2 Python Packages Installed

These are defined in `pyproject.toml` and installed via Poetry:

| Package | Version | Purpose |
|---------|---------|---------|
| `grpcio` | ^1.60.0 | gRPC runtime for Python |
| `grpcio-tools` | ^1.60.0 | Generates Python code from .proto files |
| `langgraph` | ^0.0.20 | Workflow orchestration for AI |
| `langchain` | ^0.1.0 | AI application framework |
| `pydantic` | ^2.5.0 | Data validation |
| `httpx` | ^0.26.0 | Async HTTP client |
| `qdrant-client` | ^1.7.0 | Connect to Qdrant database |
| `redis` | ^5.0.0 | Connect to Redis |
| `prometheus-client` | ^0.19.0 | Expose metrics |

### 4.3 Docker Containers

When you run `docker compose up`, these containers start:

```
┌─────────────────────────────────────────────────────────────┐
│                    YOUR COMPUTER                            │
│                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐         │
│  │   Redis     │  │   Qdrant    │  │   Ollama    │         │
│  │  Container  │  │  Container  │  │  Container  │         │
│  │  Port 6379  │  │  Port 6333  │  │  Port 11434 │         │
│  └─────────────┘  └─────────────┘  └─────────────┘         │
│                                                             │
│  Data persisted in Docker volumes:                          │
│  - trustagent_redis-data                                    │
│  - trustagent_qdrant-data                                   │
│  - trustagent_ollama-data (contains the AI model ~1.3GB)   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## 5. Every File Explained

### 5.1 Root Files

#### `docker-compose.yml`
**What:** Defines all the Docker services we need  
**Analogy:** Like a recipe that tells Docker "start these 5 containers with these settings"

```yaml
services:
  redis:              # Service 1: Message queue
    image: redis:7-alpine
    ports: ["6379:6379"]
    
  qdrant:             # Service 2: Vector database
    image: qdrant/qdrant:v1.7.0
    ports: ["6333:6333"]
    
  ollama:             # Service 3: Local AI
    image: ollama/ollama:latest
    ports: ["11434:11434"]
```

#### `proto/evaluator.proto`
**What:** The "contract" between Python and Go  
**Analogy:** Like a shared dictionary that both languages understand

```protobuf
// This defines a "service" with functions Go can call on Python
service AuditService {
    rpc SubmitAudit(AuditRequest) returns (AuditSubmission);
    rpc GetAuditResult(AuditResultRequest) returns (AuditResult);
    rpc HealthCheck(HealthRequest) returns (HealthResponse);
}

// This defines what an "AuditRequest" looks like
message AuditRequest {
    string request_id = 1;    // Field 1: unique ID
    string query = 2;         // Field 2: user's question
    string response = 3;      // Field 3: AI's answer
    // ... more fields
}
```

### 5.2 Python Files (backend-python/)

#### `pyproject.toml`
**What:** Python project configuration and dependencies  
**Analogy:** Like package.json for Node.js

```toml
[tool.poetry.dependencies]
python = "^3.11"        # Requires Python 3.11+
grpcio = "^1.60.0"      # gRPC library
langgraph = "^0.0.20"   # AI workflow library
```

#### `src/truthtable/main.py`
**What:** Entry point - starts the gRPC server  
**Analogy:** The "main()" of the Python application

```python
async def main():
    # 1. Load configuration
    settings = get_settings()
    
    # 2. Create the AI provider (Ollama)
    provider = get_provider("ollama", model="llama3.2:1b")
    
    # 3. Build the audit workflow
    audit_graph = build_audit_graph(provider)
    
    # 4. Start gRPC server on port 50051
    server = create_server(servicer, port=50051)
    await server.start()
```

#### `src/truthtable/config.py`
**What:** Configuration management using Pydantic  
**Reads environment variables and provides typed settings**

```python
class Settings(BaseSettings):
    grpc_host: str = "0.0.0.0"
    grpc_port: int = 50051
    ollama_base_url: str = "http://localhost:11434"
    llm_model: str = "llama3.2:1b"
```

#### `src/truthtable/providers/base.py`
**What:** Abstract base class for AI providers  
**Analogy:** A "template" that all AI providers must follow

```python
class LLMProvider(ABC):
    """Every AI provider must implement these methods"""
    
    @abstractmethod
    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        """Generate a response from the AI"""
        pass
    
    @abstractmethod
    async def health_check(self) -> bool:
        """Check if the AI is running"""
        pass
```

#### `src/truthtable/providers/ollama.py`
**What:** Implementation of LLMProvider for Ollama  
**Connects to Ollama's HTTP API to run AI models**

```python
class OllamaProvider(LLMProvider):
    async def complete(self, request):
        # Send HTTP POST to Ollama
        response = await self._client.post(
            "/api/chat",
            json={"model": "llama3.2:1b", "messages": [...]}
        )
        return CompletionResponse(content=response["message"]["content"])
```

#### `src/truthtable/providers/registry.py`
**What:** Factory pattern - creates the right provider based on name  
**Analogy:** A vending machine - put in "ollama", get an OllamaProvider

```python
def get_provider(name: str, **kwargs) -> LLMProvider:
    if name == "ollama":
        return OllamaProvider(**kwargs)
    elif name == "openai":
        return OpenAIProvider(**kwargs)
    # ... etc
```

#### `src/truthtable/graphs/state.py`
**What:** Defines the data structure that flows through the workflow  
**Analogy:** A form that gets passed between workers, each filling in their part

```python
class AuditState(TypedDict):
    # Input (what we receive)
    request_id: str
    user_query: str
    llm_response: str
    context_docs: List[str]
    
    # Output (what we produce)
    claims: List[str]                    # Filled by Decomposer
    claim_verifications: List[dict]      # Filled by Verifier
    faithfulness_score: float            # Filled by Scorer
    hallucination_detected: bool         # Filled by Scorer
```

#### `src/truthtable/graphs/nodes/decomposer.py`
**What:** Breaks down AI responses into individual claims  
**This is Step 1 of the audit process**

```
Input:  "Paris is the capital of France. It has the Eiffel Tower."
Output: ["Paris is the capital of France", "Paris has the Eiffel Tower"]
```

```python
class DecomposerNode:
    async def run(self, state: AuditState) -> AuditState:
        # Ask AI to extract claims from the response
        prompt = f"Break this into atomic claims: {state['llm_response']}"
        response = await self.provider.complete(prompt)
        
        claims = parse_claims(response.content)  # ["claim1", "claim2"]
        
        return {**state, "claims": claims}
```

#### `src/truthtable/graphs/nodes/verifier.py`
**What:** Checks each claim against context documents  
**This is Step 2 of the audit process**

```
Input:  claim="Paris is the capital of France", context="France's capital is Paris"
Output: {status: "SUPPORTED", confidence: 0.95, evidence: ["France's capital is Paris"]}
```

```python
class VerifierNode:
    async def run(self, state: AuditState) -> AuditState:
        verifications = []
        
        for claim in state["claims"]:
            # Ask AI: "Is this claim supported by this context?"
            result = await self.verify_claim(claim, state["context_docs"])
            verifications.append(result)
        
        return {**state, "claim_verifications": verifications}
```

#### `src/truthtable/graphs/nodes/scorer.py`
**What:** Calculates the final faithfulness score  
**This is Step 3 of the audit process**

```python
class ScorerNode:
    def run(self, state: AuditState) -> AuditState:
        # Count supported vs unsupported claims
        supported = sum(1 for v in state["claim_verifications"] if v["status"] == "SUPPORTED")
        total = len(state["claim_verifications"])
        
        score = supported / total  # 0.0 to 1.0
        hallucination = score < 1.0
        
        return {
            **state,
            "faithfulness_score": score,
            "hallucination_detected": hallucination
        }
```

#### `src/truthtable/graphs/audit_graph.py`
**What:** Connects the nodes into a workflow  
**Analogy:** An assembly line - each station does one job, then passes to the next

```python
def build_audit_graph(provider: LLMProvider) -> StateGraph:
    # Create the workers
    decomposer = DecomposerNode(provider)
    verifier = VerifierNode(provider)
    scorer = ScorerNode()
    
    # Create the assembly line
    workflow = StateGraph(AuditState)
    
    # Add stations
    workflow.add_node("decompose", decomposer.run)
    workflow.add_node("verify", verifier.run)
    workflow.add_node("score", scorer.run)
    
    # Connect stations
    workflow.add_edge("decompose", "verify")  # decompose → verify
    workflow.add_edge("verify", "score")       # verify → score
    
    return workflow.compile()
```

Visual representation:
```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│  Decomposer │───▶│  Verifier   │───▶│   Scorer    │
│             │    │             │    │             │
│ "Break into │    │ "Check each │    │ "Calculate  │
│  claims"    │    │  claim"     │    │  score"     │
└─────────────┘    └─────────────┘    └─────────────┘
```

#### `src/truthtable/grpc/server.py`
**What:** The gRPC server that Go will call  
**Implements the contract defined in evaluator.proto**

```python
class AuditServicer(evaluator_pb2_grpc.AuditServiceServicer):
    
    async def SubmitAudit(self, request, context):
        """Called by Go when it wants to audit a response"""
        
        # Run the audit workflow
        result = await run_audit(
            self.audit_graph,
            request_id=request.request_id,
            user_query=request.query,
            llm_response=request.response,
            context_docs=[doc.content for doc in request.context]
        )
        
        # Return the audit ID for later retrieval
        return AuditSubmission(audit_id=audit_id, status="completed")
```

#### `src/truthtable/grpc/pb/` (Generated Files)
**What:** Auto-generated Python code from evaluator.proto  
**NEVER edit these files - they get regenerated**

```
pb/
├── __init__.py              ← Empty, just makes it a Python package
├── evaluator_pb2.py         ← Message classes (AuditRequest, AuditResult, etc.)
└── evaluator_pb2_grpc.py    ← Server/client classes (AuditServiceServicer)
```

### 5.3 Go Files (backend-go/)

#### `go.mod`
**What:** Go module definition and dependencies  
**Analogy:** Like package.json for Go

```go
module github.com/truthtable/backend-go

go 1.22

require (
    google.golang.org/grpc v1.78.0
    google.golang.org/protobuf v1.36.11
)
```

#### `api/audit/v1/` (Generated Files)
**What:** Auto-generated Go code from evaluator.proto  
**NEVER edit these files - they get regenerated**

```
api/audit/v1/
├── evaluator.pb.go          ← Message structs (AuditRequest, etc.)
└── evaluator_grpc.pb.go     ← Client/server interfaces
```

---

## 6. How The System Works

### 6.1 The Complete Flow (When Finished)

```
┌────────────────────────────────────────────────────────────────────────┐
│                         COMPLETE FLOW                                   │
├────────────────────────────────────────────────────────────────────────┤
│                                                                        │
│  ① User's App sends question to AI                                    │
│       │                                                                │
│       ▼                                                                │
│  ② Go Proxy intercepts the request                                    │
│       │                                                                │
│       ▼                                                                │
│  ③ Go Proxy forwards to real AI (OpenAI, etc.)                        │
│       │                                                                │
│       ▼                                                                │
│  ④ AI responds with answer                                            │
│       │                                                                │
│       ├──────────────────────────────────────────┐                    │
│       ▼                                          ▼                    │
│  ⑤ Go Proxy sends response    ⑥ Go Proxy sends copy to               │
│     to User immediately          Python Brain via gRPC                │
│       │                                          │                    │
│       ▼                                          ▼                    │
│  User gets fast response      ⑦ Python decomposes → verifies → scores│
│  (no waiting!)                                   │                    │
│                                                  ▼                    │
│                               ⑧ Results sent to React Dashboard       │
│                                  via WebSocket                         │
│                                                  │                    │
│                                                  ▼                    │
│                               ⑨ Dashboard shows: "Score: 85%          │
│                                  Hallucination in claim 3"            │
│                                                                        │
└────────────────────────────────────────────────────────────────────────┘
```

### 6.2 The Audit Workflow (What Python Does)

```
┌─────────────────────────────────────────────────────────────────────┐
│                     PYTHON AUDIT WORKFLOW                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  INPUT:                                                             │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │ User Question: "What is the capital of France?"              │   │
│  │ AI Response: "Paris is the capital of France. It was        │   │
│  │              founded by Julius Caesar in 250 BC."           │   │
│  │ Context: ["France's capital is Paris.", "Paris was          │   │
│  │           founded as Lutetia in the 3rd century BC"]        │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                              │                                      │
│                              ▼                                      │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │ STEP 1: DECOMPOSER                                           │   │
│  │ Breaks response into claims:                                 │   │
│  │   • Claim 1: "Paris is the capital of France"               │   │
│  │   • Claim 2: "Paris was founded by Julius Caesar in 250 BC" │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                              │                                      │
│                              ▼                                      │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │ STEP 2: VERIFIER                                             │   │
│  │ Checks each claim against context:                           │   │
│  │   • Claim 1: ✓ SUPPORTED (matches "France's capital is...")  │   │
│  │   • Claim 2: ✗ UNSUPPORTED (context says 3rd century BC,    │   │
│  │               not 250 BC, and doesn't mention Caesar)        │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                              │                                      │
│                              ▼                                      │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │ STEP 3: SCORER                                               │   │
│  │ Calculates final score:                                      │   │
│  │   • 1 supported / 2 total = 50% faithfulness                 │   │
│  │   • Hallucination detected: YES                              │   │
│  │   • Reasoning: "Claim 2 contradicts historical records"      │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                              │                                      │
│                              ▼                                      │
│  OUTPUT:                                                            │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │ faithfulness_score: 0.50                                     │   │
│  │ hallucination_detected: true                                 │   │
│  │ claims: [                                                    │   │
│  │   {claim: "Paris is capital", status: "SUPPORTED"},         │   │
│  │   {claim: "Founded by Caesar", status: "UNSUPPORTED"}       │   │
│  │ ]                                                            │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### 6.3 How gRPC Works

**What is gRPC?**
- A way for programs to call functions on OTHER programs (even in different languages)
- Uses binary data (fast) instead of text/JSON (slow)
- Both sides need the same "contract" (the .proto file)

```
┌─────────────────┐                      ┌─────────────────┐
│                 │    gRPC (binary)     │                 │
│   Go Program    │◀────────────────────▶│  Python Program │
│                 │                      │                 │
│  Calls:         │                      │  Implements:    │
│  SubmitAudit()  │ ───Request────────▶  │  SubmitAudit()  │
│                 │ ◀──Response───────── │                 │
└─────────────────┘                      └─────────────────┘
        │                                        │
        │                                        │
        ▼                                        ▼
  Both use the same contract: proto/evaluator.proto
```

**How we generate code from .proto:**

```bash
# For Python:
python -m grpc_tools.protoc \
  --proto_path=proto \
  --python_out=backend-python/src/truthtable/grpc/pb \
  --grpc_python_out=backend-python/src/truthtable/grpc/pb \
  proto/evaluator.proto

# For Go:
protoc \
  --proto_path=proto \
  --go_out=backend-go/api/audit/v1 \
  --go-grpc_out=backend-go/api/audit/v1 \
  proto/evaluator.proto
```

---

## 7. What's Done vs What's Left

### ✅ COMPLETED (Phase 0 + Phase 1)

| Component | Status | Location |
|-----------|--------|----------|
| Project structure | ✅ | All folders created |
| Docker infrastructure | ✅ | `docker-compose.yml` |
| Protocol Buffer contract | ✅ | `proto/evaluator.proto` |
| Python virtual environment | ✅ | `backend-python/.venv/` |
| Python dependencies | ✅ | 55+ packages installed |
| Proto generation (Python) | ✅ | `grpc/pb/evaluator_pb2*.py` |
| Proto generation (Go) | ✅ | `api/audit/v1/evaluator*.go` |
| LLM Provider interface | ✅ | `providers/base.py` |
| Ollama provider | ✅ | `providers/ollama.py` |
| Decomposer node | ✅ | `graphs/nodes/decomposer.py` |
| Verifier node | ✅ | `graphs/nodes/verifier.py` |
| Scorer node | ✅ | `graphs/nodes/scorer.py` |
| Audit graph | ✅ | `graphs/audit_graph.py` |
| gRPC server | ✅ | `grpc/server.py` |
| Unit tests | ✅ | 21 tests passing |
| E2E test | ✅ | `test_e2e.py` passing |
| Docker: Redis | ✅ Running | Port 6379 |
| Docker: Qdrant | ✅ Running | Port 6333 |
| Docker: Ollama | ✅ Running | Port 11434 + model |

### 🔲 TODO (Phase 2, 3, 4)

| Phase | Component | What It Does |
|-------|-----------|--------------|
| **2.1** | Go HTTP server | Receive requests from user apps |
| **2.2** | Go reverse proxy | Forward to real AI APIs |
| **2.3** | Go TeeWriter | Copy response while streaming |
| **2.4** | Go worker pool | Handle concurrent audits |
| **2.5** | Go gRPC client | Call Python audit engine |
| **2.6** | Go WebSocket hub | Push results to dashboard |
| **3.1** | React scaffold | Create React app |
| **3.2** | WebSocket hook | Connect to Go for live updates |
| **3.3** | Audit feed | Show live audit results |
| **3.4** | Score gauge | Visual score display |
| **3.5** | Detail view | Claim-by-claim breakdown |
| **3.6** | Dark theme | Cyberpunk styling |
| **4.1** | E2E tests | Full system tests |
| **4.2** | Unit tests | More tests |
| **4.3** | Integration tests | Cross-service tests |
| **4.4** | Performance tests | Speed benchmarks |

---

## 8. Common Commands

### Starting Everything

```bash
# Terminal 1: Start infrastructure
cd /path/to/trustAgent
docker compose up -d redis qdrant ollama

# Terminal 2: Start Python audit engine
cd backend-python
source .venv/bin/activate
python -m truthtable.main
```

### Running Tests

```bash
cd backend-python
source .venv/bin/activate

# Unit tests
python -m pytest tests/unit/ -v

# E2E test
python test_e2e.py
```

### Checking Docker Services

```bash
# See what's running
docker compose ps

# View logs
docker compose logs -f ollama

# Stop everything
docker compose down

# Stop and delete all data
docker compose down -v
```

### Regenerating Proto Code

```bash
# Python
cd backend-python
source .venv/bin/activate
python -m grpc_tools.protoc \
  --proto_path=../proto \
  --python_out=src/truthtable/grpc/pb \
  --grpc_python_out=src/truthtable/grpc/pb \
  ../proto/evaluator.proto

# Go
cd backend-go
protoc \
  --proto_path=../proto \
  --go_out=api/audit/v1 \
  --go_opt=paths=source_relative \
  --go-grpc_out=api/audit/v1 \
  --go-grpc_opt=paths=source_relative \
  ../proto/evaluator.proto
```

### Installing New Python Packages

```bash
cd backend-python
source .venv/bin/activate
poetry add package-name    # Add to pyproject.toml and install
```

---

## Quick Glossary

| Term | Meaning |
|------|---------|
| **venv** | Virtual environment - isolated Python installation |
| **gRPC** | Google Remote Procedure Call - fast binary communication |
| **protobuf** | Protocol Buffers - language-agnostic data format |
| **LangGraph** | Library for building AI workflows as graphs |
| **Ollama** | Tool to run AI models locally on your computer |
| **Docker Compose** | Tool to run multiple Docker containers together |
| **Redis** | Super-fast in-memory database |
| **Qdrant** | Database specialized for AI embeddings |

---

*Last updated: January 31, 2026*
