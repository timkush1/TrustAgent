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

| Kind | Count | Status | Go runner |
|---|---|---|---|
| `canonical` | 16 | frozen, generated | `internal/decision` |
| `tokens` | 14 | frozen, generated | `internal/decision` |
| `question_hash` | 11 | frozen, generated | `internal/decision` |
| `normalize` | 29 | frozen, generated | `internal/decision` |
| `compile` | 34 | frozen, generated | `internal/decision` |
| `calibration_apply` | 13 | frozen, generated | `internal/decision` |
| `decide` | 55 | frozen, hand-authored | `internal/decision` |
| `detectors` | 23 | draft, hand-authored | P2 (skipped) |
| `evidence` | 14 | draft, hand-authored | P2 (skipped) |
| `lint` | 19 | draft, hand-authored | P2 (skipped) |

`calibrate_fit` and `cost` vectors come with the Go calibration fitter (P2) and cost calculator (P4);
`pkce` vectors are TypeScript-only.

Go runs every vector in `TestVectors` (`backend-go/internal/decision/vectors_test.go`, loaded from the
`github.com/truthtable/contracts` module): kinds with a Go runner must pass in full, draft and frozen alike;
the others are skipped with the reason listed in `backend-go/internal/decision/vectors/kinds.go`.

## Generating mechanical vectors

The mechanical kinds (`canonical`, `tokens`, `question_hash`, `normalize`, `compile`, `calibration_apply`)
have `expected` values that the Go reference computes. To add one:

1. Write `<kind>/<name>.json` with `status: "draft"`, a `description`, the `input`, and `"expected": null`.
2. Run `make vectors` (Go `cmd/vectorgen`). It fills in `expected` and `generated_by` and leaves every other
   byte of the file as it was. `make vectors VECTORGEN_FLAGS=-check` only verifies.
3. Review the diff against the SPEC, then set `status: "frozen"` (by hand or with
   `VECTORGEN_FLAGS="-kinds <kind> -freeze"`).

vectorgen never rewrites a frozen vector: when the reference no longer produces its `expected` value, it
fails, and the change needs a new vector.

Built-in evaluators are referenced as `"template": "<id>@<version>"` (resolved to
`../evaluators/<id>.v<version>.json`); a custom evaluator is given inline as a full `spec`. Released
templates are immutable, so the `question_hash` vectors also pin the template files.

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
  and use UTF-16 offsets into `response`. `findings` is not pinned for `secret_pattern` (the matched text
  must be masked) or when `system_prompt_leak` fires (its offsets are unaligned).
- The rules, including every normalization and tie-break, are in `../rules/detectors.v1.json` and
  `../rules/secrets.v1.json`.

### `evidence`

- `input`: `{api_format, body, settings}`. `api_format` is `openai_chat`, `anthropic_messages` or
  `openai_completions`; `body` is the request body as the client sent it; `settings` is the route
  `evidence` block (`sources`, `system_mode`, `line_headers`, `tag_patterns`, `untrusted_tools`).
- `expected`: `{items, system_instructions, user_request, pre_dedup_items, max_marker, forwarded_body}`.
  `items` are the extracted, deduplicated items in route source order as `{id, source, text, untrusted?}`
  (`label` and `truncated` are not pinned yet); `forwarded_body` is the body sent upstream, compared as
  parsed JSON.
- The rules are in `../rules/doc-blocks.v1.json`.

### `lint`

- `input`: `{spec}`, an evaluator spec (schema-valid, though possibly breaking a hard rule).
- `expected`: `{errors, lints}`, each a sorted list of distinct codes. `errors` holds the named hard rules
  (`unresolved_state_reference`, `no_id_reference`); `lints` holds the SPEC §2.8 lint ids. Token Jaccard
  for `near_duplicate_options` uses lowercase `[a-z0-9]+` tokens of each option's `what`.
- Every built-in template has a `builtin-*-is-clean` vector: built-ins must stay lint-clean.

## Writing a vector

1. Derive `expected` from the SPEC by hand, and say how in `description` (at most 600 characters).
2. Keep `expected` to the fields the case is about; mechanical fields (hashes, token counts, offsets you
   did not compute) come from `cmd/vectorgen`.
3. Use obviously fake credentials in detector inputs (the secret patterns must still match them).
4. Run `make contracts-check`.
