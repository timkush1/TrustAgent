# Evaluators and calibration

An **evaluator** is one question the fast check (Jev) answers about every
answer on a route: "Does the answer state facts the evidence does not
contain?", "Does it promise a refund the rules do not allow?". A route attaches
up to 16 of them, and the gateway asks all of them in **one** Jev call. Jev
answers with probabilities; the evaluator turns its answer into a **risk**
(0 to 1) and a **level**: `ok`, `warn` or `critical`. The policy
([GATEWAY.md](GATEWAY.md#2-modes)) turns levels and the Tier-0 detectors into
an action.

The full spec is [v2/SPEC.md](v2/SPEC.md) §2.8 (evaluator spec), §2.9 (the
decision algorithm) and §2.11 (calibration).

## The built-in evaluators

They live in [`contracts/evaluators/`](../contracts/evaluators) as immutable,
versioned JSON (`evaluator/v1`). Thresholds are raw-space starting points, not
measurements; the `threshold_basis` field of each file says where a number
came from.

| Evaluator | Category | Needs evidence | warn / critical | Can block |
|---|---|---|---|---|
| `groundedness@1` | grounding (verdict: unsupported) | required | 0.8 / 0.95 | yes |
| `contradiction@1` | grounding (verdict: contradicted) | required | 0.7 / 0.95 | yes |
| `factual_claims@1` | grounding | no | 0.6 / — | no |
| `commitments@1` | compliance (param `allowed_commitments`) | no | 0.6 / 0.9 | yes |
| `regulated_advice@1` | compliance | no | 0.7 / — | no |
| `pii_secret_leak@1` | security | optional | 0.6 / 0.9 | yes |
| `injection_success@1` | security | optional | 0.6 / 0.9 | yes |
| `unsafe_policy@1` | safety | no | 0.6 / 0.9 | yes |
| `refusal@1` | quality | no | 0.7 / — | no |
| `off_topic@1` | quality (param `allowed_scope`) | no | 0.7 / — | no |
| `off_brand_tone@1` | quality (param `brand_voice`) | no | 0.6 / — | no |

- **Evidence.** An evaluator that requires evidence is `not_assessed` when the
  request carries none (no documents, tool results or `trustagent.context`):
  Jev is never asked a groundedness question without evidence, because its
  scores invert.
- **Corroboration.** A block-eligible evaluator blocks only when a second
  signal it names (`requires_corroboration`) agrees: another critical
  evaluator, or a Tier-0 detector (`det:urls_not_in_evidence`,
  `det:numbers_not_in_evidence`, ...). Otherwise the reason
  `needs_second_signal` is added and the answer is flagged. An evaluator
  that names no corroborator (unsafe content) blocks on its own.

## Attaching evaluators to a route

```yaml
routes:
  - name: support-bot
    match: {headers: {X-TrustAgent-Route: support-bot}}
    mode: flag
    evaluators:
      - ref: groundedness@1
      - ref: contradiction@1
      - ref: commitments@1
        params: {allowed_commitments: "Annual plans: refund within 30 days of purchase."}
      - ref: off_brand_tone@1
        stage: shadow          # asked and recorded, never affects the action
```

- `ref` pins `id@version`; `@latest` is refused, so a new evaluator version
  never changes a route by itself.
- `stage: shadow` evaluators are asked in the same call and shown on the
  dashboard, but never change the action, verdict or risk. Use it to try an
  evaluator before it counts.
- `thresholds: {space: raw|calibrated, warn_at, critical_at}` overrides the
  evaluator's thresholds for this route.
- Your own evaluators come from two places: files in `config/evaluators/*.json`
  (policy as code; the proxy validates them at load and shows them read-only
  in the dashboard) and the **Evaluator Studio** (below), which saves new
  versions in the database. Built-ins are never edited: copy one into the
  Studio instead. `make contracts-check` validates the built-ins.

The dashboard's **Policies** page shows each route's evaluators, their
calibration state and the block-readiness checklist, and `make doctor` lists
each route's downgrades (unmet checklist items) and which routes run below
their configured mode.

## Evaluator Studio

**Evaluators → Studio** (`/app/evaluators/studio`, admin) takes you from "what should TrustAgent
check?" to a calibrated evaluator in one place. Nothing it makes is attached to a route until you
say so.

1. **Describe.** Write what the check should catch and pick a route. **Draft with AI** asks the
   deep-audit judge for a first version (about
   <!-- claim:studio_draft_cost_estimate --> ≈ $0.005 per draft [estimate]; labelled Simulated
   with the mock judge); without a judge, start from a template.
2. **Review.** A simple form (what it catches, Yes/No or Pick one, which answers are a problem,
   whether it needs the app's documents, sensitivity, and whether it may block), an Advanced
   section with every other field, and a JSON tab. Lints run as you type; the proxy runs the same
   lints and hard rules on save (Go is the reference), and a version that breaks a hard rule never
   loads.
3. **Test bench.** Run the draft on pasted samples, the draft's own samples, or recent traffic
   (flagged, random, or where checks disagreed). The cost is shown before every run (paid: one Jev
   call per sample, at most 50). Each row says whether it fired and whether that matches your label.
   Moving the threshold replays the stored answers for free.
4. **Quick calibration.** With at least 8 labels (3 of each kind), the Studio suggests a threshold
   with its uncertainty ("Suggested threshold from 12 labels", with a 95% range). It never calls
   this "calibrated" and never sets a critical level.
5. **Save** creates an immutable new version with a changelog. **Save and watch on route X** adds it
   to that route as `stage: shadow`.
6. **Test on past traffic (paid)** replays up to `TRUTHTABLE_DRYRUN_MAX_SAMPLES` stored answers
   (200) through the real Jev and reports how often it would have fired (with a 95% interval), its
   overlap with other evaluators and its agreement with deep audits, and what the test cost. Above
   `TRUTHTABLE_STUDIO_CONFIRM_ABOVE_USD` ($0.10) it asks first. Studio runs never spend the part of
   the fast-check budget kept for enforcing routes.
7. **Make active** creates a new policy version; rollback is one click on the Policies page.

Every Studio run and save is recorded in the admin audit log, dry-runs with their cost.

### The Studio on the static demo

The live demo ([DEMO.md](DEMO.md#the-live-demo-in-your-browser)) runs the Studio in the browser,
with no stack behind it. What works there:

- **Templates, the editor** (Simple, Advanced and JSON) and the lints, which run in the browser with
  the same rules as the proxy.
- **Draft with AI** with your own OpenRouter key, through the same drafting prompt and repair round
  as the engine. Without a key, **Start from a template** is the way in, and drafting says it needs
  one.
- **The test bench**: up to 6 distinct answers from the replayed tape on the route you picked
  (labelled as coming from the recording), or your own, run through the browser's copy of the
  gateway's pipeline. It needs your key: one call to the real Jev per sample, with the estimate
  shown before **Run**. The threshold what-if then replays those answers for free. Without a key,
  **Run** is disabled and says why.
- **Quick calibration** on the labels you set in the page (they are not saved anywhere).
- **Save** becomes **Copy JSON** and **Download .json**: a validated `evaluator/v1` file you can add
  to your own stack.

Watching on a route, testing on past traffic, making the check active and calibrating it for
blocking need a stack, so they are one **On your own stack** card that starts with `make demo`.
Every call with your key counts toward the demo's courtesy cap of 100 calls per tab.

## Labels: "Was this right?"

Calibration needs human labels. Two places collect them:

- **Trace detail → "Was this right?"** appears when a check fired at warn or above. **Correct**
  records `should_fire` and **False alarm** records `should_not_fire`, one label per evaluator that
  fired.
- **Evaluators → (an evaluator) → Label queue** shows the answers the evaluator is least sure about
  first (closest to its warn threshold). Keys: **C** correct, **F** false alarm, **S** skip.

Labels freeze the stored answer they refer to, so it outlives the body retention window.

## The glass box

**Trace detail → Details → Show exact Jev request** recompiles, from the stored answer and the policy of the
time, exactly the request the gateway sent: every question, the model, and a hash per request.
`matches_recorded` says whether the recompiled request is byte-for-byte the one that was sent
(it is not when the stored body was truncated, or the policy or evaluator version is gone).
`GET /api/traces/:id/decision-request` serves it (content scope).

## Calibration

A raw Jev probability is not a precision. **Calibration** maps it to a
measured one for one Jev **snapshot** (the model version the vendor reports),
so that "critical" can mean "at least 95% of these are real problems".

- Where they live: files in `config/calibration/*.json`
  (`trustagent.calibration/v1`, read from `TRUTHTABLE_CALIBRATION_DIR`, re-read
  every 30 s) and, with a database, calibrations imported or fitted in the
  dashboard. File entries are mirrored into the database; a calibration fitted
  or imported in the dashboard takes precedence over the file entry for the
  same evaluator and snapshot until you deactivate it. With a database, the
  proxy uses the active database rows: deleting a file from
  `TRUTHTABLE_CALIBRATION_DIR` does not withdraw its mirrored calibration,
  which stays active until another calibration for the same evaluator and
  snapshot replaces it (deactivate it in the dashboard to stop using it now).
  Without a database the files are the only source, and a deleted file stops
  applying at the next re-read. A change sends the WebSocket event
  `calibration_updated`.
- **Quality** decides whether a calibration may enable blocking:

  | Quality | Made from | Enables block |
  |---|---|---|
  | `measured` | Human or public-dataset labels, n ≥ 200 (≥ 30 of each class) | yes |
  | `provisional` | Fewer labels, or the snapshot family's calibration | no |
  | `proxy` | Deep-audit or judge labels | no |
  | `mock` | The mock decision model | only in demo mode with the mock backend |

- **Fitting one in the dashboard** (the adopt flow): **Evaluators → (an
  evaluator) → Calibrate now** fits from the labels (at least 8; `measured`
  from 200 labels with 30 of each class, otherwise `provisional`) without
  activating it, then:
  1. **Preview impact (free)** replays the stored answers with the new
     calibration and shows which actions would change;
  2. you confirm;
  3. the calibration is activated;
  4. the route's thresholds switch to the calibrated space with the
     recommended values, as a new policy version.

  A calibration file can also be imported (`POST /api/calibration`, admin):
  the proxy re-derives its quality and refuses unknown evaluator versions, a
  mismatched `calibration_key`, and mock snapshots outside the demo.
- **Fitting one from the command line** (paid: it needs labelled data scored by the real Jev):

  ```bash
  make gate-eval                                      # the scorer the fit uses
  make calibrate EVALUATOR=groundedness@1 IN=labels.jsonl LABEL_SOURCE=public_dataset
  make calibrate EVALUATOR=groundedness@1 IN=labels.jsonl WRITE=1   # writes config/calibration
  ```

  `make bench-lite` produces labelled scores from RAGTruth (hard cost cap);
  see [EVALUATION.md](EVALUATION.md).
- **When a block is capped at flag.** With `require_calibration: true` (the
  default), every check in a route's block set needs a calibration that can
  enable blocking (see the quality table) for the current snapshot, and the
  route's thresholds in the calibrated space (adopting does that). Otherwise
  the gateway flags instead of blocking and says why:
  - `calibration_missing`: a check has no such calibration for this snapshot
    (never calibrated, or only with a quality that cannot enable blocking).
    Label answers, calibrate and adopt.
  - `uncalibrated_snapshot`: the checks that lack one were calibrated only on
    other snapshots (see the next point).

  A calibration whose fit used different state fields than the evaluator now
  declares still applies; that check's result carries `calibration_warning`
  ("calibrated with different state fields") so you know to refit.
- **Snapshot changes.** When the vendor changes the snapshot, the dashboard
  shows a banner, calibrations of the old snapshot stop applying, and
  enforcing routes are capped at flag (`uncalibrated_snapshot`) until you
  recalibrate. Pin the model per route (`decision.model`) to control when that
  happens.
- **Retention and route labels.** For an evaluator with parameters (unapproved commitments,
  off-topic, off-brand tone), **Calibrate now** fits from the route's own labels only, over the
  last 30 days. Labels carry no route themselves: they are matched through their traces, so with
  `TRUTHTABLE_RETENTION_DAYS` under 30 the labels whose traces have expired drop out of the fit.
  Keep retention at 30 days or more while you collect labels for such an evaluator.
- **The demo** ships `config/calibration/mock__jev-heuristic-v1.json`, a
  simulated calibration of the mock decision model, so `make demo` can show
  block and fallback. Its numbers are simulated and labelled as such.
