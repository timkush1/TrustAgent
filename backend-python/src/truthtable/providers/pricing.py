"""
LLM call pricing from the effective price table (SPEC sections 3.2 and 7.4,
P4-PLAN D53 and D85), the Python port of backend-go/internal/decision/cost.go
`PriceCall` (the reference; the contracts `cost` vectors hold both to it).

The table is `contracts/pricing/prices.v1.json` (schema pricing.v1), or the file
named by TRUTHTABLE_PRICE_TABLE_FILE. A pushed runtime table (ApplyRuntimeConfig
`price_table_json`, the gateway's already-effective table: dashboard overrides
first, then the file, then contracts) replaces it with `set_active_price_table`.

The cost of one call, and its `cost_source` (CostRecord.cost_source); the first
rule that applies wins:

| Case                                                        | cost_usd              | cost_source  |
|-------------------------------------------------------------|-----------------------|--------------|
| the mock judge                                              | 0                     | `simulated`  |
| the provider reported a cost of at least $0 (`usage.cost`)  | the reported cost     | `reported`   |
| a local provider (Ollama) with no entry or a free one       | 0                     | `free_local` |
| no entry prices the model (see the lookup below)            | None                  | `unknown`    |
| otherwise                                                   | tokens x entry price  | `computed`   |

A reported cost below $0 (or not finite) is not a cost: it is ignored and the
call is priced from the table, so a malformed report never lowers the spend.

The lookup (D53): the first entry of the call's own provider whose glob matches
the model; when that provider has none, the first matching entry of any other
paid provider. A local or mock provider never borrows a paid price, and a local
or mock entry (a free `*` catch-all) never prices another provider's call. A
glob's `*` matches any run of characters; every other character is literal.

Prices are USD per million tokens. Arithmetic is exact decimal: each price is
the decimal its shortest representation spells, the cost is an exact rational,
and the result is the float nearest it (566 tokens at $0.042/M is 2.3772e-05,
never 2.3772000000000002e-05). `price_table_version` is `override` when a
dashboard override (label `override`) priced the call, otherwise the table's
version.
"""

import json
import logging
import math
import os
import threading
from dataclasses import dataclass
from fractions import Fraction
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

# PriceEntry.label of a dashboard override, and the price_table_version of a cost it priced.
PRICE_LABEL_OVERRIDE = "override"
PRICE_VERSION_OVERRIDE = "override"

# Providers that run on the operator's own hardware.
LOCAL_PROVIDERS = frozenset({"ollama"})
# Providers whose calls cost nothing because nothing is called.
SIMULATED_PROVIDERS = frozenset({"mock"})
# Providers whose entries never price another provider's call, and who never borrow a price.
_UNPRICED_PROVIDERS = LOCAL_PROVIDERS | SIMULATED_PROVIDERS

_TOKENS_PER_MTOK = 1_000_000


def glob_match(pattern: str, value: str) -> bool:
    """Whether `value` matches `pattern`, in which `*` matches any run of characters."""
    parts = pattern.split("*")
    if len(parts) == 1:
        return pattern == value
    if not value.startswith(parts[0]):
        return False
    rest = value[len(parts[0]) :]
    for part in parts[1:-1]:
        index = rest.find(part)
        if index < 0:
            return False
        rest = rest[index + len(part) :]
    return rest.endswith(parts[-1])


def _valid_amount(value: float) -> bool:
    """A price or a reported cost: a finite number of at least 0."""
    return math.isfinite(value) and value >= 0


def _decimal(value: float) -> Fraction:
    """The exact decimal `value` spells: its shortest representation (repr)."""
    return Fraction(repr(float(value)))


def token_cost_usd(
    input_tokens: int, output_tokens: int, input_per_mtok: float, output_per_mtok: float
) -> float:
    """
    input_tokens x input_per_mtok + output_tokens x output_per_mtok, divided by one
    million, as the float nearest its exact decimal value. Negative counts count as 0.
    """
    exact = (
        max(0, int(input_tokens)) * _decimal(input_per_mtok)
        + max(0, int(output_tokens)) * _decimal(output_per_mtok)
    ) / _TOKENS_PER_MTOK
    return float(exact)


@dataclass(frozen=True)
class PriceEntry:
    """One pricing.v1 entry: a model glob for one provider, USD per million tokens."""

    match: str
    provider: str
    input_per_mtok: float
    output_per_mtok: float
    label: str = ""

    def matches(self, provider: str, model: str) -> bool:
        return self.provider == provider and glob_match(self.match, model)

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
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not _valid_amount(float(value))
    ):
        raise PriceTableError(f"{where}.{key} must be a finite non-negative number")
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
        """
        The entry that prices `model` on `provider` (D53): the first entry of that
        provider whose glob matches; when it has none, the first matching entry of
        another paid provider. A local or mock provider never borrows a price, and
        a local or mock entry never prices another provider. An empty provider
        takes the second rule only.
        """
        if provider:
            for entry in self.entries:
                if entry.matches(provider, model):
                    return entry
            if provider in _UNPRICED_PROVIDERS:
                return None
        for entry in self.entries:
            if entry.provider not in _UNPRICED_PROVIDERS and glob_match(entry.match, model):
                return entry
        return None

    def version_of(self, entry: PriceEntry) -> str:
        """The price_table_version of a cost `entry` priced: `override` for an override."""
        return PRICE_VERSION_OVERRIDE if entry.label == PRICE_LABEL_OVERRIDE else self.version

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
        if reported_cost_usd is not None and _valid_amount(float(reported_cost_usd)):
            return Cost(float(reported_cost_usd), REPORTED, self.version)
        entry = self.lookup(provider, model)
        version = self.version if entry is None else self.version_of(entry)
        if provider in LOCAL_PROVIDERS and (entry is None or entry.is_free):
            return Cost(0.0, FREE_LOCAL, version)
        if entry is None:
            return Cost(None, UNKNOWN, version)
        usd = token_cost_usd(
            input_tokens, output_tokens, entry.input_per_mtok, entry.output_per_mtok
        )
        return Cost(usd, COMPUTED, version)


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
