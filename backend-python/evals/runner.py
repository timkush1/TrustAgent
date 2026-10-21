"""
Benchmark runner (SPEC section 3.2: concurrency, cost cap, resumable cache).

`run_judge` judges every example with one system:

- at most `concurrency` examples at once;
- each result is appended to a JSON Lines cache keyed by
  sha256(example id + the example's content digest + the judge's
  fingerprint: system, provider, model, prompts or binary), so an
  interrupted run resumes where it stopped, and neither a changed judge nor
  an example whose text or label changed under the same id (another dataset
  revision or preparation option) reuses stale results. Failed examples are cached too
  (a resumed run does not pay for them again) unless `retry_failed`;
  over-budget examples are not, so a resumed run with a larger cap runs them;
- the run's CostBudget is shared by every system: once a reservation is
  refused, no new example starts and the rest are recorded over_budget
  (excluded and counted, never silently dropped);
- one example raising never ends the run (D17): it is recorded failed.
"""

import asyncio
import hashlib
import json
import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence

from .budget import CostBudget
from .examples import BenchExample
from .judges import FAILED, OVER_BUDGET, Judge, JudgeRecord

logger = logging.getLogger(__name__)

DEFAULT_CONCURRENCY = 4
EXAMPLE_TIMEOUT_S = 600.0

# (system, done, total) after each example.
Progress = Callable[[str, int, int], None]


def _sha256_json(value: Any) -> str:
    canonical = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def example_digest(example: BenchExample) -> str:
    """SHA-256 of everything an example carries (texts, evidence, label, meta)."""
    return _sha256_json(example.to_json())


def cache_key(example_id: str, fingerprint: Mapping[str, Any], example_sha256: str = "") -> str:
    """The cache key of one example's result under one judge configuration."""
    return _sha256_json({"example": example_id, "content": example_sha256, "judge": fingerprint})


class ResultCache:
    """
    An append-only JSON Lines map of cache key -> JudgeRecord (the last line
    for a key wins). A partly written last line (an interrupted run) is ignored.
    """

    def __init__(self, path: Path) -> None:
        self.path = path
        self._records: Dict[str, JudgeRecord] = {}
        if path.exists():
            with open(path, encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        entry = json.loads(line)
                        self._records[str(entry["key"])] = JudgeRecord.from_json(entry["record"])
                    except (json.JSONDecodeError, KeyError, TypeError, ValueError):
                        logger.warning(f"Skipping an unreadable cache line in {path}")

    def __len__(self) -> int:
        return len(self._records)

    def get(self, key: str) -> Optional[JudgeRecord]:
        return self._records.get(key)

    def put(self, key: str, record: JudgeRecord) -> None:
        self._records[key] = record
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with open(self.path, "a", encoding="utf-8", newline="\n") as f:
            f.write(json.dumps({"key": key, "record": record.to_json()}, sort_keys=True) + "\n")


@dataclass
class SystemRun:
    """One system's records, in example order."""

    system: str
    records: List[JudgeRecord]
    reused: int = 0  # From the cache
    ran: int = 0  # Judges called in this session (not those skipped past the cap)

    def by_id(self) -> Dict[str, JudgeRecord]:
        return {record.example_id: record for record in self.records}


def _over_budget(judge: Judge, example: BenchExample) -> JudgeRecord:
    return JudgeRecord(
        example_id=example.id,
        system=judge.system,
        status=OVER_BUDGET,
        label=example.label_hallucinated,
        error_kind="cost_cap",
    )


async def run_judge(
    judge: Judge,
    examples: Sequence[BenchExample],
    *,
    budget: CostBudget,
    cache: ResultCache,
    concurrency: int = DEFAULT_CONCURRENCY,
    retry_failed: bool = False,
    example_timeout_s: float = EXAMPLE_TIMEOUT_S,
    progress: Optional[Progress] = None,
) -> SystemRun:
    """Judge `examples` with `judge` (see the module docstring)."""
    fingerprint = judge.fingerprint()
    semaphore = asyncio.Semaphore(max(1, concurrency))
    run = SystemRun(system=judge.system, records=[])
    done = 0

    async def one(example: BenchExample) -> JudgeRecord:
        nonlocal done
        key = cache_key(example.id, fingerprint, example_digest(example))
        cached = cache.get(key)
        if cached is not None and not (retry_failed and cached.status == FAILED):
            run.reused += 1
            return cached
        async with semaphore:
            if budget.exhausted:
                record = _over_budget(judge, example)
            else:
                run.ran += 1
                try:
                    record = await asyncio.wait_for(judge.judge(example, budget), example_timeout_s)
                except Exception as exc:  # one broken example must not end the run (D17)
                    logger.warning(f"{judge.system}: example {example.id} raised: {exc!r}")
                    record = JudgeRecord(
                        example_id=example.id,
                        system=judge.system,
                        status=FAILED,
                        label=example.label_hallucinated,
                        error_kind="timeout" if isinstance(exc, TimeoutError) else "internal",
                    )
            if record.status != OVER_BUDGET:
                cache.put(key, record)
            done += 1
            if progress is not None:
                progress(judge.system, done, len(examples))
            return record

    run.records = list(await asyncio.gather(*(one(example) for example in examples)))
    return run
