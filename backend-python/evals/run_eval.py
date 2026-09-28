"""
Evaluation runner.

Runs the real audit pipeline (LangGraph: preflight -> decompose -> verify ->
score) over a labeled dataset and reports hallucination-detection metrics.
Context documents from the dataset are injected directly, tagged with their
evidence source (request_context unless the example says otherwise, F-L7), so
no Qdrant instance is needed.

Tier 1 (CI regression gate, deterministic, no model needed; ADR-001):
    python -m evals.run_eval --dataset golden_v2 --provider mock --check-baseline
    python -m evals.run_eval --dataset golden --provider mock --check-baseline   # golden_v1

Tier 2 (benchmark with a live model):
    python -m evals.datasets.download halueval --samples 100
    python -m evals.run_eval --dataset halueval --provider ollama --model llama3.2

Every example runs on its own (D17): an audit that raises is recorded as
failed with its error kind, and a FAILED audit (e.g. a judge outage) is
counted rather than scored. The detection metrics cover the audits that
finished: "hallucinated" is verdict HALLUCINATED, and the ranking score is
scores.p_hallucinated (0 for an answer without claims).

Exit codes: 0 = success, 1 = baseline mismatch or run failure.
"""

import argparse
import asyncio
import json
import logging
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional

from truthtable.graphs.audit_graph import AuditStepError, build_audit_graph, run_audit
from truthtable.providers import (
    AnthropicProvider,
    OllamaProvider,
    OpenAIProvider,
)

from .metrics import compute_metrics
from .recorded_judge import RecordedJudge

logger = logging.getLogger(__name__)

EVALS_DIR = Path(__file__).parent
DATASETS_DIR = EVALS_DIR / "datasets"
FIXTURES_DIR = EVALS_DIR / "fixtures"
BASELINES_DIR = EVALS_DIR / "baselines"
HALUEVAL_DATASET = DATASETS_DIR / "halueval" / "halueval_qa.jsonl"

# Evidence source of an injected document when the example does not name one.
DEFAULT_CONTEXT_SOURCE = "request_context"

# Scores are rounded before baseline comparison so the gate is robust to
# floating-point noise but catches real logic changes.
SCORE_DECIMALS = 6


@dataclass(frozen=True)
class GoldenSet:
    """A committed golden dataset with its recorded judge and baseline."""

    dataset: Path
    fixtures: Path
    baseline: Path


GOLDEN_V1 = GoldenSet(
    dataset=DATASETS_DIR / "golden" / "golden_v1.jsonl",
    fixtures=FIXTURES_DIR / "golden_v1_fixtures.json",
    baseline=BASELINES_DIR / "golden_v1.json",
)
GOLDEN_V2 = GoldenSet(
    dataset=DATASETS_DIR / "golden" / "golden_v2.jsonl",
    fixtures=FIXTURES_DIR / "golden_v2_fixtures.json",
    baseline=BASELINES_DIR / "golden_v2.json",
)
GOLDEN_SETS: Dict[str, GoldenSet] = {
    "golden": GOLDEN_V1,
    "golden_v1": GOLDEN_V1,
    "golden_v2": GOLDEN_V2,
}

# Kept for callers of the v1 module constants.
GOLDEN_DATASET = GOLDEN_V1.dataset
GOLDEN_FIXTURES = GOLDEN_V1.fixtures
GOLDEN_BASELINE = GOLDEN_V1.baseline


def resolve_dataset(name: str) -> Path:
    if name in GOLDEN_SETS:
        return GOLDEN_SETS[name].dataset
    if name == "halueval":
        if not HALUEVAL_DATASET.exists():
            sys.exit(
                f"HaluEval dataset not found at {HALUEVAL_DATASET}. "
                "Run: python -m evals.datasets.download halueval"
            )
        return HALUEVAL_DATASET
    path = Path(name)
    if path.exists():
        return path
    sys.exit(f"Unknown dataset {name!r} (not a known name or existing path)")


def baseline_path(name: str) -> Path:
    """The committed baseline of a golden dataset (another dataset has none)."""
    golden = GOLDEN_SETS.get(name)
    if golden is None:
        sys.exit(f"Dataset {name!r} has no committed baseline (golden sets only)")
    return golden.baseline


def load_dataset(path: Path, limit: int = 0) -> List[Dict[str, Any]]:
    examples = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                examples.append(json.loads(line))
    if limit:
        examples = examples[:limit]
    return examples


def build_provider(args: argparse.Namespace):
    if args.provider == "mock":
        golden = GOLDEN_SETS.get(args.dataset, GOLDEN_V1)
        return RecordedJudge.from_file(args.fixtures or golden.fixtures)
    if args.provider == "ollama":
        return OllamaProvider(model=args.model, base_url=args.ollama_url)
    if args.provider == "openai":
        return OpenAIProvider(model=args.model)  # key from OPENAI_API_KEY
    if args.provider == "anthropic":
        return AnthropicProvider(model=args.model)  # key from ANTHROPIC_API_KEY
    sys.exit(f"Unknown provider {args.provider!r}")


def _round(value: Optional[float]) -> Optional[float]:
    return None if value is None else round(float(value), SCORE_DECIMALS)


def context_sources(example: Dict[str, Any]) -> List[str]:
    """The evidence source of each of an example's documents."""
    sources = example.get("context_sources")
    documents = example.get("context") or []
    if isinstance(sources, list) and len(sources) == len(documents):
        return [str(source) for source in sources]
    return [str(example.get("context_source") or DEFAULT_CONTEXT_SOURCE)] * len(documents)


def _claim_record(verification: Dict[str, Any]) -> Dict[str, Any]:
    status = verification.get("status")
    record: Dict[str, Any] = {
        "status": str(getattr(status, "name", status) or "").upper(),
        "confidence": _round(verification.get("confidence") or 0.0),
        "span": [
            verification.get("span_start", -1),
            verification.get("span_end", -1),
            verification.get("span_method", "none"),
        ],
    }
    if verification.get("error"):
        record["error_kind"] = verification.get("error_kind", "")
    return record


def example_result(example: Dict[str, Any], state: Dict[str, Any]) -> Dict[str, Any]:
    """The per-example record compared against the baseline."""
    scores = state.get("scores") or {}
    verdict = state.get("verdict")
    status = state.get("audit_status") or "completed"
    return {
        "id": example["id"],
        "label_hallucinated": bool(example["label_hallucinated"]),
        "predicted_hallucinated": verdict == "HALLUCINATED",
        "status": status,
        "verdict": verdict,
        "error_kind": state.get("error_kind"),
        "degraded_reasons": list(state.get("degraded_reasons") or []),
        "unsupported_policy": state.get("unsupported_policy"),
        "faithfulness_score": _round(state.get("faithfulness_score") or 0.0),
        "trust_score": _round(scores.get("trust_score")),
        "p_hallucinated": _round(scores.get("p_hallucinated")),
        "claims": [_claim_record(v) for v in state.get("claim_verifications") or []],
    }


def failed_result(example: Dict[str, Any], exc: BaseException) -> Dict[str, Any]:
    """The record of an example whose audit raised (the audit FAILED)."""
    error_kind = exc.error_kind if isinstance(exc, AuditStepError) else "internal"
    return {
        "id": example["id"],
        "label_hallucinated": bool(example["label_hallucinated"]),
        "predicted_hallucinated": False,
        "status": "failed",
        "verdict": None,
        "error_kind": error_kind,
        "degraded_reasons": [],
        "unsupported_policy": None,
        "faithfulness_score": 0.0,
        "trust_score": None,
        "p_hallucinated": None,
        "claims": [],
    }


def risk_score(result: Dict[str, Any]) -> float:
    """P(hallucinated) used for AUROC and ECE: p_hallucinated, 0 without claims."""
    p_hallucinated = result.get("p_hallucinated")
    return float(p_hallucinated) if p_hallucinated is not None else 0.0


def summarize(examples: List[Dict[str, Any]], results: List[Dict[str, Any]]) -> Dict[str, Any]:
    """Outcome counts, agreement with the expected outcomes and claim-level agreement."""
    statuses: Dict[str, int] = {}
    verdicts: Dict[str, int] = {}
    for result in results:
        statuses[result["status"]] = statuses.get(result["status"], 0) + 1
        verdict = result["verdict"] or "none"
        verdicts[verdict] = verdicts.get(verdict, 0) + 1

    summary: Dict[str, Any] = {
        "statuses": dict(sorted(statuses.items())),
        "verdicts": dict(sorted(verdicts.items())),
    }

    expected = [(e.get("expected"), r) for e, r in zip(examples, results) if e.get("expected")]
    if expected:
        summary["expected_outcome_match"] = sum(
            1
            for outcome, result in expected
            if outcome.get("status") == result["status"]
            and outcome.get("verdict") == result["verdict"]
        )
        summary["expected_outcomes"] = len(expected)

    gold_claims = [(e.get("claims"), r) for e, r in zip(examples, results) if "claims" in e]
    if gold_claims:
        confusion: Dict[str, Dict[str, int]] = {}
        matched = agree = 0
        for gold, result in gold_claims:
            if result["status"] == "failed" and not result["claims"]:
                continue
            for gold_claim, claim in zip(gold or [], result["claims"]):
                row = confusion.setdefault(gold_claim["label"], {})
                row[claim["status"]] = row.get(claim["status"], 0) + 1
                matched += 1
                agree += gold_claim["label"] == claim["status"]
        summary["claims"] = {
            "matched": matched,
            "agree": agree,
            "confusion": {
                label: dict(sorted(row.items())) for label, row in sorted(confusion.items())
            },
        }
    return summary


async def evaluate(args: argparse.Namespace) -> Dict[str, Any]:
    dataset_path = resolve_dataset(args.dataset)
    examples = load_dataset(dataset_path, limit=args.limit)
    provider = build_provider(args)
    graph = build_audit_graph(provider=provider)  # no Qdrant: context is injected

    results = []
    started = time.time()
    for i, example in enumerate(examples, 1):
        try:
            state = await run_audit(
                graph=graph,
                request_id=example["id"],
                user_query=example["query"],
                llm_response=example["response"],
                context_docs=list(example["context"]),
                context_sources=context_sources(example),
                knowledge_enabled=False,
            )
            results.append(example_result(example, state))
        except Exception as exc:  # one broken example must not end the run (D17)
            logger.warning(f"Example {example['id']} failed: {exc}")
            results.append(failed_result(example, exc))
        if args.provider != "mock" and i % 10 == 0:
            logger.info(f"  {i}/{len(examples)} examples evaluated")
    elapsed = time.time() - started

    finished = [r for r in results if r["status"] != "failed"]
    metrics = compute_metrics(
        labels=[r["label_hallucinated"] for r in finished],
        predictions=[r["predicted_hallucinated"] for r in finished],
        scores=[risk_score(r) for r in finished],
    )

    return {
        "dataset": str(dataset_path.name),
        "provider": args.provider,
        "model": args.model if args.provider != "mock" else "mock",
        "scoring_version": "v2",
        "examples": len(results),
        "failed": len(results) - len(finished),
        "elapsed_seconds": round(elapsed, 2),
        "metrics": {
            k: round(v, 6) if isinstance(v, float) else v for k, v in metrics.to_dict().items()
        },
        "summary": summarize(examples, results),
        "per_example": results,
    }


def print_summary(report: Dict[str, Any]) -> None:
    m = report["metrics"]
    print(f"\nDataset:   {report['dataset']}  ({report['examples']} examples)")
    print(f"Provider:  {report['provider']} / {report['model']}")
    print(f"Elapsed:   {report['elapsed_seconds']}s")
    print(f"Failed:    {report['failed']} (excluded from the detection metrics)")
    print("\n  Metric              Value")
    print("  ------------------  -----")
    for key in (
        "precision",
        "recall",
        "f1",
        "accuracy",
        "balanced_accuracy",
        "auroc",
        "ece",
    ):
        print(f"  {key:<18}  {m[key]:.4f}")
    print(
        f"\n  Confusion: TP={m['true_positives']} FP={m['false_positives']} "
        f"TN={m['true_negatives']} FN={m['false_negatives']}"
    )
    summary = report.get("summary") or {}
    print(f"  Statuses:  {summary.get('statuses')}")
    print(f"  Verdicts:  {summary.get('verdicts')}")
    if "expected_outcomes" in summary:
        print(
            f"  Expected outcome matched: {summary['expected_outcome_match']}"
            f"/{summary['expected_outcomes']}"
        )
    if "claims" in summary:
        claims = summary["claims"]
        print(f"  Claim verdicts agreeing with gold: {claims['agree']}/{claims['matched']}")


BASELINE_KEYS = (
    "dataset",
    "provider",
    "model",
    "scoring_version",
    "examples",
    "failed",
    "metrics",
    "summary",
    "per_example",
)


def check_baseline(report: Dict[str, Any], baseline_path: Path) -> int:
    if not baseline_path.exists():
        print(f"Baseline {baseline_path} does not exist. Create it with --write-baseline.")
        return 1

    with open(baseline_path, encoding="utf-8") as f:
        baseline = json.load(f)

    failures = []
    for key in ("scoring_version", "metrics", "summary"):
        if report.get(key) != baseline.get(key):
            failures.append(
                f"  {key} changed:\n    baseline: {baseline.get(key)}\n"
                f"    current:  {report.get(key)}"
            )

    baseline_examples = {e["id"]: e for e in baseline["per_example"]}
    for current in report["per_example"]:
        expected = baseline_examples.get(current["id"])
        if expected is None:
            failures.append(f"  new example not in baseline: {current['id']}")
        elif expected != current:
            failures.append(
                f"  {current['id']}:\n    baseline: {expected}\n    current:  {current}"
            )
    missing = set(baseline_examples) - {e["id"] for e in report["per_example"]}
    for example_id in sorted(missing):
        failures.append(f"  example missing from run: {example_id}")

    if failures:
        print("\nBASELINE REGRESSION DETECTED")
        print(
            "If this change is intentional (e.g. you changed scoring logic or the\n"
            "golden set), regenerate the baseline with --write-baseline and commit\n"
            "it in the same PR with an explanation (docs/DECISIONS.md, ADR-001)."
        )
        print("\n".join(failures[:20]))
        if len(failures) > 20:
            print(f"  ... and {len(failures) - 20} more differences")
        return 1

    print(f"\nBaseline check passed ({baseline_path.name}).")
    return 0


def main() -> None:
    parser = argparse.ArgumentParser(description="Run the TrustAgent evaluation harness")
    parser.add_argument(
        "--dataset",
        default="golden",
        help="golden (golden_v1) | golden_v2 | halueval | path to .jsonl",
    )
    parser.add_argument(
        "--provider", default="mock", choices=["mock", "ollama", "openai", "anthropic"]
    )
    parser.add_argument("--model", default="llama3.2", help="Model name (live providers)")
    parser.add_argument("--ollama-url", default="http://localhost:11434")
    parser.add_argument("--fixtures", default=None, help="Fixture file for the mock provider")
    parser.add_argument("--limit", type=int, default=0, help="Evaluate at most N examples")
    parser.add_argument("--output", default=None, help="Write the full JSON report here")
    parser.add_argument(
        "--check-baseline",
        action="store_true",
        help="Fail (exit 1) if results differ from the committed baseline",
    )
    parser.add_argument(
        "--write-baseline", action="store_true", help="Write current results as the new baseline"
    )
    args = parser.parse_args()

    # The recorded judge fails on purpose (outages, repairs); only live runs log warnings.
    logging.basicConfig(level=logging.CRITICAL if args.provider == "mock" else logging.WARNING)

    report = asyncio.run(evaluate(args))
    print_summary(report)

    if args.output:
        output_path = Path(args.output)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        with open(output_path, "w", encoding="utf-8", newline="\n") as f:
            json.dump(report, f, indent=2)
            f.write("\n")
        print(f"Report written to {output_path}")

    if args.write_baseline:
        # Strip volatile fields so regenerating the baseline is deterministic.
        target = baseline_path(args.dataset)
        baseline = {k: report[k] for k in BASELINE_KEYS}
        target.parent.mkdir(parents=True, exist_ok=True)
        with open(target, "w", encoding="utf-8", newline="\n") as f:
            json.dump(baseline, f, indent=2)
            f.write("\n")
        print(f"Baseline written to {target}")

    if args.check_baseline:
        sys.exit(check_baseline(report, baseline_path(args.dataset)))


if __name__ == "__main__":
    main()
