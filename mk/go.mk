# ============================================================================
# mk/go.mk - Go proxy (backend-go/). Owned by the W-GO stream.
# Included by the root Makefile; targets are declared .PHONY there.
# ============================================================================

# Pinned protoc plugin versions (keep in sync with the CI workflows).
PROTOC_GEN_GO_VERSION      ?= v1.34.2
PROTOC_GEN_GO_GRPC_VERSION ?= v1.5.1

# `make test-go` is verbose; the `test` aggregate runs Go tests quietly
# (a target-specific value on `test` is inherited by its prerequisites).
GO_TEST_FLAGS ?= -v

##@ Go (backend-go: proxy)

install-go: proto-tools ## Install Go dependencies and the protoc Go plugins
	@echo "Installing Go dependencies..."
	cd backend-go && go mod download

proto-tools: ## Install the protoc Go plugins (protoc-gen-go, protoc-gen-go-grpc)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

proto-go: ## Generate Go gRPC stubs into backend-go/api/audit/v1
	@echo "Generating Go protobuf code..."
	PATH="$$(go env GOPATH)/bin:$$PATH" protoc --proto_path=proto \
		--go_out=backend-go --go_opt=module=github.com/truthtable/backend-go \
		--go-grpc_out=backend-go --go-grpc_opt=module=github.com/truthtable/backend-go \
		proto/evaluator.proto

dev-go: ## Run Go proxy in development mode
	cd backend-go && go run ./cmd/proxy

test-go: ## Run Go tests only (verbose)
	@echo "Running Go tests..."
	cd backend-go && go test $(GO_TEST_FLAGS) ./...

lint-go: ## Check gofmt and run go vet (mirrors CI; `make fmt-go` fixes formatting)
	@echo "Linting Go..."
	@cd backend-go && unformatted="$$(gofmt -l .)" && \
		if [ -n "$$unformatted" ]; then echo "Files need gofmt (run make fmt-go):"; echo "$$unformatted"; exit 1; fi
	cd backend-go && go vet ./...

fmt-go: ## Format Go code (go fmt)
	cd backend-go && go fmt ./...

clean-go: ## Clean the Go build cache
	@echo "Cleaning Go cache..."
	cd backend-go && go clean -cache

##@ Measurement and replay tools (backend-go/cmd)

# make calibrate: the evaluator to fit and its labelled risks (a JSONL file, - for stdin, or the
# output of make gate-eval). LABEL_SOURCE names the labels' origin when the lines do not.
EVALUATOR      ?=
IN             ?=
LABEL_SOURCE   ?=
DATASET        ?=
WRITE          ?=
CALIBRATE_ARGS ?=
# make export-replay: where the recording goes and which traces it holds.
REPLAY_OUT    ?=
REPLAY_SINCE  ?=
REPLAY_SOURCE ?= trafficgen
REPLAY_ARGS   ?=

gate-eval: ## Build backend-go/bin/gate-eval: the fast check over a JSONL file (bench-lite and eval-live run it); prints its sha256
	cd backend-go && go build -o bin/gate-eval ./cmd/gate-eval
	@echo "✅ Built backend-go/bin/gate-eval"
	@$(PYTHON) -c "import hashlib, sys; print('   sha256 ' + hashlib.sha256(open(sys.argv[1], 'rb').read()).hexdigest() + ' (a bench run records it; a resume with another build is refused)')" backend-go/bin/gate-eval

calibrate: ## Fit a calibration: EVALUATOR=groundedness@1 IN=labels.jsonl [LABEL_SOURCE=human] [DATASET=..] [WRITE=1 writes config/calibration]
	@test -n "$(EVALUATOR)" || { \
		echo "usage: make calibrate EVALUATOR=<id>@<version> IN=<labelled JSONL> [LABEL_SOURCE=human|public_dataset|uniform_audit|judge]"; \
		echo "                      [DATASET=ragtruth:qa@<rev>] [WRITE=1] [CALIBRATE_ARGS='--source api --api-key-file admin.key']"; \
		exit 2; }
	cd backend-go && go run ./cmd/calibrate --evaluator "$(EVALUATOR)" \
		$(if $(IN),--in "$(if $(filter -,$(IN)),-,$(abspath $(IN)))") $(if $(LABEL_SOURCE),--label-source "$(LABEL_SOURCE)") \
		$(foreach d,$(DATASET),--dataset "$(d)") $(if $(WRITE),--write) $(CALIBRATE_ARGS)

export-replay: ## Export a sanitized replay recording: REPLAY_OUT=<dir> [REPLAY_SINCE=<RFC 3339>] [REPLAY_SOURCE=trafficgen] (reads TRUTHTABLE_DATABASE_URL)
	@test -n "$(REPLAY_OUT)" || { \
		echo "usage: make export-replay REPLAY_OUT=<dir> [REPLAY_SINCE=2026-09-28T00:00:00Z] [REPLAY_SOURCE=trafficgen]"; \
		echo "                          [REPLAY_ARGS='--force' or '--api http://127.0.0.1:8080 --api-key-file read.key']"; \
		exit 2; }
	cd backend-go && go run ./cmd/export-replay --out "$(abspath $(REPLAY_OUT))" \
		$(if $(REPLAY_SINCE),--since "$(REPLAY_SINCE)") $(if $(REPLAY_SOURCE),--source "$(REPLAY_SOURCE)") $(REPLAY_ARGS)

##@ Decision model check and Go benchmarks

# make jev-check: one check of the decision model's connection (cmd/jevcheck). It sends the
# one-question probe (contracts/cascade/probe.v1.json), which the vendor bills at about USD 0.00001,
# so it needs no CONFIRM=1. Inside a running stack it runs in the proxy container, where the
# gateway's own configuration is; without one it runs from this checkout with the shell's environment.
JEV_CHECK_ARGS ?=

jev-check: ## Check the decision model connection: one probe call, billed about USD 0.00001 (cmd/jevcheck; in the running stack's proxy container when there is one)
	@if $(COMPOSE) ps --status running --services 2>/dev/null | grep -qx proxy; then \
		$(COMPOSE) exec -T proxy /jevcheck $(JEV_CHECK_ARGS); \
	else \
		go -C backend-go run ./cmd/jevcheck $(JEV_CHECK_ARGS); \
	fi

# D211: jevcheck --capture records one sanitized fixture per vendor response the gateway decodes
# (the key check, a bogus key's 401, both decision paths, ZDR, a Haiku chat with usage.cost, a
# 30k-token probe) into CAPTURE_DIR with OPENROUTER_API_KEY. A dry run (the plan and its estimate,
# nothing sent) unless CONFIRM=1; hard cap USD 0.01, expected under USD 0.005. A real capture runs
# the pinned gitleaks over CAPTURE_DIR afterwards, whatever the capture's exit (a capture that
# stopped half way has written fixtures too). Fixtures from a host that is not a vendor
# (mock-upstream) say "simulated", never "captured". A real capture's result line is appended to
# P6_JOURNAL (tmp/p6/results.log), where make p6-ledger counts it.
CAPTURE_DIR ?= contracts/fixtures/vendor/captured
CAPTURE_MAX_COST_USD ?= 0.01
# The key, the OpenRouter base URL and the model come from the shell or, as the services read
# them, from .env (scripts/envfile.py; the values are expanded by the shell, never echoed).
JEV_CAPTURE_ENV = $(foreach k,OPENROUTER_API_KEY TRUTHTABLE_OPENROUTER_BASE_URL TRUTHTABLE_JEV_MODEL,$(k)="$$($(PYTHON) scripts/envfile.py get $(k))")
JEV_CAPTURE = $(JEV_CAPTURE_ENV) go -C backend-go run ./cmd/jevcheck --capture "$(abspath $(CAPTURE_DIR))" --max-cost-usd $(CAPTURE_MAX_COST_USD)

jev-capture: ## Capture sanitized vendor fixtures with jevcheck --capture into CAPTURE_DIR (a dry run unless CONFIRM=1; cap USD 0.01; then gitleaks over it)
ifeq ($(CONFIRM),1)
	@$(MAKE) --no-print-directory gitleaks-install
	@mkdir -p "$(dir $(P6_JOURNAL))"; out="$(dir $(P6_JOURNAL))jev-capture.out"; rm -f "$$out.rc"; \
	{ $(JEV_CAPTURE) || echo $$? > "$$out.rc"; } | tee "$$out"; \
	code=$$(cat "$$out.rc" 2>/dev/null || echo 0); rm -f "$$out.rc"; \
	grep '^TRUSTAGENT_P6_RESULT ' "$$out" >> "$(P6_JOURNAL)" \
		|| echo "jev-capture: no result line, so make p6-ledger cannot count this capture" >&2; \
	if [ -d "$(CAPTURE_DIR)" ]; then \
		$(GITLEAKS) dir --config .gitleaks.toml --redact --no-banner --exit-code 1 "$(CAPTURE_DIR)" || exit 1; \
		echo "✅ gitleaks: nothing found in $(CAPTURE_DIR)"; \
	fi; \
	exit $$code
else
	@$(JEV_CAPTURE) --dry-run
endif

# Go benchmarks (D120): five counts of every benchmark, written to backend-go/bench/<sha>.txt
# (ignored), so two revisions compare with benchstat. bench-go-smoke runs each benchmark once.
BENCH_PKGS ?= ./...

bench-go: ## Run the Go benchmarks (-count 5) into backend-go/bench/<git sha>.txt
	@mkdir -p backend-go/bench
	cd backend-go && go test -run '^$$' -bench . -benchmem -count 5 $(BENCH_PKGS) | tee bench/$$(git rev-parse --short HEAD).txt

bench-go-smoke: ## Run every Go benchmark once (-benchtime 1x): they compile and run
	cd backend-go && go test -run '^$$' -bench . -benchtime 1x $(BENCH_PKGS)

# Tier-0 figures for GATEWAY.md (D120, D167): five counts of the Tier-0 benchmarks at a clean
# commit, kept with that commit, the date, the Go version and the CPU in
# docs/benchmarks/tier0/<sha>.txt; scripts/tier0_report.py then writes the GATEWAY.md block.
TIER0_BENCH ?= BenchmarkDetect$$|BenchmarkGatePreJev$$

bench-tier0-publish: ## Run the Tier-0 benchmarks (-count 5) at a clean commit and publish them in docs/GATEWAY.md
	@test -z "$$(git status --porcelain -- backend-go contracts)" || { \
		echo "bench-tier0-publish: commit backend-go/ and contracts/ first: the figures name the commit"; exit 2; }
	@mkdir -p docs/benchmarks/tier0
	@sha=$$(git rev-parse --short HEAD); out=docs/benchmarks/tier0/$$sha.txt; \
	{ echo "commit: $$sha"; echo "date: $$(date -u +%Y-%m-%dT%H:%M:%SZ)"; echo "go: $$(go -C backend-go version | cut -d' ' -f3)"; \
	  go -C backend-go test -run '^$$' -bench '$(TIER0_BENCH)' -benchmem -count 5 ./internal/gate/; } > $$out.tmp \
	  && mv $$out.tmp $$out && echo "bench-tier0-publish: wrote $$out" || { rm -f $$out.tmp; exit 1; }
	$(PYTHON) scripts/tier0_report.py --gateway-md

bench-tier0-report: ## Check the GATEWAY.md Tier-0 block against the newest published run (scripts/tier0_report.py)
	$(PYTHON) scripts/tier0_report.py --gateway-md --check

# ---------- Aggregate hooks (see the root Makefile) ----------
check: bench-tier0-report
install: install-go
proto: proto-go
test: GO_TEST_FLAGS :=
test: test-go
lint: lint-go
fmt: fmt-go
clean: clean-go
