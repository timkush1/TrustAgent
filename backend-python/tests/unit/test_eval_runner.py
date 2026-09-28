"""
Benchmark runner and cost cap (SPEC section 3.2; P6 "hard cap $5"): bounded
concurrency, the resumable cache keyed by example + judge, over-budget
exclusion, and a spend ledger that carries the cap across sessions.
"""

import asyncio
import dataclasses
import json
from pathlib import Path
from typing import Any, Dict, List, Sequence

import pytest

from evals.budget import BudgetExhausted, CostBudget, ledger_total
from evals.examples import BenchExample
from evals.judges import FAILED, OK, OVER_BUDGET, JudgeRecord, SystemDescription
from evals.runner import ResultCache, cache_key, run_judge

from bench_fakes import example


class FakeJudge:
    """Charges `cost` per example through the budget; can fail or raise on demand."""

    system = "llm_same_checks"

    def __init__(
        self,
        *,
        cost: float = 1.0,
        model: str = "m1",
        fail: Sequence[str] = (),
        raise_on: Sequence[str] = (),
        delay: float = 0.0,
    ) -> None:
        self.cost = cost
        self.model = model
        self.fail = set(fail)
        self.raise_on = set(raise_on)
        self.delay = delay
        self.judged: List[str] = []
        self.in_flight = 0
        self.max_in_flight = 0

    def fingerprint(self) -> Dict[str, Any]:
        return {"system": self.system, "model": self.model}

    def describe(self, records: Sequence[JudgeRecord]) -> SystemDescription:
        return SystemDescription(judge=("fake", self.model))

    async def judge(self, item: BenchExample, budget: CostBudget) -> JudgeRecord:
        self.in_flight += 1
        self.max_in_flight = max(self.max_in_flight, self.in_flight)
        try:
            await asyncio.sleep(self.delay)
            if item.id in self.raise_on:
                raise RuntimeError("judge bug")
            try:
                reservation = budget.reserve(self.cost)
            except BudgetExhausted:
                return JudgeRecord(item.id, self.system, OVER_BUDGET, item.label_hallucinated)
            charged = budget.settle(reservation, self.cost, system=self.system, example_id=item.id)
            self.judged.append(item.id)
            status = FAILED if item.id in self.fail else OK
            return JudgeRecord(
                item.id,
                self.system,
                status,
                item.label_hallucinated,
                predicted=True if status == OK else None,
                score=0.9 if status == OK else None,
                cost_usd=charged,
                cost_source="computed",
                calls=1,
            )
        finally:
            self.in_flight -= 1


EXAMPLES = [example(f"e{i}") for i in range(5)]


# ---------------------------------------------------------------------------
# CostBudget
# ---------------------------------------------------------------------------


def test_budget_reserves_settles_and_releases(tmp_path: Path) -> None:
    ledger = tmp_path / "ledger.jsonl"
    budget = CostBudget(1.0, ledger_path=ledger)

    first = budget.reserve(0.6)
    assert budget.reserved_usd == pytest.approx(0.6) and budget.remaining_usd == pytest.approx(0.4)
    with pytest.raises(BudgetExhausted, match="cost cap"):
        budget.reserve(0.5)
    assert budget.exhausted
    assert (
        budget.settle(first, 0.25, system="gate", example_id="e1", cost_source="reported") == 0.25
    )
    assert budget.settle(first, 0.25) == 0.0  # settling twice charges nothing
    second = budget.reserve(0.75)  # exactly up to the cap
    budget.release(second)
    unknown = budget.reserve(0.1)
    assert budget.settle(unknown, None) == pytest.approx(0.1)  # unknown cost: the reservation

    assert budget.spent_usd == pytest.approx(0.35) and budget.reserved_usd == 0
    entries = [json.loads(line) for line in ledger.read_text().splitlines()]
    assert [entry["usd"] for entry in entries] == [0.25, pytest.approx(0.1)]
    assert entries[0]["system"] == "gate" and entries[0]["cost_source"] == "reported"
    assert entries[0]["at"].endswith("Z")
    assert ledger_total(ledger) == pytest.approx(0.35)
    assert ledger_total(tmp_path / "missing.jsonl") == 0.0
    with pytest.raises(ValueError):
        CostBudget(-1)


def test_a_resumed_budget_starts_from_the_ledger(tmp_path: Path) -> None:
    ledger = tmp_path / "ledger.jsonl"
    first = CostBudget.open(5.0, ledger)
    first.settle(first.reserve(4.5), 4.5)

    resumed = CostBudget.open(5.0, ledger)

    assert resumed.spent_usd == pytest.approx(4.5)
    with pytest.raises(BudgetExhausted):
        resumed.reserve(0.6)


# ---------------------------------------------------------------------------
# run_judge
# ---------------------------------------------------------------------------


async def test_concurrency_is_bounded(tmp_path: Path) -> None:
    judge = FakeJudge(cost=0.0, delay=0.02)

    run = await run_judge(
        judge,
        [example(f"c{i}") for i in range(12)],
        budget=CostBudget(1.0),
        cache=ResultCache(tmp_path / "cache.jsonl"),
        concurrency=3,
    )

    assert judge.max_in_flight == 3
    assert [record.example_id for record in run.records] == [f"c{i}" for i in range(12)]


async def test_the_cost_cap_excludes_the_rest_and_a_resume_finishes(tmp_path: Path) -> None:
    ledger, cache_path = tmp_path / "ledger.jsonl", tmp_path / "cache.jsonl"
    judge = FakeJudge(cost=1.0)

    first = await run_judge(
        judge,
        EXAMPLES,
        budget=CostBudget.open(3.0, ledger),
        cache=ResultCache(cache_path),
        concurrency=1,
    )

    assert [record.status for record in first.records] == [OK, OK, OK, OVER_BUDGET, OVER_BUDGET]
    # e3 was refused its reservation; e4 never started.
    assert first.ran == 4 and judge.judged == ["e0", "e1", "e2"]
    assert ledger_total(ledger) == pytest.approx(3.0)

    # Same cap, resumed: nothing new may be spent, nothing is judged twice.
    again = await run_judge(
        judge,
        EXAMPLES,
        budget=CostBudget.open(3.0, ledger),
        cache=ResultCache(cache_path),
        concurrency=1,
    )
    assert again.reused == 3 and judge.judged == ["e0", "e1", "e2"]
    assert [record.status for record in again.records][3:] == [OVER_BUDGET, OVER_BUDGET]

    # A larger cap finishes the run: only the excluded examples are judged.
    done = await run_judge(
        judge,
        EXAMPLES,
        budget=CostBudget.open(5.0, ledger),
        cache=ResultCache(cache_path),
        concurrency=2,
    )
    assert [record.status for record in done.records] == [OK] * 5
    assert judge.judged == ["e0", "e1", "e2", "e3", "e4"] and done.reused == 3
    assert ledger_total(ledger) == pytest.approx(5.0)


async def test_failures_are_cached_unless_retried(tmp_path: Path) -> None:
    cache = ResultCache(tmp_path / "cache.jsonl")
    judge = FakeJudge(cost=0.0, fail=["e1"], raise_on=["e2"])

    first = await run_judge(judge, EXAMPLES[:3], budget=CostBudget(1.0), cache=cache)

    assert [record.status for record in first.records] == [OK, FAILED, FAILED]
    assert first.records[2].error_kind == "internal"
    judge.fail.clear()
    judge.raise_on.clear()
    cached = await run_judge(judge, EXAMPLES[:3], budget=CostBudget(1.0), cache=cache)
    assert cached.reused == 3 and [r.status for r in cached.records] == [OK, FAILED, FAILED]

    retried = await run_judge(
        judge, EXAMPLES[:3], budget=CostBudget(1.0), cache=cache, retry_failed=True
    )
    assert retried.reused == 1 and [r.status for r in retried.records] == [OK, OK, OK]


async def test_a_new_judge_configuration_does_not_reuse_results(tmp_path: Path) -> None:
    cache = ResultCache(tmp_path / "cache.jsonl")
    await run_judge(FakeJudge(cost=0.0), EXAMPLES[:2], budget=CostBudget(1.0), cache=cache)

    other = FakeJudge(cost=0.0, model="m2")
    run = await run_judge(other, EXAMPLES[:2], budget=CostBudget(1.0), cache=cache)

    assert run.reused == 0 and other.judged == ["e0", "e1"]
    assert cache_key("e0", {"model": "m1"}) != cache_key("e0", {"model": "m2"})
    assert cache_key("e0", {"a": 1, "b": 2}) == cache_key("e0", {"b": 2, "a": 1})


async def test_a_changed_example_under_the_same_id_is_judged_again(tmp_path: Path) -> None:
    # Another dataset revision (or preparation option) can change an example's
    # text or label under the same id: its cached result must not be reused.
    cache = ResultCache(tmp_path / "cache.jsonl")
    judge = FakeJudge(cost=0.0)
    await run_judge(judge, EXAMPLES[:1], budget=CostBudget(1.0), cache=cache)

    relabelled = dataclasses.replace(EXAMPLES[0], label_hallucinated=False)
    run = await run_judge(judge, [relabelled], budget=CostBudget(1.0), cache=cache)

    assert run.reused == 0 and judge.judged == ["e0", "e0"]
    assert run.records[0].label is False
    again = await run_judge(judge, [relabelled], budget=CostBudget(1.0), cache=cache)
    assert again.reused == 1


async def test_a_slow_example_times_out(tmp_path: Path) -> None:
    run = await run_judge(
        FakeJudge(cost=0.0, delay=1.0),
        EXAMPLES[:1],
        budget=CostBudget(1.0),
        cache=ResultCache(tmp_path / "cache.jsonl"),
        example_timeout_s=0.05,
    )

    assert run.records[0].status == FAILED and run.records[0].error_kind == "timeout"


async def test_progress_is_reported(tmp_path: Path) -> None:
    seen: List[Any] = []

    await run_judge(
        FakeJudge(cost=0.0),
        EXAMPLES[:2],
        budget=CostBudget(1.0),
        cache=ResultCache(tmp_path / "cache.jsonl"),
        progress=lambda system, done, total: seen.append((system, done, total)),
    )

    assert seen == [("llm_same_checks", 1, 2), ("llm_same_checks", 2, 2)]


def test_cache_survives_a_partly_written_line(tmp_path: Path) -> None:
    path = tmp_path / "cache.jsonl"
    cache = ResultCache(path)
    record = JudgeRecord("e0", "gate", OK, True, predicted=True, score=0.7)
    cache.put("k1", record)
    with open(path, "a", encoding="utf-8") as f:
        f.write('{"key": "k2", "record": {"example_')

    reopened = ResultCache(path)

    assert len(reopened) == 1 and reopened.get("k1") == record
    assert reopened.get("k2") is None
