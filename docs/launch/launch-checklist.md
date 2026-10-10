# Launch checklist

Nothing is posted, deployed or published until every box below is ticked. The owner runs this
list (the measurements need the owner's OpenRouter key), top to bottom; each item names the
command or page that proves it. The steps match the owner actions in
[P5-PLAN §21](../v2/P5-PLAN.md#21-owner-actions-step-by-step) and the SPEC's measurement and
launch phase.

**The rule that overrides everything else: do not post while the site would replay a simulated
recording.** A simulated tape is honest on the site (it says "Simulated" everywhere), but a launch
post invites people to judge real model output.

## 1. Real material, not simulated

- [ ] **Measure.** With the key in `.env`: `make bench-lite` (it stops at its hard spending cap),
      then `make calibrate WRITE=1` on the pinned Jev snapshot.
- [ ] **Record the demo.** `make record-demo` prints the plan and its estimate; run it again with
      `CONFIRM=1`. Then
      `python3 scripts/check_recording.py --require-recorded frontend-react/demo-data/recorded`
      passes and its manifest says `"kind": "recorded"`.
- [ ] **Capture the vendor fixtures.**
      `go -C backend-go run ./cmd/jevcheck --capture contracts/fixtures/vendor/captured` records
      real vendor answers for the SPEC Appendix D vendor rows; until then the vendor fixtures are
      synthetic and say so.
- [ ] **Publish the measurements.** `make bench-publish` (it refuses mock output) fills
      `contracts/claims/measured.json` with `"status": "measured"` and `"mock": false`.
- [ ] **Refresh every measured table.** `make claims-write`, then commit the regenerated
      `<!-- measured:start/end -->` blocks (README, these launch drafts).
- [ ] **Capture from the recording.** Build with the recorded dataset, then:
      `make og` (the card, refused on a simulated tape: `docs/launch/og.png` and
      `frontend-react/public/og.png`, 1280×640, under 1 MB); `make hero-gif` (refused on a
      simulated tape: `docs/images/v2/hero.gif`, 1200 px, 12 fps, at most 8 MB, for the README's
      hero slot, and `tmp/launch/hero.mp4` for the release); `make screenshots`
      (`docs/images/v2/{page}-{light,dark}.png` for the README's screenshot block, captioned with
      the tape's label). Build the site once more afterwards, so the landing page links `og.png`.

## 2. Every claim checked

- [ ] **Comparison review.** Read every vendor cell in `contracts/claims/compare.v1.json` with its
      source link. Set `"reviewed": true` on each cell you accept, or ask for a change. Re-verify
      any cell whose `as_of` is more than a few weeks old.
- [ ] **Third-party figures are fresh.** `node scripts/check-claims.mjs` prints no
      `third-party-stale` warning (a third-party claim older than 120 days needs its source
      re-read and its `as_of` updated).
- [ ] **Market context** in [interview-talking-points.md](interview-talking-points.md) is
      re-verified line by line, or the line is deleted.
- [ ] **Terms of service.** TypeSafe's and OpenRouter's terms allow publishing benchmark results
      and showing model outputs; note the date and the links you read in the release PR.
- [ ] **The launch gate.** `make check-claims-launch` (`node scripts/check-claims.mjs --launch`)
      passes. It refuses a missing or simulated recording, an unmeasured `measured.json`, any
      unverified or unreviewed comparison cell, and a missing or wrong-size `og.png`, on top of
      every rule `make check-claims` applies.

## 3. The site is safe to open

- [ ] **Same-origin check.** List every site under `https://timkush1.github.io/`. All of them
      share one origin with the demo, so a page from another repository opened in the same tab
      could read the visitor's key from `sessionStorage`. If any of them loads third-party
      scripts (analytics, widgets, embeds), move the demo to a custom domain or an organisation
      Pages site first; the Connect callback follows `location.origin`, so no code changes.
- [ ] **The repository is public** (Pages, arm64 runners and artifact attestations are free only
      for public repositories).
- [ ] **Pages PKCE check.** On `https://timkush1.github.io/TrustAgent/app/playground/#live`, press
      **Connect OpenRouter** and note whether OpenRouter accepted the
      `/TrustAgent/connect/callback/` address. Record the result, dated, in SPEC Appendix D. If it
      refused, Paste a key becomes the primary button before launch. Then give the key that was
      created a small credit limit at openrouter.ai/settings/keys, or delete it.
- [ ] **Localhost PKCE check**, if it was skipped earlier: the same, from `make up`. Until a row
      in SPEC Appendix D carries a dated result, no doc may call PKCE verified (`check-claims`
      enforces it).
- [ ] **Images.** Each GHCR package (`truthtable-proxy`, `-engine`, `-dashboard`) is public, and
      the Actions variable `GHCR_PUBLIC` = `true` lets the `anonymous-pull` job prove it;
      `docker buildx imagetools inspect ghcr.io/timkush1/truthtable-proxy:<version>` lists
      `linux/amd64` and `linux/arm64`;
      `gh attestation verify oci://ghcr.io/timkush1/truthtable-proxy:<version> -R timkush1/TrustAgent`
      passes for each image.

## 4. Release

- [ ] **The release gate**, from the repository root: `make check`, `make check-version`,
      `make check-claims-launch` and the `check_recording.py --require-recorded` line above.
- [ ] **Version.** Read the CHANGELOG through, then `make version-bump V=2.0.0`; it renames the
      `Unreleased — 2.0.0` heading to the release date.
- [ ] **Pull request** `feat/v2-jev-gateway` → `main`: every CI check is green, and `pages.yml`
      and `release-images.yml` pass as build-only runs.
- [ ] **Tag** `v2.0.0`; `make check-version RELEASE_TAG=v2.0.0` passes; the release notes list each
      image digest and the `gh attestation verify` command.
- [ ] **Turn Pages on**: Settings → Pages → Source: GitHub Actions; Settings → Environments →
      `github-pages` → add the tag rule `v*`; Actions variable `PAGES_ENABLED` = `true`.
- [ ] **Open the live site as a stranger would** (a private window, a phone at 400 px, both
      themes): the banner says "Recorded …", never "Simulated"; the first-visit guide reaches the
      caught answer; a live check with a small-limit key works and Forget key clears it; the
      browser's network panel shows requests only to the site and openrouter.ai.

## 5. Announce

- [ ] **Links.** The README badges, the demo URL, the repository URL and every link in the docs
      and in the post open (no 404, no redirect to a login). The **Live demo** and **Release**
      badges point at the Pages site and the first GitHub release, which exist only after the
      steps above: check both badges render a value, not "not found".
- [ ] **Load run at the release commit.** Re-run `make load-test-gate LOAD_PUBLISH=1` on a quiet
      machine (no other stack running) at the release commit before `bench-publish` folds
      scenario c into `measured.json`: the P5 run predates the fix round's gateway changes and ran
      beside the owner's demo stack.
- [ ] **GitHub metadata** from [github-metadata.md](github-metadata.md) is applied, and
      `docs/launch/og.png` is uploaded as the social preview.
- [ ] **The post.** Every `{measured:…}` placeholder in [linkedin-post.md](linkedin-post.md) is
      replaced from its *Values to paste* table, each with its "(measured)" label and the run
      date; LinkedIn's post inspector shows the card from `og.png`.
- [ ] **The video** follows [demo-script.md](demo-script.md), recorded on the recorded stack, with
      every number on screen still wearing its label.
