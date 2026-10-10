# ============================================================================
# mk/launch.mk - Launch kit: claims check, OG card, hero GIF and screenshots.
# Owned by the DX-DOCS stream (D132). Included by the root Makefile;
# targets are declared .PHONY there.
# ============================================================================

LAUNCH_MK_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
LAUNCH_ROOT := $(abspath $(LAUNCH_MK_DIR)..)
NODE ?= node

##@ Launch kit

check-claims: ## Check every <!-- claim:ID --> figure in README.md and docs/ against contracts/claims (self-test first)
	$(NODE) "$(LAUNCH_ROOT)/scripts/check-claims.mjs" --self-test
	$(NODE) "$(LAUNCH_ROOT)/scripts/check-claims.mjs"

claims-write: ## Regenerate the <!-- measured:start/end --> tables from contracts/claims/measured.json, then check
	$(NODE) "$(LAUNCH_ROOT)/scripts/check-claims.mjs" --write

check-claims-launch: ## The launch gate: check-claims plus a recorded dataset, a measured run, a reviewed comparison and og.png
	$(NODE) "$(LAUNCH_ROOT)/scripts/check-claims.mjs" --launch
