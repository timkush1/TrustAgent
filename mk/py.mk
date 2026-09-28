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

# ---------- Benchmarks (SPEC P6, 2.13): owner-run, paid, hard-capped ----------
# Keys come from the environment or .env (one OpenRouter key covers Jev and the
# judge; docs/JEV-ACCESS.md). `evals.bench run` checks the gate-eval binary, the
# judge key and a price for the judge model before the first paid call, and the
# cap covers every session of a run (rerunning resumes from the cache and ledger
# in backend-python/evals/.cache/). Runs land in backend-python/evals/results/;
# only `make bench-publish` writes docs/benchmarks/ and contracts/claims/measured.json,
# and it refuses simulated (mock) output.
BENCH_MAX_COST_USD ?= 5
BENCH_N ?= 200
BENCH_FULL_AUDIT_N ?= 50
BENCH_SEED ?= 13
BENCH_CONCURRENCY ?= 4
BENCH_JUDGE_PROVIDER ?= openrouter
BENCH_REGION ?= local
RAGTRUTH_REVISION ?= main
GATE_EVAL_BIN ?= $(CURDIR)/backend-go/bin/gate-eval
EVAL_LIVE_RAGTRUTH_N ?= 400
EVAL_LIVE_AGGREFACT_N ?= 600
EVAL_LIVE_RAGTRUTH_MAX_USD ?= 40
EVAL_LIVE_AGGREFACT_MAX_USD ?= 20
AGGREFACT_FILE ?=
HF_TOKEN_FILE ?=
RUN ?=

BENCH_RUN = cd backend-python && $(POETRY) run python -m evals.bench run \
	--seed $(BENCH_SEED) --gate-eval-bin "$(GATE_EVAL_BIN)" --judge-provider $(BENCH_JUDGE_PROVIDER) \
	--concurrency $(BENCH_CONCURRENCY) --region $(BENCH_REGION)

define require_gate_eval
	@test -x "$(GATE_EVAL_BIN)" || { \
		echo "$(1) needs the Go gate-eval binary at $(GATE_EVAL_BIN):"; \
		echo "  go -C backend-go build -o bin/gate-eval ./cmd/gate-eval   (or pass GATE_EVAL_BIN=...)"; \
		exit 2; }
endef

bench-lite: ## RAGTruth-QA bench-lite: gate + LLM same checks (200), full audit + cascade (50); hard cap USD 5 (needs OPENROUTER_API_KEY, gate-eval)
	$(call require_gate_eval,bench-lite)
	cd backend-python && $(POETRY) run python -m evals.datasets.ragtruth --subset qa --split test \
		--n $(BENCH_N) --seed $(BENCH_SEED) --revision $(RAGTRUTH_REVISION) \
		--out evals/datasets/ragtruth/bench-lite.jsonl
	$(BENCH_RUN) --tier lite --dataset evals/datasets/ragtruth/bench-lite.jsonl \
		--max-cost-usd $(BENCH_MAX_COST_USD) --full-audit-n $(BENCH_FULL_AUDIT_N)

bench-publish: ## Publish the latest measured bench run (or RUN=<run.json>) to docs/benchmarks and measured.json; refuses mock output
	cd backend-python && $(POETRY) run python -m evals.bench publish $(if $(RUN),--run "$(RUN)",--latest)

eval-live: ## Full eval: RAGTruth-QA 400 + LLM-AggreFact 600 (AGGREFACT_FILE or HF_TOKEN_FILE); hard caps USD 40 + 20 (owner key)
	$(call require_gate_eval,eval-live)
	cd backend-python && $(POETRY) run python -m evals.datasets.ragtruth --subset qa --split test \
		--n $(EVAL_LIVE_RAGTRUTH_N) --seed $(BENCH_SEED) --revision $(RAGTRUTH_REVISION) \
		--out evals/datasets/ragtruth/eval-live.jsonl
	$(BENCH_RUN) --tier full --dataset evals/datasets/ragtruth/eval-live.jsonl \
		--max-cost-usd $(EVAL_LIVE_RAGTRUTH_MAX_USD) --full-audit-n 0
	cd backend-python && $(POETRY) run python -m evals.datasets.aggrefact --split test \
		--n $(EVAL_LIVE_AGGREFACT_N) --seed $(BENCH_SEED) --out evals/datasets/aggrefact/eval-live.jsonl \
		$(if $(AGGREFACT_FILE),--from-file "$(AGGREFACT_FILE)") $(if $(HF_TOKEN_FILE),--hf-token-file "$(HF_TOKEN_FILE)")
	$(BENCH_RUN) --tier full --dataset evals/datasets/aggrefact/eval-live.jsonl \
		--max-cost-usd $(EVAL_LIVE_AGGREFACT_MAX_USD) --full-audit-n 0

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
