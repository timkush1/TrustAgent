# Jev access: getting and configuring the fast check

This guide is for whoever runs TrustAgent. It covers how to get access to **Jev**, the fast
classifier behind TrustAgent's per-answer "fast check", how to connect it, what it costs, which
settings matter and what to do when something goes wrong.

## TL;DR

1. Sign in at [openrouter.ai](https://openrouter.ai).
2. Add $5 of credit at [openrouter.ai/settings/credits](https://openrouter.ai/settings/credits).
3. In TrustAgent, click **Connect OpenRouter** (step 2 of the setup wizard, or
   **Settings → Connections**).
4. Open [openrouter.ai/settings/keys](https://openrouter.ai/settings/keys) and give the new key a
   **credit limit**, for example $2. Keys created by Connect have no limit until you set one.

That is all you need: there is no waitlist and no TypeSafe account. The same OpenRouter key also
runs the deep-audit judge (Claude Haiku via OpenRouter).

No account yet? **Try it without a key (simulated checks, $0)** in step 2 runs both checks on
mocks, so you can see the whole product first. See [Simulated mode](#simulated-mode).

## What Jev is

Jev is a fast, cheap classifier made by TypeSafe AI and launched on 2026-09-15. It answers yes/no,
pick-one and scale questions and returns probabilities. It does not write text.

| | |
|---|---|
| Price | $0.042 per 1M input tokens; output is free **[vendor]** |
| Latency | About 70–500 ms; P50 about 0.23 s on OpenRouter **[third-party]** |
| Cost model | **It is not free.** HTTP 402 means your account has no credits. |

**How TrustAgent uses it.** Each answer that passes through the proxy is checked with one Jev
call. Every check attached to the route (for example "is the answer grounded in the documents?" or
"does it contradict them?") is one question inside that call, and the answers decide what happens
to the response: observe, pass, flag, block or fallback. A slower LLM "deep audit" then explains
flagged answers and a small sample of the rest, claim by claim. Groundedness is only asked when the
request carries documents; with none, it is reported as "not checked" instead of being guessed.

**Labels used in this guide:** **[vendor]** is the vendor's own published figure, **[third-party]**
is a figure someone else measured or reported, and **[estimate]** is TrustAgent's own estimate
(the inputs live in `contracts/pricing/assumptions.v1.json`).

## Ways to connect

In order of ease. Whichever you use, the key is checked with its provider before TrustAgent uses
it, and it never reaches the browser: the dashboard only ever shows a masked form such as
`sk-…3f2a`.

| # | How | What happens |
|---|---|---|
| 1 | **Connect OpenRouter** (setup wizard step 2, or Settings → Connections) | You sign in on openrouter.ai and approve; OpenRouter sends you back to `/connect/callback`, and TrustAgent exchanges the one-time code for a new key (PKCE: the browser never sees the key). The key is stored encrypted, checked, then used for the fast check and the judge. |
| 2 | **Paste a key** (Settings → Connections, or step 2) | Works for OpenRouter, TypeSafe, Anthropic and OpenAI keys. Stored encrypted and checked first; it is used only when the check passes. |
| 3 | **Environment:** `OPENROUTER_API_KEY=…` in `.env`, then `make up` | An environment key always wins and shows as read-only in the dashboard. |
| 4 | **Headless:** `make configure WITH_SECRETS=1`, then `make up` | Asks for the OpenRouter, TypeSafe, Anthropic and OpenAI keys at hidden prompts (Enter skips one) and writes them to `.env` (mode 0600). Nothing you type is shown or logged. |

### What "checked" means

| Provider | The check | Cost |
|---|---|---|
| OpenRouter | OpenRouter's key lookup (`GET /api/v1/key`: the key's label and credit limit), then one Jev probe | about $0.00001 **[estimate]** |
| TypeSafe, compatible gateway | One Jev probe | about $0.00001 **[estimate]** |
| Anthropic, OpenAI | The vendor's model list (`GET /v1/models`) | $0 |

A key that fails its check is never used. If you were replacing a key that is in use, nothing
changes and the old key keeps running. A new key that fails is saved but not used; for **402 (no
credits)**, add credits, then click **I added credits — check again**.

### Credit limits on Connect keys

A key that Connect creates has **no spending limit**. The wizard and Settings → Connections say so
when OpenRouter reports no limit. Set one at
[openrouter.ai/settings/keys](https://openrouter.ai/settings/keys) (for example $2). TrustAgent's
own daily budgets (below) also stop spending, but a limit on the key is the backstop that holds even
if TrustAgent is misconfigured.

### Connect on localhost

Connect sends OpenRouter the address to return to: `<dashboard address>/connect/callback`, for
example `http://localhost:3000/connect/callback`. The dashboard address is the page you clicked
from when the proxy allows that origin, otherwise `TRUTHTABLE_PUBLIC_URL` (compose sets both from
`DASHBOARD_HOST_PORT`).

**Status on localhost:** the end-to-end tests run Connect against a local fake of OpenRouter. The
check against the real openrouter.ai with a `localhost` callback is recorded here once the
maintainer has run it. If OpenRouter refuses the callback address on your machine, use **Paste a
key**: it is always offered on the same screen and needs nothing else.

### Which value wins

For every setting, the first of these that has a value is used: a non-empty environment variable,
then the value saved in the dashboard, then the built-in default.

- A **non-empty** environment variable always wins. The dashboard shows that setting as read-only
  ("Set in .env") and refuses to change it. To edit it in the dashboard, remove it from `.env` and
  run `make up` again. An empty variable (`KEY=`) counts as unset.
- Keys read by the engine (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) appear as "Set in the engine's
  .env". The dashboard cannot test those; `make doctor` reports them.
- An environment OpenRouter key also means Connect is unavailable (the key in `.env` is the one in
  use).

## Simulated mode

**Try it without a key (simulated checks, $0)** switches the fast check and the judge to mocks
(`mock` backend, mock judge). Everything works and nothing is billed, but:

- every number from simulated checks is labelled **Simulated** (in status, traces and Savings), and
  the top bar shows a **Simulated checks** badge;
- simulated checks **never block**: block and fallback routes need a calibration, and mock
  calibrations only load in the demo, so those routes stay at flag.

Connecting a real key turns simulated mode off by itself ("Simulated checks are now off"). The
zero-key demo (`make demo`) is a separate stack that is always simulated.

## Backends

Set with `TRUTHTABLE_JEV_BACKEND`, or on **Settings → Models**. The default is `auto`, which picks
the first match: demo mode uses `mock`; `TRUTHTABLE_JEV_BASE_URL` plus `TRUTHTABLE_JEV_API_KEY`
selects `compatible`; an OpenRouter key selects `openrouter-decisions`; a TypeSafe key selects
`typesafe`. With no key at all the fast check is `off` until you connect one: answers pass through
unchecked and the status says why.

| Backend | Use it when | Model |
|---|---|---|
| `openrouter-decisions` | Default when you have an OpenRouter key | `typesafe/jev-1.13`, pinned. `~typesafe/jev-latest` is allowed, but recalibrate after every change. |
| `openrouter-systemone` | Fallback if OpenRouter's `/alpha/decisions` endpoint changes | Same as above |
| `typesafe` | You have a key from console.typesafe.ai | `jev-1.13.0` or `jev-latest` |
| `compatible` | You go through another gateway, for example Vercel at `https://ai-gateway.vercel.sh/typesafe` | Required (`typesafe-ai/jev`). Not verified yet: check it with `make doctor`. |
| `mock` | Demos, CI and simulated mode. Labelled "Simulated" everywhere in the product. | `mock/jev-heuristic-v1` |
| `off` | Checks disabled. Answers pass through with `X-TrustAgent-Status: skipped:decision_model_off`. | — |

## Settings that matter

All of these are in the dashboard; route modes on the **Policies** page, the rest under
**Settings**. A matching environment variable makes the setting read-only there (see
[Which value wins](#which-value-wins)).

| Setting | Default | Effect | Where |
|---|---|---|---|
| Route mode | observe | Measures only and never changes an answer. Move to flag, then to block when the checks are calibrated. | Policies; `TRUTHTABLE_GATE_DEFAULT_MODE`, per route `mode` |
| Daily budgets | Fast checks $2, deep audits $5 | Past a budget, fast checks pass answers through unchecked and deep audits pause. A banner says so. The exception is a block route set to fail closed (`fail: closed`): it blocks instead. Observe and flag routes stop at 75% of the fast-check budget, which keeps the rest for routes that block or fall back. | Settings → Budgets; `TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY`, `TRUTHTABLE_SPEND_CAP_LLM_USD_DAY` (0 = unlimited) |
| Prices | The contracts price table | Override a model's price when your contract differs (a $0 price is refused for paid providers). | Settings → Budgets → Prices |
| Model pin | `typesafe/jev-1.13` on OpenRouter | Which Jev snapshot answers. Can be pinned per route. | Settings → Models; `TRUTHTABLE_JEV_MODEL`, per route `decision.model` |
| Judge | `auto` (the bound key's provider) | Which LLM runs the deep audit. | Settings → Models; `LLM_PROVIDER`, `LLM_MODEL` |
| Sampling | Deep audit on grounding-flagged answers, plus 2% of passes, plus 1% uniform | Decides which answers get the expensive LLM audit. The uniform 1% is what makes the headline hallucination rate unbiased. | Policies; per route `deep_audit.pass_sample_rate` (0.02), `deep_audit.uniform_sample_rate` (0.01) |

The budgets count real spend only, recorded per call in a cost ledger; simulated checks never
count. Several proxy replicas each enforce the budget on their own share between resyncs (up to
60 s), so a fleet can overspend slightly before it stops.

## Costs [estimate]

These are TrustAgent's own costs. Your upstream LLM calls are billed separately, as before.

| Item | Cost |
|---|---|
| Fast check | About $0.0002 per answer: $0.21 per 1,000, or about $63 a month at 10,000 answers a day |
| Deep audit (Claude Haiku via OpenRouter) | About $0.035 per audited answer |
| Shipped defaults, total (fast checks plus deep audits) | About $3 per 1,000 answers |
| Checking a key, or Settings → Models → Test | About $0.00001 |

$5 of credit covers about 1,600 answers at the default settings, or about 24,000 fast checks with
deep audits off. The default fast-check budget of $2 a day covers roughly 7,000 answers a day on
observe and flag routes (they stop at 75% of the budget); raise it before sending more traffic than
that. The **Savings** page shows what was actually spent (measured) next to these estimates.

## Errors and fixes

TrustAgent reports these errors with their fix in `make doctor` and on the dashboard (both read
`/api/status`); the affected answers carry `X-TrustAgent-Status` / `-Degraded`.

| You see | What it means | What to do |
|---|---|---|
| 401 or 403, "Key rejected" | The key is invalid or was revoked | Connect again or paste a new key in Settings → Connections, or replace it in `.env` and run `make up` |
| 402, "no credits" | The account is empty or the key's credit limit is reached | Add credits at [openrouter.ai/settings/credits](https://openrouter.ai/settings/credits), or raise the key's limit at [openrouter.ai/settings/keys](https://openrouter.ai/settings/keys); then **check again** |
| 404, "No endpoints found" or a mention of data policy | Your privacy settings exclude the only provider that serves Jev | Check [openrouter.ai/settings/privacy](https://openrouter.ai/settings/privacy), or unset `TRUTHTABLE_JEV_ZDR` |
| 200 with an HTML page, "got a web page" | Wrong base URL (OpenRouter API URLs end in `/api`) | Fix `TRUTHTABLE_JEV_BASE_URL`, or leave it empty |
| 429, 529 or another 5xx | The provider is busy | Nothing: TrustAgent retries and backs off automatically |
| Timeout | The alpha endpoint hung | Nothing: TrustAgent retries; if the route fails open, the answer passes through unchecked |
| "Saved keys cannot be decrypted" | The master key changed since the keys were saved | Restore the old master key, or re-enter the keys ([SECURITY.md](SECURITY.md#master-key)) |
| "Deep audits paused: today's deep-audit budget is used up" | The deep-audit budget is spent | Raise it in Settings → Budgets, or wait for tomorrow (UTC) |

## Limits and privacy

- Jev gives no explanations. The deep audit provides them.
- It is weak on numbers, dates and double negatives. Deterministic detectors cover these, and the
  check escalates such answers to the deep audit.
- It needs documents to judge groundedness. Without them the result is "not checked", never a
  made-up score.
- It is English-first.
- It is not a security boundary.
- Thresholds shift between Jev snapshots. Recalibrate after the dashboard reports "Jev version
  changed".
- **Privacy:** the prompt, the answer and the documents go to OpenRouter and then to TypeSafe. Per
  route you can set `store_bodies: none` or turn the gate off. Keys never reach the browser.

## How to verify

| Check | What it tells you |
|---|---|
| `make doctor` (or `python3 scripts/doctor.py`) | Which backend is active and why it was chosen, its state, the Jev snapshot, latency and the last 24 hours' calls and cost (labelled estimate); whether every saved key decrypts; that the engine runs the current settings; where the judge's key comes from; today's spend against both budgets. Each problem comes with a fix. |
| Settings → Connections → **Test** on a key | Checks that key with its provider (see [What "checked" means](#what-checked-means)) and shows the result, the key's credit limit and any fix |
| Settings → Models → **Test** | Sends one probe (about $0.00001) and shows the backend, snapshot, latency and any error with its fix |

Without a running stack you can still confirm that a key in `.env` works and see its limit, with a
free call that makes no model request:

```bash
curl -sS -w '\nHTTP %{http_code}\n' https://openrouter.ai/api/v1/key \
  -H "Authorization: Bearer $(grep -m1 '^OPENROUTER_API_KEY=' .env | cut -d= -f2- | tr -d '\r"')"
```

`HTTP 200` with a JSON description of the key (including its usage and limit) means the key is
good; `HTTP 401` means it is not. Run the command from the repository root: it reads the key from
`.env` (dropping any quotes or Windows line ending), so the key itself never appears in your shell
history.
