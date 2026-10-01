"""
LLM-AggreFact loader (SPEC section 3.2 "Knowledge and evals"; eval-live).

Source: the Hugging Face dataset `lytang/LLM-AggreFact` (claim-level
fact-checking aggregated from several benchmarks). Each row is one claim
checked against one document:

    {"dataset": "<sub-dataset>", "doc": "...", "claim": "...", "label": 1|0}

label 1 means the document supports the claim, so `label_hallucinated` is
label == 0. Examples are claim-level: there is no decomposition, the claim goes
straight to the claim verifier (design-engine section 12.1).

Rows come from a local export (`--from-file`: JSON Lines with those four
fields, e.g. written with the `datasets` library) or from the Hugging Face
datasets-server rows API (paged, 100 rows per request; the dataset is gated,
so pass `--hf-token-file`). The rows API shortens very long cells; such rows
are skipped and counted rather than used truncated. Examples over the
decision-model budget are excluded and counted too.

Sampling is stratified by sub-dataset x label with a fixed seed.

[A9] Field names, the revision and the licence are checked before anything is
published. Only metrics are committed; files stay under
evals/datasets/aggrefact/ (gitignored).

Usage:
    python -m evals.datasets.aggrefact --split test --n 600 --seed 13 --from-file export.jsonl
"""

import argparse
import sys
import urllib.parse
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

from ..examples import (
    DEFAULT_CONTEXT_SOURCE,
    LEVEL_CLAIM,
    MAX_EXAMPLE_TOKENS,
    BenchExample,
    DatasetFormatError,
    DatasetManifest,
    example_tokens,
    read_jsonl,
    write_prepared,
)
from .fetch import Fetcher, digest_rows, get_json, sha256_file
from .sampling import stratified_sample

DATASET = "aggrefact"
NAME = "LLM-AggreFact"
HF_DATASET = "lytang/LLM-AggreFact"
ROWS_URL = "https://datasets-server.huggingface.co/rows?{query}"
ROWS_PER_REQUEST = 100
LICENSE_NOTE = (
    "LLM-AggreFact (huggingface.co/datasets/lytang/LLM-AggreFact), gated; used under the "
    "licences of its source datasets [A9: verify before publishing]. Only metrics are "
    "published, never dataset text."
)
SPLITS = ("dev", "test")
DEFAULT_SEED = 13

DATASET_DIR = Path(__file__).parent / "aggrefact"


def convert(
    rows: Sequence[Mapping[str, Any]],
    *,
    split: str,
    max_tokens: int = MAX_EXAMPLE_TOKENS,
) -> Tuple[List[BenchExample], Dict[str, int]]:
    """AggreFact rows -> claim-level examples, with counts of what was skipped."""
    counts = {"rows": len(rows), "malformed": 0, "truncated_by_source": 0, "over_budget": 0}
    examples: List[BenchExample] = []
    for index, raw in enumerate(rows):
        row = raw.get("row", raw)
        if raw.get("truncated_cells"):
            counts["truncated_by_source"] += 1
            continue
        sub, doc, claim, label = (
            row.get("dataset"),
            row.get("doc"),
            row.get("claim"),
            row.get("label"),
        )
        if (
            not isinstance(sub, str)
            or not isinstance(doc, str)
            or not isinstance(claim, str)
            or isinstance(label, bool)
            or label not in (0, 1)
            or not doc.strip()
            or not claim.strip()
        ):
            counts["malformed"] += 1
            continue
        row_index = raw.get("row_idx", index)
        hallucinated = label == 0
        example = BenchExample(
            id=f"{DATASET}-{split}-{row_index}",
            dataset=DATASET,
            level=LEVEL_CLAIM,
            query="",
            response=claim.strip(),
            context=(doc.strip(),),
            label_hallucinated=hallucinated,
            stratum=f"{sub}|{int(hallucinated)}",
            context_source=DEFAULT_CONTEXT_SOURCE,
            meta={"sub_dataset": sub},
        )
        if example_tokens(example) > max_tokens:
            counts["over_budget"] += 1
            continue
        examples.append(example)
    counts["eligible"] = len(examples)
    return examples, counts


def fetch_rows(
    split: str,
    *,
    token: Optional[str] = None,
    fetch: Optional[Fetcher] = None,
    max_rows: Optional[int] = None,
) -> List[Dict[str, Any]]:
    """All rows of `split` from the datasets-server rows API (paged)."""
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    rows: List[Dict[str, Any]] = []
    offset = 0
    while True:
        query = urllib.parse.urlencode(
            {
                "dataset": HF_DATASET,
                "config": "default",
                "split": split,
                "offset": offset,
                "length": ROWS_PER_REQUEST,
            }
        )
        page = get_json(ROWS_URL.format(query=query), fetch=fetch, headers=headers)
        if not isinstance(page, dict) or not isinstance(page.get("rows"), list):
            raise DatasetFormatError("unexpected datasets-server response (no rows)")
        batch = [row for row in page["rows"] if isinstance(row, dict)]
        rows.extend(batch)
        total = page.get("num_rows_total")
        offset += len(batch)
        done = not batch or (isinstance(total, int) and offset >= total)
        if done or (max_rows is not None and len(rows) >= max_rows):
            break
    return rows[:max_rows] if max_rows is not None else rows


def sample(examples: Sequence[BenchExample], n: int, seed: int) -> List[BenchExample]:
    """Stratified (sub-dataset x label) sample of `n` examples."""
    return stratified_sample(
        examples, lambda example: example.stratum, n, seed, key=lambda example: example.id
    )


def prepare(
    *,
    split: str = "test",
    n: int = 600,
    seed: int = DEFAULT_SEED,
    from_file: Optional[Path] = None,
    token: Optional[str] = None,
    out: Optional[Path] = None,
    fetch: Optional[Fetcher] = None,
) -> Tuple[Path, DatasetManifest]:
    """Read (or download), convert, sample and write a prepared AggreFact subset."""
    if split not in SPLITS:
        raise ValueError(f"split must be one of {', '.join(SPLITS)}")
    if from_file is not None:
        rows = read_jsonl(from_file)
        revision = f"local@{sha256_file(from_file)[:12]}"
        hashes = {from_file.name: sha256_file(from_file)}
    else:
        rows = fetch_rows(split, token=token, fetch=fetch)
        digest = digest_rows(rows)
        revision = f"hf-main@{digest[:12]}"
        hashes = {f"rows-{split}": digest}

    examples, counts = convert(rows, split=split)
    if not examples:
        raise DatasetFormatError(f"no AggreFact {split} examples found ({counts}); check [A9]")
    picked = sample(examples, n, seed)
    counts["positives"] = sum(example.label_hallucinated for example in picked)
    manifest = DatasetManifest(
        name=NAME,
        subset=None,
        split=split,
        n=len(picked),
        revision=revision,
        license_note=LICENSE_NOTE,
        seed=seed,
        sampling="stratified:sub_dataset,label",
        source_sha256=hashes,
        counts=counts,
        excluded_over_budget=counts["over_budget"],
    )
    path = out or DATASET_DIR / f"aggrefact_{split}_n{len(picked)}_seed{seed}.jsonl"
    write_prepared(path, manifest, picked)
    return path, manifest


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="Prepare an LLM-AggreFact benchmark subset")
    parser.add_argument("--split", default="test", choices=SPLITS)
    parser.add_argument("--n", type=int, default=600, help="Examples to sample")
    parser.add_argument("--seed", type=int, default=DEFAULT_SEED)
    parser.add_argument("--from-file", type=Path, help="Local JSONL export of the split")
    parser.add_argument("--hf-token-file", type=Path, help="File holding a Hugging Face token")
    parser.add_argument("--out", type=Path, help="Prepared .jsonl path")
    args = parser.parse_args(argv)
    token: Optional[str] = None
    try:
        if args.hf_token_file is not None:
            token = args.hf_token_file.read_text(encoding="utf-8").strip()
        path, manifest = prepare(
            split=args.split,
            n=args.n,
            seed=args.seed,
            from_file=args.from_file,
            token=token,
            out=args.out,
        )
    except (OSError, ValueError) as exc:
        print(f"AggreFact preparation failed: {exc}", file=sys.stderr)
        return 1
    print(
        f"Wrote {manifest.n} AggreFact {manifest.split} claims "
        f"({manifest.counts.get('positives', 0)} not supported; revision {manifest.revision}; "
        f"{manifest.excluded_over_budget} over the token budget) -> {path}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
