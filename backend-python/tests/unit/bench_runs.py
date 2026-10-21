"""
Publishable benchmark runs for the report and publish tests (P6: D203-D207).

`p6_run` builds a run the way `evals.bench run` does on the vendor's hosts:
40 RAGTruth-QA answers (10 hallucinated) through the gate, the LLM same checks
and the full audit, every result scored, the P6 blocks filled in. Each knob
breaks one publish rule, so a test can show that rule's refusal alone.
"""

from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence, Tuple

from evals.examples import DatasetManifest
from evals.judges import (
    FAILED,
    GATE,
    LLM_FULL_AUDIT,
    LLM_SAME_CHECKS,
    OK,
    OVER_BUDGET,
    JudgeRecord,
    SystemDescription,
)
from evals.report import build_benchmark_run, summarize_system, write_local_run

SNAPSHOT = "typesafe/jev-1.13-20260917"
REVISION = "c103204b9ce28d6bbad859304bf30de72b8ed8fe"
VENDOR = "openrouter.ai"
N = 40

MANIFEST = DatasetManifest(
    name="RAGTruth",
    subset="qa",
    split="test",
    n=N,
    revision=f"{REVISION}@0123456789ab",
    license_note="RAGTruth (github.com/ParticleMedia/RAGTruth), MIT. Only metrics are published.",
    seed=13,
    sampling="stratified:label,model",
)

DESCRIPTIONS = {
    GATE: SystemDescription(decision_model=("openrouter-decisions", SNAPSHOT)),
    LLM_SAME_CHECKS: SystemDescription(judge=("openrouter", "anthropic/claude-haiku-4.5")),
    LLM_FULL_AUDIT: SystemDescription(judge=("openrouter", "anthropic/claude-haiku-4.5")),
}


def example_rows(n: int = N) -> List[Tuple[str, bool, bool, float]]:
    """(id, label, predicted, score): 10 of 40 hallucinated; the gate gets 34 of 40 right."""
    rows = []
    for i in range(n):
        label = i % 4 == 0
        wrong = i in (1, 5, 9, 13, 4, 8)  # 4 false positives, 2 false negatives
        predicted = (not label) if wrong else label
        score = 0.85 if predicted else 0.15
        rows.append((f"ragtruth-qa-{i}", label, predicted, score))
    return rows


def gate_records(
    *,
    host: Optional[str] = VENDOR,
    snapshot: str = SNAPSHOT,
    other_snapshot: Optional[str] = None,
    failed: int = 0,
    excluded: int = 0,
    mock: bool = False,
    cost_source: str = "reported",
    connection: str = "warm",
) -> List[JudgeRecord]:
    records = []
    for index, (example_id, label, predicted, score) in enumerate(example_rows()):
        status = FAILED if index < failed else OVER_BUDGET if index < failed + excluded else OK
        records.append(
            JudgeRecord(
                example_id=example_id,
                system=GATE,
                status=status,
                label=label,
                predicted=predicted if status == OK else None,
                score=score if status == OK else None,
                latency_ms=150.0 + index,
                cost_usd=0.00004,
                cost_source="simulated" if mock else cost_source,
                calls=1,
                error_kind={OK: None, FAILED: "gate_error:credits"}.get(status, "cost_cap"),
                mock=mock,
                details={
                    "backend": "mock" if mock else "openrouter-decisions",
                    "snapshot": other_snapshot if other_snapshot and index == N - 1 else snapshot,
                    "endpoint_host": None if mock else host,
                    "connection": connection,
                    "risk_raw": {"groundedness@1": score, "contradiction@1": score / 2},
                    "labels": {
                        "groundedness@1": label,
                        "contradiction@1": label and index % 8 == 0,
                    },
                    "verdict": "unsupported" if predicted else "supported",
                },
            )
        )
    return records


def judge_records(
    system: str, *, host: Optional[str] = VENDOR, mock: bool = False, cost_source: str = "reported"
) -> List[JudgeRecord]:
    return [
        JudgeRecord(
            example_id=example_id,
            system=system,
            status=OK,
            label=label,
            predicted=label,
            score=0.9 if label else 0.1,
            latency_ms=900.0,
            cost_usd=0.003 if system == LLM_SAME_CHECKS else 0.03,
            cost_source="simulated" if mock else cost_source,
            calls=1 if system == LLM_SAME_CHECKS else 9,
            mock=mock,
            details={"models": ["anthropic/claude-haiku-4.5"], "host": host},
        )
        for example_id, label, _, _ in example_rows()
    ]


def p6_run(
    *,
    run_id: str = "lite-p6-test",
    created_at: str = "2026-10-12T10:00:00Z",
    systems: Sequence[str] = (GATE, LLM_SAME_CHECKS, LLM_FULL_AUDIT),
    gate_host: Optional[str] = VENDOR,
    judge_host: Optional[str] = VENDOR,
    dirty: bool = False,
    failed: int = 0,
    excluded: int = 0,
    mock: bool = False,
    other_snapshot: Optional[str] = None,
    cost_source: str = "reported",
    connection: str = "warm",
) -> Tuple[Dict[str, Any], str, Dict[str, List[JudgeRecord]]]:
    """(document, label, records) of a run; publishable with the defaults."""
    records: Dict[str, List[JudgeRecord]] = {}
    if GATE in systems:
        records[GATE] = gate_records(
            host=gate_host,
            failed=failed,
            excluded=excluded,
            mock=mock,
            other_snapshot=other_snapshot,
            cost_source=cost_source,
            connection=connection,
        )
    for system in (LLM_SAME_CHECKS, LLM_FULL_AUDIT):
        if system in systems:
            records[system] = judge_records(system, host=judge_host, mock=mock)
    summaries = [
        summarize_system(system, records[system], DESCRIPTIONS[system], bootstrap_resamples=50)
        for system in records
    ]
    document, label = build_benchmark_run(
        run_id=run_id,
        created_at=created_at,
        git_sha="289a99a",
        tier="lite",
        manifest=MANIFEST,
        systems=[summary for summary in summaries if summary is not None],
        records_by_system=records,
        region="il",
        concurrency=4,
        vendor={
            "decision_host": gate_host if GATE in systems and not mock else None,
            "judge_host": judge_host if set(systems) - {GATE} else None,
        },
        inputs={
            "dataset_revision": REVISION,
            "policy_source": "builtin",
            "gate_eval_sha256": "a" * 64,
            "host_env": {
                "mem_available_mb": 3416,
                "loadavg": [0.5, 0.6, 0.7],
                "platform": "WSL2",
                "region": "il",
            },
        },
        dirty=dirty,
        latency={"connection": connection, "gate_concurrency": 1},
    )
    return document, label, records


def write_p6_run(directory: Path, **knobs: Any) -> Path:
    """Write a p6_run as evals.bench does (<directory>/<run_id>/run.json)."""
    document, label, records = p6_run(**knobs)
    return write_local_run(directory, document, label, records, MANIFEST)
