.PHONY: help install proto up down clean test

help: ## Show this help message
	@echo "TruthTable - Available Commands:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ========== Setup ==========

install: ## Install all dependencies
	@echo "Installing Go dependencies..."
	cd backend-go && go mod download
	@echo "Installing Python dependencies..."
	cd backend-python && poetry install
	@echo "Installing Node dependencies..."
	cd frontend-react && npm install
	@echo "✅ All dependencies installed"

proto: ## Generate code from protobuf definitions
	@echo "Generating Go protobuf code..."
	cd proto && protoc --go_out=../backend-go/api/audit/v1 --go-grpc_out=../backend-go/api/audit/v1 evaluator.proto
	@echo "Generating Python protobuf code..."
	cd proto && python -m grpc_tools.protoc -I. --python_out=../backend-python/src/truthtable/grpc/pb --grpc_python_out=../backend-python/src/truthtable/grpc/pb evaluator.proto
	@echo "✅ Protobuf code generated"

# ========== Docker ==========

up: ## Start all infrastructure services
	docker-compose up -d redis qdrant ollama prometheus grafana
	@echo "⏳ Waiting for services to be healthy..."
	@sleep 5
	@echo "✅ Infrastructure services running"
	@echo "Redis:      http://localhost:6379"
	@echo "Qdrant:     http://localhost:6333"
	@echo "Ollama:     http://localhost:11434"
	@echo "Prometheus: http://localhost:9090"
	@echo "Grafana:    http://localhost:3001 (admin/admin)"

up-all: ## Start all services (infrastructure + application)
	docker-compose up -d
	@echo "✅ All services running"

down: ## Stop all services
	docker-compose down
	@echo "✅ All services stopped"

logs: ## View logs from all services
	docker-compose logs -f

# ========== Development ==========

dev-python: ## Run Python audit engine in development mode
	cd backend-python && poetry run python -m truthtable.main

dev-go: ## Run Go proxy in development mode
	cd backend-go && go run ./cmd/proxy

dev-react: ## Run React dashboard in development mode
	cd frontend-react && npm run dev

# ========== Testing ==========

test: ## Run all tests
	@echo "Running Python tests..."
	cd backend-python && poetry run pytest
	@echo "Running Go tests..."
	cd backend-go && go test ./...
	@echo "Running React tests..."
	cd frontend-react && npm test
	@echo "✅ All tests passed"

test-python: ## Run Python tests only
	cd backend-python && poetry run pytest -v

test-go: ## Run Go tests only
	cd backend-go && go test -v ./...

test-react: ## Run React tests only
	cd frontend-react && npm test

# ========== Linting ==========

lint: ## Run linters for all projects
	@echo "Linting Python..."
	cd backend-python && poetry run ruff check .
	cd backend-python && poetry run black --check .
	@echo "Linting Go..."
	cd backend-go && go fmt ./...
	cd backend-go && go vet ./...
	@echo "Linting React..."
	cd frontend-react && npm run lint
	@echo "✅ All linting passed"

fmt: ## Format all code
	cd backend-python && poetry run black .
	cd backend-go && go fmt ./...
	cd frontend-react && npm run format

# ========== Ollama ==========

ollama-pull: ## Pull the default Ollama model
	docker exec -it truthtable-ollama ollama pull llama3.2
	@echo "✅ Llama 3.2 model downloaded"

ollama-list: ## List available Ollama models
	docker exec -it truthtable-ollama ollama list

# ========== Cleanup ==========

clean: ## Clean all build artifacts and caches
	@echo "Cleaning Go cache..."
	cd backend-go && go clean -cache
	@echo "Cleaning Python cache..."
	find backend-python -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
	find backend-python -type d -name .pytest_cache -exec rm -rf {} + 2>/dev/null || true
	@echo "Cleaning Node cache..."
	cd frontend-react && rm -rf node_modules dist .vite
	@echo "✅ All caches cleaned"

clean-data: ## Clean all Docker volumes (WARNING: deletes all data)
	docker-compose down -v
	@echo "✅ All data volumes removed"

# ========== Monitoring ==========

metrics: ## Open Prometheus metrics interface
	open http://localhost:9090

dashboards: ## Open Grafana dashboards
	open http://localhost:3001

# ========== Status ==========

status: ## Show status of all services
	@echo "Service Status:"
	@docker-compose ps
