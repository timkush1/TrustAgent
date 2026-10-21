"""
The plan a paid P6 step prints (D204) and what the steps share (evals/plan.py):
the price-table estimate, the hosts from the configuration, the dirty flag
and the machine's conditions. No network: without a key nothing is read.
"""

import shutil
import subprocess  # nosec B404
from pathlib import Path

import pytest

from evals import plan as planning
from evals import vendor_key
from evals.examples import BenchExample
from truthtable.providers.pricing import PriceTable

TABLE = PriceTable.from_document(
    {
        "version": "1",
        "currency": "USD",
        "entries": [
            {
                "provider": "openrouter",
                "match": "typesafe/jev-1.13*",
                "input_per_mtok": 0.042,
                "output_per_mtok": 0,
            },
            {
                "provider": "openrouter",
                "match": "anthropic/claude-haiku-4.5",
                "input_per_mtok": 1,
                "output_per_mtok": 5,
            },
        ],
    }
)


def test_the_estimate_by_hand() -> None:
    sizes = [planning.ExampleSize(total=1000, response=100)] * 10
    overheads = planning.prompt_overheads()

    estimate = planning.estimate(
        ["gate", "llm_same_checks", "llm_full_audit"],
        sizes,
        full_audit_n=4,
        judge_provider="openrouter",
        judge_model="anthropic/claude-haiku-4.5",
        table=TABLE,
    )

    gate = 10 * (1000 + planning.GATE_OVERHEAD_TOKENS) * 0.042 / 1e6
    same = (
        10 * ((1000 + overheads["same_checks"]) * 1 + planning.SAME_CHECKS_OUTPUT_TOKENS * 5) / 1e6
    )
    audit = (
        4
        * (
            (overheads["decompose"] + 100) * 1
            + planning.DECOMPOSE_OUTPUT_TOKENS * 5
            + planning.CLAIMS_PER_ANSWER
            * ((900 + overheads["verify"]) * 1 + planning.VERIFY_OUTPUT_TOKENS * 5)
        )
        / 1e6
    )
    assert estimate.by_system == pytest.approx(
        {"gate": gate, "llm_same_checks": same, "llm_full_audit": audit}
    )
    assert estimate.total == pytest.approx(gate + same + audit) and estimate.unpriced == []
    # The overheads are measured from the prompts the bench sends, not assumed.
    assert 300 < overheads["same_checks"] < 2000 and overheads["verify"] > 75


def test_an_unpriced_judge_is_named() -> None:
    estimate = planning.estimate(
        ["llm_same_checks"],
        planning.typical_sizes(2),
        full_audit_n=0,
        judge_provider="openrouter",
        judge_model="no/such-model",
        table=TABLE,
    )
    assert estimate.unpriced == ["llm_same_checks"] and estimate.total == 0


def test_example_sizes_count_every_text() -> None:
    example = BenchExample(
        id="a",
        dataset="d",
        level="response",
        query="q" * 40,
        response="r" * 400,
        context=("c" * 4000,),
        label_hallucinated=True,
    )
    assert planning.example_sizes([example]) == [planning.ExampleSize(total=1110, response=100)]


@pytest.mark.parametrize(
    "environ, host",
    [
        ({}, "openrouter.ai"),
        ({"TRUTHTABLE_OPENROUTER_BASE_URL": "http://127.0.0.1:18090/api"}, "127.0.0.1"),
        ({"TRUTHTABLE_JEV_BASE_URL": "https://gateway.example/api"}, "gateway.example"),
        ({"TRUTHTABLE_JEV_BACKEND": "typesafe"}, "api.typesafe.ai"),
        ({"TRUTHTABLE_JEV_BACKEND": "mock"}, None),
    ],
)
def test_the_decision_host_from_the_configuration(environ: dict, host: object) -> None:
    assert planning.decision_host({"TRUTHTABLE_ENV_FILE": "", **environ}) == host


def test_the_judge_host_from_the_provider() -> None:
    env = {"TRUTHTABLE_ENV_FILE": ""}
    assert planning.judge_host("openrouter", env) == "openrouter.ai"
    assert planning.judge_host("anthropic", env) == "api.anthropic.com"
    assert planning.judge_host("ollama", env) == "localhost"
    assert planning.judge_host("mock", env) is None


def test_no_key_reads_nothing() -> None:
    def fetch(url: str, headers: object, timeout: float) -> object:
        raise AssertionError("no network without a key")

    context = planning.key_context(environ={"TRUTHTABLE_ENV_FILE": ""}, fetch=fetch)  # type: ignore[arg-type]
    assert context == planning.KeyContext()


def _git(repo: Path, *args: str) -> None:
    git = shutil.which("git")
    assert git is not None
    # git with fixed arguments in a temporary repository.
    subprocess.run(  # nosec B603
        [git, "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@example.invalid", *args],
        check=True,
        capture_output=True,
    )


def test_the_dirty_flag_counts_tracked_changes_only(tmp_path: Path) -> None:
    _git(tmp_path, "init", "-q")
    (tmp_path / "a.txt").write_text("one\n")
    _git(tmp_path, "add", "a.txt")
    _git(tmp_path, "commit", "-q", "-m", "a")
    assert planning.tree_dirty(tmp_path) is False
    (tmp_path / "untracked.txt").write_text("scratch\n")
    assert planning.tree_dirty(tmp_path) is False  # untracked files do not count
    (tmp_path / "a.txt").write_text("two\n")
    assert planning.tree_dirty(tmp_path) is True
    assert planning.tree_dirty(tmp_path / "not-a-repo") is True  # unnamed source: not publishable


def test_host_env_has_the_schema_shape() -> None:
    env = planning.host_env("il")
    assert set(env) == {"mem_available_mb", "loadavg", "platform", "region"}
    assert env["region"] == "il" and len(env["loadavg"]) == 3  # type: ignore[arg-type]
    assert isinstance(env["mem_available_mb"], int) and env["mem_available_mb"] >= 0


def test_a_refused_cap_shows_in_the_plan() -> None:
    status = vendor_key.KeyStatus("openrouter.ai", 7.9, 8.0, 0.1, False)
    text = planning.render_plan(
        planning.PlanInputs(
            step="bench-smoke",
            what="20 answers",
            systems=["gate"],
            estimate=planning.Estimate({"gate": 0.001}),
            cap=vendor_key.effective_cap(0.30, status, minimum_usd=0.05),
            resume_key="smoke-p6",
            ledger_path=Path("smoke-p6.ledger.jsonl"),
            hosts={"decision": "openrouter.ai"},
            key=planning.KeyContext(
                status=status, snapshot="typesafe/jev-1.13-20260917", has_key=True
            ),
        ),
        sha256=lambda path: None,
    )
    assert text.splitlines()[0].endswith("make bench-smoke CONFIRM=1")
    assert "REFUSED     only $0.0000 can be spent" in text
    assert "snapshot    typesafe/jev-1.13-20260917" in text
