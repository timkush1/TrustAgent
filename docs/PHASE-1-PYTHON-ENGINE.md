# Phase 1: Python Audit Engine - Complete Guide

> **Status**: ✅ Complete and Working  
> **Tests**: 21/21 Passing  
> **Port**: 50051 (gRPC)  
> **Audience**: Junior developers learning the codebase

---

## Table of Contents

1. [Overview](#1-overview)
2. [What This Component Does](#2-what-this-component-does)
3. [Architecture](#3-architecture)
4. [Directory Structure](#4-directory-structure)
5. [File-by-File Explanation](#5-file-by-file-explanation)
6. [The LangGraph Workflow](#6-the-langgraph-workflow)
7. [How Claims Are Verified](#7-how-claims-are-verified)
8. [Configuration](#8-configuration)
9. [Running the Engine](#9-running-the-engine)
10. [Testing](#10-testing)
11. [Common Tasks](#11-common-tasks)
12. [Troubleshooting](#12-troubleshooting)

---

## 1. Overview

The Python Audit Engine is the **brain** of TruthTable. It receives LLM responses from the Go proxy and determines if they contain hallucinations.

### Key Technologies

| Technology | Purpose |
|------------|---------|
| Python 3.11+ | Main language |
| LangGraph | Workflow orchestration (state machine for AI) |
| gRPC | Communication with Go proxy |
| Ollama | Local LLM for verification |
| Pydantic | Configuration and data validation |

### What It Does in One Sentence

Takes an LLM response, breaks it into individual claims, checks each claim for accuracy, and returns a trust score.

---

## 2. What This Component Does

### Input (from Go Proxy)

```json
{
  "request_id": "abc-123",
  "query": "What is the capital of France?",
  "response": "Paris is the capital of France. It was founded in 508 AD."
}
```

### Processing Steps

1. **Decompose**: Break response into atomic claims
   - Claim 1: "Paris is the capital of France"
   - Claim 2: "It was founded in 508 AD"

2. **Verify**: Check each claim
   - Claim 1: ✅ Supported (true)
   - Claim 2: ❌ Unsupported (false - Paris wasn't "founded" in 508 AD)

3. **Score**: Calculate overall faithfulness
   - 1 supported + 1 unsupported = 50% faithfulness

### Output (to Go Proxy)

```json
{
  "audit_id": "audit-456",
  "trust_score": 0.50,
  "hallucination_detected": true,
  "claims": [
    {"claim": "Paris is the capital of France", "status": "SUPPORTED", "confidence": 0.95},
    {"claim": "It was founded in 508 AD", "status": "UNSUPPORTED", "confidence": 0.88}
  ]
}
```

---

## 3. Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Python Audit Engine                          │
│                     (Port 50051 - gRPC)                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│   ┌─────────────┐     ┌─────────────┐     ┌─────────────┐       │
│   │   gRPC      │────▶│  LangGraph  │────▶│   Result    │       │
│   │   Server    │     │  Workflow   │     │   Store     │       │
│   └─────────────┘     └─────────────┘     └─────────────┘       │
│                              │                                   │
│                              ▼                                   │
│   ┌─────────────────────────────────────────────────────────┐   │
│   │                   LangGraph Nodes                        │   │
│   │  ┌──────────┐  ┌──────────┐  ┌──────────┐               │   │
│   │  │Decomposer│─▶│ Verifier │─▶│  Scorer  │               │   │
│   │  └──────────┘  └──────────┘  └──────────┘               │   │
│   └─────────────────────────────────────────────────────────┘   │
│                              │                                   │
│                              ▼                                   │
│   ┌─────────────────────────────────────────────────────────┐   │
│   │                    Providers                             │   │
│   │  ┌──────────┐  ┌──────────┐                             │   │
│   │  │  Ollama  │  │  OpenAI  │  (swappable)                │   │
│   │  └──────────┘  └──────────┘                             │   │
│   └─────────────────────────────────────────────────────────┘   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 4. Directory Structure

```
backend-python/
├── src/
│   └── truthtable/
│       ├── __init__.py          # Package marker
│       ├── main.py              # Entry point - starts gRPC server
│       ├── config.py            # Configuration (Pydantic settings)
│       ├── grpc_server.py       # gRPC service implementation
│       │
│       ├── graphs/              # LangGraph workflow
│       │   ├── __init__.py
│       │   ├── state.py         # State schema definition
│       │   ├── audit_graph.py   # Graph construction
│       │   └── nodes/           # Individual processing steps
│       │       ├── __init__.py
│       │       ├── decomposer.py  # Extract claims from text
│       │       ├── verifier.py    # Verify claims (NLI)
│       │       └── scorer.py      # Calculate scores
│       │
│       ├── providers/           # LLM provider abstraction
│       │   ├── __init__.py
│       │   ├── base.py          # Abstract base class
│       │   ├── ollama.py        # Ollama implementation
│       │   └── registry.py      # Provider registry
│       │
│       ├── scoring/             # Scoring logic
│       │   ├── __init__.py
│       │   └── metrics.py       # Metric calculations
│       │
│       └── vectorstore/         # Vector DB abstraction
│           ├── __init__.py
│           ├── base.py          # Abstract base class
│           └── qdrant.py        # Qdrant implementation
│
├── tests/
│   └── unit/
│       ├── test_decomposer.py
│       ├── test_provider_base.py
│       └── test_ollama_provider.py
│
├── pyproject.toml               # Dependencies and project config
└── README.md
```

---

## 5. File-by-File Explanation

### 5.1 Entry Point: `main.py`

**Location**: `src/truthtable/main.py`

**Purpose**: Starts the gRPC server that listens for audit requests.

**What it does**:
1. Loads configuration from environment variables
2. Initializes the LLM provider (Ollama)
3. Builds the LangGraph workflow
4. Starts the gRPC server on port 50051
5. Handles graceful shutdown

**Key code**:
```python
def main():
    # Load config
    config = get_settings()
    
    # Create LLM provider
    provider = OllamaProvider(config.ollama_base_url, config.llm_model)
    
    # Build the audit graph
    graph = build_audit_graph(provider)
    
    # Start gRPC server
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    add_AuditServiceServicer_to_server(AuditServicer(graph), server)
    server.add_insecure_port('[::]:50051')
    server.start()
    server.wait_for_termination()
```

---

### 5.2 Configuration: `config.py`

**Location**: `src/truthtable/config.py`

**Purpose**: Defines all configurable settings using Pydantic.

**Key settings**:
```python
class Settings(BaseSettings):
    # gRPC server
    grpc_port: int = 50051
    
    # LLM Provider
    llm_provider: str = "ollama"
    llm_model: str = "llama3.2"
    ollama_base_url: str = "http://localhost:11434"
    
    # Scoring thresholds
    hallucination_threshold: float = 0.8  # Below this = hallucination
    confidence_threshold: float = 0.7     # Min confidence to trust
    
    class Config:
        env_file = ".env"
```

**Environment variables**:
```bash
LLM_PROVIDER=ollama
LLM_MODEL=llama3.2
OLLAMA_BASE_URL=http://localhost:11434
GRPC_PORT=50051
```

---

### 5.3 gRPC Server: `grpc_server.py`

**Location**: `src/truthtable/grpc_server.py`

**Purpose**: Handles incoming gRPC requests from the Go proxy.

**Methods**:

| Method | Purpose |
|--------|---------|
| `SubmitAudit` | Receives audit request, starts async processing |
| `GetAuditResult` | Returns result (polling-based) |
| `HealthCheck` | Returns server health status |

**Flow**:
```
Go Proxy calls SubmitAudit(request_id, query, response)
    ↓
Server stores request in pending dict
    ↓
Server runs LangGraph asynchronously
    ↓
Result stored in results dict
    ↓
Go Proxy polls GetAuditResult(audit_id)
    ↓
Server returns completed result
```

---

### 5.4 State Definition: `graphs/state.py`

**Location**: `src/truthtable/graphs/state.py`

**Purpose**: Defines the data that flows through the LangGraph workflow.

**Key types**:
```python
class VerificationStatus(Enum):
    UNKNOWN = "UNKNOWN"
    SUPPORTED = "SUPPORTED"
    UNSUPPORTED = "UNSUPPORTED"
    PARTIALLY_SUPPORTED = "PARTIALLY_SUPPORTED"

@dataclass
class ClaimVerification:
    claim: str                    # The claim text
    status: VerificationStatus    # Verification result
    confidence: float             # 0.0 to 1.0
    evidence: List[str]           # Supporting evidence

class AuditState(TypedDict):
    # Input
    request_id: str
    query: str
    response: str
    
    # Intermediate (filled by nodes)
    claims: List[str]
    verifications: List[ClaimVerification]
    
    # Output
    trust_score: float
    hallucination_detected: bool
    reasoning_trace: str
```

---

### 5.5 Decomposer Node: `graphs/nodes/decomposer.py`

**Location**: `src/truthtable/graphs/nodes/decomposer.py`

**Purpose**: Extracts individual factual claims from an LLM response.

**Input**: Full LLM response text
**Output**: List of atomic claims

**How it works**:
1. Sends prompt to LLM asking it to extract claims
2. Parses the LLM's response (numbered list)
3. Filters out empty or too-short claims
4. Returns list of claims

**Example**:
```
Input:  "Paris is the capital of France. It has the Eiffel Tower."
Output: ["Paris is the capital of France", "Paris has the Eiffel Tower"]
```

**The prompt used**:
```
Break down the following text into individual atomic factual claims.
Each claim should be a single, verifiable statement.

Text: {response}

Output each claim on a new line, numbered:
1. [first claim]
2. [second claim]
```

---

### 5.6 Verifier Node: `graphs/nodes/verifier.py`

**Location**: `src/truthtable/graphs/nodes/verifier.py`

**Purpose**: Verifies each claim using Natural Language Inference (NLI).

**Input**: List of claims + context
**Output**: List of ClaimVerification results

**How NLI works**:
1. For each claim, ask the LLM: "Given this context, is this claim true?"
2. LLM responds with: SUPPORTED, UNSUPPORTED, or PARTIALLY_SUPPORTED
3. Also extracts confidence score and evidence

**The prompt used**:
```
You are a fact-checker. Given the context below, determine if the claim is supported.

Context: {context}

Claim: {claim}

Respond with:
- VERDICT: SUPPORTED, UNSUPPORTED, or PARTIALLY_SUPPORTED
- CONFIDENCE: 0.0 to 1.0
- EVIDENCE: Quote from context that supports/refutes
```

---

### 5.7 Scorer Node: `graphs/nodes/scorer.py`

**Location**: `src/truthtable/graphs/nodes/scorer.py`

**Purpose**: Calculates the final trust score and determines if hallucination occurred.

**Input**: List of claim verifications
**Output**: Trust score (0.0-1.0), hallucination flag, reasoning trace

**Scoring algorithm**:
```python
def calculate_score(verifications):
    total_weight = 0
    weighted_score = 0
    
    for v in verifications:
        weight = v.confidence  # Higher confidence = more weight
        
        if v.status == SUPPORTED:
            score = 1.0
        elif v.status == PARTIALLY_SUPPORTED:
            score = 0.5
        else:  # UNSUPPORTED
            score = 0.0
        
        weighted_score += score * weight
        total_weight += weight
    
    return weighted_score / total_weight if total_weight > 0 else 0.0
```

**Hallucination detection**:
```python
hallucination_detected = trust_score < 0.8  # Configurable threshold
```

---

### 5.8 Graph Construction: `graphs/audit_graph.py`

**Location**: `src/truthtable/graphs/audit_graph.py`

**Purpose**: Wires all the nodes together into a LangGraph workflow.

**The graph structure**:
```
START → decompose → verify → score → END
```

**Code**:
```python
from langgraph.graph import StateGraph, END

def build_audit_graph(llm_provider):
    # Create nodes
    decomposer = DecomposerNode(llm_provider)
    verifier = VerifierNode(llm_provider)
    scorer = ScorerNode()
    
    # Build graph
    workflow = StateGraph(AuditState)
    
    workflow.add_node("decompose", decomposer)
    workflow.add_node("verify", verifier)
    workflow.add_node("score", scorer)
    
    workflow.set_entry_point("decompose")
    workflow.add_edge("decompose", "verify")
    workflow.add_edge("verify", "score")
    workflow.add_edge("score", END)
    
    return workflow.compile()
```

---

### 5.9 LLM Provider: `providers/ollama.py`

**Location**: `src/truthtable/providers/ollama.py`

**Purpose**: Communicates with Ollama (local LLM server).

**Key methods**:
```python
class OllamaProvider(LLMProvider):
    async def complete(self, prompt: str, system: str = None) -> str:
        """Send prompt to Ollama, get response."""
        response = await self.client.post(
            f"{self.base_url}/api/generate",
            json={
                "model": self.model,
                "prompt": prompt,
                "system": system,
                "stream": False
            }
        )
        return response.json()["response"]
    
    async def health_check(self) -> bool:
        """Check if Ollama is running and model is available."""
        response = await self.client.get(f"{self.base_url}/api/tags")
        models = response.json()["models"]
        return any(m["name"] == self.model for m in models)
```

---

## 6. The LangGraph Workflow

### Visual Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                        LangGraph Workflow                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌─────────┐                                                    │
│  │  START  │                                                    │
│  └────┬────┘                                                    │
│       │                                                          │
│       ▼                                                          │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ DECOMPOSE NODE                                          │    │
│  │                                                          │    │
│  │ Input:  response = "Paris is the capital. Founded 508." │    │
│  │ Output: claims = ["Paris is capital", "Founded 508"]    │    │
│  └─────────────────────────────────────────────────────────┘    │
│       │                                                          │
│       ▼                                                          │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ VERIFY NODE                                              │    │
│  │                                                          │    │
│  │ Input:  claims = ["Paris is capital", "Founded 508"]    │    │
│  │ Output: verifications = [                                │    │
│  │           {claim: "Paris...", status: SUPPORTED},       │    │
│  │           {claim: "Founded...", status: UNSUPPORTED}    │    │
│  │         ]                                                │    │
│  └─────────────────────────────────────────────────────────┘    │
│       │                                                          │
│       ▼                                                          │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ SCORE NODE                                               │    │
│  │                                                          │    │
│  │ Input:  verifications (from above)                       │    │
│  │ Output: trust_score = 0.50                               │    │
│  │         hallucination_detected = true                    │    │
│  │         reasoning_trace = "1/2 claims supported..."     │    │
│  └─────────────────────────────────────────────────────────┘    │
│       │                                                          │
│       ▼                                                          │
│  ┌─────────┐                                                    │
│  │   END   │                                                    │
│  └─────────┘                                                    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### State Evolution

| Stage | State Contents |
|-------|---------------|
| Initial | `{request_id, query, response}` |
| After Decompose | `{..., claims: ["claim1", "claim2"]}` |
| After Verify | `{..., verifications: [{claim, status, confidence}]}` |
| After Score | `{..., trust_score: 0.5, hallucination_detected: true}` |

---

## 7. How Claims Are Verified

### The NLI (Natural Language Inference) Approach

NLI is a technique that determines if a hypothesis is:
- **ENTAILED** by the premise (supported)
- **CONTRADICTED** by the premise (unsupported)
- **NEUTRAL** (unknown)

### Our Verification Process

```
For each claim:
    1. Gather context (user query + any retrieved docs)
    2. Send to LLM with NLI prompt
    3. Parse LLM response for verdict
    4. Extract confidence score
    5. Store evidence quotes
```

### Example

**Claim**: "The Eiffel Tower is 500 meters tall"

**Context**: "The Eiffel Tower, built in 1889, stands at 330 meters including antennas."

**LLM Prompt**:
```
Given this context:
"The Eiffel Tower, built in 1889, stands at 330 meters including antennas."

Is this claim supported?
"The Eiffel Tower is 500 meters tall"

Respond with SUPPORTED, UNSUPPORTED, or PARTIALLY_SUPPORTED.
```

**LLM Response**: "UNSUPPORTED - The context says 330 meters, not 500."

**Result**:
```python
ClaimVerification(
    claim="The Eiffel Tower is 500 meters tall",
    status=VerificationStatus.UNSUPPORTED,
    confidence=0.92,
    evidence=["The Eiffel Tower... stands at 330 meters"]
)
```

---

## 8. Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_PORT` | 50051 | Port for gRPC server |
| `LLM_PROVIDER` | ollama | Which LLM to use |
| `LLM_MODEL` | llama3.2 | Model name |
| `OLLAMA_BASE_URL` | http://localhost:11434 | Ollama server URL |
| `LOG_LEVEL` | INFO | Logging verbosity |

### Create a .env file

```bash
# backend-python/.env
GRPC_PORT=50051
LLM_PROVIDER=ollama
LLM_MODEL=llama3.2
OLLAMA_BASE_URL=http://localhost:11434
LOG_LEVEL=DEBUG
```

---

## 9. Running the Engine

### Development Mode

```bash
cd backend-python
source .venv/bin/activate
python -m truthtable.main
```

Expected output:
```
2026-01-31 10:00:00 INFO Starting TruthTable Audit Engine
2026-01-31 10:00:00 INFO Connecting to Ollama at http://localhost:11434
2026-01-31 10:00:01 INFO Model llama3.2 is available
2026-01-31 10:00:01 INFO gRPC server listening on port 50051
```

### With Debug Logging

```bash
LOG_LEVEL=DEBUG python -m truthtable.main
```

### Verify It's Running

```bash
# Check port is open
lsof -i:50051

# Or use grpcurl (if installed)
grpcurl -plaintext localhost:50051 list
```

---

## 10. Testing

### Run All Tests

```bash
cd backend-python
source .venv/bin/activate
pytest tests/ -v
```

Expected output:
```
tests/unit/test_decomposer.py::test_decompose_simple_claims PASSED
tests/unit/test_decomposer.py::test_decompose_empty_response PASSED
tests/unit/test_decomposer.py::test_decompose_single_claim PASSED
tests/unit/test_ollama_provider.py::test_complete_success PASSED
tests/unit/test_ollama_provider.py::test_health_check_success PASSED
...
========================= 21 passed in 2.45s =========================
```

### Run Specific Tests

```bash
# Just decomposer tests
pytest tests/unit/test_decomposer.py -v

# Just provider tests
pytest tests/unit/test_ollama_provider.py -v
```

### Test Coverage

```bash
pytest tests/ --cov=truthtable --cov-report=html
open htmlcov/index.html
```

---

## 11. Common Tasks

### Adding a New LLM Provider

1. Create `providers/anthropic.py`:
```python
from .base import LLMProvider

class AnthropicProvider(LLMProvider):
    def __init__(self, api_key: str):
        self.api_key = api_key
    
    async def complete(self, prompt: str, system: str = None) -> str:
        # Implementation here
        pass
```

2. Register in `providers/__init__.py`:
```python
from .anthropic import AnthropicProvider
```

3. Update config selection in `main.py`

### Modifying the Scoring Algorithm

Edit `graphs/nodes/scorer.py`:
```python
def calculate_score(verifications):
    # Your custom logic here
    pass
```

### Adding a New Node to the Workflow

1. Create `graphs/nodes/my_node.py`
2. Add to graph in `graphs/audit_graph.py`:
```python
workflow.add_node("my_node", my_node_function)
workflow.add_edge("verify", "my_node")  # Insert after verify
workflow.add_edge("my_node", "score")
```

---

## 12. Troubleshooting

### Problem: "Connection refused to Ollama"

```
httpx.ConnectError: Connection refused
```

**Solution**: Start Ollama
```bash
docker-compose up -d ollama
# Wait 30 seconds for model to load
```

### Problem: "Model not found"

```
Model 'llama3.2' not found
```

**Solution**: Pull the model
```bash
docker exec -it trustagent-ollama ollama pull llama3.2
```

### Problem: "gRPC server not starting"

```
Error: Address already in use
```

**Solution**: Kill existing process
```bash
lsof -ti:50051 | xargs kill -9
```

### Problem: "Audit times out"

**Cause**: LLM is slow (especially on CPU)

**Solution**: 
1. Use a smaller model: `LLM_MODEL=llama3.2:1b`
2. Or increase timeout in Go proxy config

---

## Summary

The Python Audit Engine:
1. ✅ Receives requests via gRPC on port 50051
2. ✅ Uses LangGraph to orchestrate the workflow
3. ✅ Decomposes responses into claims
4. ✅ Verifies each claim using NLI
5. ✅ Calculates trust scores
6. ✅ Detects hallucinations

**21 tests passing** - the engine is production-ready.

---

*Next: Read [PHASE-2-GO-PROXY.md](PHASE-2-GO-PROXY.md) to understand how the Go Proxy works.*
