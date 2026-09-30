# ============================================================================
# mk/dx.mk - Compose stacks (default, demo and e2e), setup tools, local models,
# load test, monitoring shortcuts. Owned by the W-DX stream. Included by the
# root Makefile; targets are declared .PHONY there.
#
# Services are addressed by compose service name (`$(COMPOSE) exec ollama ...`);
# there are no fixed container names. The default stack's project name comes
# from COMPOSE_PROJECT_NAME (.env) or `name: truthtable` in docker-compose.yml;
# the demo stack always runs as $(DEMO_PROJECT), the e2e stack as $(E2E_PROJECT).
# ============================================================================

# Image tag of the app services (docker-compose.yml reads TRUSTAGENT_VERSION).
TRUSTAGENT_VERSION ?= $(shell cat VERSION 2>/dev/null)
export TRUSTAGENT_VERSION

DEMO_PROJECT ?= truthtable-demo
DEMO_COMPOSE  = $(COMPOSE) -p $(DEMO_PROJECT) -f docker-compose.yml -f docker-compose.demo.yml --profile demo
# `make demo` builds the images from this checkout; `make demo DEMO_BUILD=` uses
# local or published images instead (CI builds them first, with a layer cache).
DEMO_BUILD   ?= --build
# Scenario checks (trafficgen --expect inside the proxy container): which
# scenarios (--scenarios, --exclude) and the gateway phase they may need
# (--phase; empty = trafficgen's default, the phase this build implements).
DEMO_SCENARIOS ?= demo
DEMO_PHASE     ?=

E2E_PROJECT ?= truthtable-e2e
E2E_COMPOSE  = $(COMPOSE) -p $(E2E_PROJECT) -f docker-compose.yml -f docker-compose.e2e.yml --profile e2e
E2E_BUILD   ?= --build
E2E_SCENARIOS ?= e2e
E2E_EXCLUDE   ?=
E2E_PHASE     ?=
# all: every answer comes from mock-upstream through the upstream path (no
# test_response); none: scripted answers, as in the demo.
E2E_UPSTREAM  ?= all
# trafficgen flags shared by the scenario targets.
TRAFFICGEN_CHECK = /trafficgen --once --expect --base-url http://127.0.0.1:8080
# Extra arguments for `make up`, e.g. UP_ARGS=--build after changing code.
UP_ARGS      ?=
# Seconds `up --wait` gives every service to become healthy.
WAIT_TIMEOUT ?= 600
# OPEN=0 prints the dashboard URL without opening a browser (CI never opens one).
OPEN         ?= 1
# `make logs SERVICE=proxy` / `make demo-logs SERVICE=engine` follow one service.
SERVICE      ?=

# Read with the same precedence as compose: process env, then .env, then default.
DASHBOARD_URL = http://localhost:$(shell $(PYTHON) scripts/envfile.py get DASHBOARD_HOST_PORT 3000)
OPEN_URL_ARGS = $(if $(filter 0,$(OPEN)),--print-only)

##@ Stack (docker compose)

up: ## Start proxy, engine, postgres, dashboard (+ COMPOSE_PROFILES add-ons), wait until healthy, open the dashboard
	$(PYTHON) scripts/configure.py --ensure
	$(COMPOSE) up -d --wait --wait-timeout $(WAIT_TIMEOUT) $(UP_ARGS)
	@echo "✅ TrustAgent is running (proxy: http://localhost:$$($(PYTHON) scripts/envfile.py get PROXY_HOST_PORT 8080)/v1)"
	@$(PYTHON) scripts/open_url.py $(OPEN_URL_ARGS) --label "Dashboard" "$(DASHBOARD_URL)"

down: ## Stop the stack (keeps its volumes)
	$(COMPOSE) down
	@echo "✅ Stack stopped"

up-infra: ## Start only the backing services (postgres + COMPOSE_PROFILES add-ons) for make dev-go / dev-python
	$(PYTHON) scripts/configure.py --ensure
	$(COMPOSE) up -d --wait --wait-timeout $(WAIT_TIMEOUT) \
		$$($(COMPOSE) config --services | grep -vxE 'proxy|engine|dashboard|trafficgen')
	@echo "✅ Backing services running (postgres on localhost:$$($(PYTHON) scripts/envfile.py get POSTGRES_HOST_PORT 5432), user truthtable, POSTGRES_PASSWORD in .env)"

demo: ## Zero-key demo (mock fast check, mock judge, scripted traffic, no cost): build, wait until healthy, open the dashboard
	$(PYTHON) scripts/configure.py --ensure
	$(DEMO_COMPOSE) up -d $(DEMO_BUILD) --wait --wait-timeout $(WAIT_TIMEOUT)
	@echo "✅ Demo stack running (project $(DEMO_PROJECT), DEMO MODE: simulated checks, no keys, \$$0)"
	@echo "   Diagnose: python3 scripts/doctor.py --expect demo   Stop: make demo-down"
	@$(PYTHON) scripts/open_url.py $(OPEN_URL_ARGS) --label "Dashboard" "$(DASHBOARD_URL)"

demo-down: ## Stop the demo stack (keeps its volumes; make demo-clean deletes them)
	$(DEMO_COMPOSE) down
	@echo "✅ Demo stack stopped"

demo-clean: ## Stop the demo stack and delete its volumes (demo database and secret key)
	$(DEMO_COMPOSE) down --volumes --remove-orphans
	@echo "✅ Demo stack and its volumes removed"

demo-scenarios: ## Check the demo scenarios against the running demo stack (trafficgen --expect; DEMO_SCENARIOS=s0, DEMO_PHASE=p3)
	$(DEMO_COMPOSE) exec -T proxy $(TRAFFICGEN_CHECK) --scenarios $(DEMO_SCENARIOS) $(if $(DEMO_PHASE),--phase $(DEMO_PHASE))

##@ End-to-end stack (real HTTP paths against mock-upstream)

e2e-up: ## Start the e2e stack (compatible fast check and LLM upstreams on mock-upstream, mock judge, sync traces), wait until healthy
	$(PYTHON) scripts/configure.py --ensure
	$(E2E_COMPOSE) up -d $(E2E_BUILD) --wait --wait-timeout $(WAIT_TIMEOUT)
	@echo "✅ E2E stack running (project $(E2E_PROJECT): every vendor is mock-upstream, \$$0)"
	@echo "   Check: make e2e-scenarios   Stop: make e2e-down"

e2e-scenarios: ## Check the e2e scenarios against the running e2e stack (E2E_SCENARIOS=e2e E2E_EXCLUDE= E2E_PHASE=p3 E2E_UPSTREAM=all)
	$(E2E_COMPOSE) exec -T proxy $(TRAFFICGEN_CHECK) --scenarios $(E2E_SCENARIOS) --upstream $(E2E_UPSTREAM) \
		$(if $(E2E_EXCLUDE),--exclude $(E2E_EXCLUDE)) $(if $(E2E_PHASE),--phase $(E2E_PHASE))

e2e-logs: ## Follow the e2e stack's logs (SERVICE=mock-upstream for one service)
	$(E2E_COMPOSE) logs -f $(SERVICE)

e2e-down: ## Stop the e2e stack and delete its volumes (it holds test data only)
	$(E2E_COMPOSE) down --volumes --remove-orphans
	@echo "✅ E2E stack and its volumes removed"

logs: ## Follow the stack's logs (SERVICE=proxy for one service)
	$(COMPOSE) logs -f $(SERVICE)

demo-logs: ## Follow the demo stack's logs (SERVICE=engine for one service)
	$(DEMO_COMPOSE) logs -f $(SERVICE)

setup-url: ## Print a new one-time setup URL (SETUP: open …#token=…) for the running stack; claim it to create the admin key
	$(COMPOSE) exec -T proxy /proxy -new-setup-token

status: ## Show the stack's containers and their health
	@$(COMPOSE) ps --all

clean-data: ## Stop the stack and delete its volumes (WARNING: deletes all data)
	$(COMPOSE) down --volumes
	@echo "✅ All data volumes removed"

##@ Setup tools

configure: ## Create or complete .env: generated secrets only, never overwrites (interactive setup comes later)
	$(PYTHON) scripts/configure.py --ensure

doctor: ## Diagnose the running stack (make doctor ARGS="--expect demo --json")
	$(PYTHON) scripts/doctor.py $(ARGS)

# Both files are generated from contracts/config/keys.v1.json (SPEC §2.7); the
# scripts unit tests (CI job "DX scripts") also fail when they are stale.
config-docs: ## Regenerate docs/CONFIGURATION.md and .env.example from the key registry
	$(PYTHON) scripts/gen_config_docs.py

config-docs-check: ## Fail with a diff when docs/CONFIGURATION.md or .env.example differ from the key registry
	$(PYTHON) scripts/gen_config_docs.py --check

# Every profile on its own, all of them together, and the demo override.
COMPOSE_CHECK_PROFILES = "" knowledge local-llm observability redis demo load "*"

compose-check: ## Validate the compose files: every profile and the demo and e2e overrides (docker compose config -q)
	@if ! docker compose version >/dev/null 2>&1; then echo "compose-check: skipped (docker compose not found)"; exit 0; fi; \
	set -e; for p in $(COMPOSE_CHECK_PROFILES); do \
		$(COMPOSE) $${p:+--profile "$$p"} config -q; echo "compose config ok: profile $${p:-default}"; \
	done; \
	$(DEMO_COMPOSE) config -q; echo "compose config ok: demo override"; \
	$(E2E_COMPOSE) config -q; echo "compose config ok: e2e override"

test-scripts: ## Unit tests of the setup scripts (scripts/tests, standard library only)
	$(PYTHON) -m unittest discover -s scripts/tests -v

lint-scripts: ## ruff + black --check on scripts/ (the engine's settings, via its Poetry env)
	cd backend-python && $(POETRY) run ruff check --config pyproject.toml ../scripts
	cd backend-python && $(POETRY) run black --check --config pyproject.toml ../scripts

##@ Ollama (profile local-llm: local judge)

ollama-pull: ## Pull OLLAMA_MODEL into the running ollama service (ollama-init pulls OLLAMA_FALLBACK_MODEL by itself)
	$(COMPOSE) --profile local-llm exec -T ollama ollama pull $(OLLAMA_MODEL)
	@echo "✅ $(OLLAMA_MODEL) model downloaded"

ollama-list: ## List the models in the ollama service
	$(COMPOSE) --profile local-llm exec -T ollama ollama list

##@ Load testing and monitoring

load-test: ## Measure proxy overhead under load (requires the demo stack: make demo; uses dockerized k6)
	docker run --rm -i --network host \
		-e BASE_URL="http://localhost:$$($(PYTHON) scripts/envfile.py get PROXY_HOST_PORT 8080)" \
		-e API_KEY="$$($(PYTHON) scripts/envfile.py get TRAFFICGEN_API_KEY)" \
		grafana/k6 run - < scripts/load/proxy-overhead.js

metrics: ## Open the Prometheus UI (profile observability; WSL/macOS/Linux aware)
	@$(PYTHON) scripts/open_url.py --label "Prometheus" http://localhost:9090

dashboards: ## Open the Grafana dashboards (profile observability; WSL/macOS/Linux aware)
	@$(PYTHON) scripts/open_url.py --label "Grafana (admin / GRAFANA_ADMIN_PASSWORD in .env)" http://localhost:3001

# ---------- Aggregate hooks (see the root Makefile) ----------
test: test-scripts
lint: lint-scripts
check: compose-check
check: config-docs-check
