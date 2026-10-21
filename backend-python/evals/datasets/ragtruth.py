"""
RAGTruth loader (SPEC section 3.2 "Knowledge and evals"; P6 bench-lite).

Source: the ParticleMedia/RAGTruth repository, `dataset/response.jsonl` (one
generated answer per row, with its hallucination spans) and
`dataset/source_info.jsonl` (the task: question and passages for QA, an article
for Summary, structured data for Data2txt).

Conversion (design-engine section 12.1):

- response-level label = any annotated span (Evident/Subtle Conflict, or
  Evident/Subtle Baseless Info); the span kinds are kept in `meta.spans`
  (`conflict`, `baseless`, both or none);
- QA: the query is the question and the evidence is the passages, split at
  their "passage N:" markers; Summary and Data2txt get a fixed instruction and
  the article or the data as one document;
- answers whose `quality` is not "good" (incorrect refusals, truncated
  answers) are skipped unless `include_low_quality`;
- examples over the decision-model budget are excluded and counted.

Sampling is stratified by label x generating model with a fixed seed
(sampling.py), proportional by default or label-balanced.

[A9] The field names, the revision and the licence are checked at P6: pin
`--revision` to a commit SHA for a published run. The manifest records the
SHA-256 of both source files either way. Only metrics are ever committed;
the downloaded and prepared files stay under evals/datasets/ragtruth/
(gitignored).

Usage:
    python -m evals.datasets.ragtruth --subset qa --split test --n 200 --seed 13
"""

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

from ..examples import (
    DEFAULT_CONTEXT_SOURCE,
    LEVEL_RESPONSE,
    MAX_EXAMPLE_TOKENS,
    BenchExample,
    DatasetFormatError,
    DatasetManifest,
    example_tokens,
    read_jsonl,
    write_prepared,
)
from .fetch import Fetcher, combined_digest, download, sha256_file
from .sampling import stratified_sample

DATASET = "ragtruth"
NAME = "RAGTruth"
REPOSITORY = "ParticleMedia/RAGTruth"
DEFAULT_REVISION = "main"
RAW_URL = "https://raw.githubusercontent.com/" + REPOSITORY + "/{revision}/dataset/{file}"
RESPONSE_FILE = "response.jsonl"
SOURCE_FILE = "source_info.jsonl"
LICENSE_NOTE = (
    "RAGTruth (github.com/ParticleMedia/RAGTruth), used under its repository licence "
    "[A9: verify before publishing]. Only metrics are published, never dataset text."
)

DATASET_DIR = Path(__file__).parent / "ragtruth"
RAW_DIR = DATASET_DIR / "raw"

# --subset -> RAGTruth task_type.
TASK_TYPES = {"qa": "QA", "summary": "Summary", "data2txt": "Data2txt"}
SPLITS = ("train", "test")

CONFLICT_LABELS = frozenset({"Evident Conflict", "Subtle Conflict"})
BASELESS_LABELS = frozenset({"Evident Baseless Info", "Subtle Baseless Info"})

SUMMARY_QUERY = "Summarize the following text."
DATA2TXT_QUERY = "Write an objective overview of the business based only on the structured data."

GOOD_QUALITY = "good"
DEFAULT_SEED = 13

_PASSAGE_MARKER = re.compile(r"(?im)^\s*passage\s*\d+\s*:\s*")


def split_passages(passages: str) -> List[str]:
    """QA passages as separate documents ("passage N:" markers), else one document."""
    parts = [part.strip() for part in _PASSAGE_MARKER.split(passages)]
    documents = [part for part in parts if part]
    return documents or ([passages.strip()] if passages.strip() else [])


def span_kinds(labels: Sequence[Mapping[str, Any]]) -> Tuple[bool, bool]:
    """(has a conflict span, has a baseless span)."""
    kinds = {str(label.get("label_type", "")) for label in labels}
    return bool(kinds & CONFLICT_LABELS), bool(kinds & BASELESS_LABELS)


def _task(source: Mapping[str, Any], subset: str) -> Optional[Tuple[str, List[str]]]:
    """(query, documents) of a source row of `subset`; None when it has no evidence."""
    info = source.get("source_info")
    if subset == "qa":
        if not isinstance(info, Mapping):
            return None
        question, passages = info.get("question"), info.get("passages")
        if not isinstance(question, str) or not isinstance(passages, str):
            return None
        documents = split_passages(passages)
        return (question.strip(), documents) if documents else None
    if subset == "summary":
        text = info if isinstance(info, str) else None
        return (SUMMARY_QUERY, [text.strip()]) if text and text.strip() else None
    if info is None or info == "" or info == {}:
        return None
    data = info if isinstance(info, str) else json.dumps(info, ensure_ascii=False, sort_keys=True)
    return DATA2TXT_QUERY, [data]


def convert(
    responses: Sequence[Mapping[str, Any]],
    sources: Sequence[Mapping[str, Any]],
    *,
    subset: str,
    split: str,
    include_low_quality: bool = False,
    max_tokens: int = MAX_EXAMPLE_TOKENS,
) -> Tuple[List[BenchExample], Dict[str, int]]:
    """
    RAGTruth rows -> examples of `subset` and `split`, with counts of what was
    kept and skipped (other task, other split, low quality, malformed, over
    the token budget).
    """
    if subset not in TASK_TYPES:
        raise ValueError(f"subset must be one of {', '.join(TASK_TYPES)}")
    task_type = TASK_TYPES[subset]
    tasks: Dict[str, Tuple[str, List[str]]] = {}
    for source in sources:
        if str(source.get("task_type", "")) != task_type:
            continue
        task = _task(source, subset)
        if task is not None:
            tasks[str(source.get("source_id", ""))] = task

    counts = {
        "rows": len(responses),
        "other_task_or_split": 0,
        "low_quality": 0,
        "malformed": 0,
        "over_budget": 0,
    }
    examples: List[BenchExample] = []
    for row in responses:
        task = tasks.get(str(row.get("source_id", "")))
        if task is None or str(row.get("split", "")) != split:
            counts["other_task_or_split"] += 1
            continue
        if not include_low_quality and str(row.get("quality", GOOD_QUALITY)) != GOOD_QUALITY:
            counts["low_quality"] += 1
            continue
        response, labels, row_id = row.get("response"), row.get("labels"), row.get("id")
        if not isinstance(response, str) or not isinstance(labels, list) or row_id is None:
            counts["malformed"] += 1
            continue
        conflict, baseless = span_kinds([label for label in labels if isinstance(label, dict)])
        hallucinated = bool(labels)
        model = str(row.get("model") or "unknown")
        spans = "+".join(
            kind for kind, on in (("conflict", conflict), ("baseless", baseless)) if on
        )
        query, documents = task
        example = BenchExample(
            id=f"{DATASET}-{subset}-{row_id}",
            dataset=DATASET,
            level=LEVEL_RESPONSE,
            query=query,
            response=response,
            context=tuple(documents),
            label_hallucinated=hallucinated,
            stratum=f"{int(hallucinated)}|{model}",
            context_source=DEFAULT_CONTEXT_SOURCE,
            meta={"model": model, "spans": spans or "none", "task_type": task_type},
        )
        if example_tokens(example) > max_tokens:
            counts["over_budget"] += 1
            continue
        examples.append(example)
    counts["eligible"] = len(examples)
    counts["positives_eligible"] = sum(example.label_hallucinated for example in examples)
    return examples, counts


def sample(
    examples: Sequence[BenchExample], n: int, seed: int, *, balance_labels: bool = False
) -> List[BenchExample]:
    """Stratified (label x model) sample of `n` examples."""
    return stratified_sample(
        examples,
        lambda example: example.stratum,
        n,
        seed,
        key=lambda example: example.id,
        balance=(lambda example: str(example.label_hallucinated)) if balance_labels else None,
    )


def fetch_sources(
    revision: str, raw_dir: Path, *, fetch: Optional[Fetcher] = None, refresh: bool = False
) -> Dict[str, str]:
    """Download both source files for `revision`; returns their SHA-256 by file name."""
    target = raw_dir / revision.replace("/", "_")
    return {
        name: download(
            RAW_URL.format(revision=revision, file=name),
            target / name,
            fetch=fetch,
            refresh=refresh,
        )
        for name in (RESPONSE_FILE, SOURCE_FILE)
    }


def prepare(
    *,
    subset: str = "qa",
    split: str = "test",
    n: int = 200,
    seed: int = DEFAULT_SEED,
    revision: str = DEFAULT_REVISION,
    source_dir: Optional[Path] = None,
    raw_dir: Path = RAW_DIR,
    out: Optional[Path] = None,
    balance_labels: bool = False,
    include_low_quality: bool = False,
    fetch: Optional[Fetcher] = None,
) -> Tuple[Path, DatasetManifest]:
    """
    Download (or read from `source_dir`), convert, sample and write a prepared
    RAGTruth subset; returns its path and manifest.
    """
    if split not in SPLITS:
        raise ValueError(f"split must be one of {', '.join(SPLITS)}")
    if source_dir is None:
        hashes = fetch_sources(revision, raw_dir, fetch=fetch)
        files_dir = raw_dir / revision.replace("/", "_")
    else:
        files_dir = source_dir
        hashes = {name: sha256_file(source_dir / name) for name in (RESPONSE_FILE, SOURCE_FILE)}

    examples, counts = convert(
        read_jsonl(files_dir / RESPONSE_FILE),
        read_jsonl(files_dir / SOURCE_FILE),
        subset=subset,
        split=split,
        include_low_quality=include_low_quality,
    )
    if not examples:
        raise DatasetFormatError(
            f"no RAGTruth {subset}/{split} examples found ({counts}); check the field names [A9]"
        )
    picked = sample(examples, n, seed, balance_labels=balance_labels)
    counts["positives"] = sum(example.label_hallucinated for example in picked)
    manifest = DatasetManifest(
        name=NAME,
        subset=subset,
        split=split,
        n=len(picked),
        revision=f"{revision}@{combined_digest(hashes)[:12]}",
        license_note=LICENSE_NOTE,
        seed=seed,
        sampling="balanced:label;strata:model" if balance_labels else "stratified:label,model",
        source_sha256=hashes,
        counts=counts,
        excluded_over_budget=counts["over_budget"],
    )
    path = out or DATASET_DIR / f"ragtruth_{subset}_{split}_n{len(picked)}_seed{seed}.jsonl"
    write_prepared(path, manifest, picked)
    return path, manifest


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="Prepare a RAGTruth benchmark subset")
    parser.add_argument("--subset", default="qa", choices=sorted(TASK_TYPES))
    parser.add_argument("--split", default="test", choices=SPLITS)
    parser.add_argument("--n", type=int, default=200, help="Examples to sample")
    parser.add_argument("--seed", type=int, default=DEFAULT_SEED)
    parser.add_argument(
        "--revision", default=DEFAULT_REVISION, help="Git ref (pin a SHA to publish)"
    )
    parser.add_argument("--source-dir", type=Path, help="Read the two source files from here")
    parser.add_argument("--out", type=Path, help="Prepared .jsonl path")
    parser.add_argument(
        "--balance-labels", action="store_true", help="Equal labels (not proportional)"
    )
    parser.add_argument("--include-low-quality", action="store_true")
    args = parser.parse_args(argv)
    try:
        path, manifest = prepare(
            subset=args.subset,
            split=args.split,
            n=args.n,
            seed=args.seed,
            revision=args.revision,
            source_dir=args.source_dir,
            out=args.out,
            balance_labels=args.balance_labels,
            include_low_quality=args.include_low_quality,
        )
    except (OSError, ValueError) as exc:
        print(f"RAGTruth preparation failed: {exc}", file=sys.stderr)
        return 1
    print(
        f"Wrote {manifest.n} RAGTruth {manifest.subset}/{manifest.split} examples "
        f"({manifest.counts.get('positives', 0)} hallucinated; revision {manifest.revision}; "
        f"{manifest.excluded_over_budget} over the token budget) -> {path}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
