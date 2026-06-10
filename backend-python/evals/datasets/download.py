"""
Benchmark dataset downloader.

Currently supports HaluEval QA (Li et al., 2023, https://github.com/RUCAIBox/HaluEval):
10,000 question/knowledge pairs, each with a correct answer and an
LLM-generated hallucinated answer. We sample N/2 items (seeded) and emit both
the faithful and the hallucinated answer per item, producing a balanced
dataset in the harness schema:

    {"id", "domain", "query", "response", "context": [...], "label_hallucinated"}

Raw and converted files are written under datasets/halueval/ and are NOT
committed (see .gitignore) — rerun this script to reproduce them exactly.

Usage:
    python -m evals.datasets.download halueval --samples 200 --seed 42
"""

import argparse
import json
import random
import sys
import urllib.request
from pathlib import Path

HALUEVAL_QA_URL = "https://raw.githubusercontent.com/RUCAIBox/HaluEval/main/data/qa_data.json"

DATASETS_DIR = Path(__file__).parent
HALUEVAL_DIR = DATASETS_DIR / "halueval"
RAW_PATH = HALUEVAL_DIR / "qa_data_raw.jsonl"
OUT_PATH = HALUEVAL_DIR / "halueval_qa.jsonl"


def download_halueval(samples: int, seed: int) -> None:
    HALUEVAL_DIR.mkdir(parents=True, exist_ok=True)

    if not RAW_PATH.exists():
        print(f"Downloading HaluEval QA data from {HALUEVAL_QA_URL} ...")
        with urllib.request.urlopen(HALUEVAL_QA_URL) as response:  # nosec B310 - fixed https URL
            RAW_PATH.write_bytes(response.read())
        print(f"Saved raw data to {RAW_PATH}")
    else:
        print(f"Using cached raw data at {RAW_PATH}")

    items = []
    with open(RAW_PATH, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                items.append(json.loads(line))
    print(f"Loaded {len(items)} HaluEval QA items")

    rng = random.Random(seed)
    n_items = min(samples // 2, len(items))
    sampled = rng.sample(items, n_items)

    examples = []
    for i, item in enumerate(sampled):
        base = {
            "domain": "halueval-qa",
            "query": item["question"],
            "context": [item["knowledge"]],
        }
        examples.append(
            {
                "id": f"halueval-{seed}-{i}-faithful",
                **base,
                "response": item["right_answer"],
                "label_hallucinated": False,
            }
        )
        examples.append(
            {
                "id": f"halueval-{seed}-{i}-hallucinated",
                **base,
                "response": item["hallucinated_answer"],
                "label_hallucinated": True,
            }
        )

    with open(OUT_PATH, "w", encoding="utf-8", newline="\n") as f:
        for example in examples:
            f.write(json.dumps(example) + "\n")

    print(f"Wrote {len(examples)} examples (balanced, seed={seed}) -> {OUT_PATH}")


def main() -> None:
    parser = argparse.ArgumentParser(description="Download benchmark datasets")
    parser.add_argument("dataset", choices=["halueval"])
    parser.add_argument(
        "--samples",
        type=int,
        default=200,
        help="Total examples to emit (half faithful, half hallucinated)",
    )
    parser.add_argument("--seed", type=int, default=42)
    args = parser.parse_args()

    if args.dataset == "halueval":
        download_halueval(samples=args.samples, seed=args.seed)
    else:  # pragma: no cover - argparse already restricts choices
        sys.exit(f"Unknown dataset {args.dataset!r}")


if __name__ == "__main__":
    main()
