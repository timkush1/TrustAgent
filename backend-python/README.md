# TruthTable Python Audit Engine

Backend service for LLM hallucination detection using LangGraph.

## 🚀 Quick Start

### Prerequisites

- Python 3.11+
- Poetry (for dependency management)
- Protocol Buffer compiler (`protoc`)

### Installation

```bash
# Install Poetry if you don't have it
curl -sSL https://install.python-poetry.org | python3 -

# Install dependencies
poetry install

# Generate protobuf code
./scripts/generate_proto.sh
```

### Running the Server

```bash
# With default settings (uses local Ollama)
poetry run python -m truthtable.main

# With custom settings
export LLM_MODEL="mistral"
export OLLAMA_BASE_URL="http://localhost:11434"
poetry run python -m truthtable.main
```

The gRPC server will start on `localhost:50051` by default.

## 📁 Project Structure

```
src/truthtable/
├── main.py                 # Application entry point
├── config.py               # Configuration management
├── providers/              # LLM provider implementations
│   ├── base.py            # Abstract provider interface
│   ├── ollama.py          # Ollama implementation
│   └── registry.py        # Provider registry
├── graphs/                 # LangGraph workflow
│   ├── audit_graph.py     # Main workflow orchestration
│   ├── state.py           # State schema
│   └── nodes/             # Individual workflow nodes
│       ├── decomposer.py  # Claim extraction
│       ├── verifier.py    # Fact verification
│       └── scorer.py      # Score calculation
└── grpc/                   # gRPC server
    ├── server.py          # Server implementation
    └── pb/                # Generated protobuf code
```

## 🧪 Testing

```bash
# Run all tests
poetry run pytest

# Run with coverage
poetry run pytest --cov=truthtable --cov-report=html

# Run specific test file
poetry run pytest tests/unit/test_provider_base.py -v
```

## 🔧 Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_PROVIDER` | `ollama` | LLM provider to use |
| `LLM_MODEL` | `llama3.2` | Model name |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama server URL |
| `GRPC_PORT` | `50051` | gRPC server port |
| `GRPC_HOST` | `0.0.0.0` | gRPC server host |
| `LOG_LEVEL` | `INFO` | Logging level |

You can also create a `.env` file in the project root.

## 🔍 How It Works

The audit engine processes LLM responses through a three-stage pipeline:

1. **Decompose** - Break response into atomic claims
2. **Verify** - Check each claim against provided context
3. **Score** - Calculate faithfulness score

```python
# Example usage (via gRPC client)
request = AuditRequest(
    request_id="abc-123",
    user_query="What is the capital of France?",
    llm_response="Paris is the capital of France and was founded by Romans.",
    context_docs=["France's capital is Paris, founded in 3rd century BC."]
)

# Returns:
# - faithfulness_score: 0.75 (one claim supported, one partially supported)
# - hallucination_detected: false
# - claims: ["Paris is the capital of France", "Paris was founded by Romans"]
```

## 📊 Output Format

The audit returns:

- **faithfulness_score** (0.0-1.0): How much is supported by context
- **hallucination_detected** (bool): True if significant unsupported claims
- **claims** (list): Extracted atomic claims
- **claim_verifications** (list): Verification result per claim
- **reasoning_trace** (string): Human-readable explanation

## 🛠️ Development

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
poetry run black src/ tests/

# Lint
poetry run ruff check src/ tests/

# Type checking
poetry run mypy src/
```

## 🐛 Troubleshooting

### Ollama Connection Errors

```bash
# Check if Ollama is running
curl http://localhost:11434/api/tags

# Start Ollama with Docker
docker-compose up ollama

# Pull the model
docker exec -it truthtable-ollama ollama pull llama3.2
```

### Proto Generation Fails

```bash
# Install protobuf compiler
brew install protobuf  # macOS
apt install protobuf-compiler  # Linux

# Install Python gRPC tools
poetry add grpcio-tools
```

## 📝 License

MIT License - See LICENSE file for details
