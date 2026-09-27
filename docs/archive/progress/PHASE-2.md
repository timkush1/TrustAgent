# Phase 2 — Evaluation Framework

**Status**: ✅ Complete (2026-06-10)
**Goal**: measurable detector quality with CI regression gates — the credibility
centerpiece of the roadmap. Full methodology: [docs/EVALUATION.md](../EVALUATION.md).

## What was done

### Pipeline enablers
- **Context-injection mode** in `RetrieverNode`
  (`backend-python/src/truthtable/graphs/nodes/retriever.py`): when the caller
  already supplies context documents (gRPC `AuditRequest.context` or the eval
  harness with ground-truth context), the node uses them and skips Qdrant.
  Previously caller-provided context was silently overwritten — a real bug fix,
  not just an eval convenience.
- **`MockLLMProvider`** (`providers/mock.py`, registered as `"mock"`):
  deterministic fixture replay matched on prompt *content* (all substrings in
  `match` must appear), so prompt-template changes don't invalidate fixtures.
  Unmatched prompts raise `FixtureNotFoundError` — evals never run on garbage.

### Golden set (Tier 1)
- `evals/build_golden.py`: one inline spec generates both committed artifacts —
  `datasets/golden/golden_v1.jsonl` (50 examples: 22 fact pairs × faithful/
  hallucinated + 6 multi-claim specials; 26 hallucinated / 24 faithful) and
  `fixtures/golden_v1_fixtures.json` (108 recorded decomposer/verifier
  responses).
- The recorded "model" is deliberately imperfect: 2 false negatives, 2 false
  positives, 1 UNKNOWN parse-failure, and 1 threshold-boundary case (1-of-3
  claims partially supported = 33% > the 30% detection threshold). Metrics are
  therefore non-trivial and sensitive to scoring changes.

### Harness
- `evals/run_eval.py`: CLI — dataset (golden/halueval/path), provider
  (mock/ollama), `--limit`, `--output`, `--write-baseline`, `--check-baseline`.
  Runs the **real LangGraph pipeline**; baseline comparison covers metrics AND
  every per-example prediction (zero tolerance, scores rounded to 6 decimals).
- `evals/metrics.py`: dependency-free precision/recall/F1/balanced accuracy,
  AUROC (rank-based with tie handling), ECE (10 bins).
- `evals/datasets/download.py`: HaluEval QA downloader — seeded balanced
  sampling, raw data gitignored and reproducible.

### CI wiring
- Tier-1 gate added to the python job in `ci.yml` (blocking).
- `.github/workflows/eval.yml`: weekly + manual Tier-2 benchmark with an Ollama
  service container; configurable sample size and model; metric table published
  to the job summary, full JSON report as artifact.
- Makefile: `make eval-golden`, `make eval-benchmark`.

### Tests (+15, total 36 passing)
- `tests/unit/test_mock_provider.py` (5): matching semantics, file loading,
  registry, failure on unmatched prompts.
- `tests/unit/test_eval_metrics.py` (7): perfect classifier, confusion counts,
  zero-division guards, AUROC tie handling, ECE calibrated/overconfident cases.
- `tests/unit/test_audit_graph_pipeline.py` (3): **first full-pipeline
  integration tests** — faithful/hallucinated/mixed responses through the
  compiled graph with the mock provider.
- `backend-python/conftest.py` added so tests can import the non-installed
  `evals` package.

## Decisions / deviations
- `evals/` lives in `backend-python/evals/` (not repo root as originally
  planned) so it shares the venv, linters, and `truthtable` imports.
- Golden fixtures are **synthesized, not recorded from a live model** — the
  spec defines exactly what the "model" returns, which makes the gate a pure
  pipeline-regression test and removes the need for Ollama at fixture-creation
  time. Documented in EVALUATION.md.
- RAGTruth deferred in favor of shipping HaluEval end-to-end; revisit alongside
  the Phase 5 provider comparison.

## Verification evidence
- Golden run: 50 examples in ~2s, fully deterministic across consecutive runs.
- Metrics: precision 0.889, recall 0.923, F1 0.906, AUROC 0.907, ECE 0.133;
  confusion TP=24 FP=3 TN=21 FN=2 — exactly matching the designed error
  injections.
- **Perturbation test**: changing the scorer's problematic-ratio threshold
  0.3 → 0.5 failed `--check-baseline` (exit 1) with a per-example diff naming
  the two boundary cases that flipped; reverting restored green.
- `pytest tests/unit` → 36 passed; `ruff check` clean; `black --check` clean.
