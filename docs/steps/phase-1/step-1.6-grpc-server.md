# Step 1.6: gRPC Server Setup

## 🎯 Goal

Build the **gRPC server** that exposes the audit workflow as a service. This allows the Go proxy to communicate with the Python audit engine using efficient binary protocol.

**Architecture Flow:**
```
┌─────────────┐      gRPC      ┌─────────────────────────┐
│  Go Proxy   │  ──────────→  │  Python Audit Service   │
│  (client)   │  AuditRequest │  (server)               │
│             │  ←──────────  │                         │
│             │  AuditResult  │  ┌─────────────────┐    │
└─────────────┘               │  │  LangGraph      │    │
                              │  │  Workflow       │    │
                              │  └─────────────────┘    │
                              └─────────────────────────┘
```

---

## 📚 Prerequisites

- Completed Step 0.2 (Protobuf Setup)
- Completed Step 1.5 (Score Calculator)
- Generated Python gRPC stubs

---

## 🧠 Concepts Explained

### What is gRPC?

gRPC is a high-performance RPC (Remote Procedure Call) framework:

| Feature | REST | gRPC |
|---------|------|------|
| Protocol | HTTP/1.1 + JSON | HTTP/2 + Protobuf |
| Payload | Text (JSON) | Binary (compact) |
| Schema | OpenAPI (optional) | Required (.proto) |
| Streaming | Limited | Full bidirectional |
| Speed | ~100ms | ~10ms |

For TruthTable, gRPC gives us:
- **Speed**: Binary serialization is faster
- **Type Safety**: Proto schema enforces contracts
- **Streaming**: Can stream results as they're ready

### gRPC Service Flow

```
1. Client creates AuditRequest message
2. Client serializes to bytes (Protobuf)
3. Bytes sent over HTTP/2
4. Server deserializes request
5. Server processes request
6. Server creates AuditResult message
7. Server serializes and sends response
8. Client deserializes response
```

### Servicer Pattern

In gRPC-Python, you implement a "servicer" class:

```python
# Proto defines the interface
service EvaluatorService {
    rpc Evaluate(AuditRequest) returns (AuditResult);
}

# Python implements it
class EvaluatorServicer:
    async def Evaluate(self, request, context):
        # Your implementation
        return AuditResult(...)
```

---

## 💻 Implementation

### Step 1: Generate Python gRPC Stubs

First, ensure stubs are generated. Create `backend-python/scripts/generate_proto.sh`:

```bash
#!/bin/bash
# Generate Python gRPC code from proto files

set -e

PROTO_DIR="../proto"
OUT_DIR="src/truthtable/grpc/generated"

# Create output directory
mkdir -p "$OUT_DIR"

# Generate Python code
python -m grpc_tools.protoc \
    -I"$PROTO_DIR" \
    --python_out="$OUT_DIR" \
    --pyi_out="$OUT_DIR" \
    --grpc_python_out="$OUT_DIR" \
    "$PROTO_DIR/evaluator.proto"

# Create __init__.py
cat > "$OUT_DIR/__init__.py" << 'EOF'
"""Generated gRPC code."""
from .evaluator_pb2 import (
    AuditRequest,
    AuditResult,
    ClaimVerification,
    ContextDocument,
    HealthRequest,
    HealthResponse,
)
from .evaluator_pb2_grpc import (
    EvaluatorServiceStub,
    EvaluatorServiceServicer,
    add_EvaluatorServiceServicer_to_server,
)

__all__ = [
    "AuditRequest",
    "AuditResult",
    "ClaimVerification",
    "ContextDocument",
    "HealthRequest",
    "HealthResponse",
    "EvaluatorServiceStub",
    "EvaluatorServiceServicer",
    "add_EvaluatorServiceServicer_to_server",
]
EOF

echo "✓ Generated Python gRPC stubs in $OUT_DIR"
```

Run it:
```bash
cd backend-python
chmod +x scripts/generate_proto.sh
./scripts/generate_proto.sh
```

### Step 2: Create the Servicer Implementation

Create `src/truthtable/grpc/servicer.py`:

```python
"""
gRPC Servicer Implementation.

This module implements the EvaluatorService defined in evaluator.proto.
It bridges the gRPC interface to the LangGraph audit workflow.
"""

import logging
import time
from typing import Any

import grpc

from .generated import (
    EvaluatorServiceServicer,
    AuditRequest,
    AuditResult,
    ClaimVerification as ProtoClaimVerification,
    HealthRequest,
    HealthResponse,
)
from ..providers import LLMProvider
from ..graphs import run_audit
from ..graphs.state import ClaimVerification

logger = logging.getLogger(__name__)


class EvaluatorServicer(EvaluatorServiceServicer):
    """
    Implementation of the EvaluatorService gRPC service.
    
    Handles incoming audit requests by running the LangGraph workflow
    and converting results to protobuf messages.
    """
    
    def __init__(self, llm: LLMProvider):
        """
        Initialize the servicer.
        
        Args:
            llm: LLM provider for the audit workflow
        """
        self.llm = llm
        self.request_count = 0
        self.start_time = time.time()
        logger.info("EvaluatorServicer initialized")
    
    async def Evaluate(
        self,
        request: AuditRequest,
        context: grpc.aio.ServicerContext,
    ) -> AuditResult:
        """
        Handle an audit request.
        
        Args:
            request: The AuditRequest protobuf message
            context: gRPC context for metadata and cancellation
            
        Returns:
            AuditResult protobuf message with scores and verifications
        """
        start_time = time.time()
        self.request_count += 1
        request_id = request.request_id or f"req_{self.request_count}"
        
        logger.info(
            f"Received audit request {request_id} "
            f"(response length: {len(request.llm_response)} chars)"
        )
        
        try:
            # Convert context documents from proto format
            context_docs = [
                {
                    "id": doc.id,
                    "content": doc.content,
                    "source": doc.source,
                    "metadata": dict(doc.metadata),
                }
                for doc in request.context_documents
            ]
            
            # Run the audit workflow
            result = await run_audit(
                llm=self.llm,
                user_query=request.user_query,
                llm_response=request.llm_response,
                context_docs=context_docs,
                request_id=request_id,
            )
            
            # Convert to protobuf response
            response = self._build_response(request_id, result)
            
            elapsed_ms = int((time.time() - start_time) * 1000)
            response.processing_time_ms = elapsed_ms
            
            logger.info(
                f"Completed audit {request_id} in {elapsed_ms}ms "
                f"(score: {response.trust_score:.1f}%)"
            )
            
            return response
            
        except Exception as e:
            logger.error(f"Audit {request_id} failed: {e}", exc_info=True)
            
            # Return error response
            await context.abort(
                grpc.StatusCode.INTERNAL,
                f"Audit processing failed: {str(e)}"
            )
    
    async def HealthCheck(
        self,
        request: HealthRequest,
        context: grpc.aio.ServicerContext,
    ) -> HealthResponse:
        """
        Health check endpoint.
        
        Returns server status including LLM provider health.
        """
        try:
            # Check LLM provider health
            llm_healthy = await self.llm.health_check()
            
            uptime_seconds = int(time.time() - self.start_time)
            
            return HealthResponse(
                status="healthy" if llm_healthy else "degraded",
                message=f"Processed {self.request_count} requests",
                uptime_seconds=uptime_seconds,
            )
        except Exception as e:
            return HealthResponse(
                status="unhealthy",
                message=str(e),
                uptime_seconds=0,
            )
    
    def _build_response(
        self,
        request_id: str,
        result: dict[str, Any],
    ) -> AuditResult:
        """
        Convert workflow result to protobuf AuditResult.
        
        Args:
            request_id: The request ID
            result: The workflow result dictionary
            
        Returns:
            Populated AuditResult protobuf message
        """
        scores = result.get("scores", {})
        verifications = result.get("verifications", [])
        
        # Convert verifications to proto format
        proto_verifications = [
            self._verification_to_proto(v)
            for v in verifications
        ]
        
        return AuditResult(
            request_id=request_id,
            trust_score=scores.get("trust_score", 0.0),
            hallucination_rate=scores.get("hallucination_rate", 0.0),
            total_claims=scores.get("total_claims", 0),
            supported_claims=scores.get("supported_count", 0),
            unsupported_claims=scores.get("unsupported_count", 0),
            verifications=proto_verifications,
            grade=scores.get("grade", "F"),
            verdict=scores.get("verdict", ""),
            processing_time_ms=result.get("processing_time_ms", 0),
        )
    
    def _verification_to_proto(
        self,
        v: ClaimVerification,
    ) -> ProtoClaimVerification:
        """Convert a ClaimVerification dict to protobuf."""
        return ProtoClaimVerification(
            claim=v.get("claim", ""),
            supported=v.get("supported", False),
            confidence=v.get("confidence", 0.0),
            evidence=v.get("evidence", []),
            reasoning=v.get("reasoning", ""),
        )


# ===== Interceptors (Middleware) =====

class LoggingInterceptor(grpc.aio.ServerInterceptor):
    """
    gRPC interceptor for request logging.
    
    Logs all incoming requests with timing information.
    """
    
    async def intercept_service(
        self,
        continuation,
        handler_call_details,
    ):
        start_time = time.time()
        method = handler_call_details.method
        
        logger.debug(f"gRPC Request: {method}")
        
        # Call the actual handler
        response = await continuation(handler_call_details)
        
        elapsed = (time.time() - start_time) * 1000
        logger.debug(f"gRPC Response: {method} ({elapsed:.1f}ms)")
        
        return response


class ErrorHandlingInterceptor(grpc.aio.ServerInterceptor):
    """
    gRPC interceptor for error handling.
    
    Catches exceptions and converts them to proper gRPC errors.
    """
    
    async def intercept_service(
        self,
        continuation,
        handler_call_details,
    ):
        try:
            return await continuation(handler_call_details)
        except Exception as e:
            logger.error(f"Unhandled error in {handler_call_details.method}: {e}")
            # The actual handler will raise, this is just for logging
            raise
```

### Step 3: Create the Server

Create `src/truthtable/grpc/server.py`:

```python
"""
gRPC Server Module.

This module provides the main server class that hosts the
EvaluatorService gRPC service.
"""

import asyncio
import logging
import signal
from typing import Callable

import grpc

from .servicer import EvaluatorServicer, LoggingInterceptor
from .generated import add_EvaluatorServiceServicer_to_server
from ..providers import LLMProvider, create_provider
from ..config import Settings

logger = logging.getLogger(__name__)


class GRPCServer:
    """
    Async gRPC server for the Evaluator service.
    
    Handles server lifecycle including graceful shutdown.
    """
    
    def __init__(
        self,
        settings: Settings | None = None,
        llm: LLMProvider | None = None,
    ):
        """
        Initialize the server.
        
        Args:
            settings: Configuration settings
            llm: LLM provider (created from settings if not provided)
        """
        self.settings = settings or Settings()
        self.llm = llm or create_provider(self.settings.llm_provider)
        self.server: grpc.aio.Server | None = None
        self._shutdown_event = asyncio.Event()
    
    async def start(self) -> None:
        """Start the gRPC server."""
        # Create interceptors
        interceptors = [LoggingInterceptor()]
        
        # Create server with options
        options = [
            ("grpc.max_send_message_length", 50 * 1024 * 1024),  # 50MB
            ("grpc.max_receive_message_length", 50 * 1024 * 1024),
            ("grpc.keepalive_time_ms", 30000),
            ("grpc.keepalive_timeout_ms", 5000),
        ]
        
        self.server = grpc.aio.server(
            interceptors=interceptors,
            options=options,
        )
        
        # Register the servicer
        servicer = EvaluatorServicer(self.llm)
        add_EvaluatorServiceServicer_to_server(servicer, self.server)
        
        # Bind to address
        address = f"[::]:{self.settings.grpc_port}"
        self.server.add_insecure_port(address)
        
        # Start serving
        await self.server.start()
        logger.info(f"gRPC server started on port {self.settings.grpc_port}")
        
        # Setup signal handlers for graceful shutdown
        self._setup_signal_handlers()
        
        # Wait until shutdown
        await self._shutdown_event.wait()
    
    async def stop(self, grace_period: float = 5.0) -> None:
        """
        Stop the server gracefully.
        
        Args:
            grace_period: Seconds to wait for in-flight requests
        """
        if self.server:
            logger.info("Shutting down gRPC server...")
            
            # Stop accepting new requests
            await self.server.stop(grace_period)
            
            # Cleanup LLM provider
            await self.llm.close()
            
            logger.info("gRPC server stopped")
    
    def _setup_signal_handlers(self) -> None:
        """Setup signal handlers for graceful shutdown."""
        loop = asyncio.get_event_loop()
        
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(
                sig,
                lambda: asyncio.create_task(self._handle_shutdown()),
            )
    
    async def _handle_shutdown(self) -> None:
        """Handle shutdown signal."""
        logger.info("Received shutdown signal")
        await self.stop()
        self._shutdown_event.set()


async def serve(
    settings: Settings | None = None,
    llm: LLMProvider | None = None,
) -> None:
    """
    Convenience function to start the server.
    
    Args:
        settings: Optional configuration
        llm: Optional LLM provider
    """
    server = GRPCServer(settings, llm)
    await server.start()


# ===== CLI Entry Point =====

def main() -> None:
    """CLI entry point for the gRPC server."""
    import argparse
    
    parser = argparse.ArgumentParser(description="TruthTable Audit Service")
    parser.add_argument(
        "--port",
        type=int,
        default=50051,
        help="gRPC server port (default: 50051)",
    )
    parser.add_argument(
        "--log-level",
        choices=["DEBUG", "INFO", "WARNING", "ERROR"],
        default="INFO",
        help="Logging level",
    )
    args = parser.parse_args()
    
    # Configure logging
    logging.basicConfig(
        level=getattr(logging, args.log_level),
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    )
    
    # Create settings with CLI overrides
    settings = Settings(grpc_port=args.port)
    
    # Run server
    asyncio.run(serve(settings))


if __name__ == "__main__":
    main()
```

### Step 4: Update Config

Update `src/truthtable/config.py` to include gRPC settings:

```python
"""
Configuration management using Pydantic Settings.
"""

from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    """Application settings loaded from environment variables."""
    
    # LLM Provider Settings
    llm_provider: str = "ollama"
    ollama_base_url: str = "http://localhost:11434"
    ollama_model: str = "llama3.2"
    ollama_timeout: float = 120.0
    
    # gRPC Server Settings
    grpc_port: int = 50051
    grpc_max_workers: int = 10
    
    # Audit Settings
    max_claims_per_response: int = 20
    verification_timeout: float = 30.0
    
    # Logging
    log_level: str = "INFO"
    
    class Config:
        env_file = ".env"
        env_prefix = "TRUTHTABLE_"


def get_settings() -> Settings:
    """Get cached settings instance."""
    return Settings()
```

### Step 5: Create Package Exports

Create `src/truthtable/grpc/__init__.py`:

```python
"""
gRPC module for the TruthTable audit service.
"""

from .server import GRPCServer, serve, main
from .servicer import EvaluatorServicer

__all__ = [
    "GRPCServer",
    "serve",
    "main",
    "EvaluatorServicer",
]
```

Update `src/truthtable/__init__.py`:

```python
"""
TruthTable - AI Response Audit Engine.

A LangGraph-based system for detecting hallucinations in LLM responses.
"""

from .config import Settings, get_settings
from .providers import LLMProvider, OllamaProvider, create_provider
from .graphs import run_audit, build_audit_graph

__version__ = "0.1.0"

__all__ = [
    "Settings",
    "get_settings",
    "LLMProvider",
    "OllamaProvider",
    "create_provider",
    "run_audit",
    "build_audit_graph",
]
```

---

## ✅ Testing

### Test 1: Start the Server

```bash
cd backend-python
poetry run python -m truthtable.grpc.server --port 50051 --log-level INFO
```

Expected output:
```
2024-XX-XX INFO - gRPC server started on port 50051
```

### Test 2: Client Test

Create `tests/integration/test_grpc_client.py`:

```python
"""Integration test for gRPC client."""

import asyncio
import grpc
import pytest

from truthtable.grpc.generated import (
    EvaluatorServiceStub,
    AuditRequest,
    ContextDocument,
    HealthRequest,
)


@pytest.fixture
def channel():
    """Create a gRPC channel."""
    return grpc.aio.insecure_channel("localhost:50051")


@pytest.fixture
def stub(channel):
    """Create a service stub."""
    return EvaluatorServiceStub(channel)


@pytest.mark.asyncio
async def test_health_check(stub):
    """Test the health check endpoint."""
    request = HealthRequest()
    response = await stub.HealthCheck(request)
    
    assert response.status in ("healthy", "degraded")
    print(f"Health: {response.status} - {response.message}")


@pytest.mark.asyncio
async def test_evaluate(stub):
    """Test the evaluate endpoint."""
    request = AuditRequest(
        request_id="test-123",
        user_query="What is the capital of France?",
        llm_response="The capital of France is Paris. Paris is also the largest city.",
        context_documents=[
            ContextDocument(
                id="doc1",
                content="Paris is the capital and most populous city of France.",
                source="Wikipedia",
            ),
        ],
    )
    
    response = await stub.Evaluate(request)
    
    print(f"Trust Score: {response.trust_score}%")
    print(f"Grade: {response.grade}")
    print(f"Processing Time: {response.processing_time_ms}ms")
    
    assert response.request_id == "test-123"
    assert 0 <= response.trust_score <= 100
    assert response.grade in ("A+", "A", "B", "C", "D", "F")
```

Run with server running:
```bash
# Terminal 1: Start server
poetry run python -m truthtable.grpc.server

# Terminal 2: Run tests
poetry run pytest tests/integration/test_grpc_client.py -v
```

### Test 3: Quick CLI Client

```bash
# Install grpcurl for testing
brew install grpcurl

# List services (requires reflection enabled)
grpcurl -plaintext localhost:50051 list

# Health check
grpcurl -plaintext \
  -d '{}' \
  localhost:50051 truthtable.EvaluatorService/HealthCheck
```

### Test 4: Unit Tests

Create `tests/unit/test_servicer.py`:

```python
"""Unit tests for the gRPC servicer."""

import pytest
from unittest.mock import AsyncMock, MagicMock, patch

from truthtable.grpc.servicer import EvaluatorServicer


@pytest.fixture
def mock_llm():
    llm = MagicMock()
    llm.health_check = AsyncMock(return_value=True)
    return llm


@pytest.fixture
def servicer(mock_llm):
    return EvaluatorServicer(mock_llm)


class TestHealthCheck:
    
    @pytest.mark.asyncio
    async def test_healthy(self, servicer, mock_llm):
        mock_context = MagicMock()
        request = MagicMock()
        
        response = await servicer.HealthCheck(request, mock_context)
        
        assert response.status == "healthy"
    
    @pytest.mark.asyncio
    async def test_unhealthy(self, servicer, mock_llm):
        mock_llm.health_check = AsyncMock(return_value=False)
        mock_context = MagicMock()
        request = MagicMock()
        
        response = await servicer.HealthCheck(request, mock_context)
        
        assert response.status == "degraded"


class TestBuildResponse:
    
    def test_build_response(self, servicer):
        result = {
            "scores": {
                "trust_score": 85.5,
                "hallucination_rate": 10.0,
                "total_claims": 5,
                "supported_count": 4,
                "unsupported_count": 1,
                "grade": "A",
                "verdict": "Generally reliable",
            },
            "verifications": [
                {
                    "claim": "Test claim",
                    "supported": True,
                    "confidence": 0.9,
                    "evidence": ["evidence"],
                    "reasoning": "Supported by context",
                },
            ],
            "processing_time_ms": 150,
        }
        
        response = servicer._build_response("req-1", result)
        
        assert response.request_id == "req-1"
        assert response.trust_score == 85.5
        assert response.grade == "A"
        assert len(response.verifications) == 1
```

---

## 📁 Final Directory Structure

```
backend-python/
├── src/truthtable/
│   ├── __init__.py
│   ├── config.py
│   ├── providers/
│   │   ├── __init__.py
│   │   ├── base.py
│   │   └── ollama.py
│   ├── graphs/
│   │   ├── __init__.py
│   │   ├── state.py
│   │   ├── audit_graph.py
│   │   └── nodes/
│   │       ├── __init__.py
│   │       ├── decomposer.py
│   │       ├── verifier.py
│   │       └── calculator.py
│   └── grpc/
│       ├── __init__.py
│       ├── server.py
│       ├── servicer.py
│       └── generated/
│           ├── __init__.py
│           ├── evaluator_pb2.py
│           ├── evaluator_pb2.pyi
│           └── evaluator_pb2_grpc.py
└── scripts/
    └── generate_proto.sh
```

---

## 🐛 Common Issues

### Issue: Import errors for generated code

**Solution:** Ensure proto generation script ran successfully:
```bash
./scripts/generate_proto.sh
```

### Issue: "Address already in use"

**Solution:** Kill existing process on the port:
```bash
lsof -ti:50051 | xargs kill -9
```

### Issue: Connection refused from Go

**Solution:** Ensure server is listening on `[::]` (all interfaces), not just localhost.

---

## 🎉 Phase 1 Complete!

You've now built the complete Python Audit Engine:

- ✅ LLM Provider abstraction with Ollama
- ✅ Claim Decomposer node
- ✅ Fact Verifier node
- ✅ Score Calculator node
- ✅ gRPC server exposing the service

---

## ⏭️ Next Phase

Continue to [Phase 2: Go Interceptor Proxy](../phase-2/step-2.1-gin-server.md) to build the high-performance Go proxy.
