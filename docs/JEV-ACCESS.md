# Jev access: getting and configuring the fast check

This guide is for whoever runs TrustAgent. It covers how to get access to **Jev**, the fast
classifier behind TrustAgent's per-answer "fast check", what it costs, which settings matter and
what to do when something goes wrong.

> **Where this stands.** TrustAgent 2.0 is being built in phases (see
> [docs/v2/SPEC.md §4](v2/SPEC.md#4-implementation-sequence)). Everything on the OpenRouter side of
> this guide works today, and so does everything untagged on the TrustAgent side. Features still on
> the way carry the phase that ships them, such as *(from P4)*; a tagged feature is not available
> until that phase has landed.

## TL;DR

1. Sign in at [openrouter.ai](https://openrouter.ai).
2. Add $5 of credit at [openrouter.ai/settings/credits](https://openrouter.ai/settings/credits).
3. Create a key at [openrouter.ai/settings/keys](https://openrouter.ai/settings/keys). Name it
   `TrustAgent dev` and give it a **credit limit of $2**.
4. Put the key in **one place only**: the `.env` file at the repository root.

   ```bash
   OPENROUTER_API_KEY=sk-or-v1-...
   ```

   `.env` is gitignored. Never paste the key into a chat, an issue, a commit, a URL or a
   screenshot. Once the dashboard ships **Connect OpenRouter** *(from P4)*, you can click that
   instead of doing steps 3 and 4.

That is all you need: there is no waitlist and no TypeSafe account. The same OpenRouter key will
also runs the deep-audit judge (Claude Haiku via OpenRouter).

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

In order of ease:

| # | How | What happens | Available |
|---|---|---|---|
| 1 | **Dashboard → Connect OpenRouter** | OpenRouter creates a key for you (PKCE sign-in). TrustAgent stores it encrypted and uses it for both the fast check and the judge. **Then open [openrouter.ai/settings/keys](https://openrouter.ai/settings/keys) and set a credit limit on the new key (for example $2)**, because keys created this way have no limit. | *(from P4)* |
| 2 | **Paste a key** in Settings → Connections | Works for OpenRouter, TypeSafe, Anthropic or OpenAI keys. Stored encrypted; never sent back to the browser. | *(from P4)* |
| 3 | **Environment:** `OPENROUTER_API_KEY=…` in `.env`, then `make up` | An environment value always wins and shows as read-only in the dashboard. | Now |
| 4 | **Headless:** `python3 scripts/configure.py --with-secrets` | Writes the configuration, secrets included, without opening the dashboard. (Without `--with-secrets` it writes only the non-secret settings.) | *(from P4)*; today `configure.py --ensure` fills in only the generated secrets |

## Backends

Set with `TRUTHTABLE_JEV_BACKEND`. The default is `auto`, which picks the first match:
demo mode uses `mock`; `TRUTHTABLE_JEV_BASE_URL` plus `TRUTHTABLE_JEV_API_KEY` selects `compatible`;
an OpenRouter key selects `openrouter-decisions`; a TypeSafe key selects `typesafe`. With no key at
all the fast check is `off`.

| Backend | Use it when | Model |
|---|---|---|
| `openrouter-decisions` | Default when you have an OpenRouter key | `typesafe/jev-1.13`, pinned. `~typesafe/jev-latest` is allowed, but recalibrate after every change. |
| `openrouter-systemone` | Fallback if OpenRouter's `/alpha/decisions` endpoint changes | Same as above |
| `typesafe` | You have a key from console.typesafe.ai | `jev-1.13.0` or `jev-latest` |
| `compatible` | You go through another gateway, for example Vercel at `https://ai-gateway.vercel.sh/typesafe` | Required (`typesafe-ai/jev`). Not verified yet: check it with `make doctor`. |
| `mock` | Demos and CI. Labelled "Simulated" everywhere in the product. | `mock/jev-heuristic-v1` |
| `off` | Checks disabled. Answers pass through with `X-TrustAgent-Status: skipped:decision_model_off`. | — |

## Settings that matter

Route modes are editable on the dashboard's **Policies** page; the other settings will be
*(from P4)*. Until then they come from the environment or from the policy file
`config/policies.yaml`.

| Setting | Default | Effect | Environment or policy key |
|---|---|---|---|
| Route mode | observe | Measures only and never changes an answer. Move to flag, then to block when the checks are calibrated. | `TRUTHTABLE_GATE_DEFAULT_MODE`; per route `mode` |
| Daily caps | Fast checks $2, deep audits $5 | Past a cap, fast checks pass answers through unchecked and deep audits pause. A banner says so. The exception is a block route set to fail closed (`fail: closed`): it blocks instead. Observe and flag routes stop at 75% of the fast-check cap, which keeps the rest for routes that block or fall back. | `TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY`, `TRUTHTABLE_SPEND_CAP_LLM_USD_DAY` (0 = unlimited) |
| Model pin | — (routes use `TRUTHTABLE_JEV_MODEL`, which is `typesafe/jev-1.13` on OpenRouter) | Which Jev snapshot answers. Can be pinned per route. | `TRUTHTABLE_JEV_MODEL`; per route `decision.model` |
| Sampling | Deep audit on grounding-flagged answers, plus 2% of passes, plus 1% uniform | Decides which answers get the expensive LLM audit. The uniform 1% is what makes the headline hallucination rate unbiased. | Per route `deep_audit.pass_sample_rate` (0.02), `deep_audit.uniform_sample_rate` (0.01) |

## Costs [estimate]

These are TrustAgent's own costs. Your upstream LLM calls are billed separately, as before.

| Item | Cost |
|---|---|
| Fast check | About $0.0002 per answer: $0.21 per 1,000, or about $63 a month at 10,000 answers a day |
| Deep audit (Claude Haiku via OpenRouter) | About $0.035 per audited answer |
| Shipped defaults, total (fast checks plus deep audits) | About $3 per 1,000 answers |
| Connectivity probe (Settings → Test) | $0.00001 |

$5 of credit covers about 1,600 answers at the default settings, or about 24,000 fast checks with
deep audits off. The default fast-check cap of $2 a day covers roughly 7,000 answers a day on
observe and flag routes (they stop at 75% of the cap); raise it before sending more traffic than
that.

## Errors and fixes

TrustAgent reports these errors with their fix in `make doctor` and on the dashboard (both read
`/api/status`); the affected answers carry `X-TrustAgent-Status` / `-Degraded`. The fixes on the
OpenRouter side apply as they are.

| You see | What it means | What to do |
|---|---|---|
| 401 or 403, "Key rejected" | The key is invalid or was revoked | Reconnect or paste a new key *(from P4)*, or replace `OPENROUTER_API_KEY` in `.env` and restart |
| 402, "no credits" | The account is empty or the key's credit limit is reached | Add credits at [openrouter.ai/settings/credits](https://openrouter.ai/settings/credits), or raise the key's limit at [openrouter.ai/settings/keys](https://openrouter.ai/settings/keys) |
| 404, "No endpoints found" or a mention of data policy | Your privacy settings exclude the only provider that serves Jev | Check [openrouter.ai/settings/privacy](https://openrouter.ai/settings/privacy), or unset `TRUTHTABLE_JEV_ZDR` |
| 200 with an HTML page, "got a web page" | Wrong base URL (OpenRouter API URLs end in `/api`) | Fix `TRUTHTABLE_JEV_BASE_URL`, or leave it empty |
| 429, 529 or another 5xx | The provider is busy | Nothing: TrustAgent retries and backs off automatically |
| Timeout | The alpha endpoint hung | Nothing: TrustAgent retries; if the route fails open, the answer passes through unchecked |

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

| Check | What it tells you | Available |
|---|---|---|
| `make doctor` (or `python3 scripts/doctor.py`) | Which backend is active and why it was chosen, its state, the Jev snapshot, latency and the last 24 hours' calls and cost (labelled estimate), with a fix for each error state | Now |
| Dashboard → Settings → Test | Sends one probe (about $0.00001) and shows the backend, snapshot, latency and any error with its fix | *(from P4)* |

**Today**, before either exists, you can confirm that the key works and see its limit with a free
call that makes no model request:

```bash
curl -sS -w '\nHTTP %{http_code}\n' https://openrouter.ai/api/v1/key \
  -H "Authorization: Bearer $(grep -m1 '^OPENROUTER_API_KEY=' .env | cut -d= -f2- | tr -d '\r"')"
```

`HTTP 200` with a JSON description of the key (including its usage and limit) means the key is
good; `HTTP 401` means it is not. Run the command from the repository root: it reads the key from
`.env` (dropping any quotes or Windows line ending), so the key itself never appears in your shell
history.
