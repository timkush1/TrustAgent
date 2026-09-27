# ============================================================================
# mk/dx.mk - Compose stack, local models, load test, monitoring shortcuts.
# Owned by the W-DX stream. Included by the root Makefile; targets are
# declared .PHONY there. Services are addressed by compose service name
# (`$(COMPOSE) exec ollama ...`); there are no fixed container names.
# ============================================================================

# open_url: open $(1) in the host browser and always print it, so a headless
# shell still gets something to click. Tries, in order: wslview (WSL), the
# Windows shell via explorer.exe (WSL without wslu), open (macOS), xdg-open
# (Linux desktop), then Python's webbrowser module.
define open_url
@url='$(1)'; echo "Opening $$url"; \
if command -v wslview >/dev/null 2>&1 && wslview "$$url" >/dev/null 2>&1; then :; \
elif grep -qi microsoft /proc/version 2>/dev/null && command -v explorer.exe >/dev/null 2>&1; then explorer.exe "$$url" || true; \
elif [ "$$(uname -s)" = Darwin ] && open "$$url"; then :; \
elif command -v xdg-open >/dev/null 2>&1 && xdg-open "$$url" >/dev/null 2>&1; then :; \
elif $(PYTHON) -c 'import sys, webbrowser; sys.exit(0 if webbrowser.open(sys.argv[1]) else 1)' "$$url" >/dev/null 2>&1; then :; \
else echo "No browser launcher found; open the URL above manually."; fi
endef

##@ Stack (docker compose)

up: ## Start infrastructure services (redis, postgres, qdrant, ollama, prometheus, grafana) and wait until healthy
	$(COMPOSE) up -d --wait redis postgres qdrant ollama prometheus grafana
	@echo "✅ Infrastructure services running"
	@echo "Redis:      redis://localhost:6379"
	@echo "Postgres:   postgres://truthtable@localhost:5432/truthtable (password: POSTGRES_PASSWORD in .env)"
	@echo "Qdrant:     http://localhost:6333"
	@echo "Ollama:     http://localhost:11434"
	@echo "Prometheus: http://localhost:9090"
	@echo "Grafana:    http://localhost:3001 (user admin, password: GRAFANA_ADMIN_PASSWORD in .env)"

up-all: ## Start all services (infrastructure + application)
	$(COMPOSE) up -d
	@echo "✅ All services running"

down: ## Stop all services
	$(COMPOSE) down
	@echo "✅ All services stopped"

logs: ## View logs from all services
	$(COMPOSE) logs -f

status: ## Show status of all services
	@echo "Service Status:"
	@$(COMPOSE) ps

clean-data: ## Remove all Docker volumes (WARNING: deletes all data)
	$(COMPOSE) down -v
	@echo "✅ All data volumes removed"

##@ Ollama (local judge fallback)

ollama-pull: ## Pull the default Ollama fallback model, OLLAMA_MODEL (1B; fits ~2GB RAM)
	$(COMPOSE) exec ollama ollama pull $(OLLAMA_MODEL)
	@echo "✅ $(OLLAMA_MODEL) model downloaded"

ollama-list: ## List available Ollama models
	$(COMPOSE) exec ollama ollama list

##@ Load testing and monitoring

load-test: ## Measure proxy overhead under load (requires stack: make up-all; uses dockerized k6)
	docker run --rm -i --network host grafana/k6 run - < scripts/load/proxy-overhead.js

metrics: ## Open the Prometheus UI in a browser (WSL/macOS/Linux aware)
	$(call open_url,http://localhost:9090)

dashboards: ## Open the Grafana dashboards in a browser (WSL/macOS/Linux aware)
	$(call open_url,http://localhost:3001)
