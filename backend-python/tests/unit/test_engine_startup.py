"""
Engine startup tests: the mock judge (LLM_PROVIDER=mock, the zero-key demo) and
lazy knowledge imports (SPEC section 3.2, test_lazy_imports).
"""

import logging
import subprocess
import sys
from pathlib import Path

import pytest

from truthtable.config import Settings
from truthtable.contracts import ContractsNotFoundError
from truthtable.main import MOCK_JUDGE_MODEL, init_provider
from truthtable.providers.mock import MockLLMProvider

BACKEND_DIR = Path(__file__).resolve().parents[2]

# The knowledge add-on's heavy dependencies: the engine must not import them
# unless knowledge retrieval is configured.
KNOWLEDGE_MODULES = ("sentence_transformers", "torch", "qdrant_client", "truthtable.vectorstore")

logger = logging.getLogger(__name__)


@pytest.fixture(autouse=True)
def isolated_settings(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    for key in ("LLM_PROVIDER", "LLM_MODEL", "TRUTHTABLE_CONTRACTS_DIR"):
        monkeypatch.delenv(key, raising=False)
    monkeypatch.chdir(tmp_path)  # no stray .env file


async def test_mock_provider_answers_from_the_contract_scenarios() -> None:
    provider, effective = await init_provider(Settings(llm_provider="mock"), logger)

    assert effective == "mock"
    assert isinstance(provider, MockLLMProvider)
    assert provider.model == MOCK_JUDGE_MODEL
    assert provider.scenarios is not None
    assert {"grounded-pass", "refund-contradiction-flag", "no-evidence-not-assessed"} <= set(
        provider.scenarios
    )


async def test_mock_provider_reports_an_explicit_model(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("LLM_MODEL", "mock/judge-v1")

    provider, _ = await init_provider(Settings(llm_provider="mock"), logger)

    assert provider.model == "mock/judge-v1"


async def test_mock_provider_never_falls_back(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    # A missing scenario pack stops the engine instead of quietly switching judge.
    monkeypatch.setenv("TRUTHTABLE_CONTRACTS_DIR", str(tmp_path / "missing"))

    with pytest.raises(ContractsNotFoundError):
        await init_provider(Settings(llm_provider="mock"), logger)


def test_engine_imports_without_the_knowledge_extras() -> None:
    probe = (
        "import sys, truthtable.main, truthtable.healthcheck; "
        f"print(','.join(m for m in {KNOWLEDGE_MODULES!r} if m in sys.modules))"
    )

    completed = subprocess.run(
        [sys.executable, "-c", probe],
        env={"PYTHONPATH": str(BACKEND_DIR / "src")},
        capture_output=True,
        text=True,
        timeout=120,
    )

    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == ""


def test_healthcheck_imports_only_grpc_and_the_stubs() -> None:
    probe = (
        "import sys, truthtable.healthcheck; "
        "print(','.join(m for m in ('langgraph', 'truthtable.graphs', 'truthtable.config') "
        "if m in sys.modules))"
    )

    completed = subprocess.run(
        [sys.executable, "-c", probe],
        env={"PYTHONPATH": str(BACKEND_DIR / "src")},
        capture_output=True,
        text=True,
        timeout=120,
    )

    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == ""
