# Launch checklist

Nothing is pushed, deployed, published or posted until the boxes before it are ticked. The list
follows the owner's sittings of the [P6 plan](../v2/P6-PLAN.md) in order (S1, Gate A, S2, the
release candidate, S3a, S3b, S3c); each item names the command or page that proves it. The plain
step-by-step guides for each sitting are in the plan's §10, and the money rules (one paid card at a
time, a dry run first, the key limit) in its §0.6.

**Two rules override everything else.**

1. **No push of anything until the terms check is answered** ([terms-check.md](terms-check.md)):
   pushing to the public repository is publishing. Mode W stops before the first push.
2. **No post while the site would replay a simulated tape** (mode A). A simulated tape is honest
   on the site (it says Simulated everywhere), but a launch post invites people to judge real model
   output. In mode R the post is the mode-R variant and names no fast-check figure.

## S1: terms and one-time setup (no spending, no code needed)

- [ ] **G0 terms check.** Every page in [terms-check.md](terms-check.md) read and searched; the
      quoted clauses (or "none found") and the date written there; the mode chosen (A, W or R).
- [ ] **The email** (courtesy note in mode A, permission request in mode W) is sent; its date is in
      terms-check.md. The drafts are kept outside the repository.
- [ ] **The legal name** replaces `{{OWNER_LEGAL_NAME}}` in `LICENSE`, `frontend-react/package.json`
      (author) and `backend-python/pyproject.toml` (authors). `make check-claims-launch` and
      `make release-notes` refuse the placeholder.
- [ ] **Other sites on the same origin.** No other `https://timkush1.github.io/<name>` site loads
      third-party scripts (they would share an origin with the demo and could read a visitor's key
      from `sessionStorage`); otherwise the demo moves to its own domain first.
- [ ] **Choices recorded:** the "open to roles" line (`make linkedin-post ROLES=1`), the interview
      talking points public or private, the launch day, the audience.
- [ ] **OpenRouter account** ready, no credit yet; the privacy settings let Jev's provider
      (TypeSafe) be reached (plan §10, G1 step 4).
- [ ] **Push guard** in the main checkout: `git config remote.origin.pushurl DISABLED` and
      `git config credential.useHttpPath true`.

## Gate A: ready to spend (the lead, before S2)

- [ ] Every P6a stream merged and its gate green; `make check` passes on the integration branch.
- [ ] **Version bump before the recording**: `make version-bump V=2.0.0`, committed as
      `chore(release): 2.0.0`, so the tape is stamped 2.0.0.
- [ ] **Rehearsals on mocks**: a bench run through mock-upstream comes out simulated and publish
      refuses it; `make bench-lite` without a key prints its plan and spends nothing;
      `make release-notes TAG=v0.0.0-rehearsal RELEASE_NOTES_INSPECT=scripts/tests/fixtures/inspect.json`
      prints rehearsal notes.
- [ ] **Hygiene**: `make hygiene-check` prints nothing, and `make gitleaks-history` finds no leak.

## S2: the measured run and the recording (owner in the loop)

- [ ] **G2 localhost Connect check** before any credit, recorded in SPEC Appendix D (accepted or
      refused), and the key it made deleted.
- [ ] **G3 credit and the measurement key** with its fixed limit, entered with
      `make configure WITH_SECRETS=1`, never pasted into a chat.
- [ ] **B3 to B8**, one approved card at a time, each ending with its `TRUSTAGENT_P6_RESULT` line:
      `make jev-check`, `make jev-capture`, `make bench-smoke`, `make calib-pass` and the free
      `make calibrate` fits, `make bench-lite` (with the held-out ECE), `make record-demo` (drift
      reviewed, the featured trace accepted).
- [ ] **G5 comparison review**, while the bench runs: every cell of
      `contracts/claims/compare.v1.json` read with its quoted source sentence, then accepted,
      changed or dropped; `"reviewed": true` on each accepted cell.
- [ ] **G6 Gate B**: the owner has read every figure with its label, n, interval and snapshot, the
      featured trace and the filled opening lines of the post, and answered "publish". Nothing is
      re-run to improve a number.
- [ ] **G7 close the key**: deleted (or limited to its usage plus a little) and removed from `.env`.

## Release candidate (the lead, after "publish")

- [ ] **C1 publish**: `make bench-publish RUN=…` (it refuses mock output and non-vendor hosts);
      the run, `measured.json`, the calibration file, the captured fixtures and the recorded tape
      are committed (mode R: the subset in terms-check.md only).
      `python3 scripts/check_recording.py frontend-react/demo-data/recorded --require-recorded`
      passes.
- [ ] **C2 fill and look**: `make claims-write`; `make og`; `make hero-gif`; `make screenshots`;
      `make launch-video`; `make linkedin-post`. The owner opens the card, the GIF, the video and
      the post text and says "looks good" (or names a change).
- [ ] **C3 records**: the dated SPEC Appendix D rows (PKCE, vendor shapes, prices, licences,
      market signals), Appendix H, the JEV-ACCESS PKCE status, [terms-check.md](terms-check.md),
      the licence name.
- [ ] **C4 checks**, each green on a clean tree: the CHANGELOG heading dated with the planned tag
      day; `make check-version RELEASE_TAG=v2.0.0`; the phase grep; `make check`;
      `make check-claims-launch`; `make pages-build PAGES_DATASET=recorded`;
      `make gitleaks-history`; `make hygiene-check`;
      `npm audit --omit=dev --audit-level=high` in `frontend-react`; the local `v1.0.0` tag on
      `d709df3`.
- [ ] **The re-verification is current**: every comparison cell's source re-read and dated, no
      claim with an open `reverify`, the market lines in
      [interview-talking-points.md](interview-talking-points.md) dated or deleted.

## S3a: first push and the pull request

- [ ] **G8 token**: a fine-grained token for this repository only, stored for its path only;
      `git push --dry-run https://github.com/timkush1/TrustAgent.git feat/v2-jev-gateway` passes.
- [ ] **G9 settings**: merge commits allowed; Pages source GitHub Actions; the `github-pages`
      environment allows `main` and the tag rule `v*`; the minimal `main` ruleset (no deletion,
      no force push, a pull request required) from [ruleset.md](ruleset.md).
- [ ] **"go push"**: the `v1.0.0` tag and the branch, to the explicit URL.
- [ ] **The PR** `feat/v2-jev-gateway` → `main`, with the body from
      `make clip FILE=docs/launch/release-pr.md`.
- [ ] **CI loop**: every required check green, `pages.yml` and `release-images.yml` green as
      build-only runs.

## S3b: merge, packages and the tag

- [ ] **G10**: the Actions variable `PAGES_ENABLED` = `true` (mode R also
      `PAGES_TAG_DATASET` = `simulated`); the PR merged with **Create a merge commit**; the three
      GHCR packages (`truthtable-proxy`, `-engine`, `-dashboard`) made public; `GHCR_PUBLIC` =
      `true`.
- [ ] **G11 Pages Connect check** on `https://timkush1.github.io/TrustAgent/app/playground/#live`:
      the result recorded in SPEC Appendix D; if OpenRouter refused the callback, the Paste-first
      fix is merged before the tag; the key it made deleted.
- [ ] **"go push" for `v2.0.0`**, after `python3 scripts/check_version.py --tag v2.0.0` on the
      merge commit. The tag run deploys Pages from the tag, pushes the semver images, and runs
      `first-run` and `anonymous-pull`.
- [ ] **Images**: `docker buildx imagetools inspect ghcr.io/timkush1/truthtable-proxy:2.0.0` lists
      `linux/amd64` and `linux/arm64`, and
      `gh attestation verify oci://ghcr.io/timkush1/truthtable-proxy:2.0.0 -R timkush1/TrustAgent`
      passes, for each image.

## S3c: the release page, then the post

- [ ] **G12 the Release**: tag `v2.0.0`, title "TrustAgent 2.0.0 — Jev gateway", the notes from
      `make release-notes TAG=v2.0.0` (it refuses a missing digest), the MP4s attached, set as
      **Latest**; the release badge shows v2.0.0.
- [ ] **About and presentation**: the description, website and 16 topics from
      [github-metadata.md](github-metadata.md); the social preview `docs/launch/og.png`.
- [ ] **The ruleset's required checks**, typed from [ruleset.md](ruleset.md).
- [ ] **Code security**: private vulnerability reporting on (the [security policy](../SECURITY.md#reporting),
      the code of conduct and the issue forms point at it), Dependabot alerts on.
- [ ] **Housekeeping**: the stale Dependabot PRs closed as superseded, their branches deleted, the
      repository pinned on the profile.
- [ ] **G13 the stranger check**: the site in a private window and on a phone at 400 px, in both
      themes: the banner says Recorded (mode A), the first-visit guide reaches the caught answer, a
      live check with a small-limit key works and Forget key clears it, the network panel shows only
      the site and openrouter.ai, and every README badge and link opens.
- [ ] **Post Inspector** shows the card from `og.png` for the site URL.
- [ ] **The post**: `make linkedin-post CLIP=1` (it exits 1 on any missing value; nothing is typed
      by hand), read through: every figure has its label, the agreement stands beside the
      like-for-like third-party figure or is left out. Posted with the 4:5 video as its media,
      the Haqq link as the first comment ([linkedin-post.md](linkedin-post.md)).
- [ ] **Afterwards**: the push token deleted or its expiry noted; every measurement key deleted or
      limited to its usage plus a little.
