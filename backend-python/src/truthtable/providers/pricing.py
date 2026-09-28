"""
LLM call pricing from the contracts price table (SPEC sections 3.2 and 7.4).

The table is `contracts/pricing/prices.v1.json` (schema pricing.v1), or the file
named by TRUTHTABLE_PRICE_TABLE_FILE. A pushed runtime table (ApplyRuntimeConfig
`price_table_json`) replaces it with `set_active_price_table`.

The cost of one call, and its `cost_source` (CostRecord.cost_source):

| Case                                                        | cost_usd              | cost_source  |
|-------------------------------------------------------------|-----------------------|--------------|
| the mock judge                                              | 0                     | `simulated`  |
| the provider reported the cost (OpenRouter `usage.cost`)    | the reported cost     | `reported`   |
| a local provider (Ollama) with no entry or a zero price     | 0                     | `free_local` |
| the first entry whose provider is equal and glob matches    | tokens x entry price  | `computed`   |
| no matching entry                                           | None                  | `unknown`    |

Entries are tried in file order; `match` is a glob on the model id in which
`*` matches any run of characters and every other character is literal.
Prices are USD per million tokens.
"""

import json
import logging
import os
import re
import threading
from dataclasses import dataclass
from pathlib import Path
from typing import Any, List, Mapping, Optional, Tuple, Union

from ..contracts import ContractsNotFoundError, contract_path

logger = logging.getLogger(__name__)

PRICE_TABLE_CONTRACT = "pricing/prices.v1.json"
PRICE_TABLE_FILE_ENV = "TRUTHTABLE_PRICE_TABLE_FILE"

# CostRecord.cost_source values.
REPORTED = "reported"
COMPUTED = "computed"
FREE_LOCAL = "free_local"
SIMULATED = "simulated"
UNKNOWN = "unknown"

# Providers that run on the operator's own hardware.
LOCAL_PROVIDERS = frozenset({"ollama"})
# Providers whose calls cost nothing because nothing is called.
SIMULATED_PROVIDERS = frozenset({"mock"})

_TOKENS_PER_MTOK = 1_000_000


def _glob_regex(pattern: str) -> "re.Pattern[str]":
    return re.compile("^" + ".*".join(re.escape(part) for part in pattern.split("*")) + "$")


@dataclass(frozen=True)
class PriceEntry:
    """One pricing.v1 entry: a model glob for one provider, USD per million tokens."""

    match: str
    provider: str
    input_per_mtok: float
    output_per_mtok: float
    label: str = ""

    def matches(self, provider: str, model: str) -> bool:
        return self.provider == provider and _glob_regex(self.match).match(model) is not None

    @property
    def is_free(self) -> bool:
        return self.input_per_mtok == 0 and self.output_per_mtok == 0


@dataclass(frozen=True)
class Cost:
    """The priced cost of one call."""

    cost_usd: Optional[float]
    cost_source: str
    price_table_version: str = ""


class PriceTableError(ValueError):
    """A price table document is not a valid pricing.v1 table."""


def _number(entry: Mapping[str, Any], key: str, where: str) -> float:
    value = entry.get(key)
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value < 0:
        raise PriceTableError(f"{where}.{key} must be a non-negative number")
    return float(value)


@dataclass(frozen=True)
class PriceTable:
    """A parsed pricing.v1 document."""

    version: str
    entries: Tuple[PriceEntry, ...]

    @classmethod
    def from_document(cls, document: Any) -> "PriceTable":
        """
        Parse a pricing.v1 document (the fields pricing uses; `make contracts-check`
        validates the committed table against its schema).

        Raises:
            PriceTableError: If the document is not a pricing.v1 table.
        """
        if not isinstance(document, Mapping):
            raise PriceTableError("price table must be a JSON object")
        version = document.get("version")
        if not isinstance(version, str) or not version:
            raise PriceTableError("price table version must be a non-empty string")
        if document.get("currency") != "USD":
            raise PriceTableError("price table currency must be USD")
        raw_entries = document.get("entries")
        if not isinstance(raw_entries, list) or not raw_entries:
            raise PriceTableError("price table entries must be a non-empty list")
        entries: List[PriceEntry] = []
        for index, raw in enumerate(raw_entries):
            where = f"entries[{index}]"
            if not isinstance(raw, Mapping):
                raise PriceTableError(f"{where} must be an object")
            match, provider = raw.get("match"), raw.get("provider")
            if not isinstance(match, str) or not match:
                raise PriceTableError(f"{where}.match must be a non-empty string")
            if not isinstance(provider, str) or not provider:
                raise PriceTableError(f"{where}.provider must be a non-empty string")
            label = raw.get("label", "")
            entries.append(
                PriceEntry(
                    match=match,
                    provider=provider,
                    input_per_mtok=_number(raw, "input_per_mtok", where),
                    output_per_mtok=_number(raw, "output_per_mtok", where),
                    label=label if isinstance(label, str) else "",
                )
            )
        return cls(version=version, entries=tuple(entries))

    @classmethod
    def from_json(cls, data: Union[str, bytes]) -> "PriceTable":
        """Parse a pricing.v1 JSON document (e.g. RuntimeConfig.price_table_json)."""
        try:
            document = json.loads(data)
        except (ValueError, UnicodeDecodeError) as exc:
            raise PriceTableError(f"price table is not valid JSON: {exc}") from exc
        return cls.from_document(document)

    @classmethod
    def load(cls, path: Union[str, Path]) -> "PriceTable":
        """Read a pricing.v1 file."""
        with open(path, encoding="utf-8") as f:
            return cls.from_json(f.read())

    def lookup(self, provider: str, model: str) -> Optional[PriceEntry]:
        """The first entry for `provider` whose glob matches `model`."""
        for entry in self.entries:
            if entry.matches(provider, model):
                return entry
        return None

    def cost(
        self,
        provider: str,
        model: str,
        input_tokens: int,
        output_tokens: int,
        reported_cost_usd: Optional[float] = None,
    ) -> Cost:
        """The cost of one call (see the module docstring for the precedence)."""
        if provider in SIMULATED_PROVIDERS:
            return Cost(0.0, SIMULATED, self.version)
        if reported_cost_usd is not None:
            return Cost(float(reported_cost_usd), REPORTED, self.version)
        entry = self.lookup(provider, model)
        if provider in LOCAL_PROVIDERS and (entry is None or entry.is_free):
            return Cost(0.0, FREE_LOCAL, self.version)
        if entry is None:
            return Cost(None, UNKNOWN, self.version)
        usd = (
            max(0, input_tokens) * entry.input_per_mtok
            + max(0, output_tokens) * entry.output_per_mtok
        ) / _TOKENS_PER_MTOK
        return Cost(usd, COMPUTED, self.version)


EMPTY_PRICE_TABLE = PriceTable(version="", entries=())

_lock = threading.Lock()
_active: Optional[PriceTable] = None


def load_default_price_table(environ: Optional[Mapping[str, str]] = None) -> PriceTable:
    """
    The price table the engine starts with: TRUTHTABLE_PRICE_TABLE_FILE when set,
    otherwise the contracts table.

    Raises:
        OSError, PriceTableError, ContractsNotFoundError: If it cannot be read.
    """
    env = os.environ if environ is None else environ
    configured = env.get(PRICE_TABLE_FILE_ENV, "").strip()
    if configured:
        return PriceTable.load(Path(configured).expanduser())
    return PriceTable.load(contract_path(PRICE_TABLE_CONTRACT, env))


def active_price_table() -> PriceTable:
    """
    The price table in use. Loaded on first use; when it cannot be loaded the
    engine keeps running and every call is priced `unknown` (logged once).
    """
    global _active
    with _lock:
        if _active is None:
            try:
                _active = load_default_price_table()
            except (OSError, PriceTableError, ContractsNotFoundError) as exc:
                logger.warning(f"Price table unavailable ({exc}); LLM costs are reported unknown")
                _active = EMPTY_PRICE_TABLE
        return _active


def set_active_price_table(table: Optional[PriceTable]) -> None:
    """Replace the price table in use (None reloads the default on next use)."""
    global _active
    with _lock:
        _active = table


def price_call(
    provider: str,
    model: str,
    input_tokens: int,
    output_tokens: int,
    reported_cost_usd: Optional[float] = None,
    table: Optional[PriceTable] = None,
) -> Cost:
    """The cost of one call with `table`, or the active price table."""
    return (table if table is not None else active_price_table()).cost(
        provider, model, input_tokens, output_tokens, reported_cost_usd
    )
