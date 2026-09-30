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
| `off_topic@1` | quality | no | 0.7 / — | no |
| `off_brand_tone@1` | quality (param `brand_voice`) | no | 0.6 / — | no |

- **Evidence.** An evaluator that requires evidence is `not_assessed` when the
  request carries none (no documents, tool results or `trustagent.context`):
  Jev is never asked a groundedness question without evidence, because its
  scores invert.
- **Corroboration.** A block-eligible evaluator blocks only when a second
  signal agrees: another critical evaluator it names, or a Tier-0 detector
  (`det:urls_not_in_evidence`, `det:numbers_not_in_evidence`, ...).
  Otherwise the reason `needs_second_signal` is added and the answer is
  flagged.

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
- Your own evaluators go in `config/evaluators/*.json` (policy as code);
  `make contracts-check` validates the built-ins, and the proxy validates
  yours at load. Evaluator Studio (drafting and testing evaluators in the
  dashboard) comes in P4.

The dashboard's **Policies** page shows each route's evaluators, their
calibration state and the block-readiness checklist, and `make doctor` lists
the routes that run below their configured mode.

## Calibration

A raw Jev probability is not a precision. **Calibration** maps it to a
measured one for one Jev **snapshot** (the model version the vendor reports),
so that "critical" can mean "at least 95% of these are real problems".

- Files: `config/calibration/*.json` (`trustagent.calibration/v1`), read from
  `TRUTHTABLE_CALIBRATION_DIR` at start and re-read every 30 s; a change
  sends the WebSocket event `calibration_updated`.
- **Quality** decides whether a calibration may enable blocking:

  | Quality | Made from | Enables block |
  |---|---|---|
  | `measured` | Human or public-dataset labels, n ≥ 200 (≥ 30 of each class) | yes |
  | `provisional` | Fewer labels, or the snapshot family's calibration | no |
  | `proxy` | Deep-audit or judge labels | no |
  | `mock` | The mock decision model | only in demo mode with the mock backend |

- **Fitting one** (paid: it needs labelled data scored by the real Jev):

  ```bash
  make gate-eval                                      # the scorer the fit uses
  make calibrate EVALUATOR=groundedness@1 IN=labels.jsonl LABEL_SOURCE=public_dataset
  make calibrate EVALUATOR=groundedness@1 IN=labels.jsonl WRITE=1   # writes config/calibration
  ```

  `make bench-lite` produces labelled scores from RAGTruth (hard cost cap);
  see [EVALUATION.md](EVALUATION.md).
- **Snapshot changes.** When the vendor changes the snapshot, the dashboard
  shows a banner, calibrations of the old snapshot stop applying, and
  enforcing routes are capped at flag (`uncalibrated_snapshot`) until you
  recalibrate. Pin the model per route (`decision.model`) to control when that
  happens.
- **The demo** ships `config/calibration/mock__jev-heuristic-v1.json`, a
  simulated calibration of the mock decision model, so `make demo` can show
  block and fallback. Its numbers are simulated and labelled as such.
