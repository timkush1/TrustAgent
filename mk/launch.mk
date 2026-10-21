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

test-launch-tools: ## Unit tests of the capture tooling (make_hero_gif.py, launch_video.py) and the list of launch specs (no browser, no build)
	cd "$(LAUNCH_ROOT)" && $(PYTHON) -m unittest scripts/tests/test_make_hero_gif.py scripts/tests/test_launch_video.py
	cd "$(LAUNCH_WEB)" && npx playwright test --list --project=launch

# ---------- Launch post and video (D210, D213; seams the SHIP and VEND streams fill in) ----------
# scripts/linkedin_post.py fills the template in docs/launch/linkedin-post.md and exits 1 on any
# missing value; scripts/clip.py pipes through iconv -f UTF-8 -t UTF-16LE into clip.exe (plain
# clip.exe garbles × and dashes) and prints to stdout where there is no clip.exe.
CLIP ?=
FILE ?=
ROLES ?=
RESTRICTED ?=
PAGES_ONLY ?=

linkedin-post: ## Print the LinkedIn post as plain text from measured.json, the recorded featured trace and the claims (CLIP=1 copies it, ROLES=1 adds the roles line, RESTRICTED=1 the mode-R post)
	@$(PYTHON) "$(LAUNCH_ROOT)/scripts/linkedin_post.py" $(if $(CLIP),--clip) $(if $(ROLES),--roles) $(if $(RESTRICTED),--restricted)

clip: ## Copy a file to the Windows clipboard as UTF-16LE: make clip FILE=... (stdout where there is no clip.exe)
	@if [ -z "$(FILE)" ]; then echo "clip: set FILE, e.g. make clip FILE=tmp/release-notes.md" >&2; exit 2; fi
	@$(PYTHON) "$(LAUNCH_ROOT)/scripts/clip.py" "$(FILE)"

test-linkedin-post: ## Unit tests of make linkedin-post and make clip (no clipboard, no network)
	cd "$(LAUNCH_ROOT)" && $(PYTHON) -m unittest scripts/tests/test_linkedin_post.py

# The owner's comparison review (G5) and the reverify claims (D113, D212): every source page is
# read again with a plain GET (no key) and each cell is printed with the sentence that supports it.
# The fetch log and the proposed contract edits go to tmp/compare-review/ (FETCH=0 re-prints them
# from the pages saved there); contracts/ is never edited by it.
FETCH ?= 1

compare-review: ## Re-read every comparison source and reverify claim; print the G5 table with each source's own words (FETCH=0: from the saved pages)
	@$(PYTHON) "$(LAUNCH_ROOT)/scripts/compare_review.py" $(if $(filter 0,$(FETCH)),,--fetch)

launch-video: ## The 60-second launch video (D210): the static beats from the recorded Pages build, then ffmpeg joins them with the stack beats and burned-in captions into tmp/launch/video-{16x9,4x5}.mp4 (PAGES_ONLY=1: the H-6 45 s take; VIDEO_ALLOW_MOCK=1: a rehearsal in tmp/launch/rehearsal/)
	$(PYTHON) "$(LAUNCH_ROOT)/scripts/launch_video.py" static
	$(PYTHON) "$(LAUNCH_ROOT)/scripts/launch_video.py" assemble $(if $(filter 1,$(PAGES_ONLY)),--pages-only)

launch-video-stack: ## The launch video's stack beats on a reviewed attempt's record stack: make launch-video-stack ATTEMPT=<id> (make record-demo ACCEPT=1 VIDEO=1 runs it; its calls are in the record ledger)
	@if [ -z "$(ATTEMPT)" ]; then echo "launch-video-stack: set ATTEMPT=<id> (make record-demo CONFIRM=1 printed it)"; exit 2; fi
	$(PYTHON) "$(LAUNCH_ROOT)/scripts/launch_video.py" stack --attempt "$(ATTEMPT)"

# ---------- Aggregate hook (see the root Makefile; P5 I-2) ----------
check: check-claims
