"""
The calibration set and pass (P6 M-6, M-7; D206): the label mapping, gate-eval
input that gate-eval accepts, no source shared with the bench's test sample,
make calib-pass through a fake gate-eval (one warm process, the built-in
policy, the key's limit) and make calib-holdout through a fake calibrate.
Synthetic RAGTruth files stand in for the download: no network.
"""

import json
import re
import stat
import sys
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Tuple

import pytest

from evals import calibset
from evals import plan as planning
from evals.examples import evaluator_labels, read_jsonl
from truthtable.config import repo_root

from bench_fakes import FakeGateEval, gate_line
from bench_runs import write_p6_run

REVISION = "c103204b9ce28d6bbad859304bf30de72b8ed8fe"
FAKE_KEY = "sk-or-v1-e2e-p6calibkey0000"

CONFLICT = [{"start": 0, "end": 4, "label_type": "Evident Conflict"}]
BASELESS = [{"start": 0, "end": 4, "label_type": "Subtle Baseless Info"}]


def _write_ragtruth(directory: Path, *, train: int = 60, test: int = 30) -> Path:
    """RAGTruth files: QA answers over their own sources, train and test never sharing one."""
    sources: List[Dict[str, Any]] = []
    responses: List[Dict[str, Any]] = []
    for split, count in (("train", train), ("test", test)):
        for i in range(count):
            source_id = f"{split}-s{i // 3}"  # three answers per source
            if i % 3 == 0:
                sources.append(
                    {
                        "source_id": source_id,
                        "task_type": "QA",
                        "source_info": {
                            "question": f"Question {source_id}?",
                            "passages": f"passage 1: Fact one of {source_id}.\n\npassage 2: Two.",
                        },
                    }
                )
            labels = CONFLICT if i % 5 == 0 else BASELESS if i % 5 == 1 else []
            responses.append(
                {
                    "id": f"{split}{i}",
                    "source_id": source_id,
                    "model": "gpt-4-0613" if i % 2 else "llama-2-7b-chat",
                    "labels": labels,
                    "split": split,
                    "quality": "good",
                    "response": f"Answer {split}{i}.",
                }
            )
    directory.mkdir(parents=True, exist_ok=True)
    for name, rows in (("response.jsonl", responses), ("source_info.jsonl", sources)):
        (directory / name).write_text("".join(json.dumps(row) + "\n" for row in rows))
    return directory


@pytest.fixture
def source_dir(tmp_path: Path) -> Path:
    return _write_ragtruth(tmp_path / "ragtruth")


def _prepare(tmp_path: Path, source_dir: Path, **options: Any) -> calibset.Prepared:
    defaults: Dict[str, Any] = {
        "n": 40,
        "seed": 13,
        "revision": REVISION,
        "exclude_sources": tmp_path / "bench" / "bench-lite.jsonl",
        "out": tmp_path / "calib" / "calib.jsonl",
        "bench_n": 20,
        "bench_seed": 13,
        "source_dir": source_dir,
    }
    defaults.update(options)
    return calibset.prepare(**defaults)


def test_the_label_mapping(tmp_path: Path, source_dir: Path) -> None:
    prepared = _prepare(tmp_path, source_dir, n=60)

    by_spans = {}
    for example in prepared.examples:
        by_spans.setdefault(example.meta["spans"], evaluator_labels(example))
    # A conflict span: hallucinated and contradicted.
    assert by_spans["conflict"] == {"groundedness@1": True, "contradiction@1": True}
    # Baseless only: unsupported, not contradicted.
    assert by_spans["baseless"] == {"groundedness@1": True, "contradiction@1": False}
    assert by_spans["none"] == {"groundedness@1": False, "contradiction@1": False}
    lines = read_jsonl(prepared.path)
    assert all(line["label_source"] == "public_dataset" for line in lines)
    assert sum(line["labels"]["contradiction@1"] for line in lines) == 12  # 1 in 5
    assert sum(line["labels"]["groundedness@1"] for line in lines) == 24  # 2 in 5
    assert prepared.manifest["positives"] == {"groundedness@1": 24, "contradiction@1": 12}
    assert prepared.manifest["dataset"] == "ragtruth:qa-train@c103204"
    assert prepared.manifest["sampling"] == "natural" and prepared.manifest["shared_sources"] == 0


def _go_members() -> set:
    root = repo_root()
    assert root is not None
    source = (root / "backend-go" / "cmd" / "gate-eval" / "example.go").read_text()
    struct = source.split("type exampleLine struct {", 1)[1].split("\n}", 1)[0]
    return set(re.findall(r'json:"([a-z_]+)', struct))


def test_the_output_is_gate_eval_input(tmp_path: Path, source_dir: Path) -> None:
    prepared = _prepare(tmp_path, source_dir)
    # The members cmd/gate-eval accepts (unknown members are an error for that line).
    assert calibset.GATE_EVAL_MEMBERS == _go_members()

    lines = read_jsonl(prepared.path)
    assert len(lines) == 40
    for line in lines:
        assert set(line) <= calibset.GATE_EVAL_MEMBERS
        assert all(set(item) <= {"id", "source", "text", "untrusted"} for item in line["evidence"])
        assert line["meta"]["source_id"]

    # The fake gate-eval checks each line the way the Go CLI does: none is an error.
    fake = FakeGateEval(tmp_path)
    fake.default(**gate_line())
    import subprocess  # nosec B404

    with open(prepared.path, "rb") as stdin:
        out = subprocess.run(  # nosec B603
            [str(fake.binary), fake.arg], stdin=stdin, capture_output=True, check=True
        ).stdout.decode()
    answers = [json.loads(row) for row in out.splitlines()]
    assert len(answers) == 40 and all(answer["error"] is None for answer in answers)
    assert answers[0]["labels"] == lines[0]["labels"]  # passed through for calibrate


def test_the_natural_sample_is_seeded_and_unstratified(tmp_path: Path, source_dir: Path) -> None:
    first = _prepare(tmp_path, source_dir, out=tmp_path / "a" / "calib.jsonl")
    again = _prepare(tmp_path, source_dir, out=tmp_path / "b" / "calib.jsonl")
    other = _prepare(tmp_path, source_dir, out=tmp_path / "c" / "calib.jsonl", seed=99)

    ids = [example.id for example in first.examples]
    assert ids == [example.id for example in again.examples]
    assert ids != [example.id for example in other.examples]
    assert all(example.id.startswith("ragtruth-qa-train") for example in first.examples)


def test_a_sample_sharing_a_source_with_the_bench_is_refused(
    tmp_path: Path, source_dir: Path
) -> None:
    bench = tmp_path / "bench" / "bench-lite.jsonl"
    bench.parent.mkdir(parents=True)
    bench.write_text(json.dumps({"id": "x", "meta": {"source_id": "train-s0"}}) + "\n")
    out = tmp_path / "calib" / "calib.jsonl"

    with pytest.raises(calibset.CalibsetError, match="shares 1 RAGTruth source"):
        _prepare(tmp_path, source_dir, n=60, exclude_sources=bench, out=out)

    assert not out.exists()


def test_a_bench_sample_without_source_ids_is_refused(tmp_path: Path, source_dir: Path) -> None:
    bench = tmp_path / "bench" / "bench-lite.jsonl"
    bench.parent.mkdir(parents=True)
    bench.write_text(json.dumps({"id": "x", "meta": {"model": "m"}}) + "\n")

    with pytest.raises(calibset.CalibsetError, match="prepare it again"):
        _prepare(tmp_path, source_dir, exclude_sources=bench)


def test_a_missing_bench_sample_is_prepared_first(tmp_path: Path, source_dir: Path) -> None:
    bench = tmp_path / "bench" / "bench-lite.jsonl"

    prepared = _prepare(tmp_path, source_dir, exclude_sources=bench, bench_n=30)

    assert bench.is_file() and prepared.manifest["bench_sources"] == 10  # 30 test answers, 3 each
    rows = read_jsonl(bench)
    assert len(rows) == 30 and all(row["meta"]["source_id"].startswith("test-") for row in rows)


def test_prepare_cli_refuses_an_overlap_with_exit_2(
    tmp_path: Path, source_dir: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    bench = tmp_path / "bench.jsonl"
    bench.write_text(json.dumps({"id": "x", "meta": {"source_id": "train-s1"}}) + "\n")
    args = [
        "prepare",
        "--n",
        "60",
        "--revision",
        REVISION,
        "--exclude-sources",
        str(bench),
        "--source-dir",
        str(source_dir),
        "--out",
        str(tmp_path / "calib.jsonl"),
    ]
    assert calibset.main(args) == 2
    assert "refused" in capsys.readouterr().err


# ---------------------------------------------------------------------------
# make calib-pass
# ---------------------------------------------------------------------------


def _result(out: str) -> Dict[str, Any]:
    lines = [line for line in out.splitlines() if line.startswith("TRUSTAGENT_P6_RESULT ")]
    assert len(lines) == 1, out
    parsed: Dict[str, Any] = json.loads(lines[0].split(" ", 1)[1])
    return parsed


def _pass_args(tmp_path: Path, source_dir: Path, fake: FakeGateEval, *extra: str) -> List[str]:
    return [
        "run",
        "--n",
        "60",
        "--revision",
        REVISION,
        "--exclude-sources",
        str(tmp_path / "bench" / "bench-lite.jsonl"),
        "--bench-n",
        "20",
        "--source-dir",
        str(source_dir),
        "--gate-eval-bin",
        str(fake.binary),
        "--out-root",
        str(tmp_path / "calibration"),
        "--run-id",
        "calib-test",
        "--max-cost-usd",
        "0.30",
        *extra,
    ]


def _fetch(remaining: Optional[float]) -> Any:
    def fetch(url: str, headers: Mapping[str, str], timeout: float) -> Tuple[int, bytes]:
        if url.endswith("/api/v1/key"):
            data = {"usage": 1.0, "limit": 8.0, "limit_remaining": remaining}
            return 200, json.dumps({"data": data}).encode()
        models = [{"id": "typesafe/jev-1.13", "canonical_slug": "typesafe/jev-1.13-20260917"}]
        return 200, json.dumps({"data": models}).encode()

    return fetch


@pytest.fixture
def gate(tmp_path: Path) -> FakeGateEval:
    directory = tmp_path / "gate"
    directory.mkdir()
    fake = FakeGateEval(directory)
    fake.default(**gate_line(groundedness=0.7, contradiction=0.2))
    return fake


def _with_fake_config(fake: FakeGateEval) -> None:
    """calibset passes no extra arguments: the fake finds its config through a wrapper."""
    wrapper = fake.directory / "gate-eval-wrapper"
    wrapper.write_text(
        f"#!{sys.executable}\nimport os, sys\n"
        f"os.execv({str(fake.binary)!r}, [{str(fake.binary)!r}, *sys.argv[1:], {fake.arg!r}])\n"
    )
    wrapper.chmod(wrapper.stat().st_mode | stat.S_IXUSR)
    fake.binary = wrapper


def test_calib_pass_runs_one_warm_gate_eval_on_the_builtin_policy(
    tmp_path: Path,
    source_dir: Path,
    gate: FakeGateEval,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    real = gate.binary
    _with_fake_config(gate)
    monkeypatch.setenv("TRUTHTABLE_POLICY_FILE", "/somewhere/policies.yaml")
    journal = tmp_path / "p6" / "results.log"

    code = calibset.main(_pass_args(tmp_path, source_dir, gate, "--journal", str(journal)))

    out = capsys.readouterr().out
    assert code == 0, out
    argv = gate_argv(real.parent)
    assert len(argv) == 1  # one process for the whole sample
    assert argv[0][:6] == ["--policy", "builtin", "--warm", "--concurrency", "1", "--evaluators"]
    assert (
        "--max-cost-usd" in argv[0] and argv[0][argv[0].index("--max-cost-usd") + 1] == "0.300000"
    )
    assert gate_envs(real.parent) == [{"TRUTHTABLE_POLICY_FILE": None}]
    run_dir = tmp_path / "calibration" / "calib-test"
    assert len(read_jsonl(run_dir / "gate.jsonl")) == 60
    result = json.loads((run_dir / "result.json").read_text())
    assert result["positives"] == {"groundedness@1": 24, "contradiction@1": 12}
    assert result["assessed"] == {"groundedness@1": 60, "contradiction@1": 60}
    assert result["label"] == "measured" and result["snapshot"] == "typesafe/jev-1.13-20260917"
    assert "groundedness@1 24 of 60 should fire, contradiction@1 12 of 60 should fire" in out
    assert "make calibrate EVALUATOR=groundedness@1 IN=" in out
    assert "DATASET=ragtruth:qa-train@c103204" in out
    line = _result(out)
    assert line["step"] == "calib-pass" and line["exit"] == 0 and line["run_id"] == "calib-test"
    assert line["counts"] == {"n": 60, "scored": 60, "failed": 0}
    assert line["hosts"]["decision"] == "openrouter.ai"
    assert journal.read_text().startswith("TRUSTAGENT_P6_RESULT ")


def gate_argv(directory: Path) -> List[List[str]]:
    path = directory / "argv.jsonl"
    rows = [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
    return [[arg for arg in row if not arg.startswith("--fake-config=")] for row in rows]


def gate_envs(directory: Path) -> List[Dict[str, Optional[str]]]:
    path = directory / "env.jsonl"
    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []


def test_calib_pass_lowers_its_cap_to_the_keys_limit(
    tmp_path: Path,
    source_dir: Path,
    gate: FakeGateEval,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    real = gate.binary
    _with_fake_config(gate)
    monkeypatch.setenv("OPENROUTER_API_KEY", FAKE_KEY)
    monkeypatch.setattr(calibset, "_key_fetch", lambda: _fetch(0.25))

    assert calibset.main(_pass_args(tmp_path, source_dir, gate)) == 0

    argv = gate_argv(real.parent)[0]
    assert argv[argv.index("--max-cost-usd") + 1] == "0.150000"
    out = capsys.readouterr().out
    assert "cap $0.15 (lowered from $0.30" in out and FAKE_KEY not in out
    assert _result(out)["key_limit_remaining_usd"] == 0.25


def test_calib_pass_refuses_below_its_minimum(
    tmp_path: Path,
    source_dir: Path,
    gate: FakeGateEval,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    real = gate.binary
    monkeypatch.setenv("OPENROUTER_API_KEY", FAKE_KEY)
    monkeypatch.setattr(calibset, "_key_fetch", lambda: _fetch(0.11))

    code = calibset.main(_pass_args(tmp_path, source_dir, gate, "--min-cost-usd", "0.06"))

    assert code == 2 and "below this step's minimum" in capsys.readouterr().err
    assert gate_argv(real.parent) == [] and not (tmp_path / "calibration").exists()


def test_calib_pass_through_a_stand_in_host_is_simulated(
    tmp_path: Path,
    source_dir: Path,
    gate: FakeGateEval,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    gate.default(**gate_line(endpoint_host="127.0.0.1"))
    _with_fake_config(gate)

    assert calibset.main(_pass_args(tmp_path, source_dir, gate)) == 0

    out = capsys.readouterr().out
    assert _result(out)["label"] == "simulated"
    assert "simulated risks (mock or a stand-in host): never a measured calibration" in out
    assert "make calibrate would refuse to write" in out and "next (free)" not in out


def test_calib_pass_dry_run_spends_nothing(
    tmp_path: Path, source_dir: Path, gate: FakeGateEval, capsys: pytest.CaptureFixture[str]
) -> None:
    real = gate.binary

    assert calibset.main(_pass_args(tmp_path, source_dir, gate, "--dry-run")) == 0

    out = capsys.readouterr().out
    assert "make calib-pass CONFIRM=1" in out and "no key: plan only" in out
    assert "a natural sample of 60 RAGTruth-QA train answers" in out
    assert gate_argv(real.parent) == [] and not (tmp_path / "calibration").exists()


def test_the_result_command_counts_a_pass(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    lines = [
        {**gate_line(groundedness=0.8), "id": "a", "labels": {"groundedness@1": True}},
        {**gate_line(groundedness=0.1), "id": "b", "labels": {"groundedness@1": False}},
        {"id": "c", "error": "invalid example", "decision": None},
        {"id": "d", "skipped": "over_budget", "decision": None},
    ]
    path = tmp_path / "gate.jsonl"
    path.write_text("".join(json.dumps(line) + "\n" for line in lines))

    assert calibset.main(["result", "--gate", str(path)]) == 0

    out = capsys.readouterr().out
    assert "2 of 4 evaluated, 1 errors, 1 over budget" in out
    assert "groundedness@1 1 of 2 should fire" in out and "provisional" in out


# ---------------------------------------------------------------------------
# make calib-holdout
# ---------------------------------------------------------------------------

FAKE_CALIBRATE = """#!{python}
import json, sys
args = sys.argv[1:]
with open({log!r}, "a", encoding="utf-8") as f:
    f.write(json.dumps(args) + "\\n")
value = lambda name: args[args.index(name) + 1]
if value("--evaluator") == "contradiction@1":
    sys.stderr.write("calibrate: no calibration for contradiction@1")
    sys.exit(2)
with open(value("--holdout-out"), "w", encoding="utf-8") as f:
    json.dump({{"schema": "trustagent.calibration-holdout/v1", "evaluator": value("--evaluator")}}, f)
print("Held-out check of the " + value("--evaluator") + " calibration")
"""


def _fake_calibrate(tmp_path: Path) -> Tuple[Path, Path]:
    log = tmp_path / "calibrate-argv.jsonl"
    binary = tmp_path / "calibrate"
    binary.write_text(FAKE_CALIBRATE.format(python=sys.executable, log=str(log)))
    binary.chmod(binary.stat().st_mode | stat.S_IXUSR)
    return binary, log


def test_calib_holdout_exports_the_runs_labels_and_runs_calibrate(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    run_file = write_p6_run(tmp_path / "results")
    calibration = tmp_path / "typesafe__jev-1.13-20260917.json"
    calibration.write_text("{}")
    binary, log = _fake_calibrate(tmp_path)

    code = calibset.main(
        [
            "holdout",
            "--run",
            str(run_file),
            "--calibration",
            str(calibration),
            "--calibrate-bin",
            str(binary),
        ]
    )

    out = capsys.readouterr()
    # contradiction@1 may have no calibration (D206): reported, not fatal.
    assert code == 0, out.err
    calls = [json.loads(line) for line in log.read_text().splitlines()]
    assert [call[call.index("--evaluator") + 1] for call in calls] == [
        "groundedness@1",
        "contradiction@1",
    ]
    labels = run_file.parent / "holdout-labels.jsonl"
    assert calls[0][calls[0].index("--holdout") + 1] == str(labels)
    assert calls[0][calls[0].index("--calibration") + 1] == str(calibration.resolve())
    assert (run_file.parent / "holdout-groundedness.json").is_file()
    exported = read_jsonl(labels)
    assert {line["target"] for line in exported} == {"groundedness@1", "contradiction@1"}
    assert len(exported) == 80 and all(
        line["dataset"] == "ragtruth:qa-test@c103204" for line in exported
    )
    assert "contradiction@1: calibrate exited 2" in out.err
    assert f"make bench-publish RUN={run_file.resolve()}" in out.out


def test_calib_holdout_without_a_calibration_is_refused(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    run_file = write_p6_run(tmp_path / "results")
    args = ["holdout", "--run", str(run_file), "--calibration", str(tmp_path / "missing.json")]

    assert calibset.main(args) == 2
    assert "make calib-pass and make calibrate" in capsys.readouterr().err


def test_calib_holdout_refuses_a_run_with_two_snapshots(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    run_file = write_p6_run(tmp_path / "results", other_snapshot="typesafe/jev-1.14-20261020")

    assert calibset.main(["holdout", "--run", str(run_file)]) == 2
    assert "2 snapshots" in capsys.readouterr().err


def test_the_calibration_file_name_matches_go() -> None:
    # decision.CalibrationFileName: "/" becomes "__".
    assert calibset.calibration_file_name("typesafe/jev-1.13-20260917") == (
        "typesafe__jev-1.13-20260917.json"
    )
    assert planning.TYPICAL_EXAMPLE_TOKENS == 515
