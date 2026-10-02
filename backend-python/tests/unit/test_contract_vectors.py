"""
The Python runner of the contracts `cost` vectors (SPEC sections 3.2 and 7.6,
P4-PLAN D85).

Every vector in contracts/vectors/cost names the calculator function it pins in
`input.fn`: `call_cost`, `per_1k` or `price_lookup` (backend-go
internal/decision/cost.go, the reference; `expected` is written by Go
cmd/vectorgen). Python runs `call_cost` only, against providers/pricing.py: the
engine receives the already-effective table (ApplyRuntimeConfig
`price_table_json`), so the layer lookup and the per-1k estimate are Go's alone.

A `call_cost` input is
    {"fn": "call_cost", "provider", "model", "input_tokens", "output_tokens",
     "reported_cost_usd"?, "table" (a pricing.v1 document)}
and its expected output is exactly {"cost_usd", "cost_source", "price_table_version"}.
Numbers compare within the vector's `tolerance` (default 1e-9); an explicit 0 is exact.
"""

import json
import math
from pathlib import Path
from typing import Any, Dict, List

import pytest

from truthtable.contracts import contracts_dir
from truthtable.providers.pricing import PriceTable

COST_FNS = frozenset({"call_cost", "per_1k", "price_lookup"})
CALL_COST_FIELDS = frozenset(
    {"fn", "provider", "model", "input_tokens", "output_tokens", "reported_cost_usd", "table"}
)
CALL_COST_REQUIRED = frozenset(
    {"fn", "provider", "model", "input_tokens", "output_tokens", "table"}
)
CALL_COST_RESULT = frozenset({"cost_usd", "cost_source", "price_table_version"})
DEFAULT_TOLERANCE = 1e-9

COST_DIR = contracts_dir() / "vectors" / "cost"


def _load(path: Path) -> Dict[str, Any]:
    vector = json.loads(path.read_text(encoding="utf-8"))
    assert isinstance(vector, dict), path.name
    return vector


ALL_COST_VECTORS: List[Path] = sorted(COST_DIR.glob("*.json"))
CALL_COST_VECTORS: List[Path] = [
    path for path in ALL_COST_VECTORS if _load(path)["input"].get("fn") == "call_cost"
]


def tolerance_of(vector: Dict[str, Any]) -> float:
    """The vector's number tolerance: an explicit 0 is exact; only a missing one defaults."""
    tolerance = vector.get("tolerance")
    return DEFAULT_TOLERANCE if tolerance is None else float(tolerance)


def matches(expected: Any, actual: Any, tolerance: float) -> bool:
    """Whether `actual` equals `expected`, numbers within `tolerance`, objects key for key."""
    if isinstance(expected, dict):
        return (
            isinstance(actual, dict)
            and set(expected) == set(actual)
            and all(matches(value, actual[key], tolerance) for key, value in expected.items())
        )
    if isinstance(expected, list):
        return (
            isinstance(actual, list)
            and len(expected) == len(actual)
            and all(matches(e, a, tolerance) for e, a in zip(expected, actual))
        )
    if isinstance(expected, bool) or expected is None or isinstance(expected, str):
        return type(expected) is type(actual) and expected == actual
    if isinstance(expected, (int, float)):
        return (
            isinstance(actual, (int, float))
            and not isinstance(actual, bool)
            and math.isclose(expected, actual, rel_tol=0.0, abs_tol=tolerance)
        )
    return bool(expected == actual)


def run_call_cost(spec: Dict[str, Any]) -> Dict[str, Any]:
    """Price one call_cost input with providers/pricing.py, as Go's runner does with PriceCall."""
    unknown = set(spec) - CALL_COST_FIELDS
    assert not unknown, f"call_cost input has unknown members {sorted(unknown)}"
    missing = CALL_COST_REQUIRED - set(spec)
    assert not missing, f"call_cost input lacks {sorted(missing)}"
    for key in ("input_tokens", "output_tokens"):
        assert isinstance(spec[key], int) and not isinstance(spec[key], bool), key
    cost = PriceTable.from_document(spec["table"]).cost(
        spec["provider"],
        spec["model"],
        spec["input_tokens"],
        spec["output_tokens"],
        spec.get("reported_cost_usd"),
    )
    return {
        "cost_usd": cost.cost_usd,
        "cost_source": cost.cost_source,
        "price_table_version": cost.price_table_version,
    }


# ---------------------------------------------------------------------------
# The committed vectors
# ---------------------------------------------------------------------------


def test_the_cost_vectors_are_present_and_name_a_known_fn() -> None:
    # D85: at least 8 cost vectors; a vector with an unknown fn would run nowhere.
    assert len(ALL_COST_VECTORS) >= 8, COST_DIR
    for path in ALL_COST_VECTORS:
        vector = _load(path)
        assert vector["kind"] == "cost", path.name
        assert vector["name"] == path.stem, path.name
        assert vector["status"] in {"draft", "frozen"}, path.name
        assert vector["input"].get("fn") in COST_FNS, path.name
    # Python's share is every call_cost vector, and there is at least one per rule it ports.
    assert len(CALL_COST_VECTORS) >= 8


@pytest.mark.parametrize("path", CALL_COST_VECTORS, ids=[p.stem for p in CALL_COST_VECTORS])
def test_call_cost_vector(path: Path) -> None:
    vector = _load(path)
    assert set(vector["expected"]) == CALL_COST_RESULT, path.name
    actual = run_call_cost(vector["input"])
    assert matches(vector["expected"], actual, tolerance_of(vector)), actual


# ---------------------------------------------------------------------------
# The runner itself
# ---------------------------------------------------------------------------

TABLE = {
    "version": "2026-09-27",
    "currency": "USD",
    "entries": [
        {
            "match": "claude-haiku-4-5*",
            "provider": "anthropic",
            "input_per_mtok": 1,
            "output_per_mtok": 5,
        }
    ],
}


def _inline(**overrides: Any) -> Dict[str, Any]:
    spec: Dict[str, Any] = {
        "fn": "call_cost",
        "provider": "anthropic",
        "model": "claude-haiku-4-5",
        "input_tokens": 2000,
        "output_tokens": 100,
        "table": TABLE,
    }
    spec.update(overrides)
    return spec


def test_runner_prices_an_inline_vector_and_refuses_a_wrong_expectation() -> None:
    actual = run_call_cost(_inline())
    expected = {"cost_usd": 0.0025, "cost_source": "computed", "price_table_version": "2026-09-27"}
    assert matches(expected, actual, 0.0)
    assert not matches({**expected, "cost_usd": 0.0026}, actual, 1e-12)
    assert not matches({**expected, "cost_source": "reported"}, actual, 1e-12)
    # The expected object is the whole result: a missing or an extra key is a mismatch.
    assert not matches({"cost_usd": 0.0025, "cost_source": "computed"}, actual, 1e-12)
    assert not matches({**expected, "extra": 1}, actual, 1e-12)


def test_runner_compares_unknown_cost_as_null_not_zero() -> None:
    actual = run_call_cost(_inline(model="gpt-unknown"))
    assert actual["cost_usd"] is None
    assert matches({"cost_usd": None}, {"cost_usd": None}, 0.0)
    assert not matches({"cost_usd": 0}, {"cost_usd": None}, 1e-9)
    assert not matches({"cost_usd": None}, {"cost_usd": 0.0}, 1e-9)


def test_runner_tolerance_zero_is_exact_and_missing_takes_the_default() -> None:
    assert tolerance_of({"tolerance": 0}) == 0.0
    assert tolerance_of({}) == DEFAULT_TOLERANCE
    naive = 566 * 0.042 / 1_000_000
    assert naive != 2.3772e-05  # the double product the exact-decimal rule exists for
    assert not matches(2.3772e-05, naive, 0.0)
    assert matches(2.3772e-05, naive, DEFAULT_TOLERANCE)


def test_runner_refuses_unknown_or_missing_members() -> None:
    with pytest.raises(AssertionError, match="unknown members"):
        run_call_cost(_inline(price_table=TABLE))
    spec = _inline()
    del spec["table"]
    with pytest.raises(AssertionError, match="lacks"):
        run_call_cost(spec)
    with pytest.raises(AssertionError, match="input_tokens"):
        run_call_cost(_inline(input_tokens=2000.0))
