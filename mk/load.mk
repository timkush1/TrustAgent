# ============================================================================
# mk/load.mk - Gateway load test (k6 on the load stack). Owned by the GO stream
# (P5-PLAN D132). Moved here from mk/dx.mk at P5 Step 0a. Included by the root
# Makefile; targets are declared .PHONY there. compose-check (mk/dx.mk) also
# validates LOAD_COMPOSE.
# ============================================================================

##@ Load testing

# The load-test stack (docker-compose.load.yml): every vendor on mock-upstream, the load policy of
# scripts/load/policies.yaml, no published host port (k6 joins the proxy container), so it can
# run next to a demo. docker-compose.load-slow-jev.yml turns the fast check into a 150 ms one for
# scenario d. docs/GATEWAY.md §13 lists the scenarios and their targets.
comma := ,
LOAD_PROJECT  ?= truthtable-load
LOAD_COMPOSE   = $(COMPOSE) -p $(LOAD_PROJECT) -f docker-compose.yml -f docker-compose.load.yml --profile load
LOAD_SLOW_JEV  = -f docker-compose.load-slow-jev.yml
LOAD_BUILD    ?= --build
# k6 (scripts/load/gateway-overhead.js): requests per second, and the time, of each scenario.
LOAD_RATE     ?= 20
LOAD_DURATION ?= 30s
# Pinned (P5-PLAN D118): the multi-arch index digest of grafana/k6 2.3.0 (2026-09-21).
K6_IMAGE      ?= grafana/k6:2.3.0@sha256:9c2dee7f8ed74d317e4027c06a10f169b625638189de8d4555d0b3486a5aeb34
# Where a load run writes its k6 summaries and the run file (scripts/load/report.py).
LOAD_OUT      ?= tmp/load
# k6 joins the running proxy container's network namespace and calls it on 127.0.0.1: no bridge
# hop in the measurement, and no run at all (docker refuses) when the proxy is not running.
# $(call k6_run,<scenarios>): k6 reads the container environment as __ENV. The proxy key reaches
# it through docker's own environment (`-e API_KEY` without a value), never the command line,
# where other local users could read it.
k6_run = API_KEY="$$($(PYTHON) scripts/envfile.py get TRAFFICGEN_API_KEY)" \
	docker run --rm -i --network "container:$$($(LOAD_COMPOSE) ps -q proxy)" \
	-e BASE_URL=http://127.0.0.1:8080 -e API_KEY \
	-e RATE=$(LOAD_RATE) -e DURATION=$(LOAD_DURATION) -e SCENARIOS=$(1) \
	$(K6_IMAGE) run - < scripts/load/gateway-overhead.js

load-up: ## Start the load-test stack (vendors on mock-upstream, no host ports) and wait until healthy
	$(PYTHON) scripts/configure.py --ensure
	$(LOAD_COMPOSE) up -d $(LOAD_BUILD) --wait --wait-timeout $(WAIT_TIMEOUT) proxy

# Pass 1 runs scenarios a, b, c, e and f; pass 2 recreates the proxy on the 150 ms fast check for
# scenario d. The stack is removed whatever happened, and the exit status says whether every
# target held. (No $$(MAKE) inside the recipe: `make -n` would run such a line for real.)
load-test-gate: load-up ## Gateway overhead under load: k6 scenarios a-f on the load stack (starts it, removes it after; LOAD_RATE=, LOAD_DURATION=)
	@status=0; \
	echo "Pass 1: scenarios a, b, c, e, f (the fast check answers at once)"; \
	$(call k6_run,a$(comma)b$(comma)c$(comma)e$(comma)f) || status=1; \
	echo "Pass 2: scenario d (the fast check answers in 150 ms)"; \
	if $(LOAD_COMPOSE) $(LOAD_SLOW_JEV) up -d --wait --wait-timeout $(WAIT_TIMEOUT) --no-deps mock-jev-slow proxy; then \
		$(call k6_run,d) || status=1; \
	else status=1; fi; \
	$(LOAD_COMPOSE) $(LOAD_SLOW_JEV) down --volumes --remove-orphans; \
	if [ $$status -ne 0 ]; then echo "load-test-gate: a target was missed or a pass did not run (see above)"; fi; \
	exit $$status

load-down: ## Stop the load-test stack and delete its volumes
	$(LOAD_COMPOSE) down --volumes --remove-orphans
