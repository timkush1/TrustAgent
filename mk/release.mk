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

# ---------- Aggregate hooks (see the root Makefile) ----------
check: check-version
