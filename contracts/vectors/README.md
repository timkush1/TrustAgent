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

| Kind | Count | Status |
|---|---|---|
| `decide` | 52 | draft, hand-authored |
| `detectors` | 23 | draft, hand-authored |
| `evidence` | 14 | draft, hand-authored |
| `lint` | 19 | draft, hand-authored |

The mechanical kinds (`canonical`, `tokens`, `question_hash`, `normalize`, `compile`, `calibration_apply`,
`calibrate_fit`, `cost`, `pkce`) are produced by Go `cmd/vectorgen` from S0 on.

### `decide`

- `input`: a `decide()` input, validated against `../schema/decide-input.v1.schema.json` by
  `validate.mjs`. Each evaluator entry carries its full spec (a copy of the template, or an inline custom
  spec for question types no built-in uses). `question_map` and `meta.qid_request` cover every evaluator
  that was not excluded; `request_status` is empty when nothing was sent.
- `expected`: a subset of the `decision/v1` core (`../schema/decision.v1.schema.json`): `status`,
  `skip_reason`, `effective_mode`, `action`, `would_action`, `enforced`, `verdict`, `max_risk`,
  `calibration`, `degraded`, `disagreement`, `deep_audit`, `reasons`, and `evaluators` keyed by ref with a
  subset of `EvaluatorResult`.
- Readings of SPEC §2.9 these vectors pin (confirm or correct them when the Go reference lands):
  - `would_action` is the final `would` (after the no-evidence rule) in every mode, fail-closed included.
  - `grounding_trigger` counts a role-bearing evaluator when its level is `warn` or `critical` or its
    `escalate` is set; `not_assessed` and `error` evaluators have level `null` and do not count as
    "level ≠ ok".
  - `degraded` for `status: partial` is `error` (`partial` is not in the map, so it falls to "others").
  - "Every request failed" needs at least one request; with nothing sent, `not_assessed` or `ok` applies.
  - `risk` goes through the calibration table whenever the threshold space is `calibrated` and a table is
    present, even when its quality is not usable (`calibrated` is then `false`).
  - `corroborated` is compared only for critical, block-eligible, active evaluators.

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
