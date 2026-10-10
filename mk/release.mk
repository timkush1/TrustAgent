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

# Downloaded tools live in tmp/tools (a tmp/.gitignore of "*" keeps them out of git); TOOLS_DIR may
# point elsewhere, and then no .gitignore is written.
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

pages-size: ## Pages step 3: the landing page's size budget over the built site
	cd frontend-react && npm run size

# The site's shape: the landing page at the root, the app under app/, 404.html a copy of the app
# (GitHub Pages serves it for every path with no file, and it boots the router); every page loads
# its assets under the base path; the dataset in demo/ is byte for byte the one that was checked;
# no source maps.
pages-files: ## Pages step 4: file checks over the built site (pages, 404 fallback, base path, shipped dataset, no source maps)
	@set -e; d="$(PAGES_DIST)"; fail() { echo "pages-files: $$1"; exit 1; }; \
	for f in index.html app/index.html 404.html demo/manifest.json; do \
		test -s "$$d/$$f" || fail "$$d/$$f is missing (run make pages-static first)"; done; \
	cmp -s "$$d/404.html" "$$d/app/index.html" || fail "404.html is not a copy of app/index.html"; \
	for f in index.html app/index.html; do \
		grep -q '="$(PAGES_BASE_PATH)assets/' "$$d/$$f" || fail "$$f loads no asset under $(PAGES_BASE_PATH)"; \
		if grep -Eq '(src|href)="/(assets|app|demo)/' "$$d/$$f"; then fail "$$f loads an asset outside $(PAGES_BASE_PATH)"; fi; \
	done; \
	diff -r -q "$(PAGES_RECORDING_DIR)" "$$d/demo" >/dev/null \
		|| fail "$$d/demo is not the checked dataset $(PAGES_RECORDING_DIR): $$(diff -r -q '$(PAGES_RECORDING_DIR)' "$$d/demo" | head -3)"; \
	maps="$$(find "$$d" -name '*.map' | head -3)"; test -z "$$maps" || fail "source maps in the site: $$maps"; \
	echo "✅ Pages files: landing, app, 404 fallback, base path $(PAGES_BASE_PATH), dataset $(PAGES_RECORDING_DIR), no source maps"

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
	mkdir -p "$(TOOLS_DIR)"; case "$(TOOLS_DIR)" in tmp/*) test -f tmp/.gitignore || printf '*\n' > tmp/.gitignore;; esac; \
	work="$$(mktemp -d)"; trap 'rm -rf "$$work"' EXIT; \
	curl -fsSLo "$$work/gitleaks.tgz" "https://github.com/gitleaks/gitleaks/releases/download/v$(GITLEAKS_VERSION)/gitleaks_$(GITLEAKS_VERSION)_$${os}_$${arch}.tar.gz"; \
	got="$$($(PYTHON) -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$$work/gitleaks.tgz")"; \
	test "$$got" = "$$sum" || { echo "gitleaks-install: checksum mismatch ($$got), refusing the download"; exit 1; }; \
	tar -xzf "$$work/gitleaks.tgz" -C "$$work" gitleaks; \
	install -m 0755 "$$work/gitleaks" "$(GITLEAKS)"; \
	echo "✅ gitleaks $(GITLEAKS_VERSION) ($$os-$$arch, SHA-256 checked) in $(GITLEAKS)"

# ---------- Aggregate hooks (see the root Makefile) ----------
check: check-version
