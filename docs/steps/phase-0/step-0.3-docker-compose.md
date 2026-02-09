# Step 0.3: Docker Compose Setup

## 🎯 Goal

Create a `docker-compose.yml` file that starts all the infrastructure services TruthTable needs:
- **Redis** - For message queuing and caching
- **Qdrant** - Vector database for storing context embeddings
- **Ollama** - Local LLM for development (free!)

This gives you a one-command setup: `docker-compose up` and everything is running.

---

## 📚 Prerequisites

- Completed Steps 0.1 and 0.2
- Docker Desktop installed and running
- At least 8GB RAM available (Ollama needs memory for LLMs)

Verify Docker is running:
```bash
docker --version
docker-compose --version
```

---

## 🧠 Concepts Explained

### What is Docker Compose?

Docker Compose lets you define multiple containers in one file and start them together. Instead of:

```bash
docker run redis
docker run qdrant
docker run ollama
# ... remembering all the flags each time
```

You just run:
```bash
docker-compose up
```

### The Services We Need

| Service | What It Does | Port | Why We Need It |
|---------|--------------|------|----------------|
| **Redis** | In-memory data store | 6379 | Queue audit jobs, pub/sub for results |
| **Qdrant** | Vector database | 6333 | Store and search document embeddings |
| **Ollama** | Local LLM server | 11434 | Run AI models locally (free!) |

### Data Persistence with Volumes

Docker containers are **ephemeral** - when you stop them, data is lost. We use **volumes** to persist data:

```yaml
volumes:
  - redis_data:/data  # Data survives container restarts
```

---

## 💻 Implementation

### Step 1: Create the Main Docker Compose File

Create `docker-compose.yml` in the project root:

```yaml
# docker-compose.yml
# TruthTable Development Environment
# Usage: docker-compose up -d

version: '3.8'

services:
  # ============================================================
  # REDIS - Message Queue & Cache
  # ============================================================
  redis:
    image: redis:7-alpine
    container_name: truthtable-redis
    ports:
      - "6379:6379"
    volumes:
      - redis_data:/data
    command: redis-server --appendonly yes
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 3
    restart: unless-stopped

  # ============================================================
  # QDRANT - Vector Database
  # ============================================================
  qdrant:
    image: qdrant/qdrant:latest
    container_name: truthtable-qdrant
    ports:
      - "6333:6333"   # REST API
      - "6334:6334"   # gRPC API
    volumes:
      - qdrant_data:/qdrant/storage
    environment:
      - QDRANT__SERVICE__GRPC_PORT=6334
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:6333/"]
      interval: 10s
      timeout: 5s
      retries: 3
    restart: unless-stopped

  # ============================================================
  # OLLAMA - Local LLM Server
  # ============================================================
  ollama:
    image: ollama/ollama:latest
    container_name: truthtable-ollama
    ports:
      - "11434:11434"
    volumes:
      - ollama_data:/root/.ollama
    # Uncomment below if you have an NVIDIA GPU
    # deploy:
    #   resources:
    #     reservations:
    #       devices:
    #         - driver: nvidia
    #           count: 1
    #           capabilities: [gpu]
    restart: unless-stopped

# ============================================================
# VOLUMES - Persistent Storage
# ============================================================
volumes:
  redis_data:
    driver: local
  qdrant_data:
    driver: local
  ollama_data:
    driver: local

# ============================================================
# NETWORKS - Service Communication
# ============================================================
networks:
  default:
    name: truthtable-network
```

### Step 2: Create Environment File Template

Create `.env.example` in the project root:

```bash
# .env.example
# Copy this to .env and fill in your values

# ============ Infrastructure ============
REDIS_URL=redis://localhost:6379
QDRANT_HOST=localhost
QDRANT_PORT=6333
OLLAMA_BASE_URL=http://localhost:11434

# ============ Go Proxy ============
TRUTHTABLE_SERVER_PORT=8080
TRUTHTABLE_WS_PORT=8081
TRUTHTABLE_GRPC_AUDIT_ADDRESS=localhost:50051

# ============ Python Engine ============
TRUTHTABLE_GRPC_PORT=50051
TRUTHTABLE_OLLAMA_MODEL=llama3.2

# ============ Observability ============
TRUTHTABLE_LOG_LEVEL=debug

# ============ External LLMs (Optional) ============
# OPENAI_API_KEY=sk-xxx
# ANTHROPIC_API_KEY=sk-ant-xxx
```

Copy it to `.env`:
```bash
cp .env.example .env
```

### Step 3: Create Helper Scripts

Create `scripts/dev-up.sh`:

```bash
#!/bin/bash
# Start development environment

set -e

echo "🚀 Starting TruthTable development environment..."

# Start infrastructure
docker-compose up -d

# Wait for services to be healthy
echo "⏳ Waiting for services to be ready..."

# Wait for Redis
until docker-compose exec -T redis redis-cli ping > /dev/null 2>&1; do
    echo "  Waiting for Redis..."
    sleep 1
done
echo "✅ Redis is ready"

# Wait for Qdrant
until curl -s http://localhost:6333/ > /dev/null 2>&1; do
    echo "  Waiting for Qdrant..."
    sleep 1
done
echo "✅ Qdrant is ready"

# Wait for Ollama
until curl -s http://localhost:11434/ > /dev/null 2>&1; do
    echo "  Waiting for Ollama..."
    sleep 1
done
echo "✅ Ollama is ready"

# Pull the LLM model if not already present
echo "📦 Ensuring LLM model is available..."
docker-compose exec -T ollama ollama pull llama3.2 || true

echo ""
echo "🎉 Development environment is ready!"
echo ""
echo "Services:"
echo "  Redis:  localhost:6379"
echo "  Qdrant: localhost:6333"
echo "  Ollama: localhost:11434"
echo ""
echo "Next steps:"
echo "  1. Start Python engine: cd backend-python && poetry run python -m truthtable.main"
echo "  2. Start Go proxy:      cd backend-go && go run ./cmd/proxy"
echo "  3. Start React app:     cd frontend-react && npm run dev"
```

Create `scripts/dev-down.sh`:

```bash
#!/bin/bash
# Stop development environment

echo "🛑 Stopping TruthTable development environment..."
docker-compose down
echo "✅ All services stopped"
```

Create `scripts/dev-logs.sh`:

```bash
#!/bin/bash
# View logs from all services

docker-compose logs -f "$@"
```

Create `scripts/dev-reset.sh`:

```bash
#!/bin/bash
# Reset all data (careful!)

read -p "⚠️  This will delete all data. Are you sure? (y/N) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "🗑️  Resetting all data..."
    docker-compose down -v
    echo "✅ All data deleted"
else
    echo "Cancelled"
fi
```

Make scripts executable:

```bash
mkdir -p scripts
chmod +x scripts/*.sh
```

### Step 4: Add to .gitignore

Create or update `.gitignore`:

```bash
# Environment variables (contains secrets)
.env

# Python
__pycache__/
*.py[cod]
*$py.class
.venv/
.pytest_cache/

# Go
*.exe
*.exe~
*.dll
*.so
*.dylib

# Node
node_modules/
dist/
.vite/

# IDE
.idea/
.vscode/
*.swp
*.swo

# Docker
docker-compose.override.yml

# OS
.DS_Store
Thumbs.db

# Generated code (optional - some teams commit these)
# backend-go/api/audit/
# backend-python/src/truthtable/grpc/pb/
```

---

## ✅ Testing

### Test 1: Start All Services

```bash
# Start in foreground to see logs
docker-compose up

# Or start in background
docker-compose up -d
```

### Test 2: Verify Redis

```bash
# Connect to Redis CLI
docker-compose exec redis redis-cli

# In Redis CLI:
127.0.0.1:6379> PING
PONG
127.0.0.1:6379> SET test "hello"
OK
127.0.0.1:6379> GET test
"hello"
127.0.0.1:6379> DEL test
(integer) 1
127.0.0.1:6379> exit
```

### Test 3: Verify Qdrant

```bash
# Check Qdrant health
curl http://localhost:6333/

# Expected response:
# {"title":"qdrant - vector search engine","version":"..."}

# Check collections (should be empty initially)
curl http://localhost:6333/collections

# Expected response:
# {"result":{"collections":[]},"status":"ok","time":...}
```

### Test 4: Verify Ollama

```bash
# Check Ollama is running
curl http://localhost:11434/

# Expected response:
# Ollama is running

# List available models
curl http://localhost:11434/api/tags

# Pull a model (this might take a few minutes)
docker-compose exec ollama ollama pull llama3.2

# Test the model
curl http://localhost:11434/api/generate -d '{
  "model": "llama3.2",
  "prompt": "Say hello in one word",
  "stream": false
}'
```

### Test 5: Check All Services

```bash
# View running containers
docker-compose ps

# Expected output:
# NAME                  STATUS         PORTS
# truthtable-ollama     Up             0.0.0.0:11434->11434/tcp
# truthtable-qdrant     Up (healthy)   0.0.0.0:6333->6333/tcp, 0.0.0.0:6334->6334/tcp
# truthtable-redis      Up (healthy)   0.0.0.0:6379->6379/tcp
```

---

## 🧪 What Each Service Does

### Redis Usage in TruthTable

```
┌─────────────────────────────────────────────────────────────────┐
│                         REDIS                                    │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Queue: truthtable:audit:queue                                  │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ Job 1 │ Job 2 │ Job 3 │ ...                             │   │
│  └─────────────────────────────────────────────────────────┘   │
│  Go Proxy pushes ──────────────────────► Python Engine pops    │
│                                                                  │
│  PubSub: truthtable:audit:events                                │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ Python Engine publishes ─────────► Go Proxy subscribes  │   │
│  │                                          │               │   │
│  │                                          ▼               │   │
│  │                                    WebSocket to React    │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Qdrant Usage in TruthTable

```
┌─────────────────────────────────────────────────────────────────┐
│                         QDRANT                                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Collection: truthtable_context                                  │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ Vector 1: [0.23, 0.87, ...]  →  "Paris is the capital"  │   │
│  │ Vector 2: [0.45, 0.12, ...]  →  "Eiffel Tower height"   │   │
│  │ Vector 3: [0.67, 0.34, ...]  →  "French cuisine..."     │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                  │
│  Python Engine searches for similar vectors to verify claims    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Ollama Usage in TruthTable

```
┌─────────────────────────────────────────────────────────────────┐
│                         OLLAMA                                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Model: llama3.2 (or mistral)                                   │
│                                                                  │
│  Used for:                                                       │
│  1. Decomposing responses into claims                           │
│     "Break this text into atomic factual statements..."         │
│                                                                  │
│  2. Verifying claims against context (NLI)                      │
│     "Given this context, is this claim supported?"              │
│                                                                  │
│  3. Generating reasoning traces                                  │
│     "Explain why this response contains hallucinations..."      │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 🐛 Common Issues

### Issue: `Cannot connect to Docker daemon`

**Solution:** Make sure Docker Desktop is running:
- macOS: Click the Docker icon in menu bar
- Windows: Start Docker Desktop from Start menu

### Issue: `Port 6379 already in use`

**Solution:** Another Redis instance is running. Either stop it:
```bash
brew services stop redis
```

Or change the port in docker-compose.yml:
```yaml
ports:
  - "6380:6379"  # Use 6380 on host
```

### Issue: Ollama model download is slow

**Solution:** The first download takes time (several GB). Let it run. Subsequent starts will be instant because the model is persisted in a volume.

### Issue: Out of memory when running Ollama

**Solution:** 
1. Increase Docker Desktop memory limit (Settings → Resources)
2. Or use a smaller model: `ollama pull llama3.2:1b` (1 billion params vs 8B)

### Issue: `no matching manifest for linux/arm64`

**Solution:** You're on Apple Silicon. Update the Ollama image:
```yaml
ollama:
  platform: linux/arm64
  image: ollama/ollama:latest
```

---

## 📖 Further Reading

- [Docker Compose Documentation](https://docs.docker.com/compose/)
- [Redis Quick Start](https://redis.io/docs/getting-started/)
- [Qdrant Documentation](https://qdrant.tech/documentation/)
- [Ollama Documentation](https://ollama.ai/docs/)

---

## ⏭️ Next Step

Continue to [Step 1.1: LLM Provider Interface](../phase-1/step-1.1-llm-provider-interface.md) to start building the Python audit engine.
