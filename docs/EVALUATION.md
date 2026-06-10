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
| Runs | Every PR (blocking, in `ci.yml`) | Weekly + on demand (`eval.yml`, `make eval-benchmark`) |
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

The `eval.yml` workflow runs this weekly (and on manual dispatch with
configurable sample size and model) in an Ollama service container, publishing
the metric table to the job summary and the full report as an artifact.

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
| `.github/workflows/eval.yml` | Tier-2 scheduled benchmark |
