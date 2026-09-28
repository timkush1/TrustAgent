"""
Judge-call pricing from the contracts price table (SPEC sections 3.2 and 7.4),
and the Python runner of the contracts `cost` vectors (SPEC section 7.6: Python
runs only kind `cost`).
"""

import json
import math
from pathlib import Path
from typing import Any, Dict, List

import pytest

from truthtable.contracts import contracts_dir
from truthtable.providers import pricing
from truthtable.providers.pricing import (
    EMPTY_PRICE_TABLE,
    PriceEntry,
    PriceTable,
    PriceTableError,
    active_price_table,
    load_default_price_table,
    price_call,
    set_active_price_table,
)

CONTRACT_TABLE = PriceTable.load(contracts_dir() / pricing.PRICE_TABLE_CONTRACT)


@pytest.fixture
def restore_active_table():
    yield
    set_active_price_table(None)


def table(*entries: PriceEntry, version: str = "t") -> PriceTable:
    return PriceTable(version=version, entries=tuple(entries))


# ---------------------------------------------------------------------------
# The contracts table
# ---------------------------------------------------------------------------


def test_contracts_table_prices_the_default_judges() -> None:
    haiku = CONTRACT_TABLE.cost("anthropic", "claude-haiku-4-5-20251001", 1000, 200)
    assert haiku.cost_source == "computed"
    assert haiku.cost_usd == pytest.approx((1000 * 1 + 200 * 5) / 1_000_000)
    assert haiku.price_table_version == CONTRACT_TABLE.version

    routed = CONTRACT_TABLE.cost("openrouter", "anthropic/claude-haiku-4.5", 1000, 200)
    assert routed.cost_source == "computed"
    assert routed.cost_usd == pytest.approx(haiku.cost_usd)


def test_reported_cost_wins() -> None:
    cost = CONTRACT_TABLE.cost("openrouter", "anthropic/claude-haiku-4.5", 1000, 200, 0.0042)
    assert (cost.cost_usd, cost.cost_source) == (0.0042, "reported")


def test_local_and_mock_calls() -> None:
    assert CONTRACT_TABLE.cost("ollama", "llama3.2:1b", 5000, 500).cost_source == "free_local"
    assert CONTRACT_TABLE.cost("ollama", "llama3.2:1b", 5000, 500).cost_usd == 0.0
    mock = CONTRACT_TABLE.cost("mock", "mock", 10, 10, reported_cost_usd=1.0)
    assert (mock.cost_usd, mock.cost_source) == (0.0, "simulated")


def test_unpriced_model_is_unknown() -> None:
    cost = CONTRACT_TABLE.cost("openai", "gpt-4o-mini", 1000, 200)
    assert (cost.cost_usd, cost.cost_source) == (None, "unknown")


# ---------------------------------------------------------------------------
# Matching and precedence
# ---------------------------------------------------------------------------


def test_first_matching_entry_wins() -> None:
    prices = table(
        PriceEntry("claude-haiku-4-5-2025*", "anthropic", 2.0, 10.0),
        PriceEntry("claude-haiku-4-5*", "anthropic", 1.0, 5.0),
    )
    assert prices.lookup("anthropic", "claude-haiku-4-5-20251001").input_per_mtok == 2.0
    assert prices.lookup("anthropic", "claude-haiku-4-5").input_per_mtok == 1.0


def test_provider_must_be_equal() -> None:
    prices = table(PriceEntry("*", "anthropic", 1.0, 5.0))
    assert prices.lookup("openrouter", "claude-haiku-4-5") is None


@pytest.mark.parametrize(
    "pattern, model, matches",
    [
        ("anthropic/claude-haiku-4.5", "anthropic/claude-haiku-4.5", True),
        ("anthropic/claude-haiku-4.5", "anthropic/claude-haiku-4x5", False),  # '.' is literal
        ("model?", "models", False),  # '?' is literal
        ("model[1]", "model1", False),  # '[' is literal
        ("jev-*", "jev-1.13.0", True),
        ("*", "", True),
        ("a*b*c", "a-x-b-y-c", True),
        ("a*b", "a-b-c", False),
    ],
)
def test_glob_only_star_is_special(pattern: str, model: str, matches: bool) -> None:
    assert PriceEntry(pattern, "p", 1, 1).matches("p", model) is matches


def test_priced_local_model_is_computed() -> None:
    prices = table(PriceEntry("*", "ollama", 0.1, 0.2))
    cost = prices.cost("ollama", "llama3.2:1b", 1_000_000, 1_000_000)
    assert (cost.cost_usd, cost.cost_source) == (pytest.approx(0.3), "computed")


def test_negative_token_counts_are_clamped() -> None:
    prices = table(PriceEntry("*", "anthropic", 1.0, 5.0))
    assert prices.cost("anthropic", "m", -5, -5).cost_usd == 0.0


# ---------------------------------------------------------------------------
# Parsing and loading
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "document, message",
    [
        ([], "JSON object"),
        ({"currency": "USD", "entries": [{}]}, "version"),
        ({"version": "v", "currency": "EUR", "entries": []}, "USD"),
        ({"version": "v", "currency": "USD", "entries": []}, "non-empty list"),
        (
            {
                "version": "v",
                "currency": "USD",
                "entries": [
                    {"match": "*", "provider": "p", "input_per_mtok": -1, "output_per_mtok": 0}
                ],
            },
            "non-negative",
        ),
        (
            {
                "version": "v",
                "currency": "USD",
                "entries": [{"match": "", "provider": "p", "input_per_mtok": 0}],
            },
            "match",
        ),
    ],
)
def test_invalid_tables_are_rejected(document: Any, message: str) -> None:
    with pytest.raises(PriceTableError, match=message):
        PriceTable.from_document(document)


def test_from_json_rejects_malformed_json() -> None:
    with pytest.raises(PriceTableError, match="not valid JSON"):
        PriceTable.from_json(b"{")


def test_price_table_file_overrides_the_contracts_table(tmp_path: Path) -> None:
    override = tmp_path / "prices.json"
    override.write_text(
        json.dumps(
            {
                "version": "override-1",
                "currency": "USD",
                "entries": [
                    {
                        "match": "*",
                        "provider": "openai",
                        "input_per_mtok": 0.15,
                        "output_per_mtok": 0.6,
                    }
                ],
            }
        ),
        encoding="utf-8",
    )

    loaded = load_default_price_table({"TRUTHTABLE_PRICE_TABLE_FILE": str(override)})

    assert loaded.version == "override-1"
    assert loaded.cost("openai", "gpt-4o-mini", 1_000_000, 0).cost_usd == pytest.approx(0.15)


def test_default_table_is_the_contracts_table() -> None:
    assert load_default_price_table({}) == CONTRACT_TABLE


def test_unloadable_table_prices_everything_unknown(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, restore_active_table: None
) -> None:
    set_active_price_table(None)
    monkeypatch.setenv("TRUTHTABLE_PRICE_TABLE_FILE", str(tmp_path / "missing.json"))

    assert active_price_table() is EMPTY_PRICE_TABLE
    assert price_call("anthropic", "claude-haiku-4-5", 10, 10).cost_source == "unknown"


def test_pushed_table_replaces_the_active_one(restore_active_table: None) -> None:
    pushed = table(PriceEntry("*", "openai", 1.0, 1.0), version="pushed")
    set_active_price_table(pushed)

    assert active_price_table() is pushed
    assert price_call("openai", "gpt-4o-mini", 1_000_000, 0).cost_usd == pytest.approx(1.0)


# ---------------------------------------------------------------------------
# contracts/vectors/cost (kind "cost"; generated by Go cmd/vectorgen)
# ---------------------------------------------------------------------------
#
# The Python runner covers per-call vectors, whose input is
#     {"provider", "model", "input_tokens", "output_tokens",
#      "reported_cost_usd"?, "price_table"? (a pricing.v1 document; default: the
#      contracts table)}
# and whose expected output is a subset of {"cost_usd", "cost_source"}.
# Calculator vectors (the ROI counterfactuals) are run by Go and TypeScript.


def _cost_vectors() -> List[Path]:
    directory = contracts_dir() / "vectors" / "cost"
    return sorted(directory.glob("*.json")) if directory.is_dir() else []


def _matches(expected: Any, actual: Any, tolerance: float) -> bool:
    if isinstance(expected, dict):
        return isinstance(actual, dict) and all(
            key in actual and _matches(value, actual[key], tolerance)
            for key, value in expected.items()
        )
    if isinstance(expected, bool) or expected is None or isinstance(expected, str):
        return expected == actual
    if isinstance(expected, (int, float)):
        return (
            isinstance(actual, (int, float))
            and not isinstance(actual, bool)
            and math.isclose(expected, actual, rel_tol=0.0, abs_tol=tolerance)
        )
    if isinstance(expected, list):
        return (
            isinstance(actual, list)
            and len(expected) == len(actual)
            and all(_matches(e, a, tolerance) for e, a in zip(expected, actual))
        )
    return expected == actual


def _run_cost_vector(vector: Dict[str, Any]) -> Dict[str, Any]:
    spec = vector["input"]
    prices = (
        PriceTable.from_document(spec["price_table"]) if "price_table" in spec else CONTRACT_TABLE
    )
    cost = prices.cost(
        spec["provider"],
        spec["model"],
        int(spec.get("input_tokens", 0)),
        int(spec.get("output_tokens", 0)),
        spec.get("reported_cost_usd"),
    )
    return {"cost_usd": cost.cost_usd, "cost_source": cost.cost_source}


COST_VECTORS = _cost_vectors()


@pytest.mark.parametrize("path", COST_VECTORS, ids=[p.stem for p in COST_VECTORS])
def test_cost_vector(path: Path) -> None:
    vector = json.loads(path.read_text(encoding="utf-8"))
    assert vector["kind"] == "cost"
    if not {"provider", "model"} <= set(vector["input"]):
        pytest.skip("calculator vector: run by the Go and TypeScript runners")
    actual = _run_cost_vector(vector)
    assert _matches(vector["expected"], actual, float(vector.get("tolerance") or 1e-9)), actual


def test_cost_vector_runner_on_an_inline_vector() -> None:
    # Keeps the runner honest while contracts/vectors/cost is still empty.
    vector = {
        "vector": "1",
        "kind": "cost",
        "name": "inline-haiku-computed",
        "input": {
            "provider": "anthropic",
            "model": "claude-haiku-4-5",
            "input_tokens": 2000,
            "output_tokens": 100,
        },
        "expected": {"cost_usd": 0.0025, "cost_source": "computed"},
        "tolerance": 1e-12,
    }
    assert _matches(vector["expected"], _run_cost_vector(vector), vector["tolerance"])
    assert not _matches({"cost_usd": 0.0026}, _run_cost_vector(vector), 1e-12)
