# Step 0.1: Project Structure Setup

## 🎯 Goal

Create the foundational folder structure for TruthTable with three separate service roots:
- `/backend-go` - The Go reverse proxy
- `/backend-python` - The Python audit engine
- `/frontend-react` - The React dashboard

This separation allows each service to:
- Be developed independently
- Have its own dependencies
- Be deployed as separate containers
- Scale independently

---

## 📚 Prerequisites

Before starting, make sure you have installed:
- **Git** - Version control
- **Go 1.22+** - For the proxy service
- **Python 3.11+** - For the audit engine
- **Node.js 20+** - For the React dashboard
- **Docker & Docker Compose** - For running infrastructure

---

## 🧠 Concepts Explained

### Why Three Separate Folders?

In a **microservices architecture**, each service is a separate, independent unit. This is different from a **monolith** where everything is in one codebase.

```
Monolith:                    Microservices:
┌─────────────────┐          ┌──────────┐  ┌──────────┐  ┌──────────┐
│  Everything in  │    vs    │ Service  │  │ Service  │  │ Service  │
│  one folder     │          │    A     │  │    B     │  │    C     │
└─────────────────┘          └──────────┘  └──────────┘  └──────────┘
```

**Benefits:**
- 🔧 Different languages for different jobs (Go for speed, Python for AI)
- 📦 Deploy only what changed
- 🔄 Scale busy services independently
- 👥 Teams can work in parallel

### Why This Specific Structure?

We follow conventions for each language:

**Go Convention:**
- `cmd/` - Entry points (main.go files)
- `internal/` - Private code (can't be imported by others)
- `pkg/` - Public, reusable code

**Python Convention:**
- `src/` - Source code
- `tests/` - Test files
- `pyproject.toml` - Modern Python project config

**React Convention:**
- `src/` - All source code
- `public/` - Static assets
- `package.json` - Dependencies and scripts

---

## 💻 Implementation

### Step 1: Create Root Directory Structure

Open your terminal and navigate to your project folder:

```bash
cd /Users/t.kushmaro/personal2/my_projects/trustAgent
```

Create the three service directories:

```bash
mkdir -p backend-go backend-python frontend-react
```

### Step 2: Set Up Go Project Structure

```bash
# Create Go directory structure
mkdir -p backend-go/cmd/proxy
mkdir -p backend-go/internal/{config,proxy,grpc,websocket,buffer,metrics}
mkdir -p backend-go/pkg/{llmprovider,models}
mkdir -p backend-go/api/proto
mkdir -p backend-go/scripts
```

Create the initial Go module:

```bash
cd backend-go
go mod init github.com/truthtable/backend-go
cd ..
```

**What each folder is for:**

| Folder | Purpose |
|--------|---------|
| `cmd/proxy/` | The main.go entry point |
| `internal/config/` | Configuration loading |
| `internal/proxy/` | HTTP handlers and middleware |
| `internal/grpc/` | gRPC client to Python |
| `internal/websocket/` | WebSocket hub for dashboard |
| `internal/buffer/` | Response capture and queuing |
| `internal/metrics/` | Prometheus metrics |
| `pkg/llmprovider/` | LLM provider interfaces (reusable) |
| `pkg/models/` | Shared data structures |
| `api/proto/` | Protocol buffer definitions |

### Step 3: Set Up Python Project Structure

```bash
# Create Python directory structure
mkdir -p backend-python/src/truthtable/{grpc/pb,graphs/nodes,providers,vectorstore,scoring}
mkdir -p backend-python/tests/{unit,integration,fixtures}
```

Create empty `__init__.py` files (Python requires these):

```bash
find backend-python/src -type d -exec touch {}/__init__.py \;
```

**What each folder is for:**

| Folder | Purpose |
|--------|---------|
| `src/truthtable/` | Main package |
| `grpc/` | gRPC server implementation |
| `grpc/pb/` | Generated protobuf code |
| `graphs/` | LangGraph workflow |
| `graphs/nodes/` | Individual workflow steps |
| `providers/` | LLM provider implementations |
| `vectorstore/` | Vector database adapters |
| `scoring/` | Evaluation metrics |

### Step 4: Set Up React Project Structure

```bash
# Create React directory structure
mkdir -p frontend-react/src/{components/{ui,layout,dashboard,audit,charts},hooks,stores,services,types,lib,styles}
mkdir -p frontend-react/public
```

**What each folder is for:**

| Folder | Purpose |
|--------|---------|
| `components/ui/` | Reusable UI components (Button, Card) |
| `components/layout/` | Page layout components |
| `components/dashboard/` | Dashboard-specific components |
| `components/audit/` | Audit feed and detail views |
| `components/charts/` | Data visualizations |
| `hooks/` | Custom React hooks |
| `stores/` | State management (Zustand) |
| `services/` | API and WebSocket clients |
| `types/` | TypeScript type definitions |

### Step 5: Create Shared Proto Directory

```bash
# Create shared proto directory at root level
mkdir -p proto
```

This is where the `.proto` file lives that both Go and Python use.

### Step 6: Create Documentation Structure

```bash
# Create docs structure
mkdir -p docs/steps/{phase-0,phase-1,phase-2,phase-3,phase-4}
mkdir -p docs/adrs  # Architecture Decision Records
```

---

## ✅ Testing

Verify your structure is correct:

```bash
# From the project root
tree -L 3 -d
```

Expected output:
```
.
├── backend-go
│   ├── api
│   │   └── proto
│   ├── cmd
│   │   └── proxy
│   ├── internal
│   │   ├── buffer
│   │   ├── config
│   │   ├── grpc
│   │   ├── metrics
│   │   ├── proxy
│   │   └── websocket
│   ├── pkg
│   │   ├── llmprovider
│   │   └── models
│   └── scripts
├── backend-python
│   ├── src
│   │   └── truthtable
│   └── tests
│       ├── fixtures
│       ├── integration
│       └── unit
├── docs
│   ├── adrs
│   └── steps
├── frontend-react
│   ├── public
│   └── src
│       ├── components
│       ├── hooks
│       ├── lib
│       ├── services
│       ├── stores
│       ├── styles
│       └── types
└── proto
```

Also verify the Go module was created:

```bash
cat backend-go/go.mod
```

Should show:
```
module github.com/truthtable/backend-go

go 1.22
```

---

## 🐛 Common Issues

### Issue: `tree` command not found

**Solution:** Install it:
```bash
# macOS
brew install tree

# Ubuntu/Debian
sudo apt install tree
```

### Issue: Go module creation fails

**Solution:** Make sure Go is installed:
```bash
go version
# Should show: go version go1.22.x darwin/arm64 (or similar)
```

If not installed:
```bash
brew install go
```

### Issue: Python __init__.py files not created

**Solution:** Run the find command from the project root:
```bash
find backend-python/src -type d -exec touch {}/__init__.py \;
```

---

## 📖 Further Reading

- [Go Project Layout Standard](https://github.com/golang-standards/project-layout)
- [Python Project Structure Best Practices](https://docs.python-guide.org/writing/structure/)
- [Vite Project Structure](https://vitejs.dev/guide/)
- [Microservices Architecture Patterns](https://microservices.io/patterns/index.html)

---

## ⏭️ Next Step

Continue to [Step 0.2: Protocol Buffer Definitions](step-0.2-protobuf-setup.md) to define the communication contract between services.
