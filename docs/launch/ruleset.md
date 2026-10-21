# The `main` ruleset

The branch ruleset on `main` is created in two steps (D215). At sitting S3a, before the first
push, it is minimal: **Restrict deletions**, **Block force pushes** and **Require a pull request**
(0 approvals), with Repository admin as the only bypass. At sitting S3c, after these checks have
run on `main` at least once (GitHub offers a check name only after it has seen it), **Require
status checks to pass** is added with exactly the names below. Type each one into the search box
and pick it; the source is "any source" (GitHub Actions).

```text
Go (proxy)
Python (audit engine)
Frontend (dashboard)
Docker builds
Contracts
DX scripts
E2E (compose stack)
gosec (Go SAST)
bandit (Python SAST)
gitleaks (secrets)
npm audit (frontend deps)
```

Why these and not others:

- They are the jobs of `ci-go`, `ci-python`, `ci-web`, `ci-docker`, `ci-contracts`, `ci-e2e` and
  `security.yml`, which run on every pull request with no path filter, so a required check can
  never wait for a run that will not come. `scripts/tests/test_workflows.py` checks this list
  against the workflows.
- **Not** `Trivy (filesystem scan)`: it is informational (`continue-on-error`) until a scheduled
  run is reviewed.
- **Not** the Pages and release-image jobs: they publish, and on a pull request they only build;
  `release-images.yml` runs on a pull request only when an image's inputs change.
- **Not** the knowledge, eval and eval-live workflows: manual or scheduled runs, never a gate.

Keep "Require branches to be up to date" off (each merge would wait for a full re-run), and keep
"Require linear history" off: the release merges with a merge commit, so the history stays as it
was built.
