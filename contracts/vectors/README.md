# contracts/vectors/

Golden vectors that hold the Go reference (`backend-go/internal/decision`) and the TypeScript port
(`frontend-react/src/lib/decision`) to the same behaviour (SPEC §7.6). The lifecycle (draft, generated,
frozen) is described in `../README.md`; this file documents the `input` and `expected` shape of each kind.

Every file is `{"vector":"1","kind","name","status","description","input","expected","tolerance"}` at
`<kind>/<name>.json` (schema `../schema/vector.v1.schema.json`). An unknown kind fails every runner.

## Comparing `expected`

- Objects in `expected` are subsets: a key that `expected` omits is not compared. This applies at every
  depth, including objects inside arrays.
- Arrays must have the same length and order; their elements are compared with these same rules.
- Numbers compare as parsed values within `tolerance` (1e-9 unless stated); strings, booleans and `null`
  compare exactly. A key present in `expected` with value `null` must be present and `null`.

## Kinds with vectors

| Kind | Count (frozen + draft) | SPEC §7.6 minimum | Expected values | Go runner |
|---|---|---|---|---|
| `canonical` | 16 (16 + 0) | 14 | generated | `internal/decision` |
| `tokens` | 14 (14 + 0) | 10 | generated | `internal/decision` |
| `question_hash` | 11 (11 + 0) | 1 per template (11) | generated | `internal/decision` |
| `normalize` | 29 (29 + 0) | 18 | generated | `internal/decision` |
| `compile` | 34 (34 + 0) | 1 per template + 12 (23) | generated | `internal/decision` |
| `calibration_apply` | 13 (13 + 0) | 10 | generated | `internal/decision` |
| `calibrate_fit` | 37 (37 + 0) | 10 | generated | `internal/decision` |
| `decide` | 55 (55 + 0) | 40 | hand-authored | `internal/decision` |
| `detectors` | 53 (53 + 0) | 40, at least 5 per detector | hand-authored | `internal/decision` |
| `evidence` | 23 (19 + 4) | 20 | hand-authored | `internal/evidence` (`openai_chat` only) |
| `lint` | 19 (0 + 19) | 14 | hand-authored | P2 (skipped) |

`cost` vectors come with the cost calculator
(`internal/costs`, P4, which also defines their input); `pkce` vectors are TypeScript-only.

Go runs every vector in `TestVectors` (`backend-go/internal/decision/vectors_test.go`, loaded from the
`github.com/truthtable/contracts` module): kinds with a Go runner must pass in full, draft and frozen alike;
the others are skipped with the reason listed in `backend-go/internal/decision/vectors/kinds.go`. The
`evidence` vectors run in `TestEvidenceVectors` (`backend-go/internal/evidence`) instead: the `openai_chat`
ones must pass there and are frozen; the `anthropic_messages` and `openai_completions` ones stay draft until
those formats are extracted (P2).

A vector becomes frozen only once the Go reference passes it and its `expected` value has been reviewed
against the SPEC (SPEC §4.1). The section [SPEC §7.6 coverage](#spec-76-coverage) maps every required case
to its vectors.

## Generating and freezing vectors

The mechanical kinds (`canonical`, `tokens`, `question_hash`, `normalize`, `compile`, `calibration_apply`,
`calibrate_fit`)
have `expected` values that the Go reference computes. To add one:

1. Write `<kind>/<name>.json` with `status: "draft"`, a `description`, the `input`, and `"expected": null`.
2. Run `make vectors` (Go `cmd/vectorgen`). It fills in `expected` and `generated_by` and leaves every other
   byte of the file as it was. `make vectors VECTORGEN_FLAGS=-check` only verifies.
3. Review the diff against the SPEC, then set `status: "frozen"` (by hand or with
   `VECTORGEN_FLAGS="-kinds <kind> -freeze"`).

The semantic kinds with a Go runner (`decide`, `detectors`) are written by hand, `expected` included.
`make vectors` checks them against the reference too (a disagreement fails, draft or frozen) but never
writes their `expected`. After reviewing a draft, freeze it with `VECTORGEN_FLAGS="-kinds <kind> -freeze"`:
semantic drafts are frozen only when their kind is named in `-kinds`. `evidence` drafts are frozen by hand
once `TestEvidenceVectors` passes them.

vectorgen never rewrites a frozen vector: when the reference no longer produces its `expected` value, it
fails, and the change needs a new vector.

Built-in evaluators are referenced as `"template": "<id>@<version>"` (resolved to
`../evaluators/<id>.v<version>.json`); a custom evaluator is given inline as a full `spec`. Released
templates are immutable, so the `question_hash` vectors also pin the template files.

## Input and expected, by kind

### `canonical`

- `input`: `{value}`, any JSON value. Runners parse the file with their JSON parser (Go `encoding/json`,
  `JSON.parse`) and canonicalize the parsed value.
- `expected`: `{jcs, sha256}`: the canonical text (SPEC §2.1: RFC 8785, U+2028 and U+2029 as six-character
  escapes, lone surrogates as U+FFFD) and the lowercase hex SHA-256 of its UTF-8 bytes.

### `tokens`

- `input`: `{value, cpt}`.
- `expected`: `{narrow, wide, est}`: the code points of the canonical text of `value` below and at or above
  U+3000, and `est = ceil(narrow/cpt + wide)` (SPEC §2.9 Tokens). A string is estimated with its quotes,
  since its canonical text carries them.

### `question_hash`

- `input`: `{template | spec, cpt}`.
- `expected`: `{question_id, spec_sha256, question_sha256, calibration_key, question_tokens}`:
  `<id>__v<version>`, `sha256(JCS(spec))`, `sha256(JCS(spec.question))`,
  `sha256(JCS({question, signal}))` (SPEC §2.11) and `est(question)` at `cpt`. Hashes cover the spec as
  written: schema defaults are not filled in.

### `normalize`

- `input`: `{template | spec, answer}`; `answer` is a `RawAnswer` or `null`.
- `expected`: `{error, probabilities, risk_raw, top, vendor_confidence, confidence_source, anomalies}`.
  With an `error` (`missing_answer`, `unexpected_option`, `invalid_distribution`, `probabilities_missing`)
  every other field is `null` and `anomalies` is empty. `probabilities` lists every option (choice) or
  level `"0".."n-1"` (score), renormalized; it is `null` for noul, and so is `top`.
- Readings of SPEC §2.9 these vectors pin:
  - Every tolerance compares as `x > tol + EPS`: a foreign key is an error above `0.01 + EPS`, the sum
    check uses `max(0.10, 0.005*k) + EPS`, and `sum_out_of_range` needs `|s - 1| > 0.02 + EPS`.
  - Ties for `top` are within `EPS`.
  - A choice answer without `probabilities` uses `{choice: 1}` (`confidence_source: choice_only`); a choice
    outside the options then fails as `unexpected_option`, and no choice at all is `missing_answer`.
  - A score answer is `probabilities_missing` when `probabilities` is absent or has no level key
    `"0".."n-1"`; other keys follow the choice rule (dropped at or below 0.01, `unexpected_option` above).
  - `confidence_source` is `vendor` when the answer has a `confidence`, else `fallback_top_prob` for choice
    and score and `null` for noul. The answer's `type` and the fractional `score` are never read.

### `compile`

- `input`: `{route, specs?, inputs, budget}`.
  - `route`: `{evaluators: [{ref, stage?, enabled?, params?}], on_no_evidence, evidence_sources, client_rules,
    model}`; `evidence_sources` is the route `evidence.sources` order (`source_rank`), `client_rules` is
    `overrides.client_rules` and `model` the decision model.
  - `specs`: inline specs by ref, for evaluators that are not built-in templates (templates resolve by ref;
    `factual_claims@1` always resolves).
  - `inputs`: `{user_request, assistant_response, system_instructions, evidence: [{id, source, text,
    untrusted?}], rules}`: the texts of one exchange, the priority-ordered pre-packing evidence, and the
    request's `trustagent.rules`.
  - `budget`: `{tokens, cpt, max_questions, overhead, max_response_tokens, evidence_max_tokens}`.
- `expected`: the compile result: `{requests: [{model, state}], evaluators: [{ref, stage, internal}],
  question_map, qid_request, excluded, packed_ids, truncated_ids, truncated, cpt, est_tokens,
  request_sha256, question_set_sha256}`. `requests[k].questions` is left out: `request_sha256[k]` pins the
  whole body `{model, state, questions}` and `qid_request` says which question went where.
- Readings of SPEC §2.9 these vectors pin:
  - `est_tokens[k] = overhead + est({model, state, questions})`; the split test uses
    `overhead + est(state) + sum est(q)` over the question bodies.
  - Untrusted items are removed before the pre-pack gate: they are never evidence.
  - `rules` holds the declared params of the included evaluators that read `rules`: the request value when
    `client_rules` is on, else the route value, else the default. Undeclared request rules are ignored. An
    empty value, and an empty `rules` object, become `"(none provided)"`, as do empty text fields.
  - `user_request` is shortened only when it has more code points than the kept head and tail together;
    `system_instructions` is cut to its head without a marker.
  - The state is rebuilt after the post-pack gate, so fields that only excluded evaluators read are dropped.
  - `truncated_ids` lists items cut short; a skipped item sets `truncated` without an id. `packed_ids` is
    the packing result even when the post-pack gate then drops every evaluator that reads evidence.
  - The implicit `factual_claims@1` is added when no enabled evaluator has id `factual_claims`.
  - Split: first-fit-decreasing by `est(q)`, ties by question id; requests are numbered in the order they
    are opened; a question that fits no open request opens a new one, which fails compile only above
    28800 tokens.

### `calibration_apply`

- `input`: `{table, x}`: a 101-point table and a raw risk (clamped to [0,1] first).
- `expected`: `{y}`, the interpolated calibrated risk (SPEC §2.9 calibration apply).

### `calibrate_fit`

The calibration fitter (SPEC §2.11, §7.3; `backend-go/internal/decision/calibrate*.go`). `input.op` selects
what is run; labels are given either as `samples: [{risk_raw, label}]` (`label: true` = the evaluator should
fire) or as `groups: [{risk_raw, pos, neg}]`, which expand in order to `pos` positives then `neg` negatives
at that risk. Input order matters: every sum runs in input order.

| `op` | Other input | `expected` |
|---|---|---|
| `wilson` | `k, n` | `{lo, hi}`: the Wilson score interval at z = 1.959963984540054 |
| `isotonic` | labels | `{blocks: [{x_lo, x_hi, value, n}], table}` |
| `platt` | labels | `{platt: {a, b}, table, warnings}` |
| `threshold` | labels, `objective` (`max_f1` \| `min_threshold_at_precision`), `min_precision?` | `{result: null \| {threshold, tp, fp, tn, fn, precision, recall, f1}}` |
| `critical_at` | labels, `min_predicted_pos?` (50), `min_precision_lb?` (0.95) | `{critical_at: null \| {value, precision_lb95, predicted_pos}, critical_null_reason}` |
| `quality` | `snapshot, label_source, n_pos, n_neg, family?` | `{quality}` |
| `quick` | a `QuickCalibrationRequest` (`labeled, objective?, target_precision?, current?`) | the `QuickCalibration` without `message` (UI copy is not pinned) |
| `fit` | labels, `method?` (`auto` \| `isotonic` \| `platt`) | `{method, table, platt, n, n_pos, n_neg, metrics, reliability, recommended, cv_brier, warnings}` |

- Readings of SPEC §2.11 and §7.3 these vectors pin:
  - An answer fires at threshold t when `risk >= t - EPS`. Candidate thresholds are the distinct risks; risks
    within EPS of their sorted neighbour form one candidate (its smallest risk) and one isotonic group.
  - Max-F1 compares F1 exactly (integer cross-multiplication); a tie goes to the higher threshold. It needs a
    true positive. `min_threshold_at_precision` is the smallest candidate with precision `>= target - EPS`.
  - `critical_at` is the smallest candidate t such that every candidate `t' >= t` with at least
    `min_predicted_pos` predicted positives has a Wilson lower bound `>= 0.95 - EPS`; candidates with fewer
    predicted positives are not judged. Otherwise `null` with `"insufficient evidence to block"`.
  - PAV pools adjacent blocks while the earlier share of positives is `>=` the later one (compared exactly), so
    blocks are maximal and strictly increasing. The table interpolates linearly between block ends and clips
    outside them.
  - Platt is `calibrated(x) = 1/(1 + exp(-(a·x + b)))` on the raw risk, fitted by Newton's method on Platt's
    smoothed targets (N+ + 1)/(N+ + 2) and 1/(N- + 2) from `a = 0, b = log((N+ + 1)/(N- + 1))`, to
    convergence. A negative slope, or risks all within 1e-9, give the flat `a = 0, b = logit(mean target)`
    with a warning. `a` and `b` are R6-rounded (`floor(x·1e6 + 0.5)/1e6`) and the table is computed from the
    rounded parameters.
  - Every table is R6-rounded; Platt vectors carry `tolerance: 1e-6` for `exp`/`log` differences between
    runtimes.
  - `fit`: at least 8 labels with 3 of each class. `auto` uses Platt below 500 labels; from 500 it compares
    5-fold cross-validated Brier scores (folds by rank mod 5 after a stable sort by risk, then label) and keeps
    the lower (isotonic on a tie). Metrics are in-sample: ECE (10 equal-width bins, bin
    `min(floor(10p), 9)`) and Brier before (raw risk) and after (the table applied as decide() applies it),
    AUROC of the raw risk with ties counting one half. `reliability` lists the non-empty bins of the
    calibrated predictions. Recommended thresholds are in calibrated space: `warn_at` is max-F1 with precision
    `>= 0.8`, `critical_at` as above.
  - `quick`: needs `n >= 8` with 3 of each class (else `enough: false` and only counts and the grid). The
    threshold is raw-space; `precision` and `recall` carry Wilson intervals; `loo_range` is the range of
    thresholds the same objective picks with each label left out; `unstable` when an interval is wider than
    `0.4 + EPS` or the range exceeds `0.15 + EPS`. `mapping` is Platt from 30 labels and isotonic from 50 with
    15 of each class. `grid` has a row per candidate plus `current` when it is not a candidate.
  - Quality: `mock` for a `mock/*` snapshot; `proxy` with any uniform-audit or judge label; `provisional` for a
    family fallback; `measured` with human and public-dataset labels only and `n >= 200`, `n_pos >= 30`,
    `n_neg >= 30`; `provisional` otherwise.

### `decide`

- `input`: a `decide()` input, validated against `../schema/decide-input.v1.schema.json` by
  `validate.mjs`. Each evaluator entry carries its full spec (a copy of the template, or an inline custom
  spec for question types no built-in uses). `question_map` and `meta.qid_request` cover every evaluator
  that was not excluded; `request_status` is empty when nothing was sent.
- `expected`: a subset of the `decision/v1` core (`../schema/decision.v1.schema.json`): `status`,
  `skip_reason`, `effective_mode`, `action`, `would_action`, `enforced`, `verdict`, `max_risk`,
  `calibration`, `degraded`, `disagreement`, `deep_audit`, `reasons`, and `evaluators` keyed by ref with a
  subset of `EvaluatorResult`.
- Readings of SPEC §2.9 these vectors pin (the Go reference implements every one):
  - `would_action` is the final `would` (after the no-evidence rule) in every mode, fail-closed included.
  - `grounding_trigger` counts a role-bearing evaluator when its level is `warn` or `critical` or its
    `escalate` is set; `not_assessed` and `error` evaluators have level `null` and do not count as
    "level ≠ ok".
  - `degraded` for `status: partial` is `error` (`partial` is not in the map, so it falls to "others").
  - "Every request failed" needs at least one request; with nothing sent, `not_assessed` or `ok` applies.
  - `risk` goes through the calibration table whenever the threshold space is `calibrated` and a table is
    present, even when its quality is not usable (`calibrated` is then `false`).
  - `corroborated` is compared only for critical, block-eligible, active evaluators.
  - An evaluator whose request status is missing is judged by its answer; a missing answer is
    `missing_answer`. A failed request's status (`timeout`, `error:<kind>`) is the evaluator's `reason`.
  - With the no-evidence rule applied, the calibration cap treats a missing or non-assessed
    `factual_claims` as uncalibrated.
- The `s0-*` vectors are the three S0 demo scenarios (`../demo/scenarios.v1.json`) on the `support-bot`
  route of `config/policies.yaml`.

### `detectors`

- `input`: `{detector, response, evidence, user_request, system_instructions, rules, evidence_meta?, config?}`.
  `evidence` is the full pre-packing item list `[{id, source, text, untrusted?}]`; `rules` maps param
  names to values; `evidence_meta` is `{pre_dedup_items, max_marker}` (citation range, defaults: the
  length of `evidence` and 0); `config` is `{min_integer}` (default 10). Only the named detector runs.
- `expected`: a `DetectorResult` without `id`: `{ran, fired, findings}`. Findings are ordered by `start`
  and use UTF-16 offsets into `response`; `validate.mjs` checks that a pinned finding selects its `text`.
  `secret_pattern` findings pin `start` and `end` only (the matched text must be masked, and the mask is
  not part of the contract), and `findings` is not pinned when `system_prompt_leak` fires (its finding is
  unaligned, and which window is reported is not specified).
- Readings of SPEC §2.10 these vectors pin:
  - "Evidence is present" means trusted evidence: with only untrusted items, the URL, quote and citation
    detectors do not run, and numbers and dates run only when `rules` is non-empty.
  - Numbers compare as normalized text, not as values: `4.50` is not `4.5`, `12.0` is `12`.
  - The unit window is the next two `window_token` matches after the number, whatever they are.
  - A quote cannot span two reference parts: the normalized parts are joined with a newline.
  - `system_prompt_leak` measures the 200-code-point minimum after normalization.
- The rules, including every normalization and tie-break, are in `../rules/detectors.v1.json` and
  `../rules/secrets.v1.json`.

### `evidence`

- `input`: `{api_format, body, settings}`. `api_format` is `openai_chat`, `anthropic_messages` or
  `openai_completions`; `body` is the request body as the client sent it; `settings` is the route
  `evidence` block (`sources`, `system_mode`, `line_headers`, `tag_patterns`, `untrusted_tools`).
- `expected`: `{items, system_instructions, user_request, pre_dedup_items, max_marker, forwarded_body}`.
  `items` are the extracted, deduplicated items in route source order as `{id, source, text, untrusted?}`
  (`label` and `truncated` are not pinned yet); `forwarded_body` is the body sent upstream, compared as
  parsed JSON. Like every object in `expected` it is a subset, so it shows the members that are kept but
  cannot prove that `trustagent` was removed; runner tests check the removal.
- The rules are in `../rules/doc-blocks.v1.json`.

### `lint`

- `input`: `{spec}`, an evaluator spec (schema-valid, though possibly breaking a hard rule).
- `expected`: `{errors, lints}`, each a sorted list of distinct codes. `errors` holds the named hard rules
  (`unresolved_state_reference`, `no_id_reference`); `lints` holds the SPEC §2.8 lint ids. Token Jaccard
  for `near_duplicate_options` uses lowercase `[a-z0-9]+` tokens of each option's `what`.
- Every built-in template has a `builtin-*-is-clean` vector: built-ins must stay lint-clean.

## SPEC §7.6 coverage

Every case SPEC §7.6 requires, and the vectors that pin it.

- **canonical** (14): U+2028/2029 `line-and-paragraph-separators-escaped`; astral
  `astral-characters-stay-literal`; control characters `control-characters-escaped`; `<>&`
  `html-characters-not-escaped`; key order `key-order-utf16-code-units`,
  `whitespace-removed-and-nested-keys-sorted`; lone surrogate `lone-surrogates-become-replacement-character`;
  1.0 `number-one-point-zero`; 0.1 `number-zero-point-one`; 1e-7 `number-small-exponent`; 1e21
  `number-large-exponent`; -0 `number-negative-zero`.
- **tokens** (10, CJK): `cjk-ideographs-count-as-whole-tokens`, `mixed-cjk-and-ascii`,
  `kana-and-fullwidth-are-wide`, `state-with-cjk-evidence`, plus 10 more.
- **question_hash** (1 per template): `template-<id>` for all 11 built-ins, each with `calibration_key`.
- **normalize** (18): sums 0.99 `choice-sum-0.99-renormalized`, 1.01 `choice-sum-1.01-renormalized`, 0.90
  `choice-sum-0.90-anomaly`, 1.10 `choice-sum-1.10-anomaly`, 0 `choice-sum-zero-invalid`; unexpected key
  `choice-unexpected-option`, `score-foreign-level-key`; missing option `choice-missing-options-filled`;
  choice-only `choice-only-answer`; tie-break `choice-tie-smallest-key`, `choice-tie-vendor-choice-wins`;
  score re-key `score-rekeyed-levels`; invert `noul-inverted-signal`.
- **compile** (1 per template + 12): `template-<id>` for all 11 built-ins; no evidence
  `no-evidence-excludes-required`; too long `response-too-long`; optional placeholder
  `optional-evidence-placeholder`; packing order `packing-order-by-source-rank-and-overlap`; truncation
  `truncation-of-an-item`; pack-to-zero `pack-to-zero-evidence-packed-away`; split by budget
  `split-by-token-budget` and by count `split-by-question-count`; params `params-route-override-and-default`;
  `client_rules` off `client-rules-ignored-when-off`; `factual_claims` implicit `factual-claims-implicit`;
  untrusted tool `untrusted-tool-never-enters`.
- **calibration_apply** (10): 13 vectors, from `zero` and `top-end` to interpolation inside a step.
- **calibrate_fit** (10): PAV `isotonic-pools-violators`, `isotonic-monotone-labels-keep-their-blocks`,
  `isotonic-equal-shares-merge-into-one-block`, `isotonic-risks-within-eps-pool-first`,
  `isotonic-decreasing-labels-pool-to-the-base-rate`; Platt `platt-symmetric-labels`,
  `platt-anticorrelated-slope-clamped`, `platt-one-risk-is-flat`, `platt-separable-labels-stay-finite`;
  max-F1 tie-break `threshold-max-f1-tie-goes-to-higher-threshold`, plus `threshold-max-f1-precision-floor`,
  `threshold-min-at-precision`, `threshold-none-without-positives`; Wilson `wilson-eight-of-ten`,
  `wilson-zero-successes`, `wilson-all-successes`, `wilson-no-trials`,
  `wilson-eighty-of-eighty-clears-block-bound`; LOO `quick-max-f1-with-loo-range-unstable`,
  `quick-min-threshold-at-precision-with-current`, with `quick-not-enough-labels`,
  `quick-platt-mapping-from-30-labels`, `quick-isotonic-mapping-from-50-labels`; Wilson-LB `critical_at`
  `critical-at-every-higher-threshold-must-pass`, `critical-at-extends-down-while-bound-holds`,
  `critical-at-null-with-too-few-predicted-positives`, `critical-at-null-when-highest-judgeable-fails`,
  `fit-isotonic-recommends-wilson-critical-at`; quality derivation `quality-measured-public-dataset`,
  `quality-provisional-below-class-minimum`, `quality-proxy-with-one-judge-label`,
  `quality-proxy-with-uniform-audit-labels`, `quality-mock-snapshot`, `quality-family-fallback-is-provisional`;
  the full fit `fit-auto-below-500-uses-platt`, `fit-isotonic-below-500-warns`,
  `fit-auto-at-500-compares-cv-brier`.
- **decide** (40 required cases):

  | Required case | Vectors |
  |---|---|
  | Groundedness alone cannot block | `groundedness-alone-cannot-block` |
  | Groundedness plus contradiction blocks | `groundedness-and-contradiction-block` |
  | Groundedness plus the URL detector blocks | `groundedness-and-url-detector-block` |
  | A baseless answer is not a disagreement | `baseless-answer-is-not-disagreement` |
  | Contradiction critical, groundedness ok is a disagreement | `contradiction-critical-groundedness-ok-is-disagreement` |
  | `needs_second_signal` | `pii-secret-needs-second-signal`, `groundedness-alone-cannot-block` |
  | Observe records `would_action` | `observe-records-would-action` |
  | No evidence: nothing sent, `not_assessed` | `no-evidence-nothing-sent-not-assessed`, `s0-no-evidence-not-assessed` |
  | `on_no_evidence` flag and block, with the `factual_claims` calibration cap | `on-no-evidence-flag`, `on-no-evidence-block-with-calibrated-factual-claims`, `on-no-evidence-block-caps-uncalibrated-factual-claims`, `on-no-evidence-fired-by-number-detector` |
  | Timeout fail-open | `timeout-fail-open` |
  | Fail-closed: timeout, `skipped:budget`, `skipped:breaker`, partial with a block-eligible error | `timeout-fail-closed-blocks`, `skipped-budget-fail-closed-blocks`, `skipped-breaker-fail-closed-fallback-route-blocks`, `partial-block-eligible-error-fail-closed-blocks` |
  | `sampled_out` is not degraded | `sampled-out-not-degraded` |
  | Fallback mapping | `fallback-mode-maps-block-to-fallback`, `fallback-mode-keeps-flag` |
  | Uncalibrated cap to flag | `uncalibrated-snapshot-caps-block-to-flag` |
  | Raw space when calibration is active but not adopted | `calibration-active-but-not-adopted-uses-raw-risk` |
  | Mock quality counts only with the mock backend | `mock-calibration-counts-with-mock-backend`, `mock-calibration-ignored-with-live-backend` |
  | `det_block` is not capped | `det-block-not-capped` |
  | Shadow critical does not flag and is still listed; shadow does not corroborate | `shadow-critical-does-not-flag-but-is-listed`, `shadow-does-not-corroborate` |
  | `min_confidence` on aggregated risk | `min-confidence-uses-aggregated-risk` |
  | Missing choice confidence | `missing-choice-confidence` |
  | 0.99 distribution; invalid distribution | `distribution-sum-0.99-is-renormalized`, `invalid-distribution-is-evaluator-error` |
  | `warn_at`, `critical_at`, `escalate_from` edges with an override | `threshold-edge-warn-at-override`, `threshold-edge-critical-at-override-and-explicit-null`, `threshold-edge-escalate-from-override` |
  | `numeric_or_date_unverified` | `numeric-or-date-unverified-escalates-without-flagging` |
  | `on_error` ignore | `on-error-ignore-vs-escalate` |
  | Truncated evidence caps groundedness, not contradiction | `truncated-evidence-caps-groundedness-not-contradiction` |
  | Knowledge-only cap | `knowledge-only-caps-role-evaluators` |
  | Verdict precedence: contradicted, then unsupported, then unknown | `verdict-contradicted-beats-unsupported`, `partial-request-timeout-fail-open` (unsupported over an error), `verdict-unknown-when-role-evaluator-errors` |
  | Per-request partial failure | `partial-request-timeout-fail-open` |
  | Stream passthrough downgrade | `stream-passthrough-downgrades-block-to-flag` |
  | Reasons ordering | `reasons-ordering`, `reasons-tie-broken-by-evaluator-id` |
  | Score `risk_levels` | `score-risk-levels` |
  | Invert noul | `invert-noul` |
  | `deep_audit` classes | high `grounding-detector-alone-is-high`; sample `clean-pass-is-sampled`, `evidence-packed-away-not-assessed-is-sampled`, `sampled-out-not-degraded`; sample_error `timeout-fail-open`; none `no-evidence-nothing-sent-not-assessed` |

- **detectors** (40, at least 5 per detector): numbers 11, dates 8, URLs 7, quotes 7, citation 6, secret 7,
  system prompt leak 7. UTF-16 with emoji `numbers-utf16-offsets-after-emoji`, `dates-utf16-offsets-after-emoji`,
  `urls-path-and-query-keep-their-case`, `quotes-utf16-offsets-after-emoji`; rules in the known set
  `numbers-known-from-rules-after-normalization`, `dates-known-from-user-request-and-rules-only`,
  `urls-known-from-user-request-and-rules`; unit exception `numbers-small-integer-with-unit-is-checked`,
  `numbers-unit-window-is-two-tokens`, `numbers-currency-prefix-checks-small-integers`; citation marker range
  `citation-range-from-document-markers`, `citation-range-counts-items-before-dedup`.
- **evidence** (20): content parts `developer-role-and-content-parts`, `assistant-messages-are-not-evidence`;
  developer role `developer-role-and-content-parts`; Anthropic system and `tool_result`
  `anthropic-system-string-and-tool-result`, `anthropic-system-blocks-markdown-section`,
  `anthropic-system-blocks-and-tool-result-parts`; tags `system-document-tags-with-ids`,
  `tag-ids-valid-quoted-and-length-limits`, `route-tag-patterns-and-crlf`; markers
  `marker-blocks-dedup-and-max-marker`; markdown `markdown-section-runs-to-same-level-heading`,
  `anthropic-system-blocks-markdown-section`; line headers `line-headers-on`, `line-headers-off`,
  `line-header-ends-at-markdown-heading`; extension strip `extension-context-ids-and-strip`,
  `route-source-order-and-unconfigured-sources`; dedup `dedup-across-sources-first-wins`,
  `marker-blocks-dedup-and-max-marker`.
- **lint** (14, every built-in clean): `builtin-<id>-is-clean` for all 11 built-ins, plus 8 lint and hard-rule
  cases.

## Writing a vector

1. Derive `expected` from the SPEC by hand, and say how in `description` (at most 600 characters).
2. Keep `expected` to the fields the case is about; mechanical values (hashes, token counts) belong in a
   mechanical kind, where `cmd/vectorgen` computes them. Detector offsets are UTF-16 code units, which are
   JavaScript string indices: compute them with `text.indexOf(...)`, not by counting bytes or code points.
3. Use obviously fake credentials in detector inputs (the secret patterns must still match them).
4. Run `make contracts-check`, then the Go runner (`make vectors VECTORGEN_FLAGS=-check`, or
   `TestEvidenceVectors` for evidence); freeze only after both pass and the vector has been reviewed.
