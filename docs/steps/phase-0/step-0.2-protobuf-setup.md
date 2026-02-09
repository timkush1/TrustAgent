# Step 0.2: Protocol Buffer Definitions

## 🎯 Goal

Create the `evaluator.proto` file that defines how the Go proxy and Python audit engine communicate. This is the "contract" between the two services - if both follow it, they can talk to each other regardless of their programming language.

---

## 📚 Prerequisites

- Completed Step 0.1 (Project structure exists)
- Install Protocol Buffer compiler:
  ```bash
  # macOS
  brew install protobuf
  
  # Verify installation
  protoc --version
  ```

---

## 🧠 Concepts Explained

### What are Protocol Buffers (Protobuf)?

Protocol Buffers are Google's way of defining data structures that work across languages. Think of it like JSON, but:

| JSON | Protobuf |
|------|----------|
| Text-based (readable) | Binary (compact, fast) |
| Schema-free | Strongly typed schema |
| ~100 bytes for small message | ~30 bytes for same message |
| Any field, any type | Must define fields first |

### What is gRPC?

gRPC is a way for services to call functions on other services. Instead of:

```
HTTP: POST /api/audit
Body: {"query": "...", "response": "..."}
```

You have:

```
gRPC: AuditService.SubmitAudit(AuditRequest)
Returns: AuditSubmission
```

**Benefits:**
- Type safety (compiler catches mistakes)
- Faster (binary protocol)
- Streaming support (send data in chunks)
- Auto-generated client code

### The .proto File

A `.proto` file defines:
1. **Messages** - Data structures (like classes/structs)
2. **Services** - Functions you can call remotely
3. **Enums** - Fixed sets of values

---

## 💻 Implementation

### Step 1: Create the Proto File

Create `proto/evaluator.proto`:

```protobuf
// proto/evaluator.proto
syntax = "proto3";

package truthtable.audit.v1;

// Go package path for generated code
option go_package = "github.com/truthtable/backend-go/api/audit/v1";

// ============================================================
// SERVICE DEFINITION
// These are the functions Python exposes for Go to call
// ============================================================

service AuditService {
    // Async audit - returns immediately, results come via Redis PubSub
    // Use this in production for non-blocking operation
    rpc SubmitAudit(AuditRequest) returns (AuditSubmission);
    
    // Sync audit - blocks until complete (useful for testing)
    // Use this for debugging or when you need immediate results
    rpc AuditSync(AuditRequest) returns (AuditResult);
    
    // Health check - verify the service is running
    rpc HealthCheck(HealthRequest) returns (HealthResponse);
}

// ============================================================
// REQUEST MESSAGES
// Data sent FROM Go TO Python
// ============================================================

// The main audit request containing all data needed for verification
message AuditRequest {
    // Unique identifier for this request (UUID format)
    // Used to correlate results back to the original request
    string request_id = 1;
    
    // The original question the user asked
    string user_query = 2;
    
    // The LLM's generated response that we need to verify
    string llm_response = 3;
    
    // The RAG context chunks that were used to generate the response
    // These are the "source of truth" we verify against
    repeated ContextDocument context = 4;
    
    // Which LLM provider generated this (e.g., "openai", "anthropic")
    string provider = 5;
    
    // Specific model name (e.g., "gpt-4", "claude-3-opus")
    string model = 6;
    
    // When the request was made (Unix timestamp in milliseconds)
    int64 timestamp_ms = 7;
    
    // Additional metadata (custom tags, user info, etc.)
    map<string, string> metadata = 8;
}

// A single context document from the RAG retrieval
message ContextDocument {
    // Unique identifier for this document
    string id = 1;
    
    // The actual text content
    string content = 2;
    
    // Where this document came from (filename, URL, etc.)
    string source = 3;
    
    // How relevant the RAG system thought this was (0.0 - 1.0)
    float relevance_score = 4;
}

// Empty message for health check (no parameters needed)
message HealthRequest {}

// ============================================================
// RESPONSE MESSAGES
// Data sent FROM Python TO Go
// ============================================================

// Immediate response when submitting an async audit
message AuditSubmission {
    // Unique ID for this audit job (different from request_id)
    string audit_id = 1;
    
    // Current status: "queued", "processing"
    string status = 2;
    
    // Position in the queue (0 if processing immediately)
    int32 queue_position = 3;
}

// Full audit result (returned by AuditSync or via Redis PubSub)
message AuditResult {
    // The audit job identifier
    string audit_id = 1;
    
    // Links back to the original request
    string request_id = 2;
    
    // ===== SCORES =====
    // How much of the response is supported by the context (0.0 - 1.0)
    float faithfulness_score = 3;
    
    // How relevant the response is to the question (0.0 - 1.0)
    float relevancy_score = 4;
    
    // Combined overall score (weighted average)
    float overall_score = 5;
    
    // ===== FINDINGS =====
    // True if any claims were unsupported
    bool hallucination_detected = 6;
    
    // Detailed breakdown of each claim
    repeated ClaimVerification claims = 7;
    
    // ===== DIAGNOSTICS =====
    // Human-readable explanation of findings
    string reasoning_trace = 8;
    
    // How long the audit took (milliseconds)
    int64 processing_time_ms = 9;
}

// Verification result for a single atomic claim
message ClaimVerification {
    // The extracted claim (e.g., "Paris is the capital of France")
    string claim = 1;
    
    // Whether this claim is supported by the context
    VerificationStatus status = 2;
    
    // Model's confidence in the verification (0.0 - 1.0)
    float confidence = 3;
    
    // Relevant snippets from context that support/refute this claim
    repeated string evidence = 4;
}

// Possible verification outcomes
enum VerificationStatus {
    // Default value (should not occur in valid responses)
    UNKNOWN = 0;
    
    // Claim is fully supported by the context
    SUPPORTED = 1;
    
    // Claim contradicts the context (hallucination!)
    UNSUPPORTED = 2;
    
    // Some aspects supported, others not verifiable
    PARTIALLY_SUPPORTED = 3;
}

// Health check response
message HealthResponse {
    // Overall health status
    bool healthy = 1;
    
    // Service version
    string version = 2;
    
    // Status of dependencies (redis, qdrant, ollama, etc.)
    map<string, bool> dependencies = 3;
}
```

### Step 2: Create Go Code Generator Script

Create `backend-go/scripts/generate.sh`:

```bash
#!/bin/bash
# Generate Go code from proto files

set -e  # Exit on error

PROTO_DIR="../proto"
GO_OUT_DIR="./api"

# Ensure output directory exists
mkdir -p ${GO_OUT_DIR}/audit/v1

# Generate Go code
protoc \
    --proto_path=${PROTO_DIR} \
    --go_out=${GO_OUT_DIR} \
    --go_opt=paths=source_relative \
    --go-grpc_out=${GO_OUT_DIR} \
    --go-grpc_opt=paths=source_relative \
    ${PROTO_DIR}/evaluator.proto

echo "Go gRPC code generated successfully!"
```

Make it executable:

```bash
chmod +x backend-go/scripts/generate.sh
```

### Step 3: Create Python Code Generator Script

Create `backend-python/scripts/generate.sh`:

```bash
#!/bin/bash
# Generate Python code from proto files

set -e  # Exit on error

PROTO_DIR="../proto"
PY_OUT_DIR="./src/truthtable/grpc/pb"

# Ensure output directory exists
mkdir -p ${PY_OUT_DIR}

# Generate Python code
python -m grpc_tools.protoc \
    --proto_path=${PROTO_DIR} \
    --python_out=${PY_OUT_DIR} \
    --grpc_python_out=${PY_OUT_DIR} \
    ${PROTO_DIR}/evaluator.proto

# Fix imports in generated files (Python quirk)
# The generated code uses absolute imports, but we need relative ones
sed -i '' 's/import evaluator_pb2/from . import evaluator_pb2/' ${PY_OUT_DIR}/evaluator_pb2_grpc.py

echo "Python gRPC code generated successfully!"
```

Make it executable:

```bash
mkdir -p backend-python/scripts
chmod +x backend-python/scripts/generate.sh
```

### Step 4: Install Required Tools

**For Go:**

```bash
cd backend-go

# Install protoc plugins for Go
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Add gRPC dependency
go get google.golang.org/grpc

cd ..
```

**For Python:**

```bash
cd backend-python

# Create pyproject.toml (we'll use poetry for dependency management)
cat > pyproject.toml << 'EOF'
[tool.poetry]
name = "truthtable"
version = "0.1.0"
description = "AI Hallucination Detection Engine"
authors = ["TruthTable Team"]
packages = [{include = "truthtable", from = "src"}]

[tool.poetry.dependencies]
python = "^3.11"
grpcio = "^1.60.0"
grpcio-tools = "^1.60.0"
protobuf = "^4.25.0"

[tool.poetry.group.dev.dependencies]
pytest = "^7.4.0"
pytest-asyncio = "^0.21.0"

[build-system]
requires = ["poetry-core"]
build-backend = "poetry.core.masonry.api"
EOF

# Install dependencies (if you have poetry installed)
# poetry install

cd ..
```

---

## ✅ Testing

### Verify the Proto File is Valid

```bash
# Check proto syntax
protoc --proto_path=proto --lint_out=. proto/evaluator.proto

# If no errors, the file is valid!
```

### Generate and Verify Go Code

```bash
cd backend-go
./scripts/generate.sh

# Check generated files exist
ls -la api/audit/v1/
# Should show: evaluator.pb.go, evaluator_grpc.pb.go
```

### Generate and Verify Python Code

```bash
cd backend-python
./scripts/generate.sh

# Check generated files exist
ls -la src/truthtable/grpc/pb/
# Should show: evaluator_pb2.py, evaluator_pb2_grpc.py
```

---

## 🧪 Understanding the Generated Code

### Go Generated Code

The generator creates two files:

**`evaluator.pb.go`** - Data structures:
```go
// Generated struct for AuditRequest
type AuditRequest struct {
    RequestId   string              `protobuf:"bytes,1,opt,name=request_id"`
    UserQuery   string              `protobuf:"bytes,2,opt,name=user_query"`
    LlmResponse string              `protobuf:"bytes,3,opt,name=llm_response"`
    Context     []*ContextDocument  `protobuf:"bytes,4,rep,name=context"`
    // ... etc
}
```

**`evaluator_grpc.pb.go`** - Client and server interfaces:
```go
// Client interface (Go uses this to call Python)
type AuditServiceClient interface {
    SubmitAudit(ctx context.Context, in *AuditRequest) (*AuditSubmission, error)
    AuditSync(ctx context.Context, in *AuditRequest) (*AuditResult, error)
    HealthCheck(ctx context.Context, in *HealthRequest) (*HealthResponse, error)
}

// Server interface (Python implements this)
type AuditServiceServer interface {
    SubmitAudit(context.Context, *AuditRequest) (*AuditSubmission, error)
    AuditSync(context.Context, *AuditRequest) (*AuditResult, error)
    HealthCheck(context.Context, *HealthRequest) (*HealthResponse, error)
}
```

### Python Generated Code

**`evaluator_pb2.py`** - Data structures:
```python
# Generated class for AuditRequest
class AuditRequest:
    request_id: str
    user_query: str
    llm_response: str
    context: List[ContextDocument]
    # ... etc
```

**`evaluator_pb2_grpc.py`** - Server base class:
```python
# Servicer base class (we implement this)
class AuditServiceServicer:
    def SubmitAudit(self, request, context):
        raise NotImplementedError()
    
    def AuditSync(self, request, context):
        raise NotImplementedError()
```

---

## 🐛 Common Issues

### Issue: `protoc: command not found`

**Solution:** Install protobuf compiler:
```bash
brew install protobuf
```

### Issue: `protoc-gen-go: program not found`

**Solution:** Make sure Go bin directory is in PATH:
```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

Add this to your `~/.zshrc` or `~/.bashrc`.

### Issue: Python import errors in generated code

**Solution:** The sed command might not work on Linux. Use:
```bash
sed -i 's/import evaluator_pb2/from . import evaluator_pb2/' file.py
```
(Note: no `''` after `-i` on Linux)

### Issue: Poetry not installed

**Solution:** Install poetry:
```bash
curl -sSL https://install.python-poetry.org | python3 -
```

---

## 📖 Further Reading

- [Protocol Buffers Language Guide](https://developers.google.com/protocol-buffers/docs/proto3)
- [gRPC Concepts](https://grpc.io/docs/what-is-grpc/core-concepts/)
- [Go gRPC Tutorial](https://grpc.io/docs/languages/go/basics/)
- [Python gRPC Tutorial](https://grpc.io/docs/languages/python/basics/)

---

## ⏭️ Next Step

Continue to [Step 0.3: Docker Compose Setup](step-0.3-docker-compose.md) to configure the infrastructure services.
