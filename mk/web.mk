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

clean-react: ## Remove node_modules and build output
	@echo "Cleaning Node cache..."
	cd frontend-react && rm -rf node_modules dist .vite

# ---------- Aggregate hooks (see the root Makefile) ----------
install: install-react
test: test-react
lint: lint-react
fmt: fmt-react
clean: clean-react
