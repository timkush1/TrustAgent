# ============================================================================
# mk/web.mk - React dashboard (frontend-react/). Owned by the W-WEB stream.
# Included by the root Makefile; targets are declared .PHONY there.
# ============================================================================

##@ Web (frontend-react: dashboard)

install-react: ## Install Node dependencies (npm install)
	@echo "Installing Node dependencies..."
	cd frontend-react && npm install

dev-react: ## Run React dashboard in development mode
	cd frontend-react && npm run dev

test-react: ## Run React tests only (vitest)
	@echo "Running React tests..."
	cd frontend-react && npm test

lint-react: ## Run eslint
	@echo "Linting React..."
	cd frontend-react && npm run lint

fmt-react: ## Auto-fix lint issues (eslint --fix)
	cd frontend-react && npm run lint -- --fix

clean-react: ## Remove node_modules, build output and Playwright results
	@echo "Cleaning Node cache..."
	cd frontend-react && rm -rf node_modules dist .vite playwright-report test-results

# ---------- Contract types and golden vectors (SPEC §2.6, §7.6) ----------
# types.gen.ts is generated from contracts/schema and committed; the vitest drift test fails when it
# is stale. api.v1.schema.json is generated the other way round, from src/types/api.ts, by the tool in
# contracts/tools/api-schema (its own package, installed on first use).

gen-types: ## Regenerate src/lib/contracts/types.gen.ts from contracts/schema (commit the result)
	cd frontend-react && node scripts/gen-types.ts

gen-types-check: ## Fail when src/lib/contracts/types.gen.ts is stale
	cd frontend-react && node scripts/gen-types.ts --check

gen-policy-validator: ## Regenerate src/lib/policy/policyValidator.gen.ts (policy.v1 compiled by ajv, CSP-safe; commit the result)
	cd frontend-react && node scripts/gen-policy-validator.ts

gen-policy-validator-check: ## Fail when src/lib/policy/policyValidator.gen.ts is stale
	cd frontend-react && node scripts/gen-policy-validator.ts --check

gen-api-schema: ## Regenerate contracts/schema/api.v1.schema.json from src/types/api.ts (commit the result)
	cd contracts/tools/api-schema && (test -d node_modules || npm ci --no-audit --no-fund) && node gen-api-schema.mjs

gen-api-schema-check: ## Fail when contracts/schema/api.v1.schema.json is stale
	cd contracts/tools/api-schema && (test -d node_modules || npm ci --no-audit --no-fund) && node gen-api-schema.mjs --check

# --reporter=default keeps the per-kind conformance report visible: Vitest otherwise picks its
# minimal reporter in some environments, which hides the console output of passing tests.
vectors-ts: ## Run the golden vectors against the TypeScript decision core (src/lib/decision)
	cd frontend-react && npx vitest run src/lib/decision/vectors --maxWorkers=2 --reporter=default

# ---------- End-to-end (Playwright) ----------
# The `stack` project runs against a running stack's dashboard: start one with `make demo`.
# PLAYWRIGHT_BASE_URL overrides the address (default http://localhost:${DASHBOARD_HOST_PORT:-3000}).
# PLAYWRIGHT_ARGS narrows the run, e.g. `make test-e2e-web PLAYWRIGHT_ARGS="-g skeleton"` (the S0 gate).
# The policy-edit spec signs in as admin: set TRUSTAGENT_SETUP_URL (the demo's one-time setup link:
# export TRUSTAGENT_SETUP_URL="$(make -s demo-setup-url)") or TRUSTAGENT_ADMIN_KEY (a key from
# TRUTHTABLE_ADMIN_KEYS).
PLAYWRIGHT_ARGS ?=

playwright-install: ## Install the Chromium build the Playwright specs use (once per machine; add --with-deps on a bare Linux box)
	cd frontend-react && npx playwright install chromium

test-e2e-web: ## Run the dashboard e2e specs against a running stack (make demo first; PLAYWRIGHT_ARGS="-g skeleton")
	cd frontend-react && npx playwright test --project=stack $(PLAYWRIGHT_ARGS)

# ---------- Static site (GitHub Pages) and its demo recording (SPEC §3.3, §2.13, §7.5) ----------
# The static build is the landing page at the site root plus the dashboard replaying
# frontend-react/public/demo/recorded (no backend). VITE_BASE_PATH defaults to /TrustAgent/.

build-static: ## Build the static site (landing page + replay) into frontend-react/dist
	cd frontend-react && npm run build:static

size-landing: ## Fail when the landing page's first load is over 110 KB gzip (run after build-static)
	cd frontend-react && npm run size

recording: ## Rebuild the simulated demo recording from the s0 scenarios (commit the result)
	cd frontend-react && npm run recording

recording-check: ## Fail when the demo recording is stale or check_recording.py refuses it
	cd frontend-react && node scripts/build-demo-recording.ts --check
	$(PYTHON) scripts/check_recording.py frontend-react/public/demo/recorded

test-e2e-static: ## Build the static site and run the Playwright static specs against vite preview
	cd frontend-react && npm run build:static && PLAYWRIGHT_STATIC_PREVIEW=1 npx playwright test --project=static $(PLAYWRIGHT_ARGS)

# ---------- Aggregate hooks (see the root Makefile) ----------
install: install-react
test: test-react
lint: lint-react
fmt: fmt-react
clean: clean-react
