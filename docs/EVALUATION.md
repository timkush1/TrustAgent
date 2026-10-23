# Evaluation Framework

How TrustAgent's hallucination detector is measured, what the numbers mean,
and how regressions are blocked in CI.

## Why two tiers

A hallucination detector has two failure surfaces:

1. **The pipeline code** — claim-extraction parsing, graph wiring, score math,
   detection thresholds. Deterministic, cheap to test, and where most
   regressions actually come from during development.
2. **The judge model** — how well the underlying LLM decomposes and verifies.
   Expensive and stochastic to test; only meaningful against public benchmarks.

Mixing these gives flaky CI and meaningless numbers, so they're separated:

| | Tier 1 — Regression gate | Tier 2 — Benchmark |
|---|---|---|
| Runs | Every PR (blocking, in `ci-python.yml`) | On demand (`eval.yml`'s Run workflow button, `make eval-benchmark`) |
| Model | None — recorded responses via `MockLLMProvider` | Live Ollama model (or cloud providers, Phase 5) |
| Dataset | Golden set, 50 hand-curated examples | HaluEval QA (sampled, balanced) |
| Measures | Pipeline behavior given fixed model outputs | Real end-to-end detector quality |
| Determinism | Bit-exact, compared to a committed baseline | Stochastic, tracked over time |

## Tier 1 — the golden regression gate

`backend-python/evals/build_golden.py` generates two committed artifacts from
one spec:

- `evals/datasets/golden/golden_v1.jsonl` — 50 examples (26 hallucinated / 24
  faithful) across geography, science, history, technology, and medicine,
  including multi-claim, partially-supported, and threshold-boundary cases.
- `evals/fixtures/golden_v1_fixtures.json` — the "recorded model": decomposer
  and verifier responses replayed by `MockLLMProvider`
  (`truthtable/providers/mock.py`), matched on prompt content so prompt
  template changes don't invalidate fixtures.

**The recorded model is deliberately imperfect.** Five fixtures return wrong
verdicts (2 false negatives, 2 false positives, 1 UNKNOWN parse-failure), and
one boundary example sits exactly on the >30%-problematic-claims threshold.
This keeps every metric non-trivial, so any change to scoring or thresholds
moves the numbers — which is the point.

```
make eval-golden
# or directly:
cd backend-python
python -m evals.run_eval --dataset golden --provider mock --check-baseline
```

The run executes the **real pipeline** (LangGraph decompose → verify → score,
context injected from the dataset) and compares metrics *and per-example
predictions* against `evals/baselines/golden_v1.json` with zero tolerance.
Any drift fails CI. If the change is intentional, regenerate with
`--write-baseline` and commit the new baseline in the same PR with an
explanation.

Current golden-tier numbers (these characterize the *pipeline + recorded
model*, not a live model):

| Metric | Value |
|---|---|
| Precision | 0.889 |
| Recall | 0.923 |
| F1 | 0.906 |
| Balanced accuracy | 0.899 |
| AUROC | 0.907 |
| ECE (calibration) | 0.133 |

Verified behaviors of the gate:
- Two consecutive runs are bit-identical.
- Perturbing the hallucination threshold in `scorer.py` (0.3 → 0.5) fails the
  gate with a per-example diff naming exactly the two boundary cases that flip.

## Tier 2 — public benchmarks

**HaluEval QA** (Li et al., 2023): 10k question/knowledge pairs, each with a
correct and an LLM-generated hallucinated answer. The downloader samples a
seeded, balanced subset and converts it to the harness schema; raw data is
never committed.

```
make eval-benchmark               # 200 samples against local Ollama
# or with custom settings:
cd backend-python
python -m evals.datasets.download halueval --samples 500 --seed 42
python -m evals.run_eval --dataset halueval --provider ollama --model llama3.2 --output report.json
```

The `eval.yml` workflow runs this on manual dispatch only (with a configurable
sample size and model) in an Ollama service container, publishing the metric
table to the job summary, labelled as that run's own measurement with that
local judge, and the full report as an artifact.

### Metrics

All implemented dependency-free in `evals/metrics.py` (positive class =
hallucinated):

- **Precision / Recall / F1 / balanced accuracy** on the binary
  `hallucination_detected` decision.
- **AUROC** using `1 − faithfulness_score` as a continuous detector score
  (rank-based Mann-Whitney formulation with tie handling).
- **ECE** (Expected Calibration Error, 10 bins) — measures whether the
  faithfulness score behaves like a probability. This matters because LLM
  verbalized confidence is known to be poorly calibrated; we report
  calibration instead of trusting it.

## Tier 3 — the gateway against LLM judges (v2 harness)

The v2 harness (`backend-python/evals/bench.py`) scores several systems on the
same examples: the gateway's fast check itself (`gate`, through the Go
`gate-eval` tool), an LLM asked the same questions (`llm_same_checks`), a full
claim-level LLM audit, and the fast check escalating to the audit (cascade).
Every paid run has a hard cost cap, checks everything that could fail before the
first paid call, and is resumable; mock output, and output from any host that
is not the vendor's (for example a local stand-in behind
`TRUTHTABLE_OPENROUTER_BASE_URL`), is labelled simulated and can never be
published.

Every paid target is a **dry run unless `CONFIRM=1`**: it prints the plan (n,
the systems, the price-table estimate, the cap, the resume key and what its
ledger has spent, the hosts, and with a key the snapshot) and spends nothing.
Each paid step lowers its cap to what the OpenRouter key has left minus $0.10
(the free `GET /api/v1/key`) and ends with one `TRUSTAGENT_P6_RESULT {json}`
line that never contains the key.

```
make gate-eval        # build backend-go/bin/gate-eval (the fast check over a JSONL file); prints its sha256
make bench-smoke      # 20 RAGTruth-QA answers, full audit 3; cap $0.30 (plan only without CONFIRM=1)
make bench-lite       # RAGTruth-QA: 200 gate + same-checks, 50 full audits; cap $5 over every session
make bench-lite CONFIRM=1 BENCH_ARGS="--retry-failed"   # continue after the stop rule (exit 3)
make calib-pass       # a natural sample of 800 RAGTruth-QA train answers through one warm gate-eval; cap $0.30
make calibrate EVALUATOR=groundedness@1 IN=<calib-pass gate.jsonl> LABEL_SOURCE=public_dataset DATASET=ragtruth:qa-train@<sha>
make calib-holdout RUN=<abs run.json>             # ECE and Brier before and after, on the bench's held-out test sample
make bench-publish RUN=<abs run.json> [HOLDOUT=<holdout-groundedness.json>] [CHECK_ONLY=1]
make eval-live        # RAGTruth-QA 400 + LLM-AggreFact 600; caps $40 + $20 (owner key)
make p6-ledger        # read-only: every step's spend against its cap and the key's usage
```

A bench run stops after 5 consecutive failures of one system (exit 3) and
prints the failure kind with its fix; the examples it never reached are not
cached, so the same command with `BENCH_ARGS="--retry-failed"` continues under
the same ledger. The gate runs first and alone, one answer at a time on a warm
connection, so its latency leaves out connection setup; the run records the
gate-eval binary's sha256 (a resume with another build is refused), the
built-in policy, the machine's memory and load, and whether tracked files were
dirty when it started.

`make calibrate` fits the fast check's calibration (isotonic, or Platt below
500 labels, with Wilson bounds for `critical_at`) from labelled risks; without
`WRITE=1` it only prints the summary. It is fitted on the RAGTruth-QA train
split (`calib-pass`) and measured on the bench's test sample
(`calib-holdout`), so the published "ECE after calibration" is held out, never
in-sample. Numbers from these runs replace the estimates on the landing page
and in the dashboard only through `bench-publish`, which needs one named run
(`RUN=`) and lists every refusal at once: mock output or a non-vendor host, a
tree that was dirty at the start, more than 5% failures or fewer than 95%
scored, more than one decision-model snapshot, an unknown cost source,
dataset text in the per-example records, or a usable `holdout-groundedness.json`
from `calib-holdout` beside the run that was not passed as `HOLDOUT=` (the
held-out ECE would go unpublished). `CHECK_ONLY=1` writes the would-be files
into `tmp/bench-check/` only.

## Honest limitations

- Golden-tier numbers measure the pipeline against *recorded* verdicts; they
  say nothing about live model quality. That's Tier 2's job.
- Tier-2 quality is bounded by the judge model. A 1B local model is a weak
  verifier; the cross-model comparison (local vs. frontier APIs) lands in
  Phase 5.
- HaluEval answers are GPT-generated circa 2023 and stylistically detectable;
  treat absolute numbers as comparable across our own runs, not against
  published leaderboards.
- The 50-example golden set guards code paths, not statistical power. Growing
  it is cheap (edit the spec in `build_golden.py`, regenerate, re-baseline).

## Files

| Path | Purpose |
|---|---|
| `backend-python/evals/run_eval.py` | CLI runner, baseline check |
| `backend-python/evals/metrics.py` | Metric implementations |
| `backend-python/evals/build_golden.py` | Golden dataset + fixture generator |
| `backend-python/evals/datasets/download.py` | HaluEval downloader/sampler |
| `backend-python/evals/baselines/golden_v1.json` | Committed regression baseline |
| `backend-python/src/truthtable/providers/mock.py` | Fixture-replay provider |
| `.github/workflows/eval.yml` | Tier-2 benchmark, started by hand |
| `backend-python/evals/bench.py` | Tier-3 harness: `run`, `export-labels` and `publish` (bench-lite, bench-smoke, eval-live) |
| `backend-python/evals/report.py` | Run documents, the publish rules and the measured.json fold |
| `backend-python/evals/calibset.py` | The calibration set, `calib-pass` and `calib-holdout` |
| `backend-python/evals/vendor_key.py` | The key's spend and limit (free `GET /api/v1/key`), the effective cap, the result line |
| `backend-python/evals/p6_ledger.py` | `make p6-ledger`: the spend of every paid step against its cap |
| `backend-python/evals/judges.py` | The systems compared (gate, LLM same checks, full audit, cascade) |
| `backend-go/cmd/gate-eval` | The fast check over a JSONL file, with a hard cost cap |
| `backend-go/cmd/calibrate` | Calibration fitting (`trustagent.calibration/v1` files) |
