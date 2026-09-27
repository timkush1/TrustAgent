# ============================================================================
# TrustAgent - root Makefile
# ============================================================================
# Thin root: shared settings, `help`, and the cross-stream aggregate targets.
# Everything else lives in mk/*.mk, one fragment per stream (go, py, web, dx,
# contracts); a new mk/*.mk is picked up automatically by the include below.
#
# Aggregates (install, proto, test, lint, fmt, clean, check) are declared here
# with only a closing message as their recipe. Each fragment attaches its own
# per-stream targets as prerequisites, without a recipe and without a `##`
# help comment, e.g.
#
#     test: test-go
#
# so `make test` runs every stream without this file having to know about it.
# Every target is declared .PHONY automatically (see the end of this file).
# ============================================================================

.DEFAULT_GOAL := help

# ---------- Shared settings (override per call, e.g. `make up COMPOSE=docker-compose`) ----------

# Compose v2 plugin when available, else the standalone binary. Recursively
# expanded, so docker is only probed by targets that actually use it.
COMPOSE ?= $(shell docker compose version >/dev/null 2>&1 && echo "docker compose" || echo "docker-compose")

ifeq ($(OS),Windows_NT)
PYTHON ?= py -3
else
PYTHON ?= python3
endif

# One model name for the local Ollama judge: pulled by `ollama-pull`, used by the evals.
OLLAMA_MODEL ?= llama3.2:1b
EVAL_MODEL   ?= $(OLLAMA_MODEL)

##@ General

help: ## Show this help (targets from Makefile and mk/*.mk)
	@echo "TrustAgent - Available Commands:"
	@awk 'BEGIN {FS = ":.*?## "} \
		/^##@ / { printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next } \
		/^[a-zA-Z0-9_.-]+:.*?## / { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

##@ Aggregates (each mk/*.mk attaches its per-stream targets)

install: ## Install all dependencies (incl. protoc Go plugins)
	@echo "✅ All dependencies installed"

proto: ## Generate Go + Python gRPC stubs (mirrors CI; run `make proto-tools` first on a fresh clone)
	@echo "✅ Protobuf code generated"

test: ## Run all unit tests (excludes opt-in e2e/integration; safe on a bare box)
	@echo "✅ All tests passed"

lint: ## Run linters for all projects
	@echo "✅ All linting passed"

fmt: ## Format all code
	@echo "✅ All code formatted"

clean: ## Clean all build artifacts and caches
	@echo "✅ All caches cleaned"

# Later phases extend the gate by attaching prerequisites (`check: contracts-check`).
check: lint test ## Run every local quality gate (lint + unit tests)
	@echo "✅ make check passed"

include $(sort $(wildcard mk/*.mk))

# Every target defined above or in mk/*.mk is phony (none of them produce a
# file of the same name), so declare them all instead of hand-kept lists.
.PHONY: $(shell sed -nE 's/^([A-Za-z0-9_][A-Za-z0-9_.-]*)[[:space:]]*:([^=]|$$).*/\1/p' $(MAKEFILE_LIST) | sort -u)
