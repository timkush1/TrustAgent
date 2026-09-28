"""
Benchmark dataset loaders (SPEC section 3.2 "Knowledge and evals"; A9).

Tiny synthetic RAGTruth and LLM-AggreFact rows stand in for the real files;
downloads go through a fake fetcher, so no network is used. Sampling must be
seeded, stratified and independent of row order.
"""

import json
from collections import Counter
from pathlib import Path
from typing import Any, Dict, List, Mapping

import pytest

from evals.datasets import aggrefact, ragtruth
from evals.datasets.fetch import combined_digest, download, sha256_file
from evals.datasets.sampling import allocate, stratified_sample
from evals.examples import (
    LEVEL_CLAIM,
    LEVEL_RESPONSE,
    BenchExample,
    DatasetFormatError,
    DatasetManifest,
    load_prepared,
    manifest_path,
    write_prepared,
)

# ---------------------------------------------------------------------------
# RAGTruth
# ---------------------------------------------------------------------------

QA_SOURCE: Dict[str, Any] = {
    "source_id": "s1",
    "task_type": "QA",
    "source": "MARCO",
    "source_info": {
        "question": "How long do refunds take?",
        "passages": "passage 1: Refunds take 30 days.\n\npassage 2: Shipping is free.",
    },
    "prompt": "Answer the question using the passages.",
}
SOURCES: List[Dict[str, Any]] = [
    QA_SOURCE,
    {"source_id": "s2", "task_type": "Summary", "source_info": "An article about refunds."},
    {"source_id": "s3", "task_type": "Data2txt", "source_info": {"name": "Cafe", "stars": 4}},
    {"source_id": "s4", "task_type": "QA", "source_info": "not a mapping"},
]


def _response(row_id: str, **fields: Any) -> Dict[str, Any]:
    row: Dict[str, Any] = {
        "id": row_id,
        "source_id": "s1",
        "model": "gpt-4-0613",
        "temperature": 0.7,
        "labels": [],
        "split": "test",
        "quality": "good",
        "response": f"Answer {row_id}.",
    }
    row.update(fields)
    return row


RESPONSES = [
    _response("r1"),
    _response("r2", labels=[{"start": 0, "end": 5, "label_type": "Evident Conflict"}]),
    _response(
        "r3",
        labels=[{"label_type": "Subtle Baseless Info"}, {"label_type": "Evident Conflict"}],
        model="llama-2-7b-chat",
    ),
    _response("r4", split="train"),
    _response("r5", quality="truncated"),
    _response("r6", source_id="s2"),
    _response("r7", response=None),
    _response("r8", source_id="s4"),
]


def test_split_passages_on_their_markers() -> None:
    assert ragtruth.split_passages("passage 1: A.\nPassage 2:B.\n  passage 10 : C") == [
        "A.",
        "B.",
        "C",
    ]
    assert ragtruth.split_passages("No markers here.") == ["No markers here."]
    assert ragtruth.split_passages("  ") == []


def test_ragtruth_qa_conversion() -> None:
    examples, counts = ragtruth.convert(RESPONSES, SOURCES, subset="qa", split="test")

    assert [example.id for example in examples] == [
        "ragtruth-qa-r1",
        "ragtruth-qa-r2",
        "ragtruth-qa-r3",
    ]
    faithful, conflict, both = examples
    assert faithful.label_hallucinated is False and faithful.meta["spans"] == "none"
    assert conflict.label_hallucinated is True and conflict.meta["spans"] == "conflict"
    assert both.meta["spans"] == "conflict+baseless"
    assert faithful.query == "How long do refunds take?"
    assert faithful.context == ("Refunds take 30 days.", "Shipping is free.")
    assert faithful.level == LEVEL_RESPONSE and faithful.context_source == "request_context"
    assert both.stratum == "1|llama-2-7b-chat" and faithful.stratum == "0|gpt-4-0613"
    assert counts == {
        "rows": 8,
        "other_task_or_split": 3,
        "low_quality": 1,
        "malformed": 1,
        "over_budget": 0,
        "eligible": 3,
        "positives_eligible": 2,
    }


def test_ragtruth_low_quality_can_be_included() -> None:
    examples, counts = ragtruth.convert(
        RESPONSES, SOURCES, subset="qa", split="test", include_low_quality=True
    )

    assert "ragtruth-qa-r5" in {example.id for example in examples}
    assert counts["low_quality"] == 0


def test_ragtruth_other_subsets() -> None:
    summary, _ = ragtruth.convert(
        [_response("r6", source_id="s2")], SOURCES, subset="summary", split="test"
    )
    data, _ = ragtruth.convert(
        [_response("r9", source_id="s3")], SOURCES, subset="data2txt", split="test"
    )

    assert summary[0].query == ragtruth.SUMMARY_QUERY
    assert summary[0].context == ("An article about refunds.",)
    assert data[0].context == ('{"name": "Cafe", "stars": 4}',)
    with pytest.raises(ValueError):
        ragtruth.convert([], SOURCES, subset="dialogue", split="test")


def test_ragtruth_excludes_and_counts_examples_over_the_token_budget() -> None:
    examples, counts = ragtruth.convert(
        RESPONSES, SOURCES, subset="qa", split="test", max_tokens=10
    )

    assert examples == [] and counts["over_budget"] == 3


def _many_rows(n: int) -> List[Dict[str, Any]]:
    rows = []
    for i in range(n):
        labels = [{"label_type": "Evident Conflict"}] if i % 4 == 0 else []
        model = "model-a" if i % 3 else "model-b"
        rows.append(_response(f"q{i:03d}", labels=labels, model=model))
    return rows


def _fake_fetch(files: Mapping[str, bytes], calls: List[str]) -> Any:
    def fetch(url: str, headers: Mapping[str, str]) -> bytes:
        calls.append(url)
        return files[url.rsplit("/", 1)[1]]

    return fetch


def _jsonl(rows: List[Dict[str, Any]]) -> bytes:
    return "".join(json.dumps(row) + "\n" for row in rows).encode()


def test_ragtruth_prepare_downloads_samples_and_writes_a_manifest(tmp_path: Path) -> None:
    rows = _many_rows(120)
    files = {"response.jsonl": _jsonl(rows), "source_info.jsonl": _jsonl(SOURCES)}
    calls: List[str] = []
    out = tmp_path / "prepared" / "rt.jsonl"

    path, manifest = ragtruth.prepare(
        n=40, seed=7, raw_dir=tmp_path / "raw", out=out, fetch=_fake_fetch(files, calls)
    )

    assert path == out
    assert calls == [
        "https://raw.githubusercontent.com/ParticleMedia/RAGTruth/main/dataset/response.jsonl",
        "https://raw.githubusercontent.com/ParticleMedia/RAGTruth/main/dataset/source_info.jsonl",
    ]
    loaded_manifest, examples = load_prepared(out)
    assert loaded_manifest == manifest
    assert manifest.n == len(examples) == 40
    assert manifest.name == "RAGTruth" and manifest.subset == "qa" and manifest.split == "test"
    hashes = {
        "response.jsonl": sha256_file(tmp_path / "raw" / "main" / "response.jsonl"),
        "source_info.jsonl": sha256_file(tmp_path / "raw" / "main" / "source_info.jsonl"),
    }
    assert manifest.source_sha256 == hashes
    assert manifest.revision == f"main@{combined_digest(hashes)[:12]}"
    assert manifest.run_revision == f"{manifest.revision}/stratified:label,model/seed7"
    assert "A9" in manifest.license_note
    # Proportional strata: 30 of 120 rows are hallucinated, so 10 of 40.
    assert manifest.counts["positives"] == sum(e.label_hallucinated for e in examples) == 10

    # Cached raw files: a second preparation does not download again.
    again, _ = ragtruth.prepare(
        n=40,
        seed=7,
        raw_dir=tmp_path / "raw",
        out=tmp_path / "again.jsonl",
        fetch=_fake_fetch(files, calls),
    )
    assert len(calls) == 2
    assert again.read_text() == out.read_text()


def test_ragtruth_sampling_is_seeded_and_label_balance_is_optional(tmp_path: Path) -> None:
    source_dir = tmp_path / "src"
    source_dir.mkdir()
    (source_dir / "response.jsonl").write_bytes(_jsonl(_many_rows(120)))
    (source_dir / "source_info.jsonl").write_bytes(_jsonl(SOURCES))

    def ids(seed: int, balance: bool = False) -> List[str]:
        path, _ = ragtruth.prepare(
            n=20,
            seed=seed,
            source_dir=source_dir,
            out=tmp_path / f"s{seed}{balance}.jsonl",
            balance_labels=balance,
        )
        return [example.id for example in load_prepared(path)[1]]

    assert ids(1) == ids(1)
    assert ids(1) != ids(2)
    _, balanced = load_prepared(
        ragtruth.prepare(
            n=20, seed=3, source_dir=source_dir, out=tmp_path / "b.jsonl", balance_labels=True
        )[0]
    )
    assert Counter(example.label_hallucinated for example in balanced) == {True: 10, False: 10}


def test_ragtruth_prepare_fails_loudly_without_examples(tmp_path: Path) -> None:
    source_dir = tmp_path / "src"
    source_dir.mkdir()
    (source_dir / "response.jsonl").write_bytes(_jsonl([_response("x", split="train")]))
    (source_dir / "source_info.jsonl").write_bytes(_jsonl(SOURCES))

    with pytest.raises(DatasetFormatError, match="A9"):
        ragtruth.prepare(n=5, source_dir=source_dir, out=tmp_path / "x.jsonl")
    assert (
        ragtruth.main(
            ["--n", "5", "--source-dir", str(source_dir), "--out", str(tmp_path / "y.jsonl")]
        )
        == 1
    )


def test_download_refuses_plain_http(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="non-HTTPS"):
        download("http://example.com/file", tmp_path / "file", fetch=lambda url, headers: b"")


# ---------------------------------------------------------------------------
# LLM-AggreFact
# ---------------------------------------------------------------------------


def _aggrefact_row(index: int, sub: str, label: int, **extra: Any) -> Dict[str, Any]:
    return {
        "row_idx": index,
        "row": {
            "dataset": sub,
            "doc": f"Document {index}.",
            "claim": f"Claim {index}.",
            "label": label,
        },
        "truncated_cells": [],
        **extra,
    }


def test_aggrefact_conversion() -> None:
    rows = [
        _aggrefact_row(0, "ExpertQA", 1),
        _aggrefact_row(1, "ExpertQA", 0),
        _aggrefact_row(2, "RAGTruth", 1, truncated_cells=["doc"]),
        {"row_idx": 3, "row": {"dataset": "X", "doc": "d", "claim": "c", "label": 2}},
        {"dataset": "Lfqa", "doc": "Plain export row.", "claim": "A claim.", "label": 0},
    ]

    examples, counts = aggrefact.convert(rows, split="test")

    assert [example.id for example in examples] == [
        "aggrefact-test-0",
        "aggrefact-test-1",
        "aggrefact-test-4",
    ]
    supported, unsupported, plain = examples
    assert supported.level == LEVEL_CLAIM and supported.label_hallucinated is False
    assert unsupported.label_hallucinated is True
    assert supported.response == "Claim 0." and supported.context == ("Document 0.",)
    assert supported.query == "" and supported.stratum == "ExpertQA|0"
    assert plain.meta == {"sub_dataset": "Lfqa"}
    assert counts == {
        "rows": 5,
        "malformed": 1,
        "truncated_by_source": 1,
        "over_budget": 0,
        "eligible": 3,
    }


def test_aggrefact_rows_api_is_paged_with_the_token() -> None:
    pages = {
        0: {"rows": [_aggrefact_row(i, "A", i % 2) for i in range(100)], "num_rows_total": 150},
        100: {
            "rows": [_aggrefact_row(i, "A", i % 2) for i in range(100, 150)],
            "num_rows_total": 150,
        },
    }
    seen: List[Dict[str, Any]] = []

    def fetch(url: str, headers: Mapping[str, str]) -> bytes:
        from urllib.parse import parse_qs, urlparse

        query = {key: values[0] for key, values in parse_qs(urlparse(url).query).items()}
        seen.append({"query": query, "headers": dict(headers)})
        return json.dumps(pages[int(query["offset"])]).encode()

    rows = aggrefact.fetch_rows("test", token="hf_secret", fetch=fetch)

    assert len(rows) == 150
    assert [call["query"]["offset"] for call in seen] == ["0", "100"]
    assert seen[0]["query"]["dataset"] == "lytang/LLM-AggreFact"
    assert seen[0]["query"]["split"] == "test" and seen[0]["query"]["length"] == "100"
    assert seen[0]["headers"] == {"Authorization": "Bearer hf_secret"}


def test_aggrefact_prepare_from_a_local_export(tmp_path: Path) -> None:
    export = tmp_path / "export.jsonl"
    rows = [
        {"dataset": sub, "doc": f"Doc {i}.", "claim": f"Claim {i}.", "label": i % 2}
        for i, sub in enumerate(["A"] * 40 + ["B"] * 20)
    ]
    export.write_bytes(_jsonl(rows))

    path, manifest = aggrefact.prepare(n=30, seed=5, from_file=export, out=tmp_path / "af.jsonl")

    _, examples = load_prepared(path)
    assert manifest.n == 30 and manifest.subset is None
    assert manifest.revision == f"local@{sha256_file(export)[:12]}"
    assert Counter(example.meta["sub_dataset"] for example in examples) == {"A": 20, "B": 10}
    assert all(example.level == LEVEL_CLAIM for example in examples)


# ---------------------------------------------------------------------------
# Sampling and prepared files
# ---------------------------------------------------------------------------


def test_allocate_uses_largest_remainders() -> None:
    assert allocate({"a": 50, "b": 30, "c": 20}, 10) == {"a": 5, "b": 3, "c": 2}
    # Quotas 3.33 / 3.33 / 3.33: the remainder goes to the first key on a tie.
    assert allocate({"a": 10, "b": 10, "c": 10}, 10) == {"a": 4, "b": 3, "c": 3}
    assert allocate({"a": 3, "b": 1}, 10) == {"a": 3, "b": 1}
    assert allocate({"a": 3, "b": 1}, 0) == {"a": 0, "b": 0}


def test_stratified_sample_is_independent_of_input_order() -> None:
    items = [f"{group}{i:02d}" for group in "xy" for i in range(30)]

    first = stratified_sample(items, lambda item: item[0], 12, 42, key=str)
    second = stratified_sample(list(reversed(items)), lambda item: item[0], 12, 42, key=str)

    assert first == second == sorted(first)
    assert Counter(item[0] for item in first) == {"x": 6, "y": 6}
    assert stratified_sample(items, lambda item: item[0], 100, 1, key=str) == sorted(items)


def test_balanced_sampling_needs_strata_within_one_group() -> None:
    items = ["a1", "a2", "b1"]
    with pytest.raises(ValueError, match="mixes balance groups"):
        stratified_sample(items, lambda item: "one", 2, 1, key=str, balance=lambda item: item[0])


def _manifest(n: int) -> DatasetManifest:
    return DatasetManifest(
        name="Synthetic",
        subset=None,
        split="test",
        n=n,
        revision="r@1",
        license_note="test",
        seed=1,
        sampling="stratified:label",
    )


def test_prepared_files_round_trip_and_are_checked(tmp_path: Path) -> None:
    example = BenchExample(
        id="e1",
        dataset="synthetic",
        level=LEVEL_RESPONSE,
        query="q",
        response="r",
        context=("d",),
        label_hallucinated=True,
        stratum="1",
        meta={"k": "v"},
    )
    path = tmp_path / "set.jsonl"

    write_prepared(path, _manifest(1), [example])

    assert manifest_path(path).name == "set.manifest.json"
    assert load_prepared(path) == (_manifest(1), [example])
    with pytest.raises(DatasetFormatError):
        write_prepared(path, _manifest(2), [example])
    path.write_text(path.read_text() * 2)
    with pytest.raises(DatasetFormatError, match="duplicate"):
        load_prepared(path)
    with pytest.raises(DatasetFormatError, match="level"):
        BenchExample(
            id="x",
            dataset="d",
            level="document",
            query="",
            response="",
            context=(),
            label_hallucinated=False,
        )
    with pytest.raises(DatasetFormatError, match="label_hallucinated"):
        BenchExample.from_json({**example.to_json(), "label_hallucinated": "yes"})
