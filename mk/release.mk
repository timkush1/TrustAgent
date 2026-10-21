# ============================================================================
# mk/release.mk - Release, recording and doctor targets. Owned by the DX-RELEASE
# stream (D132). Included by the root Makefile; targets are declared
# .PHONY there.
# ============================================================================

##@ Release and recording

# ---------- Version (D127) ----------
# VERSION is the one place the version is decided; scripts/bump_version.py rewrites its mirrors
# (pyproject.toml, __init__.py, package.json, package-lock.json, version.go and the compose default
# image tag), renames the CHANGELOG "Unreleased — X.Y.Z" heading and regenerates the recording.
V       ?=
DRY_RUN ?=
RELEASE_TAG ?=

version-bump: ## Set a new version everywhere: make version-bump V=2.0.0 (DRY_RUN=1 prints the plan only)
	@if [ -z "$(V)" ]; then echo "version-bump: set V, e.g. make version-bump V=2.0.0 (DRY_RUN=1 to preview)"; exit 2; fi
	$(PYTHON) scripts/bump_version.py $(if $(filter 1,$(DRY_RUN)),--dry-run) $(V)

check-version: ## Check that every version mirror agrees with VERSION (RELEASE_TAG=v2.0.0 also checks a release tag)
	$(PYTHON) scripts/check_version.py $(if $(RELEASE_TAG),--tag $(RELEASE_TAG))

# release-notes prints Markdown on stdout only (make release-notes TAG=v2.0.0 | make clip). It reads
# each image's digest anonymously with docker buildx imagetools inspect, or canned output from
# RELEASE_NOTES_INSPECT (a JSON file, as scripts/tests/fixtures/inspect.json; the R-4 rehearsal).
TAG ?=
RELEASE_NOTES_INSPECT ?=

release-notes: ## Release notes for a tag: make release-notes TAG=v2.0.0 (CHANGELOG section, image digests, verify commands, measured labels; refuses a missing digest)
	@if [ -z "$(TAG)" ]; then echo "release-notes: set TAG, e.g. make release-notes TAG=v2.0.0" >&2; exit 2; fi
	@$(PYTHON) scripts/release_notes.py --tag $(TAG) $(if $(RELEASE_NOTES_INSPECT),--inspect $(RELEASE_NOTES_INSPECT))

# ---------- GitHub Pages site (D124, D110, D137) ----------
# `make pages-build` runs, in the same order, the steps of the build job of
# .github/workflows/pages.yml, which calls the same targets: the recording check, the static build,
# the landing size budget, the file checks and the gitleaks scan of the built site. It never
# deploys; only pages.yml's deploy job does, and only when the owner turned it on.
#
# PAGES_DATASET is the build's VITE_DEMO_DATASET (D110): auto ships demo-data/recorded when it
# holds a manifest, else demo-data/simulated; recorded requires a real recording (a release tag:
# check_recording --require-recorded); mock always ships the simulated one.
PAGES_DATASET   ?= auto
PAGES_BASE_PATH ?= /TrustAgent/
PAGES_DIST       = frontend-react/dist
DEMO_DATA        = frontend-react/demo-data
PAGES_RECORDING_DIR ?= $(if $(filter recorded,$(PAGES_DATASET)),$(DEMO_DATA)/recorded,$(if \
	$(filter mock,$(PAGES_DATASET)),$(DEMO_DATA)/simulated,$(if \
	$(wildcard $(DEMO_DATA)/recorded/manifest.json),$(DEMO_DATA)/recorded,$(DEMO_DATA)/simulated)))

# Downloaded tools live in tmp/tools: tmp/ is scratch space the root .gitignore ignores (D143).
# TOOLS_DIR may point elsewhere.
TOOLS_DIR ?= tmp/tools
# gitleaks: the release security.yml pins, checked against its published SHA-256 (D137).
GITLEAKS_VERSION := 8.28.0
GITLEAKS_SHA256_linux_x64    := a65b5253807a68ac0cafa4414031fd740aeb55f54fb7e55f386acb52e6a840eb
GITLEAKS_SHA256_linux_arm64  := eff65261156100e5d94a6b3dec313d532fddfe19ae1590bf7a2b4f2699128356
GITLEAKS_SHA256_darwin_x64   := edf5a507008b0d2ef4959575772772770586409c1f6f74dabf19cbe7ec341ced
GITLEAKS_SHA256_darwin_arm64 := d942f3ad147250c9edbaab3fed9e482f98d3b59ba10ae97b8d75647e3ade492c
GITLEAKS = $(TOOLS_DIR)/gitleaks-$(GITLEAKS_VERSION)

##@ GitHub Pages site (static landing + replay demo; never deploys)

pages-build: ## Build the Pages site as pages.yml does: recording check, static build, size budget, file checks, gitleaks (PAGES_DATASET=auto|mock|recorded)
	@$(MAKE) --no-print-directory pages-recording-check
	@test -d frontend-react/node_modules || (cd frontend-react && npm ci)
	@$(MAKE) --no-print-directory pages-static
	@$(MAKE) --no-print-directory pages-size
	@$(MAKE) --no-print-directory pages-files
	@$(MAKE) --no-print-directory pages-gitleaks
	@echo "✅ Pages site built in $(PAGES_DIST) from $(PAGES_RECORDING_DIR) (base $(PAGES_BASE_PATH)); preview: cd frontend-react && npm run preview:static"

pages-recording-check: ## Pages step 1: check_recording.py on the dataset the site will ship (--require-recorded when PAGES_DATASET=recorded)
	@case "$(PAGES_DATASET)" in auto|mock|recorded) ;; \
		*) echo "pages: PAGES_DATASET must be auto, mock or recorded (got '$(PAGES_DATASET)')"; exit 2;; esac
	$(PYTHON) scripts/check_recording.py $(PAGES_RECORDING_DIR)$(if $(filter recorded,$(PAGES_DATASET)), --require-recorded)

pages-static: ## Pages step 2: the static build (VITE_BASE_PATH from PAGES_BASE_PATH, VITE_DEMO_DATASET from PAGES_DATASET)
	cd frontend-react && VITE_APP_MODE=static VITE_BASE_PATH=$(PAGES_BASE_PATH) VITE_DEMO_DATASET=$(PAGES_DATASET) npm run build:static

# The size check reads the build's manifests in dist/.vite; the site has no use for them, so they
# go once it has run (the public artifact would otherwise publish the whole source module map).
pages-size: ## Pages step 3: the landing page's size budget over the built site, then drops the build manifests
	cd frontend-react && npm run size
	rm -rf $(PAGES_DIST)/.vite

# The site's shape: the landing page at the root, the app under app/, 404.html a copy of the app
# (GitHub Pages serves it for every path with no file, and it boots the router); every page loads
# its assets under the base path; the dataset in demo/ is byte for byte the one that was checked
# (.gitkeep placeholders aside: vite/plugins/dataset.ts does not copy them); no source maps and no
# build manifests (dist/.vite).
pages-files: ## Pages step 4: file checks over the built site (pages, 404 fallback, base path, shipped dataset, no source maps or build manifests)
	@set -e; d="$(PAGES_DIST)"; fail() { echo "pages-files: $$1"; exit 1; }; \
	for f in index.html app/index.html 404.html demo/manifest.json; do \
		test -s "$$d/$$f" || fail "$$d/$$f is missing (run make pages-static first)"; done; \
	cmp -s "$$d/404.html" "$$d/app/index.html" || fail "404.html is not a copy of app/index.html"; \
	for f in index.html app/index.html; do \
		grep -q '="$(PAGES_BASE_PATH)assets/' "$$d/$$f" || fail "$$f loads no asset under $(PAGES_BASE_PATH)"; \
		if grep -Eq '(src|href)="/(assets|app|demo)/' "$$d/$$f"; then fail "$$f loads an asset outside $(PAGES_BASE_PATH)"; fi; \
	done; \
	diff -r -q -x .gitkeep "$(PAGES_RECORDING_DIR)" "$$d/demo" >/dev/null \
		|| fail "$$d/demo is not the checked dataset $(PAGES_RECORDING_DIR): $$(diff -r -q -x .gitkeep '$(PAGES_RECORDING_DIR)' "$$d/demo" | head -3)"; \
	maps="$$(find "$$d" -name '*.map' | head -3)"; test -z "$$maps" || fail "source maps in the site: $$maps"; \
	test ! -e "$$d/.vite" || fail "$$d/.vite (the build manifests) is in the site: run make pages-size, which drops it"; \
	echo "✅ Pages files: landing, app, 404 fallback, base path $(PAGES_BASE_PATH), dataset $(PAGES_RECORDING_DIR), no source maps or build manifests"

pages-gitleaks: gitleaks-install ## Pages step 5: gitleaks over the built site (the public artifact holds no key)
	$(GITLEAKS) dir --config .gitleaks.toml --redact --no-banner --exit-code 1 $(PAGES_DIST)

gitleaks-install: ## Download the pinned gitleaks release into tmp/tools, checked against its published SHA-256
	@set -e; test -x "$(GITLEAKS)" && exit 0; \
	os="$$(uname -s | tr '[:upper:]' '[:lower:]')"; arch="$$(uname -m)"; \
	case "$$arch" in x86_64|amd64) arch=x64;; aarch64|arm64) arch=arm64;; esac; \
	case "$$os-$$arch" in \
		linux-x64) sum=$(GITLEAKS_SHA256_linux_x64);; linux-arm64) sum=$(GITLEAKS_SHA256_linux_arm64);; \
		darwin-x64) sum=$(GITLEAKS_SHA256_darwin_x64);; darwin-arm64) sum=$(GITLEAKS_SHA256_darwin_arm64);; \
		*) echo "gitleaks-install: no pinned checksum for $$os-$$arch; install gitleaks $(GITLEAKS_VERSION) and copy it to $(GITLEAKS)"; exit 2;; \
	esac; \
	mkdir -p "$(TOOLS_DIR)"; \
	work="$$(mktemp -d)"; trap 'rm -rf "$$work"' EXIT; \
	curl -fsSLo "$$work/gitleaks.tgz" "https://github.com/gitleaks/gitleaks/releases/download/v$(GITLEAKS_VERSION)/gitleaks_$(GITLEAKS_VERSION)_$${os}_$${arch}.tar.gz"; \
	got="$$($(PYTHON) -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$$work/gitleaks.tgz")"; \
	test "$$got" = "$$sum" || { echo "gitleaks-install: checksum mismatch ($$got), refusing the download"; exit 1; }; \
	tar -xzf "$$work/gitleaks.tgz" -C "$$work" gitleaks; \
	install -m 0755 "$$work/gitleaks" "$(GITLEAKS)"; \
	echo "✅ gitleaks $(GITLEAKS_VERSION) ($$os-$$arch, SHA-256 checked) in $(GITLEAKS)"

gitleaks-history: gitleaks-install ## gitleaks over the whole git history with the repository's rules (as CI's security job)
	$(GITLEAKS) git --config .gitleaks.toml --redact --no-banner --exit-code 1 .

# The public-repo hygiene grep (D216): no home-directory paths, orchestration paths or personal
# email addresses in tracked files outside docs/archive. Each pattern ends in a one-letter class,
# so this file does not match itself.
HYGIENE_PATTERN := /home/timkushma[r]|\.cache/trustagent-or[c]h|timandga[l]|mail\.tau\.ac\.i[l]

hygiene-check: ## No local paths or personal emails in tracked files (docs/archive aside; D216)
	@hits="$$(git grep -n -E '$(HYGIENE_PATTERN)' -- ':!docs/archive' || true)"; \
	if [ -n "$$hits" ]; then printf '%s\n' "$$hits" | head -20; \
		echo "hygiene-check: local paths or personal emails in tracked files (D216)" >&2; exit 1; fi; \
	echo "✅ hygiene: no local paths or personal emails in tracked files"

check: hygiene-check

# ---------- Demo recording (D123, D207-D210) ----------
# scripts/record_demo.py records the public demo with the real fast check and judge (the owner's
# OpenRouter key, about $1 an attempt). Without CONFIRM=1 it is a dry run: the plan, the ledger,
# this attempt's caps and which keys it found. CONFIRM=1 records one attempt into
# tmp/record-demo/<attempt>/ and leaves its stack (docker-compose.record.yml, project
# truthtable-record, images tagged p6rec, no host port) up for the review; ACCEPT=1 ATTEMPT=<id>
# takes it (FEATURED=<trace id> is the owner's pick when no trace meets the featured rule, VIDEO=1
# also records the video's stack beats) and DISCARD=1 drops it. tmp/record-demo/ledger.jsonl keeps
# the key's usage across attempts: RECORD_MAX_TOTAL_USD caps every attempt together,
# RECORD_PASSES sets the trafficgen passes, ALLOW_UNCALIBRATED=1 records a snapshot without a
# calibration file (the tape then says uncalibrated).
RECORD_PROJECT := truthtable-record
RECORD_COMPOSE  = $(COMPOSE) -p $(RECORD_PROJECT) -f docker-compose.yml -f docker-compose.record.yml
CONFIRM        ?=
RECORD_ARGS    ?=
ACCEPT               ?=
DISCARD              ?=
VIDEO                ?=
ATTEMPT              ?=
FEATURED             ?=
RECORD_MAX_TOTAL_USD ?= 4
RECORD_PASSES        ?= 2
ALLOW_UNCALIBRATED   ?=
RECORD_DEMO_ARGS = --passes $(RECORD_PASSES) --max-total-usd $(RECORD_MAX_TOTAL_USD) \
	$(if $(filter 1,$(ALLOW_UNCALIBRATED)),--allow-uncalibrated) \
	$(if $(filter 1,$(ACCEPT)),--accept "$(ATTEMPT)" $(if $(FEATURED),--featured "$(FEATURED)") $(if $(filter 1,$(VIDEO)),--video)) \
	$(if $(filter 1,$(DISCARD)),--discard)
# record-check-config: what the proxy must see in the record stack, proved without Docker with
# fake keys (the project's conventions) in an otherwise empty environment, as onboarding-check-config.
RECORD_CHECK_ENV = OPENROUTER_API_KEY=sk-or-v1-e2e-record TRAFFICGEN_API_KEY=record-check \
	TRUTHTABLE_API_KEYS=trafficgen=record-check POSTGRES_PASSWORD=record-check
RECORD_EXPECT = $(foreach kv,TRUTHTABLE_TEST_MODE=true TRUTHTABLE_STRICT_CONFIG=true \
		TRUTHTABLE_DEMO_MODE=false TRUTHTABLE_PUBLIC_READ=false TRUTHTABLE_TRACE_FLUSH=sync \
		TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY=0.50 TRUTHTABLE_SPEND_CAP_LLM_USD_DAY=3 \
		TRUTHTABLE_UPSTREAM_URL=https://openrouter.ai/api,--expect $(kv)) \
	--expect-default TRUTHTABLE_JEV_BACKEND

##@ Demo recording (the real fast check and judge; your OpenRouter key)

record-demo: ## Record the public demo with the real fast check and judge (a dry run unless CONFIRM=1; ACCEPT=1 ATTEMPT=<id> takes a reviewed attempt, DISCARD=1 drops it; about $1 an attempt on your OpenRouter key)
	CONFIRM="$(CONFIRM)" $(PYTHON) scripts/record_demo.py $(RECORD_DEMO_ARGS) $(RECORD_ARGS)

record-check-config: ## Prove, without a container, that the gateway accepts the record stack's environment (test mode, real backends, strict config) with fake keys: proxy -check-config
	@if ! docker compose version >/dev/null 2>&1; then echo "record-check-config: skipped (docker compose not found)"; exit 0; fi; \
	out="$$($(ONBOARDING_CONFIG_ENV) $(RECORD_CHECK_ENV) $(RECORD_COMPOSE) --env-file /dev/null config --format json \
		| $(PYTHON) scripts/service_env.py proxy $(RECORD_EXPECT) -- go -C backend-go run ./cmd/proxy -check-config)" \
		|| { printf '%s\n' "$$out"; echo "record-check-config: failed: the record stack is not configured as D123 says, or the gateway would refuse to start it (see above)"; exit 1; }; \
	echo "✅ The record stack's proxy configuration passes -check-config (test mode with real backends, strict config, caps \$$0.50 + \$$3 a day)"

# ---------- First run and golden path (D130) ----------
# Each starts a fresh stack (the onboarding stack's configuration: no admin, no key, every vendor on
# mock-upstream, so nothing costs anything) as their own project on their own ports, run one
# Playwright spec against it and remove it with its volumes (FIRST_RUN_KEEP=1 leaves it up; make
# first-run-down removes it).
#   FIRST_RUN_LOCAL=1     prebuilt images tagged FIRST_RUN_LOCAL_TAG, never rebuilt (FIRST_RUN_BUILD=1
#                         builds them under that tag first)
#   FIRST_RUN_TAG=<tag>   pulls ghcr.io/timkush1/truthtable-*:<tag> (release-images.yml: sha-<short>)
# first-run times the run from before `up` (T0): the pull (pull mode), every service healthy,
# ready (doctor --expect fresh passes) and the first flagged answer the spec sees. The spec writes
# FIRST_RUN_OUT (tmp/first-run.json), labelled measured, locally with prebuilt images or in CI with
# pulled ones, and fails past 5 minutes. golden-path walks from the setup link to the flagged
# trace's detail in at most 8 actions. first-run-onboarding runs the onboarding spec the same way.
# Each spec claims its stack's setup link and refuses a stack that already has an admin, so each
# target starts its own fresh stack; first-run-proof runs the three one after another (D165).
# The default ports keep clear of make up and make demo.
FIRST_RUN_PROJECT        ?= truthtable-first-run
FIRST_RUN_LOCAL          ?=
FIRST_RUN_LOCAL_TAG      ?= p5local
FIRST_RUN_TAG            ?=
FIRST_RUN_BUILD          ?=
FIRST_RUN_KEEP           ?=
FIRST_RUN_PROXY_PORT     ?= 28080
FIRST_RUN_DASHBOARD_PORT ?= 23000
FIRST_RUN_MOCK_PORT      ?= 28090
# tmp/ is scratch space the root .gitignore ignores (D143), so a local run leaves nothing to commit.
FIRST_RUN_OUT            ?= tmp/first-run.json
# local or pull; empty when neither or both were asked for.
FIRST_RUN_MODE = $(if $(FIRST_RUN_TAG),$(if $(filter 1,$(FIRST_RUN_LOCAL)),,pull),$(if $(filter 1,$(FIRST_RUN_LOCAL)),local,))
FIRST_RUN_IMAGE_TAG = $(if $(FIRST_RUN_TAG),$(FIRST_RUN_TAG),$(FIRST_RUN_LOCAL_TAG))
FIRST_RUN_ENV = TRUSTAGENT_VERSION=$(FIRST_RUN_IMAGE_TAG) PROXY_HOST_PORT=$(FIRST_RUN_PROXY_PORT) \
	DASHBOARD_HOST_PORT=$(FIRST_RUN_DASHBOARD_PORT) MOCK_UPSTREAM_HOST_PORT=$(FIRST_RUN_MOCK_PORT)
FIRST_RUN_COMPOSE = $(FIRST_RUN_ENV) $(COMPOSE) -p $(FIRST_RUN_PROJECT) -f docker-compose.yml \
	-f docker-compose.e2e.yml -f docker-compose.onboarding.yml --profile e2e
# Ready means doctor passes on a linked stack: right after `up --wait` the proxy has not always
# pinged the healthy engine yet (every TRUTHTABLE_ENGINE_HEALTH_INTERVAL, 10 s), and doctor's
# --wait lets engine.health re-read /health for at most 25 s rather than warn about start-up order.
FIRST_RUN_DOCTOR_WAIT ?= 25
NOW_MS = $(PYTHON) -c "import time; print(int(time.time() * 1000))"

# The shell every target here starts with: the mode check, .env, a clean slate, the images (checked,
# built or pulled), T0, `up --wait`, the setup link, and the removal on exit. It sets t0, pull,
# healthy and url.
define FIRST_RUN_UP
if [ -z "$(FIRST_RUN_MODE)" ]; then \
	echo "$@: set FIRST_RUN_LOCAL=1 (prebuilt images tagged $(FIRST_RUN_LOCAL_TAG)) or FIRST_RUN_TAG=<tag> (pulled), not both"; exit 2; \
fi; \
$(PYTHON) scripts/configure.py --ensure >/dev/null; \
$(FIRST_RUN_COMPOSE) down -v --remove-orphans >/dev/null 2>&1 || true; \
if [ "$(FIRST_RUN_KEEP)" != 1 ]; then trap '$(FIRST_RUN_COMPOSE) down -v --remove-orphans >/dev/null 2>&1' EXIT; fi; \
if [ "$(FIRST_RUN_MODE)" = local ]; then \
	if [ "$(FIRST_RUN_BUILD)" = 1 ]; then $(FIRST_RUN_COMPOSE) build; fi; \
	for svc in proxy engine dashboard; do \
		docker image inspect "ghcr.io/timkush1/truthtable-$$svc:$(FIRST_RUN_IMAGE_TAG)" >/dev/null 2>&1 \
			|| { echo "$@: no local image ghcr.io/timkush1/truthtable-$$svc:$(FIRST_RUN_IMAGE_TAG); build them once with FIRST_RUN_BUILD=1"; exit 2; }; \
	done; \
fi; \
t0=$$($(NOW_MS)); pull=null; \
if [ "$(FIRST_RUN_MODE)" = pull ]; then $(FIRST_RUN_COMPOSE) pull --quiet; pull=$$(( $$($(NOW_MS)) - t0 )); fi; \
$(FIRST_RUN_COMPOSE) up -d --no-build --pull never --wait --wait-timeout $(WAIT_TIMEOUT); \
healthy=$$(( $$($(NOW_MS)) - t0 )); \
url="$$($(FIRST_RUN_ENV) $(PYTHON) scripts/setup_url.py --url-only --project $(FIRST_RUN_PROJECT))"
endef

# The environment of the Playwright run against that stack: CI=1, because with make every
# variable is set, so a spec that would skip has found a real problem. The run goes in a subshell,
# so the removal on exit runs from the repository root, where the compose files are.
FIRST_RUN_PLAYWRIGHT_ENV = env -u TRUSTAGENT_ADMIN_KEY CI=1 TRUSTAGENT_SETUP_URL="$$url" \
	DASHBOARD_HOST_PORT=$(FIRST_RUN_DASHBOARD_PORT) MOCK_UPSTREAM_HOST_PORT=$(FIRST_RUN_MOCK_PORT)

##@ First run and golden path (fresh stacks on mock vendors, $0)

first-run: ## A fresh stack to its first flagged answer, timed (under 5 min): FIRST_RUN_LOCAL=1 (prebuilt images) or FIRST_RUN_TAG=<tag> (pulled); writes tmp/first-run.json
	@set -e; $(FIRST_RUN_UP); \
	$(FIRST_RUN_ENV) $(PYTHON) scripts/doctor.py --target docker --expect fresh --project $(FIRST_RUN_PROJECT) --no-probe \
		--wait $(FIRST_RUN_DOCTOR_WAIT) --proxy-url http://127.0.0.1:$(FIRST_RUN_PROXY_PORT) --dashboard-url http://127.0.0.1:$(FIRST_RUN_DASHBOARD_PORT); \
	ready=$$(( $$($(NOW_MS)) - t0 )); \
	(cd frontend-react && $(FIRST_RUN_PLAYWRIGHT_ENV) FIRST_RUN_T0=$$t0 FIRST_RUN_PULL_MS=$$pull \
		FIRST_RUN_HEALTHY_MS=$$healthy FIRST_RUN_READY_MS=$$ready FIRST_RUN_MODE=$(FIRST_RUN_MODE) \
		FIRST_RUN_IMAGES="ghcr.io/timkush1/truthtable-{proxy,engine,dashboard}:$(FIRST_RUN_IMAGE_TAG)" \
		FIRST_RUN_OUT="$(abspath $(FIRST_RUN_OUT))" npx playwright test --project=stack --workers=1 -g first-run)

golden-path: ## A fresh stack from its setup link to the flagged trace's detail in at most 8 clicks (D130, D162): FIRST_RUN_LOCAL=1 or FIRST_RUN_TAG=<tag>
	@set -e; $(FIRST_RUN_UP); \
	(cd frontend-react && $(FIRST_RUN_PLAYWRIGHT_ENV) npx playwright test --project=stack --workers=1 -g golden-path)

first-run-onboarding: ## A fresh stack through the onboarding spec, from its setup link to a checked answer on mock vendors (D130, D165): FIRST_RUN_LOCAL=1 or FIRST_RUN_TAG=<tag>
	@set -e; $(FIRST_RUN_UP); \
	(cd frontend-react && $(FIRST_RUN_PLAYWRIGHT_ENV) npx playwright test --project=stack --workers=1 -g onboarding)

first-run-proof: ## first-run, golden-path and first-run-onboarding one after another, each on its own fresh stack (D165): FIRST_RUN_LOCAL=1 or FIRST_RUN_TAG=<tag>
	@$(MAKE) --no-print-directory first-run
	@$(MAKE) --no-print-directory golden-path
	@$(MAKE) --no-print-directory first-run-onboarding

first-run-logs: ## Print the first-run / golden-path / onboarding stack's logs, setup tokens masked (FIRST_RUN_KEEP=1 runs)
	@$(FIRST_RUN_COMPOSE) logs --no-color --timestamps 2>&1 | sed -E 's/(token=)[A-Za-z0-9_-]+/\1[masked]/g'

first-run-down: ## Remove the first-run / golden-path / onboarding stack and its volumes
	$(FIRST_RUN_COMPOSE) down -v --remove-orphans

# ---------- Workflow lint (D140) ----------
# actionlint checks the workflow syntax, expressions and (through shellcheck) every run: script;
# zizmor audits them for security (unpinned actions, persisted credentials, template injection,
# excessive permissions, cache poisoning) and the Dependabot settings. Every version is pinned:
# actionlint runs with `go run` at its tag (Go's checksum database verifies the module), zizmor and
# shellcheck come from PyPI at an exact version into a venv under TOOLS_DIR. zizmor runs offline
# (no token, the same answer everywhere) and fails on high-severity findings; an accepted finding
# carries an inline `# zizmor: ignore[<audit>]` with its reason.
ACTIONLINT_VERSION    := 1.7.12
ZIZMOR_VERSION        := 1.30.1
SHELLCHECK_PY_VERSION := 0.11.0.1
WORKFLOW_LINT_VENV = $(TOOLS_DIR)/workflow-lint-$(ZIZMOR_VERSION)-$(SHELLCHECK_PY_VERSION)
ifeq ($(OS),Windows_NT)
WORKFLOW_LINT_BIN = $(WORKFLOW_LINT_VENV)/Scripts
else
WORKFLOW_LINT_BIN = $(WORKFLOW_LINT_VENV)/bin
endif

##@ Workflows

# actionlint is built with the repository's own Go toolchain (backend-go/go.mod), so an older local
# go does not fetch whichever release is newest just to build a linter.
ACTIONLINT_GOTOOLCHAIN ?= $(shell sed -n 's/^toolchain //p' backend-go/go.mod)

lint-workflows: workflow-lint-install ## Lint .github/: actionlint (with shellcheck) and zizmor (high-severity findings fail), all pinned
	GOTOOLCHAIN=$(ACTIONLINT_GOTOOLCHAIN)+auto go run github.com/rhysd/actionlint/cmd/actionlint@v$(ACTIONLINT_VERSION) -shellcheck=$(WORKFLOW_LINT_BIN)/shellcheck
	$(WORKFLOW_LINT_BIN)/zizmor --offline --min-severity high --format plain .
	@echo "✅ Workflows: actionlint $(ACTIONLINT_VERSION) and zizmor $(ZIZMOR_VERSION) found nothing to fix"

workflow-lint-install: ## Install the pinned zizmor and shellcheck into tmp/tools (a venv; actionlint needs only Go)
	@set -e; test -x "$(WORKFLOW_LINT_BIN)/zizmor" && test -x "$(WORKFLOW_LINT_BIN)/shellcheck" && exit 0; \
	mkdir -p "$(TOOLS_DIR)"; \
	$(PYTHON) -m venv "$(WORKFLOW_LINT_VENV)"; \
	"$(WORKFLOW_LINT_BIN)/python" -m pip install --quiet --disable-pip-version-check \
		"zizmor==$(ZIZMOR_VERSION)" "shellcheck-py==$(SHELLCHECK_PY_VERSION)"; \
	echo "✅ zizmor $(ZIZMOR_VERSION) and shellcheck (shellcheck-py $(SHELLCHECK_PY_VERSION)) in $(WORKFLOW_LINT_VENV)"

# ---------- Aggregate hooks (see the root Makefile) ----------
check: check-version
check: record-check-config
