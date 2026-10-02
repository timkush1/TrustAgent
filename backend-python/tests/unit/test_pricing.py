"""
Judge-call pricing from the effective price table (SPEC sections 3.2 and 7.4,
P4-PLAN D53). The contracts `cost` vectors run in test_contract_vectors.py.
"""

import json
from pathlib import Path
from typing import Any

import pytest

from truthtable.contracts import contracts_dir
from truthtable.providers import pricing
from truthtable.providers.pricing import (
    EMPTY_PRICE_TABLE,
    PriceEntry,
    PriceTable,
    PriceTableError,
    active_price_table,
    glob_match,
    load_default_price_table,
    price_call,
    set_active_price_table,
    token_cost_usd,
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
    free = CONTRACT_TABLE.cost("openrouter", "anthropic/claude-haiku-4.5", 1000, 200, 0.0)
    assert (free.cost_usd, free.cost_source) == (0.0, "reported")


@pytest.mark.parametrize("reported", [-0.0041, float("nan"), float("inf"), float("-inf")])
def test_a_reported_cost_below_zero_or_not_finite_is_ignored(reported: float) -> None:
    cost = CONTRACT_TABLE.cost("openrouter", "anthropic/claude-haiku-4.5", 1000, 200, reported)
    assert (cost.cost_usd, cost.cost_source) == (0.002, "computed")


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


def test_the_call_provider_entries_come_before_any_other() -> None:
    # D53: a bedrock entry listed first never prices an anthropic call the anthropic entry matches.
    prices = table(
        PriceEntry("claude-*", "bedrock", 1.1, 5.5),
        PriceEntry("claude-haiku-4-5*", "anthropic", 1.0, 5.0),
    )
    assert prices.lookup("anthropic", "claude-haiku-4-5").provider == "anthropic"
    assert prices.lookup("bedrock", "claude-haiku-4-5").provider == "bedrock"


def test_a_provider_without_entries_borrows_the_first_paid_match() -> None:
    prices = table(
        PriceEntry("*", "ollama", 0.0, 0.0),
        PriceEntry("*", "mock", 0.0, 0.0),
        PriceEntry("anthropic/claude-haiku-4.5", "openrouter", 1.0, 5.0),
    )
    borrowed = prices.lookup("compatible", "anthropic/claude-haiku-4.5")
    assert borrowed is not None and borrowed.provider == "openrouter"
    # The free local and mock catch-alls never price another provider's call.
    assert prices.lookup("compatible", "some-other-model") is None
    cost = prices.cost("compatible", "some-other-model", 1000, 100)
    assert (cost.cost_usd, cost.cost_source) == (None, "unknown")
    # An empty provider (a backend with no pricing provider) takes the borrowing rule only.
    assert prices.lookup("", "anthropic/claude-haiku-4.5") is borrowed


def test_local_and_mock_providers_never_borrow_a_paid_price() -> None:
    prices = table(PriceEntry("*", "openrouter", 1.0, 5.0))
    assert prices.lookup("ollama", "llama3.2:1b") is None
    assert prices.lookup("mock", "mock") is None
    local = prices.cost("ollama", "llama3.2:1b", 5000, 500)
    assert (local.cost_usd, local.cost_source) == (0.0, "free_local")


def test_an_override_entry_records_the_override_version() -> None:
    prices = table(
        PriceEntry("anthropic/claude-sonnet-*", "openrouter", 3.0, 15.0, label="override"),
        PriceEntry("*", "openrouter", 1.0, 5.0, label="vendor_price"),
        version="2026-09-27",
    )
    sonnet = prices.cost("openrouter", "anthropic/claude-sonnet-4.5", 1000, 100)
    assert (sonnet.cost_usd, sonnet.price_table_version) == (0.0045, "override")
    other = prices.cost("openrouter", "anthropic/claude-haiku-4.5", 1000, 100)
    assert other.price_table_version == "2026-09-27"
    # A reported cost is the provider's, not the override's.
    reported = prices.cost("openrouter", "anthropic/claude-sonnet-4.5", 1000, 100, 0.01)
    assert (reported.cost_source, reported.price_table_version) == ("reported", "2026-09-27")


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


@pytest.mark.parametrize(
    "pattern, value, matches",
    [
        ("a*a", "a", False),  # prefix and suffix may not overlap
        ("a*a", "aa", True),
        ("*b*b", "bb", True),
        ("*bb*b", "bbb", True),
        ("x*", "x\ny", True),  # '*' spans every character, a newline included
        ("**", "", True),
        ("a**c", "abc", True),
        ("abc", "abcd", False),
    ],
)
def test_glob_edge_cases_match_the_go_reference(pattern: str, value: str, matches: bool) -> None:
    assert glob_match(pattern, value) is matches


def test_priced_local_model_is_computed() -> None:
    prices = table(PriceEntry("*", "ollama", 0.1, 0.2))
    cost = prices.cost("ollama", "llama3.2:1b", 1_000_000, 1_000_000)
    assert (cost.cost_usd, cost.cost_source) == (pytest.approx(0.3), "computed")


def test_negative_token_counts_are_clamped() -> None:
    prices = table(PriceEntry("*", "anthropic", 1.0, 5.0))
    assert prices.cost("anthropic", "m", -5, -5).cost_usd == 0.0


@pytest.mark.parametrize(
    "tokens, prices, exact",
    [
        # Exact decimals: the naive double products of the first, third and fourth miss by an ulp.
        ((566, 3), (0.042, 0.0), 2.3772e-05),
        ((3525, 120), (1.0, 5.0), 0.004125),
        ((1, 1), (0.1, 0.2), 3e-07),
        ((7, 0), (0.07, 0.0), 4.9e-07),
        # Prices with more than six decimals and huge counts go through the same exact path.
        ((10**12, 0), (1e-07, 0.0), 0.1),
        ((3, 0), (1.234567891, 0.0), 3.703703673e-06),
    ],
)
def test_token_cost_is_the_float_nearest_the_exact_decimal(
    tokens: Any, prices: Any, exact: float
) -> None:
    assert token_cost_usd(*tokens, *prices) == exact


def test_token_cost_differs_from_the_naive_double_product() -> None:
    assert 566 * 0.042 / 1_000_000 != 2.3772e-05
    assert token_cost_usd(566, 0, 0.042, 0.0) == 2.3772e-05


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
                "entries": [
                    {"match": "*", "provider": "p", "input_per_mtok": 1, "output_per_mtok": 1e400}
                ],
            },
            "finite",
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
