"""
The hard cost cap of a benchmark run (SPEC P6: bench-lite <= $5; eval-live <= $60).

Every paid call reserves an upper bound of its cost before it is made (input
tokens over-estimated from the prompt length, output tokens at the request's
max_tokens, both at the price-table rate; a gate-eval call reserves a fixed
per-example ceiling that it also passes to the binary as --max-cost-usd). A
call whose reservation would take spent + reserved past the cap is not made:
the example is excluded as over budget and the run stops starting new ones.
After the call, the reservation is settled with the cost the provider reported
or the price table computed (the reservation itself when neither is known).

So the total never exceeds the cap unless a provider bills above its
price-table rate. Spend is appended to a JSON Lines ledger, and a resumed run
starts from the ledger's total: the cap covers every session of a run.
"""

import json
import threading
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

# Float tolerance of the cap comparison.
EPS = 1e-12


class BudgetExhausted(RuntimeError):
    """A call was not made because its reservation would pass the cost cap."""


@dataclass
class Reservation:
    """An amount held against the cap until the call it covers is settled."""

    usd: float
    open: bool = True


class CostBudget:
    """
    A run's cost cap with reservations (see the module docstring).

    Args:
        cap_usd: The hard cap.
        spent_usd: Spend carried over (a resumed run).
        ledger_path: JSON Lines file every settlement is appended to.
    """

    def __init__(
        self, cap_usd: float, *, spent_usd: float = 0.0, ledger_path: Optional[Path] = None
    ) -> None:
        if cap_usd < 0:
            raise ValueError("the cost cap must not be negative")
        self.cap_usd = float(cap_usd)
        self._spent = float(spent_usd)
        self._reserved = 0.0
        self.exhausted = False  # A reservation was refused
        self.ledger_path = ledger_path
        self._lock = threading.Lock()

    @classmethod
    def open(cls, cap_usd: float, ledger_path: Path) -> "CostBudget":
        """A budget that resumes from (and appends to) `ledger_path`."""
        return cls(cap_usd, spent_usd=ledger_total(ledger_path), ledger_path=ledger_path)

    @property
    def spent_usd(self) -> float:
        return self._spent

    @property
    def reserved_usd(self) -> float:
        return self._reserved

    @property
    def remaining_usd(self) -> float:
        return max(0.0, self.cap_usd - self._spent - self._reserved)

    def reserve(self, usd: float) -> Reservation:
        """
        Hold `usd` against the cap.

        Raises:
            BudgetExhausted: If spent + reserved + usd would pass the cap.
        """
        amount = max(0.0, float(usd))
        with self._lock:
            if self._spent + self._reserved + amount > self.cap_usd + EPS:
                self.exhausted = True
                raise BudgetExhausted(
                    f"cost cap ${self.cap_usd:.2f} reached (spent ${self._spent:.4f}, "
                    f"reserved ${self._reserved:.4f}, next call up to ${amount:.4f})"
                )
            self._reserved += amount
        return Reservation(amount)

    def settle(
        self,
        reservation: Reservation,
        actual_usd: Optional[float],
        *,
        system: str = "",
        example_id: str = "",
        cost_source: str = "",
    ) -> float:
        """
        Close a reservation with the call's cost (the reservation when the cost
        is unknown) and return the amount charged.
        """
        with self._lock:
            if not reservation.open:
                return 0.0
            reservation.open = False
            self._reserved = max(0.0, self._reserved - reservation.usd)
            charged = reservation.usd if actual_usd is None else max(0.0, float(actual_usd))
            self._spent += charged
        if charged > 0 and self.ledger_path is not None:
            self._append(charged, system, example_id, cost_source or "unknown")
        return charged

    def release(self, reservation: Reservation) -> None:
        """Close a reservation whose call was never made (nothing is charged)."""
        with self._lock:
            if reservation.open:
                reservation.open = False
                self._reserved = max(0.0, self._reserved - reservation.usd)

    def _append(self, usd: float, system: str, example_id: str, cost_source: str) -> None:
        if self.ledger_path is None:
            return
        self.ledger_path.parent.mkdir(parents=True, exist_ok=True)
        entry = {
            "at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
            "system": system,
            "example_id": example_id,
            "usd": usd,
            "cost_source": cost_source,
        }
        with open(self.ledger_path, "a", encoding="utf-8", newline="\n") as f:
            f.write(json.dumps(entry, sort_keys=True) + "\n")


def ledger_total(path: Path) -> float:
    """The spend recorded in a ledger (0 when it does not exist)."""
    if not path.exists():
        return 0.0
    total = 0.0
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            entry = json.loads(line)
            total += float(entry.get("usd", 0.0))
    return total
