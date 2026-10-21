"""
Benchmark examples and prepared-dataset files (SPEC sections 2.13 and 3.2).

A dataset loader (datasets/ragtruth.py, datasets/aggrefact.py) turns public
data into `BenchExample`s, samples them with a fixed seed and writes a
prepared file:

    <name>.jsonl            one BenchExample per line (dataset text: never committed)
    <name>.manifest.json    the DatasetManifest (name, split, revision, sampling, ...)

Both live under evals/datasets/<dataset>/, which is gitignored: only metrics
are committed (A9), in docs/benchmarks/.

An example is either response-level (`level: response`, RAGTruth: the answer
to a question over passages; the label is "any hallucinated span") or
claim-level (`level: claim`, LLM-AggreFact: one claim against one document;
the label is "not supported").
"""

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

LEVEL_RESPONSE = "response"
LEVEL_CLAIM = "claim"
LEVELS = (LEVEL_RESPONSE, LEVEL_CLAIM)

# Evidence source of benchmark documents: the app supplied them with the request
# (SPEC section 2.10), so unsupported content counts as hallucination.
DEFAULT_CONTEXT_SOURCE = "request_context"

MANIFEST_SUFFIX = ".manifest.json"

# Examples whose request would not fit the decision model's budget are excluded
# when a dataset is prepared, and counted (benchmark-run.v1
# errors.excluded_over_budget), never silently truncated. The gate compiles at
# most 24,000 tokens per call; this leaves room for the questions and overhead.
MAX_EXAMPLE_TOKENS = 20_000
CHARS_PER_TOKEN = 4


class DatasetFormatError(ValueError):
    """A prepared-dataset file or row is malformed."""


def _require_str(data: Mapping[str, Any], key: str, where: str) -> str:
    value = data.get(key)
    if not isinstance(value, str):
        raise DatasetFormatError(f"{where}: {key} must be a string")
    return value


@dataclass(frozen=True)
class BenchExample:
    """One labelled benchmark example."""

    id: str
    dataset: str
    level: str
    query: str  # The user request ("" when the task has none)
    response: str  # The answer (response level) or the claim (claim level)
    context: Tuple[str, ...]  # Evidence documents
    label_hallucinated: bool
    stratum: str = ""  # Sampling stratum
    context_source: str = DEFAULT_CONTEXT_SOURCE
    meta: Mapping[str, str] = field(default_factory=dict)

    def __post_init__(self) -> None:
        if not self.id:
            raise DatasetFormatError("example id must not be empty")
        if self.level not in LEVELS:
            raise DatasetFormatError(f"example {self.id}: level must be one of {LEVELS}")

    def to_json(self) -> Dict[str, Any]:
        return {
            "id": self.id,
            "dataset": self.dataset,
            "level": self.level,
            "query": self.query,
            "response": self.response,
            "context": list(self.context),
            "context_source": self.context_source,
            "label_hallucinated": self.label_hallucinated,
            "stratum": self.stratum,
            "meta": dict(self.meta),
        }

    @classmethod
    def from_json(cls, data: Mapping[str, Any]) -> "BenchExample":
        where = f"example {data.get('id')!r}"
        context = data.get("context")
        if not isinstance(context, list) or not all(isinstance(doc, str) for doc in context):
            raise DatasetFormatError(f"{where}: context must be a list of strings")
        label = data.get("label_hallucinated")
        if not isinstance(label, bool):
            raise DatasetFormatError(f"{where}: label_hallucinated must be a boolean")
        meta = data.get("meta") or {}
        if not isinstance(meta, dict):
            raise DatasetFormatError(f"{where}: meta must be an object")
        return cls(
            id=_require_str(data, "id", where),
            dataset=_require_str(data, "dataset", where),
            level=_require_str(data, "level", where),
            query=_require_str(data, "query", where),
            response=_require_str(data, "response", where),
            context=tuple(context),
            label_hallucinated=label,
            stratum=str(data.get("stratum") or ""),
            context_source=str(data.get("context_source") or DEFAULT_CONTEXT_SOURCE),
            meta={str(key): str(value) for key, value in meta.items()},
        )


def estimate_tokens(text: str) -> int:
    """A rough token count (CHARS_PER_TOKEN characters per token)."""
    return -(-len(text) // CHARS_PER_TOKEN)


def example_tokens(example: BenchExample) -> int:
    """Estimated tokens of what an example sends: query, response and evidence."""
    return sum(
        estimate_tokens(text) for text in (example.query, example.response, *example.context)
    )


@dataclass(frozen=True)
class DatasetManifest:
    """What a prepared file holds and how it was drawn (benchmark-run.v1 `dataset`)."""

    name: str  # e.g. "RAGTruth"
    subset: Optional[str]  # e.g. "qa"
    split: str
    n: int
    revision: str  # Source revision and content digest, e.g. "main@3f2a9c1b4d5e"
    license_note: str
    seed: int
    sampling: str  # e.g. "stratified:label,model"
    source_sha256: Mapping[str, str] = field(default_factory=dict)
    counts: Mapping[str, int] = field(default_factory=dict)  # positives, skipped, ...
    excluded_over_budget: int = 0  # Examples dropped as too long for the decision model

    @property
    def run_revision(self) -> str:
        """The revision recorded in a benchmark run: source revision plus the sampling draw."""
        return f"{self.revision}/{self.sampling}/seed{self.seed}"

    def to_json(self) -> Dict[str, Any]:
        return {
            "name": self.name,
            "subset": self.subset,
            "split": self.split,
            "n": self.n,
            "revision": self.revision,
            "license_note": self.license_note,
            "seed": self.seed,
            "sampling": self.sampling,
            "source_sha256": dict(self.source_sha256),
            "counts": dict(self.counts),
            "excluded_over_budget": self.excluded_over_budget,
        }

    @classmethod
    def from_json(cls, data: Mapping[str, Any]) -> "DatasetManifest":
        where = "dataset manifest"
        n, seed = data.get("n"), data.get("seed")
        if not isinstance(n, int) or not isinstance(seed, int):
            raise DatasetFormatError(f"{where}: n and seed must be integers")
        subset = data.get("subset")
        if subset is not None and not isinstance(subset, str):
            raise DatasetFormatError(f"{where}: subset must be a string or null")
        return cls(
            name=_require_str(data, "name", where),
            subset=subset,
            split=_require_str(data, "split", where),
            n=n,
            revision=_require_str(data, "revision", where),
            license_note=_require_str(data, "license_note", where),
            seed=seed,
            sampling=_require_str(data, "sampling", where),
            source_sha256={str(k): str(v) for k, v in (data.get("source_sha256") or {}).items()},
            counts={str(k): int(v) for k, v in (data.get("counts") or {}).items()},
            excluded_over_budget=int(data.get("excluded_over_budget") or 0),
        )


def manifest_path(path: Path) -> Path:
    """The manifest file next to a prepared `.jsonl` file."""
    return path.with_name(path.stem + MANIFEST_SUFFIX)


def write_prepared(path: Path, manifest: DatasetManifest, examples: Sequence[BenchExample]) -> None:
    """Write a prepared dataset (examples JSONL plus its manifest)."""
    if manifest.n != len(examples):
        raise DatasetFormatError(f"manifest n={manifest.n} but {len(examples)} examples")
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        for example in examples:
            f.write(json.dumps(example.to_json(), ensure_ascii=False, sort_keys=True) + "\n")
    with open(manifest_path(path), "w", encoding="utf-8", newline="\n") as f:
        json.dump(manifest.to_json(), f, indent=2, sort_keys=True)
        f.write("\n")


def read_jsonl(path: Path) -> List[Dict[str, Any]]:
    """The JSON objects of a JSONL file (blank lines skipped)."""
    rows: List[Dict[str, Any]] = []
    with open(path, encoding="utf-8") as f:
        for number, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError as exc:
                raise DatasetFormatError(f"{path}:{number}: invalid JSON ({exc})") from exc
            if not isinstance(row, dict):
                raise DatasetFormatError(f"{path}:{number}: expected a JSON object")
            rows.append(row)
    return rows


def load_prepared(path: Path) -> Tuple[DatasetManifest, List[BenchExample]]:
    """Read a prepared dataset and its manifest."""
    with open(manifest_path(path), encoding="utf-8") as f:
        manifest = DatasetManifest.from_json(json.load(f))
    examples = [BenchExample.from_json(row) for row in read_jsonl(path)]
    ids = [example.id for example in examples]
    if len(set(ids)) != len(ids):
        raise DatasetFormatError(f"{path}: duplicate example ids")
    if manifest.n != len(examples):
        raise DatasetFormatError(f"{path}: manifest n={manifest.n} but {len(examples)} examples")
    return manifest, examples
