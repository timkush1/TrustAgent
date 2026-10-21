"""
The key helper (P6 M-9, D201) and make p6-ledger (M-10): the free
GET /api/v1/key read through a fake fetch (no network), the effective cap,
the result line, and the read-only spend sum reconciled with the key.
"""

import json
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Tuple

import pytest

from evals import p6_ledger, vendor_key
from evals import plan as planning

from bench_fakes import isolate_vendor_env

FAKE_KEY = "sk-or-v1-e2e-p6keyhelper000"


@pytest.fixture(autouse=True)
def no_vendor(monkeypatch: pytest.MonkeyPatch) -> None:
    """No key, base URL or dotenv from the checkout, and no network."""
    isolate_vendor_env(monkeypatch)


def _fetch(
    status: int = 200, data: Optional[Dict[str, Any]] = None, calls: Optional[List[Any]] = None
) -> vendor_key.Fetch:
    def fetch(url: str, headers: Mapping[str, str], timeout: float) -> Tuple[int, bytes]:
        if calls is not None:
            calls.append((url, dict(headers)))
        if url.endswith("/api/v1/models?output_modalities=all"):
            models = [
                {"id": "openai/gpt-4o-mini", "canonical_slug": "openai/gpt-4o-mini"},
                {"id": "typesafe/jev-1.13", "canonical_slug": "typesafe/jev-1.13-20260917"},
            ]
            return 200, json.dumps({"data": models}).encode()
        body = {"data": data if data is not None else {}}
        return status, json.dumps(body).encode()

    return fetch


def test_the_key_status_keeps_amounts_and_the_host_only() -> None:
    calls: List[Any] = []
    data = {
        "label": "sk-or-v1-e2e…0000",
        "usage": 0.42,
        "limit": 8,
        "limit_remaining": 7.58,
        "is_free_tier": False,
    }

    status = vendor_key.read_key_status(
        FAKE_KEY, "https://openrouter.ai/api/v1/", fetch=_fetch(data=data, calls=calls)
    )

    assert status == vendor_key.KeyStatus("openrouter.ai", 0.42, 8.0, 7.58, False)
    assert "label" not in status.to_json() and FAKE_KEY not in json.dumps(status.to_json())
    assert status.describe() == "used $0.4200 of the $8.00 limit; $7.5800 left"
    assert calls == [("https://openrouter.ai/api/v1/key", {"Authorization": f"Bearer {FAKE_KEY}"})]


def test_a_key_without_a_limit_and_derived_remaining() -> None:
    unlimited = vendor_key.read_key_status(
        FAKE_KEY, fetch=_fetch(data={"usage": 1.5, "limit": None, "limit_remaining": None})
    )
    assert (
        unlimited.limit_usd is None and unlimited.describe() == "used $1.5000; the key has no limit"
    )
    derived = vendor_key.read_key_status(FAKE_KEY, fetch=_fetch(data={"usage": 3.0, "limit": 8.0}))
    assert derived.limit_remaining_usd == 5.0


@pytest.mark.parametrize(
    "status, kind",
    [(401, vendor_key.AUTH), (403, vendor_key.AUTH), (500, vendor_key.HTTP)],
)
def test_a_refused_key_never_echoes_the_key(status: int, kind: str) -> None:
    with pytest.raises(vendor_key.KeyCheckError) as error:
        vendor_key.read_key_status(FAKE_KEY, fetch=_fetch(status=status))
    assert error.value.kind == kind and FAKE_KEY not in str(error.value)


def test_an_answer_without_data_is_a_bad_response() -> None:
    def fetch(url: str, headers: Mapping[str, str], timeout: float) -> Tuple[int, bytes]:
        return 200, b"<html>not json</html>"

    with pytest.raises(vendor_key.KeyCheckError) as error:
        vendor_key.read_key_status(FAKE_KEY, fetch=fetch)
    assert error.value.kind == vendor_key.BAD_RESPONSE


def test_the_snapshot_comes_from_the_free_models_list() -> None:
    assert (
        vendor_key.model_snapshot("typesafe/jev-1.13", fetch=_fetch())
        == "typesafe/jev-1.13-20260917"
    )
    assert vendor_key.model_snapshot("typesafe/jev-9", fetch=_fetch()) is None


def test_only_http_urls_are_fetched() -> None:
    with pytest.raises(vendor_key.KeyCheckError):
        vendor_key._urllib_fetch("file:///etc/passwd", {}, 1.0)


def test_origin_and_host() -> None:
    assert vendor_key.normalize_origin("https://openrouter.ai/api/v1/") == "https://openrouter.ai"
    assert vendor_key.normalize_origin("http://127.0.0.1:18090/api") == "http://127.0.0.1:18090"
    assert vendor_key.normalize_origin(None) == "https://openrouter.ai"
    assert vendor_key.host_of("http://127.0.0.1:18090") == "127.0.0.1"


def test_the_process_environment_wins_over_the_dotenv(tmp_path: Path) -> None:
    dotenv = tmp_path / "test.env"
    dotenv.write_text(
        f"OPENROUTER_API_KEY={FAKE_KEY}\nTRUTHTABLE_OPENROUTER_BASE_URL=http://127.0.0.1:18090/api\n"
    )
    key, origin = vendor_key.resolve({"TRUTHTABLE_ENV_FILE": str(dotenv)})
    assert key == FAKE_KEY and origin == "http://127.0.0.1:18090"
    key, origin = vendor_key.resolve(
        {
            "TRUTHTABLE_ENV_FILE": str(dotenv),
            "OPENROUTER_API_KEY": "",
            "TRUTHTABLE_OPENROUTER_BASE_URL": "",
        }
    )
    assert key is None and origin == "https://openrouter.ai"


def _status(remaining: Optional[float]) -> vendor_key.KeyStatus:
    return vendor_key.KeyStatus("openrouter.ai", 1.0, 8.0, remaining, False)


@pytest.mark.parametrize(
    "requested, remaining, spent, minimum, cap, lowered, refused",
    [
        (5.0, 7.0, 0.0, 2.0, 5.0, False, False),  # plenty left
        (5.0, 3.1, 0.0, 2.0, 3.0, True, False),  # lowered to remaining - 0.10
        (5.0, 1.0, 2.5, 0.5, 3.4, True, False),  # earlier sessions count: spent + room
        (5.0, 1.5, 0.0, 2.0, 1.4, True, True),  # below the step's minimum
        (0.30, None, 0.0, 0.05, 0.30, False, False),  # no limit on the key
        (0.30, 0.05, 0.0, 0.0, 0.0, True, False),  # nothing left but no minimum
    ],
)
def test_the_effective_cap(
    requested: float,
    remaining: Optional[float],
    spent: float,
    minimum: float,
    cap: float,
    lowered: bool,
    refused: bool,
) -> None:
    decision = vendor_key.effective_cap(
        requested, _status(remaining), spent_usd=spent, minimum_usd=minimum
    )
    assert decision.cap_usd == pytest.approx(cap)
    assert decision.lowered is lowered and (decision.refused is not None) is refused
    assert vendor_key.effective_cap(requested, None).cap_usd == requested
    with pytest.raises(ValueError):
        vendor_key.effective_cap(float("inf"), None)


def test_the_result_line(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    line = vendor_key.result_line(
        "bench-smoke",
        run_id="lite-x",
        exit_code=0,
        cost_usd=0.0712345678,
        cap_usd=0.3,
        key=_status(7.2),
        label="measured",
        snapshot="typesafe/jev-1.13-20260917",
        hosts={"decision": "openrouter.ai", "judge": "openrouter.ai"},
        counts={"n": 60, "scored": 60, "failed": 0},
    )
    prefix, body = line.split(" ", 1)
    assert prefix == "TRUSTAGENT_P6_RESULT"
    assert json.loads(body) == {
        "step": "bench-smoke",
        "run_id": "lite-x",
        "exit": 0,
        "cost_usd": 0.071235,
        "cap_usd": 0.3,
        "key_usage_usd": 1.0,
        "key_limit_remaining_usd": 7.2,
        "label": "measured",
        "snapshot": "typesafe/jev-1.13-20260917",
        "hosts": {"decision": "openrouter.ai", "judge": "openrouter.ai", "upstream": None},
        "counts": {"n": 60, "scored": 60, "failed": 0},
    }
    journal = tmp_path / "p6" / "results.log"
    vendor_key.emit_result(line, journal=journal)
    vendor_key.emit_result(line, journal=journal)
    assert capsys.readouterr().out.count("TRUSTAGENT_P6_RESULT") == 2
    assert journal.read_text().splitlines() == [line, line]


def test_the_cli_without_a_key_reads_nothing(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", "")
    assert vendor_key.main([]) == 0
    assert "no key" in capsys.readouterr().out


# ---------------------------------------------------------------------------
# make p6-ledger
# ---------------------------------------------------------------------------


def _ledger_files(tmp_path: Path, *, bench_lite: float = 2.9) -> Dict[str, Path]:
    cache = tmp_path / "cache"
    cache.mkdir()
    rows = [{"usd": bench_lite / 2, "system": "gate"}, {"usd": bench_lite / 2, "system": "llm"}]
    (cache / "lite-p6.ledger.jsonl").write_text("".join(json.dumps(row) + "\n" for row in rows))
    (cache / "smoke-p6.ledger.jsonl").write_text(json.dumps({"usd": 0.07}) + "\n")
    (cache / "lite-other.ledger.jsonl").write_text(json.dumps({"usd": 99}) + "\n")  # not P6
    calibration = tmp_path / "calibration"
    (calibration / "calib-a").mkdir(parents=True)
    (calibration / "calib-a" / "result.json").write_text(json.dumps({"cost_usd": 0.11}))
    (calibration / "calib-b").mkdir()
    gate_lines = [{"id": "x", "cost_usd": 0.05}, {"id": "y", "cost_usd": 0.04}]
    (calibration / "calib-b" / "gate.jsonl").write_text(
        "".join(json.dumps(row) + "\n" for row in gate_lines)
    )
    record = tmp_path / "record" / "ledger.jsonl"
    record.parent.mkdir()
    record.write_text(json.dumps({"attempt": "a1", "usage_delta_usd": 1.2}) + "\n")
    journal = tmp_path / "p6" / "results.log"
    journal.parent.mkdir()
    capture = vendor_key.result_line(
        "jev-capture",
        run_id=None,
        exit_code=0,
        cost_usd=0.003,
        cap_usd=0.01,
        key=None,
        label="measured",
        snapshot=None,
        hosts={},
        counts={},
    )
    journal.write_text(capture + "\nunrelated line\n")
    return {
        "cache_dir": cache,
        "calibration_dir": calibration,
        "record_ledger": record,
        "journal": journal,
    }


def test_the_ledger_sums_every_step_and_reconciles_with_the_key(tmp_path: Path) -> None:
    files = _ledger_files(tmp_path)
    key = planning.KeyContext(
        status=vendor_key.KeyStatus("openrouter.ai", 4.31, 8.0, 3.69, False), has_key=True
    )

    ledger = p6_ledger.collect(**files, key=key)

    spent = {step.name: round(step.spent_usd, 6) for step in ledger.steps}
    assert spent == {
        "jev-capture": 0.003,
        "bench-smoke": 0.07,
        "bench-lite": 2.9,
        "calib-pass": 0.2,
        "record-demo": 1.2,
        "jev-check": 0.0,
    }
    calib = next(step for step in ledger.steps if step.name == "calib-pass")
    assert calib.entries == 2 and calib.cap_usd == pytest.approx(0.60)  # one re-run allowed
    assert round(ledger.total, 6) == 4.373
    assert ledger.unattributed == pytest.approx(4.31 - 4.373)
    text = p6_ledger.render(ledger)
    assert "the files count $0.0630 more than the key used" in text
    assert "OVER CAP" not in text and "$9.61" in text


def test_a_step_over_its_cap_fails_the_ledger(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    files = _ledger_files(tmp_path, bench_lite=5.2)
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", "")
    args = [f"--{name.replace('_', '-')}={path}" for name, path in files.items()]

    assert p6_ledger.main(args) == 1

    out = capsys.readouterr().out
    assert "bench-lite      $5.2000    $5.00" in out and "OVER CAP" in out
    assert "no key: plan only: no reconciliation" in out

    assert p6_ledger.main(args + ["--json"]) == 1
    document = json.loads(capsys.readouterr().out)
    assert document["key"] is None and document["unattributed_usd"] is None
    assert [step["step"] for step in document["steps"] if step["over_cap"]] == ["bench-lite"]


def test_an_empty_ledger_is_zero(tmp_path: Path) -> None:
    ledger = p6_ledger.collect(
        cache_dir=tmp_path / "none",
        calibration_dir=tmp_path / "none",
        record_ledger=tmp_path / "none.jsonl",
        journal=tmp_path / "none.log",
    )
    assert ledger.total == 0 and all(not step.sources for step in ledger.steps)
    assert "nothing recorded" in p6_ledger.render(ledger)
