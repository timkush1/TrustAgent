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

# ---------- End-to-end (Playwright) ----------
# The `stack` project runs against a running stack's dashboard: start one with `make demo`.
# PLAYWRIGHT_BASE_URL overrides the address (default http://localhost:${DASHBOARD_HOST_PORT:-3000}).
# PLAYWRIGHT_ARGS narrows the run, e.g. `make test-e2e-web PLAYWRIGHT_ARGS="-g skeleton"` (the S0 gate).
PLAYWRIGHT_ARGS ?=

playwright-install: ## Install the Chromium build the Playwright specs use (once per machine; add --with-deps on a bare Linux box)
	cd frontend-react && npx playwright install chromium

test-e2e-web: ## Run the dashboard e2e specs against a running stack (make demo first; PLAYWRIGHT_ARGS="-g skeleton")
	cd frontend-react && npx playwright test --project=stack $(PLAYWRIGHT_ARGS)

# ---------- Aggregate hooks (see the root Makefile) ----------
install: install-react
test: test-react
lint: lint-react
fmt: fmt-react
clean: clean-react
