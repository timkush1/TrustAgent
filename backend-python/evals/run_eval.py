"""
Evaluation runner.

Runs the real audit pipeline (LangGraph: decompose -> verify -> score) over a
labeled dataset and reports hallucination-detection metrics. Context documents
from the dataset are injected directly (the retriever's context-injection
mode), so no Qdrant instance is needed.

Tier 1 (CI regression gate, deterministic, no model needed):
    python -m evals.run_eval --dataset golden --provider mock --check-baseline

Tier 2 (benchmark with a live model):
    python -m evals.datasets.download halueval --samples 100
    python -m evals.run_eval --dataset halueval --provider ollama --model llama3.2

Exit codes: 0 = success, 1 = baseline mismatch or run failure.
"""

import argparse
import asyncio
import json
import logging
import sys
import time
from pathlib import Path
from typing import Any, Dict, List

from truthtable.graphs.audit_graph import build_audit_graph, run_audit
from truthtable.providers import MockLLMProvider, OllamaProvider

from .metrics import compute_metrics

logger = logging.getLogger(__name__)

EVALS_DIR = Path(__file__).parent
GOLDEN_DATASET = EVALS_DIR / "datasets" / "golden" / "golden_v1.jsonl"
GOLDEN_FIXTURES = EVALS_DIR / "fixtures" / "golden_v1_fixtures.json"
GOLDEN_BASELINE = EVALS_DIR / "baselines" / "golden_v1.json"
HALUEVAL_DATASET = EVALS_DIR / "datasets" / "halueval" / "halueval_qa.jsonl"

# Per-example faithfulness scores are rounded before baseline comparison so
# the gate is robust to floating-point noise but catches real logic changes.
SCORE_DECIMALS = 6


def resolve_dataset(name: str) -> Path:
    if name == "golden":
        return GOLDEN_DATASET
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
        return MockLLMProvider(fixtures_path=args.fixtures or GOLDEN_FIXTURES)
    if args.provider == "ollama":
        return OllamaProvider(model=args.model, base_url=args.ollama_url)
    sys.exit(f"Unknown provider {args.provider!r}")


async def evaluate(args: argparse.Namespace) -> Dict[str, Any]:
    dataset_path = resolve_dataset(args.dataset)
    examples = load_dataset(dataset_path, limit=args.limit)
    provider = build_provider(args)
    graph = build_audit_graph(provider=provider)  # no Qdrant: context is injected

    results = []
    started = time.time()
    for i, example in enumerate(examples, 1):
        state = await run_audit(
            graph=graph,
            request_id=example["id"],
            user_query=example["query"],
            llm_response=example["response"],
            context_docs=example["context"],
        )
        score = round(float(state["faithfulness_score"]), SCORE_DECIMALS)
        results.append(
            {
                "id": example["id"],
                "label_hallucinated": bool(example["label_hallucinated"]),
                "predicted_hallucinated": bool(state["hallucination_detected"]),
                "faithfulness_score": score,
            }
        )
        if args.provider != "mock" and i % 10 == 0:
            logger.info(f"  {i}/{len(examples)} examples evaluated")
    elapsed = time.time() - started

    metrics = compute_metrics(
        labels=[r["label_hallucinated"] for r in results],
        predictions=[r["predicted_hallucinated"] for r in results],
        # P(hallucinated) := 1 - faithfulness score
        scores=[1.0 - r["faithfulness_score"] for r in results],
    )

    return {
        "dataset": str(dataset_path.name),
        "provider": args.provider,
        "model": args.model if args.provider != "mock" else "mock",
        "examples": len(results),
        "elapsed_seconds": round(elapsed, 2),
        "metrics": {
            k: round(v, 6) if isinstance(v, float) else v for k, v in metrics.to_dict().items()
        },
        "per_example": results,
    }


def print_summary(report: Dict[str, Any]) -> None:
    m = report["metrics"]
    print(f"\nDataset:   {report['dataset']}  ({report['examples']} examples)")
    print(f"Provider:  {report['provider']} / {report['model']}")
    print(f"Elapsed:   {report['elapsed_seconds']}s")
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


def check_baseline(report: Dict[str, Any], baseline_path: Path) -> int:
    if not baseline_path.exists():
        print(f"Baseline {baseline_path} does not exist. Create it with --write-baseline.")
        return 1

    with open(baseline_path, encoding="utf-8") as f:
        baseline = json.load(f)

    failures = []
    if report["metrics"] != baseline["metrics"]:
        failures.append(
            f"  metrics changed:\n    baseline: {baseline['metrics']}\n    current:  {report['metrics']}"
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
            "it in the same PR with an explanation."
        )
        print("\n".join(failures[:20]))
        if len(failures) > 20:
            print(f"  ... and {len(failures) - 20} more differences")
        return 1

    print(f"\nBaseline check passed ({baseline_path.name}).")
    return 0


def main() -> None:
    parser = argparse.ArgumentParser(description="Run the TrustAgent evaluation harness")
    parser.add_argument("--dataset", default="golden", help="golden | halueval | path to .jsonl")
    parser.add_argument("--provider", default="mock", choices=["mock", "ollama"])
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

    logging.basicConfig(level=logging.WARNING)

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
        baseline = {
            k: report[k]
            for k in ("dataset", "provider", "model", "examples", "metrics", "per_example")
        }
        GOLDEN_BASELINE.parent.mkdir(parents=True, exist_ok=True)
        with open(GOLDEN_BASELINE, "w", encoding="utf-8", newline="\n") as f:
            json.dump(baseline, f, indent=2)
            f.write("\n")
        print(f"Baseline written to {GOLDEN_BASELINE}")

    if args.check_baseline:
        sys.exit(check_baseline(report, GOLDEN_BASELINE))


if __name__ == "__main__":
    main()
