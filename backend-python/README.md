# TruthTable Python Audit Engine

Backend service for LLM hallucination detection using LangGraph.

## Overview

The Python audit engine is the core intelligence of TrustAgent. It receives audit requests via gRPC, runs a 4-stage LangGraph workflow to detect hallucinations, and returns verification results with evidence.

**Pipeline Stages:**
1. **Decompose** - Extract atomic claims from LLM response
2. **Retrieve** - Search Qdrant vector DB for relevant knowledge (RAG)
3. **Verify** - Check each claim against retrieved context using NLI
4. **Score** - Calculate faithfulness score and detect hallucinations

## Features

### LangGraph Workflow
- State machine orchestration with `StateGraph`
- 4 nodes: Decomposer → Retriever → Verifier → Scorer
- Automatic state passing between nodes
- Per-step timing instrumentation

### RAG (Retrieval-Augmented Generation)
- **Vector Store:** Qdrant with vector similarity search
- **Embeddings:** Sentence Transformers (`all-MiniLM-L6-v2`)
- **Retrieval:** Top-K relevant documents for each claim
- **IngestDocuments gRPC method** for dynamic knowledge base updates

### Hallucination Detection
- **NLI (Natural Language Inference)** via Ollama LLM
- **Verification Statuses:** SUPPORTED, PARTIALLY_SUPPORTED, UNSUPPORTED
- **Evidence Extraction:** Relevant passages from knowledge base
- **Confidence Scores:** 0-100% per claim
- **Faithfulness Score:** Weighted average across all claims

### Observability
- **LangSmith Integration** - Optional tracing for LangGraph pipelines
- **Prometheus Metrics** - Exposed on port 8001
  - `truthtable_audits_total{status}` - Total audits processed
  - `truthtable_audit_duration_seconds` - Pipeline execution time
  - `truthtable_faithfulness_score` - Score distribution
  - `truthtable_claims_total{status}` - Claim verification outcomes
  - `truthtable_hallucinations_detected_total` - Hallucinations caught
  - `truthtable_active_audits` - Currently processing audits
- **Step Timings** - Per-node execution time (decompose_ms, retrieve_ms, verify_ms, score_ms)

### gRPC API
- **SubmitAudit** - Run full audit pipeline
- **IngestDocuments** - Upload documents to Qdrant knowledge base
- **Health Check** - Service readiness

## Quick Start

### Prerequisites

- Python 3.11+
- Qdrant running on `http://localhost:6333`
- Ollama running on `http://localhost:11434` with `llama3.2` model

### Installation

```bash
# Create virtual environment
python -m venv .venv
source .venv/bin/activate  # Windows: .venv\Scripts\activate

# Install dependencies
pip install -e .
```

### Seed Knowledge Base

```bash
# Seed Qdrant with initial facts
python scripts/seed_knowledge.py
```

This uploads the facts from `data/knowledge_base.txt` to Qdrant.

### Running the Server

```bash
# With default settings
python -m truthtable.main

# With LangSmith tracing (optional)
export LANGSMITH_API_KEY=your_api_key
export LANGSMITH_PROJECT=truthtable
python -m truthtable.main

# gRPC server starts on 0.0.0.0:50051
# Prometheus metrics on http://localhost:8001/metrics
```

## Configuration

All configuration via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_PROVIDER` | `ollama` | LLM provider to use |
| `LLM_MODEL` | `llama3.2` | Model name |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama server URL |
| `QDRANT_URL` | `http://localhost:6333` | Qdrant vector database URL |
| `QDRANT_COLLECTION` | `truthtable_knowledge` | Qdrant collection name |
| `EMBEDDING_MODEL` | `all-MiniLM-L6-v2` | Sentence Transformers model |
| `GRPC_PORT` | `50051` | gRPC server port |
| `GRPC_HOST` | `0.0.0.0` | gRPC server host |
| `METRICS_PORT` | `8001` | Prometheus metrics port |
| `LOG_LEVEL` | `INFO` | Logging level |
| `LANGSMITH_API_KEY` | - | (Optional) LangSmith API key for tracing |
| `LANGSMITH_PROJECT` | `truthtable` | LangSmith project name |
| `LANGSMITH_TRACING` | `false` | Enable LangSmith tracing |

You can also create a `.env` file in the project root.

## Project Structure

```
src/truthtable/
├── main.py                 # Application entry point, gRPC + metrics servers
├── config.py               # Configuration management (Pydantic settings)
├── metrics.py              # Prometheus metric definitions
├── providers/              # LLM provider implementations
│   ├── base.py            # Abstract provider interface
│   ├── ollama.py          # Ollama implementation
│   └── registry.py        # Provider registry
├── vectorstore/            # RAG components
│   ├── embeddings.py      # Sentence Transformers wrapper
│   └── qdrant_store.py    # Qdrant client + operations
├── graphs/                 # LangGraph workflow
│   ├── audit_graph.py     # Main workflow orchestration
│   ├── state.py           # State schema (AuditState TypedDict)
│   └── nodes/             # Individual workflow nodes
│       ├── decomposer.py  # Claim extraction (LLM prompt)
│       ├── retriever.py   # Qdrant vector search
│       ├── verifier.py    # NLI verification per claim
│       └── scorer.py      # Faithfulness score calculation
├── grpc/                   # gRPC server
│   ├── server.py          # Server implementation (SubmitAudit, IngestDocuments)
│   └── pb/                # Generated protobuf code
├── data/                   # Seed data
│   └── knowledge_base.txt # Initial facts for RAG
└── scripts/
    └── seed_knowledge.py  # Qdrant seeding script
```

## How It Works

### Audit Pipeline

1. **gRPC Request**
```python
request = AuditRequest(
    request_id="abc-123",
    user_query="What is the capital of France?",
    llm_response="London is the capital of France.",
    context_docs=[]  # Optional pre-provided context
)
```

2. **LangGraph Workflow**

**Decompose Node** (`graphs/nodes/decomposer.py`)
- Prompts LLM to extract atomic claims
- Input: LLM response
- Output: List of claims

```python
# Example:
claims = ["London is the capital of France"]
```

**Retrieve Node** (`graphs/nodes/retriever.py`)
- Embeds each claim using Sentence Transformers
- Searches Qdrant for top-K similar documents
- Input: Claims + user query
- Output: Retrieved context documents

```python
# Example:
context_docs = [
  "Paris is the capital of France.",
  "France is a country in Western Europe."
]
```

**Verify Node** (`graphs/nodes/verifier.py`)
- For each claim, prompts LLM to verify against context
- Uses NLI-style prompt: Is claim supported?
- Input: Claims + context_docs
- Output: Verification per claim

```python
# Example:
claim_verifications = [
  {
    "claim": "London is the capital of France",
    "status": "UNSUPPORTED",
    "confidence": 0.95,
    "evidence": ["Paris is the capital of France"]
  }
]
```

**Score Node** (`graphs/nodes/scorer.py`)
- Calculates faithfulness score
- Formula: `(SUPPORTED + 0.5 * PARTIALLY_SUPPORTED) / TOTAL`
- Detects hallucination if score < 0.8
- Input: Claim verifications
- Output: Faithfulness score, hallucination_detected flag

```python
# Example:
faithfulness_score = 0.0  # 0 supported / 1 total
hallucination_detected = True
```

3. **gRPC Response**
```python
response = AuditResult(
    request_id="abc-123",
    faithfulness_score=0.0,
    hallucination_detected=True,
    claims=["London is the capital of France"],
    claim_verifications=[...],
    reasoning_trace="Claim contradicts knowledge base...",
    step_timings={"decompose_ms": 150, "retrieve_ms": 800, "verify_ms": 1200, "score_ms": 50}
)
```

### IngestDocuments API

Upload new documents to the knowledge base:

```python
# Via gRPC client
documents = [
    ContextDocument(
        content="The Eiffel Tower is in Paris.",
        metadata={"source": "facts.txt"}
    )
]
response = stub.IngestDocuments(IngestRequest(documents=documents))
# response.documents_ingested = 1
```

Python implementation (`grpc/server.py:IngestDocuments`):
1. Extract text from documents
2. Generate embeddings using Sentence Transformers
3. Upsert to Qdrant collection
4. Return count of ingested documents

## Testing

```bash
# Run all tests
pytest

# Run with coverage
pytest --cov=truthtable --cov-report=html

# Run specific test file
pytest tests/unit/test_decomposer.py -v

# Run only unit tests
pytest tests/unit/ -v

# Run only integration tests
pytest tests/integration/ -v
```

**Test Coverage:**
- `tests/unit/test_provider_base.py` - Provider interface tests
- `tests/unit/test_decomposer.py` - Claim extraction tests
- `tests/unit/test_verifier.py` - NLI verification tests
- `tests/unit/test_scorer.py` - Score calculation tests
- `tests/integration/test_audit_graph.py` - Full pipeline tests

## Observability

### Prometheus Metrics

Metrics exposed at `http://localhost:8001/metrics`:

```
# Counters
truthtable_audits_total{status="success"} 42
truthtable_audits_total{status="error"} 3
truthtable_hallucinations_detected_total 12
truthtable_claims_total{status="SUPPORTED"} 80
truthtable_claims_total{status="UNSUPPORTED"} 15

# Histograms
truthtable_audit_duration_seconds_bucket{le="1.0"} 10
truthtable_audit_duration_seconds_bucket{le="5.0"} 35
truthtable_faithfulness_score_bucket{le="0.8"} 12

# Gauges
truthtable_active_audits 2
```

### LangSmith Tracing

Enable LangSmith for pipeline observability:

```bash
export LANGSMITH_API_KEY=your_api_key
export LANGSMITH_PROJECT=truthtable
export LANGSMITH_TRACING=true
python -m truthtable.main
```

LangSmith dashboard will show:
- Per-run traces with input/output
- Node-level latencies
- LLM token usage
- Error tracking

### Step Timings

Each audit result includes per-node execution time:

```json
{
  "step_timings": {
    "decompose_ms": 150,
    "retrieve_ms": 800,
    "verify_ms": 1200,
    "score_ms": 50
  }
}
```

These timings are displayed in the React dashboard's PipelineView component.

## Performance

Based on benchmarking (single worker, llama3.2 on CPU):
- **Decompose:** ~150ms
- **Retrieve:** ~800ms (Qdrant vector search + embedding)
- **Verify:** ~1200ms (Ollama LLM call for NLI)
- **Score:** ~50ms (arithmetic)
- **Total:** ~2.2s (p50), ~8s (p95)

Performance improvements:
- Use GPU for Ollama (5-10x faster)
- Batch multiple claims in verify step
- Cache embeddings for repeated claims

## Dependencies

Key dependencies (from `pyproject.toml`):
- `grpcio` + `grpcio-tools` - gRPC server
- `langgraph` - Workflow orchestration
- `langchain` + `langchain-community` - LLM abstractions
- `langsmith` - Pipeline tracing
- `qdrant-client` - Vector database client
- `sentence-transformers` - Embedding generation
- `torch` - PyTorch (for sentence-transformers)
- `prometheus-client` - Metrics
- `pydantic` + `pydantic-settings` - Configuration
- `httpx` - HTTP client (for Ollama)

## Development

### Adding a New LLM Provider

1. Create provider class inheriting from `LLMProvider`:

```python
from truthtable.providers.base import LLMProvider, CompletionRequest, CompletionResponse

class MyProvider(LLMProvider):
    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        # Implement API call
        pass

    async def health_check(self) -> bool:
        # Implement health check
        pass
```

2. Register it:

```python
from truthtable.providers import register_provider

register_provider("myprovider", MyProvider)
```

### Code Quality

```bash
# Format code
black src/ tests/

# Lint
ruff check src/ tests/

# Type checking
mypy src/
```

## Troubleshooting

### Ollama Connection Errors

```bash
# Check if Ollama is running
curl http://localhost:11434/api/tags

# Start Ollama with Docker
docker-compose up ollama

# Pull the model
docker exec -it truthtable-ollama ollama pull llama3.2
```

### Qdrant Connection Errors

```bash
# Check if Qdrant is running
curl http://localhost:6333/healthz

# Start Qdrant with Docker
docker-compose up qdrant

# Verify collection exists
curl http://localhost:6333/collections/truthtable_knowledge
```

### Proto Generation Fails

```bash
# Install protobuf compiler
brew install protobuf  # macOS
apt install protobuf-compiler  # Linux

# Install Python gRPC tools
pip install grpcio-tools
```

### Import Error: `evaluator_pb2_grpc`

If you see:
```
ModuleNotFoundError: No module named 'evaluator_pb2'
```

Fix the import in `src/truthtable/grpc/pb/evaluator_pb2_grpc.py`:
```python
# Change:
import evaluator_pb2 as evaluator__pb2

# To:
from . import evaluator_pb2 as evaluator__pb2
```

## Related Documentation

- Root README: [../README.md](../README.md)
- Go Proxy: [../backend-go/README.md](../backend-go/README.md)
- React Dashboard: [../frontend-react/README.md](../frontend-react/README.md)
- Protocol Buffers: [../proto/evaluator.proto](../proto/evaluator.proto)
