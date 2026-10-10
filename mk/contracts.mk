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

.PHONY: contracts-install contracts-check contracts-test contracts-go forbidden-check phase-refs-check vectors install check

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

# SPEC Appendix A as data (contracts/superseded.v1.json); ci-contracts runs the same two steps.
forbidden-check: ## Fail on a superseded identifier (scripts/check-forbidden.mjs: its self-test, then the scan)
	$(NODE) "$(CONTRACTS_MK_DIR)../scripts/check-forbidden.mjs" --self-test
	$(NODE) "$(CONTRACTS_MK_DIR)../scripts/check-forbidden.mjs"

# The phase grep (P4-PLAN §17 I-3, P5-PLAN D134): no code or comment names the delivery phase it
# shipped in. The only exceptions are the fixed substrings in scripts/phase-refs.allow, matched
# against git grep's `path:line:text` output. `P4_` and `P4[A-Z][a-z]` (and the P5 forms) also
# catch names that run on into an identifier (P4_ERROR_CODES, isP4ErrorCode, TestP4Seams…), which
# `\bP4\b` misses. Lowercase scenario phase tags such as "p5" are data, not references. Raise
# PHASE_REFS_RE to the next phase when that phase's cleanup lands.
PHASE_REFS_RE ?= \bP[45]\b|"p4"|in P[45]|P[45]_|P[45][A-Z][a-z]
PHASE_REFS_PATHS ?= frontend-react/src backend-go backend-python/src scripts

phase-refs-check: ## Fail on a code or comment line that names a delivery phase (scripts/phase-refs.allow lists the exceptions)
	@cd "$(CONTRACTS_MK_DIR).." && hits="$$(git grep -nE '$(PHASE_REFS_RE)' -- $(PHASE_REFS_PATHS) | grep -v -F -f scripts/phase-refs.allow)"; \
	if [ -n "$$hits" ]; then printf '%s\n' "$$hits"; echo "phase-refs-check: the lines above name a delivery phase; reword them (or, for a deliberate exception, add the exact line to scripts/phase-refs.allow)"; exit 1; fi; \
	echo "✅ No code or comment names a delivery phase outside scripts/phase-refs.allow"

# Extra cmd/vectorgen flags, e.g. VECTORGEN_FLAGS=-check (verify only) or VECTORGEN_FLAGS="-kinds compile -freeze".
VECTORGEN_FLAGS ?=

vectors: ## Generate the expected values of the mechanical golden vectors with Go cmd/vectorgen (review the diff, then freeze)
	cd "$(CONTRACTS_MK_DIR)../backend-go" && $(GO) run ./cmd/vectorgen -contracts "$(CONTRACTS_DIR)" $(VECTORGEN_FLAGS)

# Hook into the aggregate targets. A prerequisite-only rule appends to a single-colon rule defined
# elsewhere (with or without a recipe) and also works when no other rule for the target exists.
# If the root Makefile ever declares them `::` (double-colon), these lines must become `::` too.
install: contracts-install
check: contracts-check contracts-test contracts-go forbidden-check phase-refs-check
