# mk/contracts.mk: targets for contracts/ (schemas, registries, vectors).
#
# Included by the root Makefile (include mk/*.mk); also usable on its own:
#   make -f mk/contracts.mk contracts-check
#
# Paths are resolved from this file, so the targets work from any working directory.
# The root Makefile should set .DEFAULT_GOAL (e.g. help) if it includes this file before its first target.

CONTRACTS_MK_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
CONTRACTS_DIR ?= $(patsubst %/,%,$(abspath $(CONTRACTS_MK_DIR)../contracts))
NPM ?= npm
NODE ?= node
GO ?= go

# Stamp npm writes on every install; it is older than package-lock.json when dependencies changed.
CONTRACTS_NODE_STAMP := $(CONTRACTS_DIR)/node_modules/.package-lock.json

.PHONY: contracts-install contracts-check contracts-test contracts-go vectors check

contracts-install: ## Install the contracts validator dependencies (npm ci)
	cd "$(CONTRACTS_DIR)" && $(NPM) ci --no-audit --no-fund

$(CONTRACTS_NODE_STAMP): $(CONTRACTS_DIR)/package-lock.json
	cd "$(CONTRACTS_DIR)" && $(NPM) ci --no-audit --no-fund

contracts-check: $(CONTRACTS_NODE_STAMP) ## Validate every contracts/ file against its JSON Schema, plus cross-file checks
	cd "$(CONTRACTS_DIR)" && $(NODE) validate.mjs

contracts-test: $(CONTRACTS_NODE_STAMP) ## Run the contracts validator's own tests
	cd "$(CONTRACTS_DIR)" && $(NPM) test --silent

contracts-go: ## Vet and test the embedded Go contracts module (github.com/truthtable/contracts)
	cd "$(CONTRACTS_DIR)" && $(GO) vet ./... && $(GO) test ./...

vectors: ## Generate golden vectors (placeholder: Go cmd/vectorgen lands in S0)
	@echo "vectors: nothing to generate yet. Go cmd/vectorgen lands in S0 and will write the mechanical"
	@echo "expected values under contracts/vectors/<kind>/ (status draft until reviewed, then frozen)."

# Hook into the aggregate target. A prerequisite-only rule appends to a single-colon `check:` rule
# defined elsewhere (with or without a recipe) and also works when no other `check` rule exists.
# If the root Makefile declares `check::` (double-colon), change this line to `check:: contracts-check`.
CHECK_TARGETS += contracts-check contracts-test contracts-go
check: contracts-check contracts-test contracts-go
