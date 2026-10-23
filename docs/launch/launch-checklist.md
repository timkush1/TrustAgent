# Launch checklist

Nothing is deployed, published or posted until the boxes before it are ticked. The list follows
the owner's sittings of the [P6 plan](../v2/P6-PLAN.md) in order (S1, Gate A, S2, the release
candidate, S3a, S3b, S3c); each item names the command or page that proves it. The plain
step-by-step guides for each sitting are in the plan's §10 (with a table of what runs on the
laptop, in the Claude cloud session and in GitHub's web UI), and the money rules (one paid card at a
time, a dry run first, the key limit) in its §0.6.

`main` has been public with v2 phases P0 to P5 since 2026-10-21, by the owner's decision (D220).
Claude pushes branches; `main` moves only through pull requests the owner merges on GitHub. The
paid runs and the release steps run from a Claude cloud session (D221).

**Two rules override everything else.**

1. **Nothing Jev-derived is pushed until the terms check says mode A** ([terms-check.md](terms-check.md)):
   no measured Jev figure, recorded Jev tape or captured Jev response on any pushed branch, since
   every branch is public (D222). Vendor and third-party figures quoted from public pages, with
   their labels, are not measurements. Mode W stops the paid Jev steps.
2. **In mode A, no post while the site would replay a simulated tape.** A simulated tape is honest
   on the site (it says Simulated everywhere), but a launch post invites people to judge real model
   output. In mode R the site replays the simulated tape by design, and the post is the mode-R
   variant, which names no fast-check figure and no recorded answer.

## S1: terms and one-time setup (no spending, no code needed)

- [ ] **G0 terms check.** Every page in [terms-check.md](terms-check.md) read and searched; the
      quoted clauses (or "none found") and the date written there; the mode chosen (A, W or R).
      Mode A only if the Master Customer Agreement itself was read: an agreement that could not
      be found or opened counts as unsure, mode W (D231).
- [ ] **The email** (courtesy note in mode A, permission request in mode W) is sent; its date is in
      terms-check.md. The drafts are kept outside the repository.
- [ ] **The legal name** replaces `{{OWNER_LEGAL_NAME}}` in `LICENSE`, `frontend-react/package.json`
      (author) and `backend-python/pyproject.toml` (authors). `make check-claims-launch` and
      `make release-notes` refuse the placeholder.
- [ ] **Other sites on the same origin.** No other `https://timkush1.github.io/<name>` site loads
      third-party scripts (they would share an origin with the demo and could read a visitor's key
      from `sessionStorage`); otherwise the demo moves to its own domain first.
- [ ] **Choices recorded:** the "open to roles" line (`make linkedin-post ROLES=1`), the interview
      talking points public or private, the "built with AI coding agents" wording (D227), the
      launch day, the audience.
- [ ] **OpenRouter account** ready, no credit yet; the privacy settings let Jev's provider
      (TypeSafe) be reached (plan §10, G1 step 4).
- [ ] **The cloud environment** (D221): network access keeps the default trusted list and adds
      `openrouter.ai` (a custom list also needs GitHub, the package registries, `ghcr.io`, Docker
      Hub and the OS package mirrors; plan §10, G1 step 5); the setup script installs `ffmpeg`;
      no key yet.

## Gate A: ready to spend (the lead, before S2)

- [ ] Every P6a stream merged and its gate green; `make check` passes on the integration branch.
- [ ] **Version bump before the recording**: `make version-bump V=2.0.0`, committed as
      `chore(release): 2.0.0`, so the tape is stamped 2.0.0.
- [ ] **Rehearsals on mocks**: a bench run through mock-upstream comes out simulated and publish
      refuses it; `make bench-lite` without a key prints its plan and spends nothing;
      `make release-notes TAG=v0.0.0-rehearsal RELEASE_NOTES_INSPECT=scripts/tests/fixtures/inspect.json`
      prints rehearsal notes.
- [ ] **Hygiene**: `make hygiene-check` prints nothing, and `make gitleaks-history` finds no leak.
- [ ] **Containment**: no worktree `.env` on the laptop holds a key; the key goes into the cloud
      environment's variables only.
- [ ] **Handoff**: `docs/v2/STATUS.md` rewritten for the cloud session; int7 merged into
      `feat/v2-jev-gateway` and both pushed.

## S2: the measured run and the recording (owner in the loop)

- [ ] **G2 localhost Connect check** (on the laptop, optional) before any credit, recorded in SPEC
      Appendix D (accepted, refused or skipped), and the key it made deleted.
- [ ] **G3 credit and the measurement key** with its fixed limit, entered as the cloud
      environment variable `OPENROUTER_API_KEY` (on the laptop, `make configure WITH_SECRETS=1`),
      never pasted into a chat.
- [ ] **B3 to B8**, one approved card at a time, each ending with its `TRUSTAGENT_P6_RESULT` line
      (except `make jev-check`, whose single probe reports its snapshot and limit lines instead,
      D232): `make jev-check`, `make jev-capture`, `make bench-smoke`, `make
      calib-pass` and the free `make calibrate` fits, `make bench-lite` with `BENCH_REGION` set
      (then `make calib-holdout` and the check-only publish with `HOLDOUT=`), `make record-demo`
      (drift reviewed, the featured trace accepted).
- [ ] **G5 comparison review**, while the bench runs: every cell of
      `contracts/claims/compare.v1.json` read with its quoted source sentence, then accepted,
      changed or dropped; `"reviewed": true` on each accepted cell.
- [ ] **G6 Gate B**: the owner has read every figure with its label, n, interval and snapshot, the
      featured trace and the filled opening lines of the post, and answered "publish". Nothing is
      re-run to improve a number.
- [ ] **G7 close the key**: deleted (or limited to its usage plus a little) and removed from the
      cloud environment's variables (and any `.env`).

## Release candidate (the lead, after "publish")

- [ ] **C1 publish**: `make bench-publish RUN=<abs run.json> HOLDOUT=<abs path to the
      holdout-groundedness.json that make calib-holdout wrote beside it>` (it refuses mock output,
      non-vendor hosts, and a usable held-out check left out, so the ECE before and after is
      published); the run, `measured.json`, the calibration file, the captured fixtures and the recorded tape
      are committed (mode R: the subset in terms-check.md only).
      `python3 scripts/check_recording.py frontend-react/demo-data/recorded --require-recorded`
      passes.
- [ ] **C2 fill and look**: `make claims-write`; `make og`; `make hero-gif`; `make screenshots`;
      `make launch-video`; `make linkedin-post`; the README Roadmap and Known limits brought up to
      date. The session sends the card, the GIF, the video and the post text; the owner opens them
      and says "looks good" (or names a change).
- [ ] **C3 records**: the dated SPEC Appendix D rows (PKCE, vendor shapes, prices, licences,
      market signals), Appendix H, the JEV-ACCESS PKCE status, [terms-check.md](terms-check.md),
      the licence name.
- [ ] **C4 checks**, each green on a clean tree: the CHANGELOG heading dated with the planned tag
      day; `make check-version RELEASE_TAG=v2.0.0`; the phase grep; `make check`;
      `make check-claims-launch`; `make pages-build PAGES_DATASET=recorded`;
      `make gitleaks-history`; `make hygiene-check`;
      `npm audit --omit=dev --audit-level=high` in `frontend-react`. Optional, on the laptop: the
      `v1.0.0` tag on `d709df3`, pushed with the owner's OK (the cloud session cannot push tags).
- [ ] **The re-verification is current**: every comparison cell's source re-read and dated, no
      claim with an open `reverify`, the market lines in
      [interview-talking-points.md](interview-talking-points.md) dated or deleted.

## S3a: settings and the pull request

- [ ] **G9 settings**: merge commits allowed; Pages source GitHub Actions; the `github-pages`
      environment allows `main` and the tag rule `v*`; the minimal `main` ruleset (no deletion,
      no force push, a pull request required, the admin bypass for pull requests only) from
      [ruleset.md](ruleset.md). These can be set any time before.
- [ ] **The branch is pushed** by Claude (D220): the P6b work merged into `feat/v2-jev-gateway`.
- [ ] **The PR** `feat/v2-jev-gateway` → `main`, with the body of
      [release-pr.md](release-pr.md); the owner merges it, Claude never does.
- [ ] **CI loop**: every required check green, `pages.yml` and `release-images.yml` green as
      build-only runs.

## S3b: merge, packages and the tag

- [ ] **G10**: the Actions variable `PAGES_ENABLED` = `true` (mode R also
      `PAGES_TAG_DATASET` = `simulated`); the PR merged with **Create a merge commit**; the three
      GHCR packages (`truthtable-proxy`, `-engine`, `-dashboard`), public since 2026-10-21 (D224),
      still public; `GHCR_PUBLIC` = `true`.
- [ ] **G11 Pages Connect check** on `https://timkush1.github.io/TrustAgent/app/playground/#live`:
      a small credit limit set on OpenRouter's authorize page if it offers one; one example run,
      with the browser's network panel showing requests only to the site and openrouter.ai; Forget
      key clears it; the result recorded in SPEC Appendix D; if OpenRouter refused the callback,
      the Paste-first fix is merged before the tag; the key it made deleted right after.
- [ ] **The tag**: `python3 scripts/check_version.py --tag v2.0.0` passes on the merge commit; the
      owner publishes the release `v2.0.0` as a **pre-release** in the web UI, which creates the
      tag on `main` (D221). The tag run deploys Pages from the tag, pushes the semver images, and
      runs `first-run` and `anonymous-pull`.
- [ ] **Images**: `docker buildx imagetools inspect ghcr.io/timkush1/truthtable-proxy:2.0.0` lists
      `linux/amd64` and `linux/arm64`, and
      `gh attestation verify oci://ghcr.io/timkush1/truthtable-proxy:2.0.0 -R timkush1/TrustAgent`
      passes, for each image.

## S3c: the release page, then the post

- [ ] **G12 the Release**: the `v2.0.0` pre-release edited: the notes from
      `make release-notes TAG=v2.0.0` (it refuses a missing digest), the MP4s attached, pre-release
      unticked and set as **Latest**; the release badge shows v2.0.0.
- [ ] **About and presentation**: the description, website and 16 topics from
      [github-metadata.md](github-metadata.md); the social preview `docs/launch/og.png`.
- [ ] **The ruleset's required checks**, typed from [ruleset.md](ruleset.md).
- [ ] **Code security**: private vulnerability reporting on (the [security policy](../SECURITY.md#reporting),
      the code of conduct and the issue forms point at it), Dependabot alerts on.
- [ ] **Housekeeping**: the stale Dependabot PRs (#21, #23, #24, #28, #30) closed as superseded;
      the stale branches deleted (`copilot/check-insecurities`, every `dependabot/*`,
      `fix/deps-security` once PR #45 is closed or merged); the repository pinned on the profile.
- [ ] **G13 the stranger check**: the site in a private window and on a phone at 400 px, in both
      themes: the banner says Recorded (mode A), the first-visit guide reaches the caught answer,
      and every README badge and link opens (the live check with a key is G11's).
- [ ] **Post Inspector** shows the card from `og.png` for the site URL.
- [ ] **Mode A: the results went to TypeSafe** at least 3 days before the post, as the courtesy
      note said they would (the measured figures, the link to the published run and the post's
      text); the date is in [terms-check.md](terms-check.md). Tag TypeSafe only if they said yes.
- [ ] **The post**: `make linkedin-post` (with `ROLES=1` if the owner chose the roles line,
      `RESTRICTED=1` in mode R; `CLIP=1` on the laptop). It exits 1 on any missing value and
      refuses a post over 2,800 characters or a first line that would hide behind "…see more";
      nothing is typed by hand. Read
      through: every figure has its label, the cost and the latency say what they were measured
      on, the example says where its answer came from, Jev is credited to TypeSafe AI, the ratio
      names the judge, the agreement names the thresholds the bench flags at and stands beside
      Arize's two figures, said not to be like for like (D233), or is left out, and the "built with AI coding agents" line is the owner's. Posted with the 4:5
      video as its media, the Haqq link as the first comment ([linkedin-post.md](linkedin-post.md)).
- [ ] **Afterwards**: every measurement key deleted or limited to its usage plus a little, and gone
      from the cloud environment; the laptop's push token kept or deleted, as the owner chooses.
