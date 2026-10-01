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

gate-eval: ## Build backend-go/bin/gate-eval: the fast check over a JSONL file (bench-lite and eval-live run it)
	cd backend-go && go build -o bin/gate-eval ./cmd/gate-eval
	@echo "✅ Built backend-go/bin/gate-eval"

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

# ---------- Aggregate hooks (see the root Makefile) ----------
install: install-go
proto: proto-go
test: GO_TEST_FLAGS :=
test: test-go
lint: lint-go
fmt: fmt-go
clean: clean-go
