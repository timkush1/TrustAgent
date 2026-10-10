# ============================================================================
# mk/load.mk - Gateway load test (k6 on the load stack). Owned by the GO stream
# (D132). Moved here from mk/dx.mk on 2026-10-09. Included by the root
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
# Pinned (D118): the multi-arch index digest of grafana/k6 2.3.0 (2026-09-21).
K6_IMAGE      ?= grafana/k6:2.3.0@sha256:9c2dee7f8ed74d317e4027c06a10f169b625638189de8d4555d0b3486a5aeb34
# Where a load run writes its k6 summaries and the run file (scripts/load/report.py).
LOAD_OUT      ?= tmp/load
# Each scenario is its own k6 run: the counters k6 reads before and after a run (the gateway's
# /metrics, the fast-check mock's request totals) then belong to that scenario alone. Pass 1 runs
# with the fast check answering at once, pass 2 on mock-jev-slow (150 ms).
LOAD_PASS1    ?= a b c e f g
LOAD_PASS2    ?= d
# LOAD_PUBLISH=1 writes docs/benchmarks/load/<run_id>.json from the two passes (scripts/load/report.py,
# which refuses a dirty tree, n < 500, a missing environment and a missed target); LOAD_REPORT_ONLY=1
# publishes a run that missed a target, marked report-only.
LOAD_PUBLISH     ?=
LOAD_REPORT_ONLY ?=
# k6 joins the running proxy container's network namespace and calls it on 127.0.0.1: no bridge
# hop in the measurement, and no run at all (docker refuses) when the proxy is not running. It
# reads the proxy's /metrics (127.0.0.1:8002) and the fast-check mock ($(2)) before and after.
# $(call k6_run,<scenarios>,<fast-check mock URL>): k6 reads the container environment as __ENV.
# The proxy key reaches it through docker's own environment (`-e API_KEY` without a value), never
# the command line, where other local users could read it.
k6_run = API_KEY="$$($(PYTHON) scripts/envfile.py get TRAFFICGEN_API_KEY)" \
	docker run --rm --network "container:$$($(LOAD_COMPOSE) ps -q proxy)" \
	-v "$(CURDIR)/scripts/load:/scripts/load:ro" \
	-e BASE_URL=http://127.0.0.1:8080 -e API_KEY -e JEV_MOCK_URL=$(2) \
	-e RATE=$(LOAD_RATE) -e DURATION=$(LOAD_DURATION) -e SCENARIOS=$(1) \
	$(K6_IMAGE) run /scripts/load/gateway-overhead.js

load-up: ## Start the load-test stack (vendors on mock-upstream, no host ports) and wait until healthy
	$(PYTHON) scripts/configure.py --ensure
	$(LOAD_COMPOSE) up -d $(LOAD_BUILD) --wait --wait-timeout $(WAIT_TIMEOUT) proxy

# Pass 1 runs scenarios a, b, c, e, f and g; pass 2 recreates the proxy on the 150 ms fast check for
# scenario d. Each pass is teed to $(LOAD_OUT)/pass<n>.log, whose TRUSTAGENT_LOAD_RESULT lines
# scripts/load/report.py reads. The stack is removed whatever happened, and the exit status says
# whether every target held. (No $$(MAKE) inside the recipe: `make -n` would run such a line for real.)
load-test-gate: load-up ## Gateway overhead under load: k6 scenarios a-g on the load stack (starts it, removes it after; LOAD_RATE=, LOAD_DURATION=, LOAD_PUBLISH=1)
	@status=0; mkdir -p $(LOAD_OUT); rm -f $(LOAD_OUT)/pass1.log $(LOAD_OUT)/pass2.log $(LOAD_OUT)/failed.log; \
	echo "Pass 1: scenarios $(LOAD_PASS1) (the fast check answers at once), one k6 run each"; \
	for s in $(LOAD_PASS1); do \
		{ $(call k6_run,$$s,http://mock-upstream:8090) || echo "$$s" >> $(LOAD_OUT)/failed.log; } 2>&1 | tee -a $(LOAD_OUT)/pass1.log; \
	done; \
	echo "Pass 2: scenarios $(LOAD_PASS2) (the fast check answers in 150 ms)"; \
	if $(LOAD_COMPOSE) $(LOAD_SLOW_JEV) up -d --wait --wait-timeout $(WAIT_TIMEOUT) --no-deps mock-jev-slow proxy; then \
		for s in $(LOAD_PASS2); do \
			{ $(call k6_run,$$s,http://mock-jev-slow:8090) || echo "$$s" >> $(LOAD_OUT)/failed.log; } 2>&1 | tee -a $(LOAD_OUT)/pass2.log; \
		done; \
	else status=1; fi; \
	if [ -e $(LOAD_OUT)/failed.log ]; then status=1; echo "load-test-gate: k6 failed for scenario(s): $$(tr '\n' ' ' < $(LOAD_OUT)/failed.log)"; fi; \
	$(LOAD_COMPOSE) $(LOAD_SLOW_JEV) down --volumes --remove-orphans; \
	if [ -n "$(LOAD_PUBLISH)" ]; then \
		$(PYTHON) scripts/load/report.py --out-dir docs/benchmarks/load --k6-image "$(K6_IMAGE)" \
			--compose "$(LOAD_COMPOSE)" $(if $(LOAD_REPORT_ONLY),--report-only) \
			$(LOAD_OUT)/pass1.log $(LOAD_OUT)/pass2.log || status=1; \
	fi; \
	if [ $$status -ne 0 ]; then echo "load-test-gate: a target was missed or a pass did not run (see above)"; fi; \
	exit $$status

load-report: ## Check the published load runs and the GATEWAY.md block against the newest clean one (scripts/load/report.py)
	$(PYTHON) scripts/load/report.py --gateway-md --check

load-down: ## Stop the load-test stack and delete its volumes
	$(LOAD_COMPOSE) down --volumes --remove-orphans
