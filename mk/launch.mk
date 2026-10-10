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

# ---------- Capture tooling (D115) ----------
# Each capture builds the launch site (`vite build --mode launch`: the Pages site plus the /og
# route, never published) and runs one spec of the Playwright `launch` project against it
# (frontend-react/e2e/launch/). The social card and the hero refuse the simulated tape:
# OG_ALLOW_MOCK=1 / HERO_ALLOW_MOCK=1 make scratch copies labelled Simulated, only under
# frontend-react/test-results/launch/. The screenshots may use the simulated tape (each page shows
# its Simulated banner). Captures run on the static build, so they change nothing anywhere (CO-20).
LAUNCH_WEB := $(LAUNCH_ROOT)/frontend-react

og: ## Capture the social card (/og, 1280x640, under 1 MB) into frontend-react/public/og.png and docs/launch/og.png; refuses the simulated tape (OG_ALLOW_MOCK=1: a scratch copy)
	cd "$(LAUNCH_WEB)" && npm run og

hero-gif: ## Record the README hero (npm run hero), then make the GIF (1200 px, 12 fps, at most 8 MB: docs/images/v2/hero.gif) and the release MP4 (tmp/launch/hero.mp4); refuses the simulated tape (HERO_ALLOW_MOCK=1: scratch copies)
	cd "$(LAUNCH_WEB)" && npm run hero
	$(PYTHON) "$(LAUNCH_ROOT)/scripts/make_hero_gif.py"

screenshots: ## Capture the dashboard pages in both themes into docs/images/v2/{page}-{light,dark}.png (the simulated tape is allowed and shows its Simulated label)
	cd "$(LAUNCH_WEB)" && npm run screenshots

test-launch-tools: ## Unit tests of the capture tooling (make_hero_gif.py) and the list of launch specs (no browser, no build)
	cd "$(LAUNCH_ROOT)" && $(PYTHON) -m unittest scripts/tests/test_make_hero_gif.py
	cd "$(LAUNCH_WEB)" && npx playwright test --list --project=launch
