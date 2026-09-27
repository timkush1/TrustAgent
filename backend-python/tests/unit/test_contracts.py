"""Tests for the contracts directory loader (TRUTHTABLE_CONTRACTS_DIR)."""

from pathlib import Path

import pytest

from truthtable import contracts
from truthtable.contracts import (
    DEMO_SCENARIOS,
    ContractsNotFoundError,
    contract_path,
    contracts_dir,
    load_contract,
)

REPO_CONTRACTS = Path(__file__).resolve().parents[3] / "contracts"


def _make_contracts(root: Path) -> Path:
    directory = root / "contracts"
    (directory / "schema").mkdir(parents=True)
    (directory / "demo").mkdir()
    (directory / "demo" / "scenarios.v1.json").write_text('{"schema": "x"}', encoding="utf-8")
    return directory


@pytest.fixture(autouse=True)
def no_image_contracts(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    # Keep a real /app/contracts (inside the engine image) out of these tests.
    monkeypatch.setattr(contracts, "IMAGE_CONTRACTS_DIR", tmp_path / "no-image" / "contracts")


def test_auto_finds_the_repository_contracts() -> None:
    assert contracts_dir(environ={}) == REPO_CONTRACTS
    assert contracts_dir(environ={"TRUTHTABLE_CONTRACTS_DIR": "auto"}) == REPO_CONTRACTS
    assert contracts_dir(environ={"TRUTHTABLE_CONTRACTS_DIR": " "}) == REPO_CONTRACTS


def test_env_override_wins(tmp_path: Path) -> None:
    directory = _make_contracts(tmp_path / "custom")

    assert contracts_dir(environ={"TRUTHTABLE_CONTRACTS_DIR": str(directory)}) == directory


def test_env_override_must_exist(tmp_path: Path) -> None:
    with pytest.raises(ContractsNotFoundError, match="TRUTHTABLE_CONTRACTS_DIR"):
        contracts_dir(environ={"TRUTHTABLE_CONTRACTS_DIR": str(tmp_path / "missing")})


def test_walks_up_to_the_nearest_contracts_directory(tmp_path: Path) -> None:
    directory = _make_contracts(tmp_path)
    start = tmp_path / "backend-python" / "src" / "truthtable" / "contracts.py"
    start.parent.mkdir(parents=True)

    assert contracts_dir(environ={}, start=start) == directory


def test_ignores_a_contracts_directory_without_schemas(tmp_path: Path) -> None:
    directory = _make_contracts(tmp_path)
    (tmp_path / "pkg" / "contracts").mkdir(parents=True)  # unrelated, no schema/

    assert contracts_dir(environ={}, start=tmp_path / "pkg" / "module.py") == directory


def test_falls_back_to_the_image_directory(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    image = _make_contracts(tmp_path / "app")
    monkeypatch.setattr(contracts, "IMAGE_CONTRACTS_DIR", image)
    elsewhere = tmp_path / "site-packages" / "truthtable" / "contracts.py"

    assert contracts_dir(environ={}, start=elsewhere) == image


def test_not_found(tmp_path: Path) -> None:
    with pytest.raises(ContractsNotFoundError, match="TRUTHTABLE_CONTRACTS_DIR"):
        contracts_dir(environ={}, start=tmp_path / "nowhere" / "module.py")


def test_contract_path_and_load(tmp_path: Path) -> None:
    directory = _make_contracts(tmp_path)
    environ = {"TRUTHTABLE_CONTRACTS_DIR": str(directory)}

    assert contract_path(DEMO_SCENARIOS, environ) == directory / DEMO_SCENARIOS
    assert load_contract(DEMO_SCENARIOS, environ) == {"schema": "x"}
    with pytest.raises(ContractsNotFoundError, match="contract file not found"):
        contract_path("demo/missing.json", environ)


def test_reads_the_process_environment(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    directory = _make_contracts(tmp_path)
    monkeypatch.setenv("TRUTHTABLE_CONTRACTS_DIR", str(directory))

    assert contracts_dir() == directory
