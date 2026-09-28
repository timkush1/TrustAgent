# ============================================================================
# mk/py.mk - Python audit engine (backend-python/). Owned by the W-PY stream.
# Included by the root Makefile; targets are declared .PHONY there.
# ============================================================================

POETRY ?= poetry

# The e2e/integration markers are excluded by `addopts` in pyproject.toml, so a
# plain pytest run is the unit suite. `make test-python` is verbose; the `test`
# aggregate runs quietly (a target-specific value on `test` is inherited).
PYTEST_FLAGS ?= -v

##@ Python (backend-python: audit engine)

install-python: ## Install Python dependencies (poetry install)
	@echo "Installing Python dependencies..."
	cd backend-python && $(POETRY) install

proto-python: ## Generate Python gRPC stubs into backend-python/src/truthtable/grpc/pb
	@echo "Generating Python protobuf code..."
	cd backend-python && $(POETRY) run python -m grpc_tools.protoc -I../proto \
		--python_out=src/truthtable/grpc/pb \
		--grpc_python_out=src/truthtable/grpc/pb \
		../proto/evaluator.proto

dev-python: ## Run Python audit engine in development mode
	cd backend-python && $(POETRY) run python -m truthtable.main

test-python: ## Run Python unit tests only (verbose; e2e/integration excluded)
	@echo "Running Python tests..."
	cd backend-python && $(POETRY) run pytest $(PYTEST_FLAGS)

test-e2e: ## Run end-to-end tests (requires full stack running: make up-all)
	cd backend-python && $(POETRY) run pytest -m e2e -v

eval-golden: ## Deterministic golden-set evals, golden_v2 then golden v1 (the CI regression gates, no model needed)
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset golden_v2 --provider mock --check-baseline
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset golden --provider mock --check-baseline

eval-benchmark: ## HaluEval benchmark against local Ollama, EVAL_MODEL (requires make up + ollama-pull)
	cd backend-python && $(POETRY) run python -m evals.datasets.download halueval --samples 200
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --provider ollama --model $(EVAL_MODEL) --output eval-report.json

eval-compare: ## Judge-model comparison on HaluEval (needs OPENAI_API_KEY + ANTHROPIC_API_KEY; about USD 1-5 of usage)
	cd backend-python && $(POETRY) run python -m evals.datasets.download halueval --samples 100
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --provider ollama --model $(EVAL_MODEL) --output eval-ollama.json
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --provider openai --model gpt-4o-mini --output eval-openai.json
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --provider anthropic --model claude-haiku-4-5-20251001 --output eval-anthropic.json
	@echo "Reports written: backend-python/eval-{ollama,openai,anthropic}.json"

lint-python: ## Run ruff and black --check
	@echo "Linting Python..."
	cd backend-python && $(POETRY) run ruff check .
	cd backend-python && $(POETRY) run black --check .

fmt-python: ## Format Python code (black)
	cd backend-python && $(POETRY) run black .

clean-python: ## Remove Python caches
	@echo "Cleaning Python cache..."
	find backend-python -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
	find backend-python -type d -name .pytest_cache -exec rm -rf {} + 2>/dev/null || true

# ---------- Aggregate hooks (see the root Makefile) ----------
install: install-python
proto: proto-python
test: PYTEST_FLAGS :=
test: test-python
lint: lint-python
fmt: fmt-python
clean: clean-python
