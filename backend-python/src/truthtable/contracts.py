"""
Contracts directory loader.

The repository's contracts/ directory is the single source of truth shared by
the proxy, the engine and the dashboard (scenario packs, evaluator templates,
price tables, ...). The engine finds it, in order (SPEC section 3.2,
TRUTHTABLE_CONTRACTS_DIR):

1. TRUTHTABLE_CONTRACTS_DIR, unless it is empty or "auto";
2. the nearest contracts/ directory walking up from this package (a source
   checkout: backend-python/src/truthtable -> repository root);
3. /app/contracts, where the engine image copies it.
"""

import json
import os
from pathlib import Path
from typing import Any, Mapping, Optional

CONTRACTS_DIR_ENV = "TRUTHTABLE_CONTRACTS_DIR"
IMAGE_CONTRACTS_DIR = Path("/app/contracts")

# The S0 demo and e2e scenario pack (schema trustagent.scenarios/v1).
DEMO_SCENARIOS = "demo/scenarios.v1.json"
# The vendor-faults pack (D119): same schema, scenario ids unique across both packs.
DEMO_FAULTS = "demo/faults.v1.json"

# A contracts/ directory always carries its JSON schemas; checking for them keeps
# the upward walk from stopping at an unrelated directory that happens to be
# called "contracts".
_MARKER = Path("schema")


class ContractsNotFoundError(FileNotFoundError):
    """The contracts directory (or a file in it) could not be found."""


def _is_contracts_dir(path: Path) -> bool:
    return (path / _MARKER).is_dir()


def contracts_dir(
    environ: Optional[Mapping[str, str]] = None, start: Optional[Path] = None
) -> Path:
    """
    Resolve the contracts directory.

    Args:
        environ: Environment to read TRUTHTABLE_CONTRACTS_DIR from (default os.environ).
        start: Where the upward walk begins (default: this package's directory).

    Raises:
        ContractsNotFoundError: If an explicit TRUTHTABLE_CONTRACTS_DIR is not a
            directory, or no contracts directory is found automatically.
    """
    env = os.environ if environ is None else environ
    configured = env.get(CONTRACTS_DIR_ENV, "").strip()
    if configured and configured.lower() != "auto":
        path = Path(configured).expanduser()
        if not path.is_dir():
            raise ContractsNotFoundError(f"{CONTRACTS_DIR_ENV}={configured!r} is not a directory")
        return path.resolve()

    origin = (start or Path(__file__)).resolve()
    for directory in (origin, *origin.parents):
        candidate = directory / "contracts"
        if _is_contracts_dir(candidate):
            return candidate

    if _is_contracts_dir(IMAGE_CONTRACTS_DIR):
        return IMAGE_CONTRACTS_DIR

    raise ContractsNotFoundError(
        f"contracts directory not found (searched upwards from {origin} and "
        f"{IMAGE_CONTRACTS_DIR}); set {CONTRACTS_DIR_ENV}"
    )


def contract_path(relative: str, environ: Optional[Mapping[str, str]] = None) -> Path:
    """
    Path of a file inside the contracts directory, e.g. "demo/scenarios.v1.json".

    Raises:
        ContractsNotFoundError: If the contracts directory or the file is missing.
    """
    path = contracts_dir(environ) / relative
    if not path.is_file():
        raise ContractsNotFoundError(f"contract file not found: {path}")
    return path


def load_contract(relative: str, environ: Optional[Mapping[str, str]] = None) -> Any:
    """Parse a JSON contract file (see contract_path)."""
    with open(contract_path(relative, environ), encoding="utf-8") as f:
        return json.load(f)
