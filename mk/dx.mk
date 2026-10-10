# ============================================================================
# mk/dx.mk - Compose stacks (default, demo and e2e), setup tools, local models,
# monitoring shortcuts. Owned by the DX-RELEASE stream. Included by the root
# Makefile; targets are declared .PHONY there. The load test lives in
# mk/load.mk (GO), release targets in mk/release.mk, launch in mk/launch.mk.
#
# Services are addressed by compose service name (`$(COMPOSE) exec ollama ...`);
# there are no fixed container names. The default stack's project name comes
# from COMPOSE_PROJECT_NAME (.env) or `name: truthtable` in docker-compose.yml;
# the demo stack always runs as $(DEMO_PROJECT), the e2e stack as $(E2E_PROJECT).
# ============================================================================

# Image tag of the app services (docker-compose.yml reads TRUSTAGENT_VERSION).
TRUSTAGENT_VERSION ?= $(shell cat VERSION 2>/dev/null)
export TRUSTAGENT_VERSION
# The git revision the app images are built from (docker-compose.yml passes GIT_SHA to the
# Dockerfiles, which label each image with it). Read once; empty outside a git checkout, and a
# value from the environment (CI) wins.
ifeq ($(origin GIT_SHA),undefined)
GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null)
endif
export GIT_SHA
# Build or pull (D126). A release checkout (HEAD is exactly the tag v<VERSION>) runs the published
# images of its version: compose pulls them (pull_policy missing). Any other checkout builds the
# app images from its own code, so `make up` and `make demo` never run images older than the code.
# RELEASE_CHECKOUT=1 forces pulling, RELEASE_CHECKOUT=0 building. Read once, from VERSION (not
# TRUSTAGENT_VERSION, which a proof may set to a local tag such as p5local).
ifeq ($(origin RELEASE_CHECKOUT),undefined)
RELEASE_CHECKOUT := $(if $(shell git describe --exact-match --tags --match "v$$(cat VERSION 2>/dev/null)" HEAD 2>/dev/null),1,0)
endif
APP_BUILD = $(if $(filter 1,$(RELEASE_CHECKOUT)),,--build)

DEMO_PROJECT ?= truthtable-demo
DEMO_COMPOSE  = $(COMPOSE) -p $(DEMO_PROJECT) -f docker-compose.yml -f docker-compose.demo.yml --profile demo
# `make demo` builds the images from this checkout (pulls them on a release checkout);
# `make demo DEMO_BUILD=` uses local or published images instead (CI builds them first, with a
# layer cache).
DEMO_BUILD   ?= $(APP_BUILD)
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

# The onboarding stack (docker-compose.onboarding.yml, P4 D70): the e2e stack with no admin key and
# no decision-model key, so the dashboard opens on the setup wizard. `down -v` on the way out.
ONBOARDING_PROJECT ?= truthtable-onboarding
ONBOARDING_COMPOSE  = $(COMPOSE) -p $(ONBOARDING_PROJECT) -f docker-compose.yml -f docker-compose.e2e.yml \
	-f docker-compose.onboarding.yml --profile e2e
ONBOARDING_BUILD   ?= --build
# What onboarding-check-config requires of the proxy's composed environment before it runs
# -check-config: strict config on, the decision model on backend auto, and no key in any form.
ONBOARDING_EXPECT = --expect TRUTHTABLE_STRICT_CONFIG=true \
	$(foreach k,TRUTHTABLE_JEV_BACKEND TRUTHTABLE_JEV_API_KEY TRUTHTABLE_JEV_API_KEY_FILE OPENROUTER_API_KEY \
		TYPESAFE_API_KEY TRUTHTABLE_ADMIN_KEYS TRUTHTABLE_ADMIN_KEY TRUTHTABLE_API_KEYS TRUTHTABLE_READ_KEYS \
		TRUTHTABLE_SETUP_TOKEN,--expect-default $(k))
# It proves the committed stack, not this machine's: compose resolves the files with no .env and
# an empty environment (PATH, HOME and the docker CLI config only), as in CI. Neither a developer's
# .env (configure.py puts the trafficgen key in TRUTHTABLE_API_KEYS) nor a variable exported in the
# shell changes what it proves, so make check gives the same answer on every machine.
ONBOARDING_CONFIG_ENV = env -i PATH="$$PATH" HOME="$$HOME" DOCKER_CONFIG="$${DOCKER_CONFIG:-$$HOME/.docker}"
# trafficgen flags shared by the scenario targets.
TRAFFICGEN_CHECK = /trafficgen --once --expect --base-url http://127.0.0.1:8080
# `make up` builds the app images from this checkout, or pulls them on a release checkout
# (RELEASE_CHECKOUT); UP_BUILD= uses local or published images as they are.
UP_BUILD     ?= $(APP_BUILD)
# Extra arguments for `make up`, e.g. UP_ARGS=--force-recreate.
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
# The `make up` stack's project: COMPOSE_PROJECT_NAME (env, then .env), else `name: truthtable`.
UP_PROJECT = $(shell $(PYTHON) scripts/envfile.py get COMPOSE_PROJECT_NAME truthtable)
# The one-time setup link (scripts/setup_url.py runs /proxy -new-setup-token in the stack's proxy
# and reads its "SETUP: open <url>" line). `make up` and `make demo` open it, or the dashboard when
# an admin already exists. SETUP_FORCE=1 mints a recovery link although an admin exists.
SETUP_FORCE   ?=
SETUP_PROJECT ?=
SETUP_URL_ARGS = $(if $(filter 1,$(SETUP_FORCE)),--force)
SETUP_OPEN = $(PYTHON) scripts/setup_url.py --open $(OPEN_URL_ARGS) --dashboard-url "$(DASHBOARD_URL)"

##@ Stack (docker compose)

up: ## Start proxy, engine, postgres, dashboard (+ COMPOSE_PROFILES add-ons; builds them, or pulls on a release tag), wait until healthy, open the setup link (or the dashboard)
	$(PYTHON) scripts/configure.py --ensure
	$(COMPOSE) up -d $(UP_BUILD) --wait --wait-timeout $(WAIT_TIMEOUT) $(UP_ARGS)
	@echo "✅ TrustAgent is running (proxy: http://localhost:$$($(PYTHON) scripts/envfile.py get PROXY_HOST_PORT 8080)/v1)"
	@$(SETUP_OPEN) --project "$(UP_PROJECT)"

down: ## Stop the stack (keeps its volumes)
	$(COMPOSE) down
	@echo "✅ Stack stopped"

up-infra: ## Start only the backing services (postgres + COMPOSE_PROFILES add-ons) for make dev-go / dev-python
	$(PYTHON) scripts/configure.py --ensure
	$(COMPOSE) up -d --wait --wait-timeout $(WAIT_TIMEOUT) \
		$$($(COMPOSE) config --services | grep -vxE 'proxy|engine|dashboard|trafficgen')
	@echo "✅ Backing services running (postgres on localhost:$$($(PYTHON) scripts/envfile.py get POSTGRES_HOST_PORT 5432), user truthtable, POSTGRES_PASSWORD in .env)"

demo: ## Zero-key demo (mock fast check, mock judge, scripted traffic, no cost): build (or pull on a release tag), wait until healthy, open the setup link (or the dashboard)
	$(PYTHON) scripts/configure.py --ensure
	$(DEMO_COMPOSE) up -d $(DEMO_BUILD) --wait --wait-timeout $(WAIT_TIMEOUT)
	@echo "✅ Demo stack running (project $(DEMO_PROJECT), DEMO MODE: simulated checks, no keys, \$$0)"
	@echo "   Diagnose: python3 scripts/doctor.py --expect demo   Stop: make demo-down"
	@$(SETUP_OPEN) --project $(DEMO_PROJECT)

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

# The faults pack (contracts/demo/faults.v1.json, D119): mock-upstream's fault counters are
# reset first (from inside the proxy container, which reaches mock-upstream by name), so counted
# faults such as 429@1 start fresh; then every faults scenario runs against the e2e stack.
e2e-faults: ## Check the vendor-fault scenarios against the running e2e stack (resets mock-upstream, then trafficgen --pack faults)
	$(E2E_COMPOSE) exec -T proxy wget -q -O /dev/null --post-data= http://mock-upstream:8090/_mock/reset
	$(E2E_COMPOSE) exec -T proxy $(TRAFFICGEN_CHECK) --pack faults --scenarios faults --upstream all

e2e-logs: ## Follow the e2e stack's logs (SERVICE=mock-upstream for one service)
	$(E2E_COMPOSE) logs -f $(SERVICE)

e2e-down: ## Stop the e2e stack and delete its volumes (it holds test data only)
	$(E2E_COMPOSE) down --volumes --remove-orphans
	@echo "✅ E2E stack and its volumes removed"

##@ Onboarding stack (a fresh install: setup wizard, Connect OpenRouter on mock-upstream)

onboarding-up: ## Start the onboarding stack (no admin key, no fast-check key, vendors on mock-upstream), wait until healthy
	$(PYTHON) scripts/configure.py --ensure
	$(ONBOARDING_COMPOSE) up -d $(ONBOARDING_BUILD) --wait --wait-timeout $(WAIT_TIMEOUT)
	@echo "✅ Onboarding stack running (project $(ONBOARDING_PROJECT): no admin and no keys yet, every vendor is mock-upstream, \$$0)"
	@echo "   Setup link: make -s onboarding-setup-url   Stop: make onboarding-down"

onboarding-setup-url: ## Print only a new one-time setup URL of the onboarding stack (for TRUSTAGENT_SETUP_URL: make -s onboarding-setup-url)
	@$(PYTHON) scripts/setup_url.py --url-only --project $(ONBOARDING_PROJECT) $(SETUP_URL_ARGS)

onboarding-logs: ## Follow the onboarding stack's logs (SERVICE=proxy for one service)
	$(ONBOARDING_COMPOSE) logs -f $(SERVICE)

onboarding-down: ## Stop the onboarding stack and delete its volumes (every run starts fresh)
	$(ONBOARDING_COMPOSE) down -v --remove-orphans
	@echo "✅ Onboarding stack and its volumes removed"

onboarding-check-config: ## Prove, without a container, that the gateway accepts the onboarding stack's environment (strict config, backend auto, no key): proxy -check-config
	@if ! docker compose version >/dev/null 2>&1; then echo "onboarding-check-config: skipped (docker compose not found)"; exit 0; fi; \
	out="$$($(ONBOARDING_CONFIG_ENV) $(ONBOARDING_COMPOSE) --env-file /dev/null config --format json \
		| $(PYTHON) scripts/service_env.py proxy $(ONBOARDING_EXPECT) -- go -C backend-go run ./cmd/proxy -check-config)" \
		|| { printf '%s\n' "$$out"; echo "onboarding-check-config: failed: the stack is not the fresh install it should be, or the gateway would refuse to start it (see above)"; exit 1; }; \
	echo "✅ The onboarding stack's proxy configuration passes -check-config (strict config, backend auto, no key)"

##@ Logs, setup links and status

logs: ## Follow the stack's logs (SERVICE=proxy for one service)
	$(COMPOSE) logs -f $(SERVICE)

demo-logs: ## Follow the demo stack's logs (SERVICE=engine for one service)
	$(DEMO_COMPOSE) logs -f $(SERVICE)

setup-url: ## Print a new one-time setup URL (SETUP: open …#token=…) of the running stack (make up's, else the demo's; SETUP_PROJECT=, SETUP_FORCE=1)
	@$(PYTHON) scripts/setup_url.py $(if $(SETUP_PROJECT),--project "$(SETUP_PROJECT)") $(SETUP_URL_ARGS)

demo-setup-url: ## Print only a new one-time setup URL of the demo stack (for TRUSTAGENT_SETUP_URL: make -s demo-setup-url)
	@$(PYTHON) scripts/setup_url.py --url-only --project $(DEMO_PROJECT) $(SETUP_URL_ARGS)

status: ## Show the stack's containers and their health
	@$(COMPOSE) ps --all

clean-data: ## Stop the stack and delete its volumes (WARNING: deletes all data)
	$(COMPOSE) down --volumes
	@echo "✅ All data volumes removed"

##@ Setup tools

# WITH_SECRETS=1 also asks for the vendor keys (OpenRouter, TypeSafe, Anthropic, OpenAI) at hidden
# prompts: nothing typed is shown, and .env stays mode 0600 (P4 D74).
WITH_SECRETS ?=

configure: ## Create or complete .env (generated values, never overwrites); WITH_SECRETS=1 also asks for vendor keys at hidden prompts
	$(PYTHON) scripts/configure.py --ensure$(if $(filter 1,$(WITH_SECRETS)), --with-secrets)

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

# The record overlay's stack (RECORD_COMPOSE) is defined in mk/release.mk with make record-demo.
KNOWLEDGE_COMPOSE  = $(COMPOSE) -f docker-compose.yml -f docker-compose.knowledge.yml --profile knowledge

compose-check: ## Validate the compose files: every profile and the demo, e2e, onboarding, load, record and knowledge overrides (docker compose config -q)
	@if ! docker compose version >/dev/null 2>&1; then echo "compose-check: skipped (docker compose not found)"; exit 0; fi; \
	set -e; for p in $(COMPOSE_CHECK_PROFILES); do \
		$(COMPOSE) $${p:+--profile "$$p"} config -q; echo "compose config ok: profile $${p:-default}"; \
	done; \
	$(DEMO_COMPOSE) config -q; echo "compose config ok: demo override"; \
	$(E2E_COMPOSE) config -q; echo "compose config ok: e2e override"; \
	$(ONBOARDING_COMPOSE) config -q; echo "compose config ok: onboarding override"; \
	$(LOAD_COMPOSE) config -q; echo "compose config ok: load override"; \
	$(LOAD_COMPOSE) $(LOAD_SLOW_JEV) config -q; echo "compose config ok: load override, slow fast check"; \
	$(RECORD_COMPOSE) config -q; echo "compose config ok: record override"; \
	$(KNOWLEDGE_COMPOSE) config -q; echo "compose config ok: knowledge override"

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

##@ Monitoring

metrics: ## Open the Prometheus UI (profile observability; WSL/macOS/Linux aware)
	@$(PYTHON) scripts/open_url.py --label "Prometheus" http://localhost:9090

dashboards: ## Open the Grafana dashboards (profile observability; WSL/macOS/Linux aware)
	@$(PYTHON) scripts/open_url.py --label "Grafana (admin / GRAFANA_ADMIN_PASSWORD in .env)" http://localhost:3001

# ---------- Aggregate hooks (see the root Makefile) ----------
test: test-scripts
lint: lint-scripts
check: compose-check
check: config-docs-check
check: onboarding-check-config
