## What and why

<!-- What this changes and the problem it solves. Link the issue: "Fixes #123". -->

## How it was tested

<!-- The tests you added or ran (make check, go test, pytest, npm test, Playwright), and anything you checked by hand. -->

## Checklist

- [ ] `make check` passes locally (or the parts this change touches).
- [ ] Tests cover the change.
- [ ] No real key, `.env` content or personal data in the diff (tests use fake keys only).
- [ ] A contract change (`contracts/`, `proto/`, decision logic, migrations, `src/types/api.ts`) keeps Go, TypeScript and Python in step.
- [ ] Every new figure a reader sees carries its label; nothing from a mock is called measured.
- [ ] CHANGELOG.md has a line, if a user or an operator would notice the change.
