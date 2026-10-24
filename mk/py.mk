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

test-e2e: ## Run the engine e2e tests (proxy on :8080, engine gRPC on :50051: make up-infra, dev-python, dev-go; absent services skip)
	cd backend-python && $(POETRY) run pytest -m e2e -v

eval-golden: ## Deterministic golden-set evals, golden_v2 then golden v1 (the CI regression gates, no model needed)
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset golden_v2 --provider mock --check-baseline
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset golden --provider mock --check-baseline

eval-benchmark: ## HaluEval benchmark against local Ollama, EVAL_MODEL (requires make up + ollama-pull)
	cd backend-python && $(POETRY) run python -m evals.datasets.download halueval --samples 200
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --provider ollama --model $(EVAL_MODEL) --output eval-report.json

# eval-compare spends on two vendor keys, so it is a dry run unless CONFIRM=1 (D204, as for every
# paid target). run_eval.py keeps no cost ledger: the bound is the example count, EVAL_COMPARE_N.
EVAL_COMPARE_N ?= 100

eval-compare: ## Judge-model comparison on HaluEval, EVAL_COMPARE_N answers (needs OPENAI_API_KEY + ANTHROPIC_API_KEY; about USD 1-5 of usage; a dry run unless CONFIRM=1)
ifneq ($(CONFIRM),1)
	@echo "eval-compare: a dry run, nothing is spent (to run it: make eval-compare CONFIRM=1)"
	@echo "  plan: $(EVAL_COMPARE_N) HaluEval QA answers (half faithful, half hallucinated) through the full audit, one judge at a time:"
	@echo "    ollama $(EVAL_MODEL) (local, free), then openai gpt-4o-mini and anthropic claude-haiku-4-5-20251001 (paid)"
	@echo "  estimate: about USD 1-5 of usage in total [estimate]; no cost cap: each judge stops after $(EVAL_COMPARE_N) answers"
	@echo "  reports: backend-python/eval-{ollama,openai,anthropic}.json"
else
	cd backend-python && $(POETRY) run python -m evals.datasets.download halueval --samples $(EVAL_COMPARE_N)
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --limit $(EVAL_COMPARE_N) --provider ollama --model $(EVAL_MODEL) --output eval-ollama.json
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --limit $(EVAL_COMPARE_N) --provider openai --model gpt-4o-mini --output eval-openai.json
	cd backend-python && $(POETRY) run python -m evals.run_eval --dataset halueval --limit $(EVAL_COMPARE_N) --provider anthropic --model claude-haiku-4-5-20251001 --output eval-anthropic.json
	@echo "Reports written: backend-python/eval-{ollama,openai,anthropic}.json"
endif

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
# RAGTruth is read from GitHub raw at this commit (pinned at P6 Step 0, 2026-10-10; MIT licence).
RAGTRUTH_REVISION ?= c103204b9ce28d6bbad859304bf30de72b8ed8fe
GATE_EVAL_BIN ?= $(CURDIR)/backend-go/bin/gate-eval
EVAL_LIVE_RAGTRUTH_N ?= 400
EVAL_LIVE_AGGREFACT_N ?= 600
EVAL_LIVE_RAGTRUTH_MAX_USD ?= 40
EVAL_LIVE_AGGREFACT_MAX_USD ?= 20
AGGREFACT_FILE ?=
HF_TOKEN_FILE ?=
RUN ?=
# ---------- P6: dry runs, pinned inputs, the key's limit (P6 plan section 3, D201-D207) ----------
# D204: every paid target prints its plan (n, systems, the price-table estimate, the effective
# cap, the resume key and its ledger, the hosts, the snapshot) and exits 0 unless CONFIRM=1.
# BENCH_ARGS passes extra flags to `evals.bench run` (appended last, e.g. --retry-failed after a
# stop); BENCH_RESUME_KEY names the cache and ledger a run resumes (fixed, never a date, so a run
# continued on another day keeps one cap). D201: each paid step lowers its cap to the key's
# remaining limit minus $0.10 and refuses below its minimum. D206: CALIB_N was fixed at Step 0
# from the free RAGTruth download: the QA train split has 4,892 answers (1,546 hallucinated, 342
# with a conflict span, no source shared with the test split), so a natural sample of 800
# expects about 56 conflicts and 253 hallucinated answers. Every paid step ends with one
# TRUSTAGENT_P6_RESULT line, also appended to P6_JOURNAL (gitignored), which p6-ledger reads.
CONFIRM ?=
BENCH_ARGS ?=
BENCH_RESUME_KEY ?= lite-p6
BENCH_MIN_COST_USD ?= 2
SMOKE_N ?= 20
SMOKE_FULL_AUDIT_N ?= 3
SMOKE_MAX_COST_USD ?= 0.30
SMOKE_MIN_COST_USD ?= 0.05
SMOKE_RESUME_KEY ?= smoke-p6
CALIB_N ?= 800
CALIB_SEED ?= 13
CALIB_MAX_COST_USD ?= 0.30
CALIB_MIN_COST_USD ?= 0.06
CALIBRATE_BIN ?=
HOLDOUT ?=
CHECK_ONLY ?=
PUBLISH_ARGS ?=
P6_JOURNAL ?= $(CURDIR)/tmp/p6/results.log

# The recipes run in backend-python/: paths given relative to the repository
# root (GATE_EVAL_BIN, RUN, HOLDOUT, AGGREFACT_FILE, HF_TOKEN_FILE) are made absolute.
GATE_EVAL_PATH = $(abspath $(GATE_EVAL_BIN))
BENCH_LITE_DATASET = evals/datasets/ragtruth/bench-lite.jsonl
BENCH_SMOKE_DATASET = evals/datasets/ragtruth/bench-smoke.jsonl

BENCH_RUN = cd backend-python && $(POETRY) run python -m evals.bench run \
	--seed $(BENCH_SEED) --gate-eval-bin "$(GATE_EVAL_PATH)" --judge-provider $(BENCH_JUDGE_PROVIDER) \
	--concurrency $(BENCH_CONCURRENCY) --region $(BENCH_REGION) --journal "$(P6_JOURNAL)"
RAGTRUTH_PREPARE = cd backend-python && $(POETRY) run python -m evals.datasets.ragtruth --subset qa \
	--split test --seed $(BENCH_SEED) --revision $(RAGTRUTH_REVISION)
CALIBSET = cd backend-python && $(POETRY) run python -m evals.calibset

define require_gate_eval
	@test -x "$(GATE_EVAL_PATH)" || { \
		echo "$(1) needs the Go gate-eval binary at $(GATE_EVAL_PATH):"; \
		echo "  make gate-eval   (or pass GATE_EVAL_BIN=...)"; \
		exit 2; }
endef

# $(call bench_lite_args,<extra>): the bench-lite run, its plan with --dry-run.
bench_lite_args = --tier lite --step bench-lite --dataset $(BENCH_LITE_DATASET) --plan-n $(BENCH_N) \
	--max-cost-usd $(BENCH_MAX_COST_USD) --min-cost-usd $(BENCH_MIN_COST_USD) \
	--full-audit-n $(BENCH_FULL_AUDIT_N) --resume-key $(BENCH_RESUME_KEY) $(1) $(BENCH_ARGS)
bench_smoke_args = --tier lite --step bench-smoke --dataset $(BENCH_SMOKE_DATASET) --plan-n $(SMOKE_N) \
	--max-cost-usd $(SMOKE_MAX_COST_USD) --min-cost-usd $(SMOKE_MIN_COST_USD) \
	--full-audit-n $(SMOKE_FULL_AUDIT_N) --resume-key $(SMOKE_RESUME_KEY) \
	--out-dir evals/results/smoke $(1) $(BENCH_ARGS)
calib_args = --n $(CALIB_N) --seed $(CALIB_SEED) --revision $(RAGTRUTH_REVISION) \
	--exclude-sources $(BENCH_LITE_DATASET) --bench-n $(BENCH_N) --bench-seed $(BENCH_SEED) \
	--gate-eval-bin "$(GATE_EVAL_PATH)" --max-cost-usd $(CALIB_MAX_COST_USD) \
	--min-cost-usd $(CALIB_MIN_COST_USD) --journal "$(P6_JOURNAL)" $(1)

bench-lite: ## RAGTruth-QA bench-lite: gate (alone, warm) + LLM same checks (200), full audit + cascade (50); cap USD 5 over every session (a dry run unless CONFIRM=1; BENCH_ARGS appended)
ifneq ($(CONFIRM),1)
	@echo "bench-lite: a dry run, nothing is spent (to run it: make bench-lite CONFIRM=1)"
	$(BENCH_RUN) $(call bench_lite_args,--dry-run)
else
	$(call require_gate_eval,bench-lite)
	$(RAGTRUTH_PREPARE) --n $(BENCH_N) --out $(BENCH_LITE_DATASET)
	$(BENCH_RUN) $(call bench_lite_args,)
endif

bench-smoke: ## Smoke bench: 20 RAGTruth-QA answers, full audit 3, cap USD 0.30, resume key smoke-p6, into evals/results/smoke (a dry run unless CONFIRM=1)
ifneq ($(CONFIRM),1)
	@echo "bench-smoke: a dry run, nothing is spent (to run it: make bench-smoke CONFIRM=1)"
	$(BENCH_RUN) $(call bench_smoke_args,--dry-run)
else
	$(call require_gate_eval,bench-smoke)
	$(RAGTRUTH_PREPARE) --n $(SMOKE_N) --out $(BENCH_SMOKE_DATASET)
	$(BENCH_RUN) $(call bench_smoke_args,)
endif

calib-pass: ## Calibration pass: a natural RAGTruth-QA train sample of CALIB_N through one warm gate-eval, cap USD 0.30 (a dry run unless CONFIRM=1)
ifneq ($(CONFIRM),1)
	@echo "calib-pass: a dry run, nothing is spent (to run it: make calib-pass CONFIRM=1)"
	$(CALIBSET) run $(call calib_args,--dry-run)
else
	$(call require_gate_eval,calib-pass)
	$(CALIBSET) run $(call calib_args,)
endif

calib-holdout: ## Held-out ECE and Brier, before and after calibration, of a bench run: RUN=<abs run.json> [CALIBRATE_BIN=...] (free)
ifeq ($(RUN),)
	@echo "usage: make calib-holdout RUN=<abs path to backend-python/evals/results/<run_id>/run.json> [CALIBRATE_BIN=<built cmd/calibrate>]"; exit 2
else
	$(CALIBSET) holdout --run "$(abspath $(RUN))" $(if $(CALIBRATE_BIN),--calibrate-bin "$(abspath $(CALIBRATE_BIN))")
endif

p6-ledger: ## Read-only sum of the P6 spend (bench ledgers, calib-pass, record ledger, result lines) against the key usage and the D201 caps
	cd backend-python && $(POETRY) run python -m evals.p6_ledger --journal "$(P6_JOURNAL)" \
		--record-ledger "$(CURDIR)/tmp/record-demo/ledger.jsonl"

bench-publish: ## Publish one measured bench run: RUN=<abs run.json> [HOLDOUT=<holdout-groundedness.json>] [CHECK_ONLY=1 into tmp/ only]; refuses mock output and every D204 rule
ifeq ($(RUN),)
	@echo "usage: make bench-publish RUN=<abs path to backend-python/evals/results/<run_id>/run.json> [HOLDOUT=<that run's holdout-groundedness.json>] [CHECK_ONLY=1]"; \
		echo "  (RUN= is required: never the latest by time, D204)"; exit 2
else
	cd backend-python && $(POETRY) run python -m evals.bench publish --run "$(abspath $(RUN))" \
		$(if $(HOLDOUT),--calibration-holdout "$(abspath $(HOLDOUT))") $(if $(filter 1,$(CHECK_ONLY)),--check-only) $(PUBLISH_ARGS)
endif

# The two eval-live datasets share the file stem eval-live: each gets its own resume key, so each
# cap has its own ledger.
eval-live: ## Full eval: RAGTruth-QA 400 + LLM-AggreFact 600 (AGGREFACT_FILE or HF_TOKEN_FILE); hard caps USD 40 + 20 (a dry run unless CONFIRM=1; owner key)
ifneq ($(CONFIRM),1)
	@echo "eval-live: a dry run, nothing is spent (to run it: make eval-live CONFIRM=1)"
	$(BENCH_RUN) --tier full --step eval-live --dataset evals/datasets/ragtruth/eval-live.jsonl \
		--resume-key eval-live-ragtruth --plan-n $(EVAL_LIVE_RAGTRUTH_N) --max-cost-usd $(EVAL_LIVE_RAGTRUTH_MAX_USD) --full-audit-n 0 \
		--dry-run $(BENCH_ARGS)
	$(BENCH_RUN) --tier full --step eval-live --dataset evals/datasets/aggrefact/eval-live.jsonl \
		--resume-key eval-live-aggrefact --plan-n $(EVAL_LIVE_AGGREFACT_N) --max-cost-usd $(EVAL_LIVE_AGGREFACT_MAX_USD) --full-audit-n 0 \
		--dry-run $(BENCH_ARGS)
else
	$(call require_gate_eval,eval-live)
	$(RAGTRUTH_PREPARE) --n $(EVAL_LIVE_RAGTRUTH_N) --out evals/datasets/ragtruth/eval-live.jsonl
	$(BENCH_RUN) --tier full --step eval-live --dataset evals/datasets/ragtruth/eval-live.jsonl \
		--resume-key eval-live-ragtruth --max-cost-usd $(EVAL_LIVE_RAGTRUTH_MAX_USD) --full-audit-n 0 $(BENCH_ARGS)
	cd backend-python && $(POETRY) run python -m evals.datasets.aggrefact --split test \
		--n $(EVAL_LIVE_AGGREFACT_N) --seed $(BENCH_SEED) --out evals/datasets/aggrefact/eval-live.jsonl \
		$(if $(AGGREFACT_FILE),--from-file "$(abspath $(AGGREFACT_FILE))") $(if $(HF_TOKEN_FILE),--hf-token-file "$(abspath $(HF_TOKEN_FILE))")
	$(BENCH_RUN) --tier full --step eval-live --dataset evals/datasets/aggrefact/eval-live.jsonl \
		--resume-key eval-live-aggrefact --max-cost-usd $(EVAL_LIVE_AGGREFACT_MAX_USD) --full-audit-n 0 $(BENCH_ARGS)
endif

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
