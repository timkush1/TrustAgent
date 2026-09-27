"""
Tests for `python -m truthtable.healthcheck` (the compose engine healthcheck).

The success case serves the real AuditServicer on an in-process grpc.aio server
bound to an ephemeral loopback port; the healthcheck's blocking client runs in a
worker thread so the event loop keeps serving.
"""

import asyncio
import socket
import subprocess
import sys
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, AsyncIterator, Dict, List, Optional

import pytest
from grpc import aio

from truthtable import __version__, healthcheck
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer

BACKEND_DIR = Path(__file__).resolve().parents[2]


class RecordingServicer(AuditServicer):
    """Records the metadata of each HealthCheck call."""

    def __init__(self) -> None:
        super().__init__(audit_graph=None)
        self.metadata: List[Dict[str, str]] = []

    async def HealthCheck(self, request: Any, context: Any) -> pb.HealthResponse:
        self.metadata.append({key: value for key, value in context.invocation_metadata()})
        response: pb.HealthResponse = await super().HealthCheck(request, context)
        return response


@asynccontextmanager
async def serving(servicer: AuditServicer) -> AsyncIterator[int]:
    """Serve `servicer` on 127.0.0.1:<ephemeral> and yield the port."""
    server = aio.server()
    pb_grpc.add_AuditServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    try:
        yield port
    finally:
        await server.stop(grace=None)


def _unused_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        port: int = sock.getsockname()[1]
        return port


async def _run(argv: List[str], environ: Dict[str, str]) -> int:
    return await asyncio.to_thread(healthcheck.main, argv, environ)


async def test_exits_zero_when_the_engine_answers(capsys: pytest.CaptureFixture[str]) -> None:
    servicer = RecordingServicer()
    async with serving(servicer) as port:
        code = await _run(["--timeout", "5"], {"GRPC_HOST": "0.0.0.0", "GRPC_PORT": str(port)})

    assert code == 0
    out = capsys.readouterr().out
    assert f"engine ok: 127.0.0.1:{port} version {__version__} healthy=True" in out
    assert "judge=ok" in out


async def test_sends_the_internal_token(tmp_path: Path) -> None:
    servicer = RecordingServicer()
    token_file = tmp_path / "internal.token"
    token_file.write_text("file-token\n", encoding="utf-8")

    async with serving(servicer) as port:
        target = ["--target", f"127.0.0.1:{port}"]
        assert await _run(target, {"TRUTHTABLE_INTERNAL_TOKEN_FILE": str(token_file)}) == 0
        assert await _run(target, {"TRUTHTABLE_INTERNAL_TOKEN": "env-token"}) == 0
        assert await _run(target, {"TRUTHTABLE_INTERNAL_TOKEN_FILE": str(tmp_path / "none")}) == 0

    tokens = [call.get("x-trustagent-internal") for call in servicer.metadata]
    assert tokens == ["file-token", "env-token", None]


async def test_exits_zero_when_the_judge_is_unhealthy(capsys: pytest.CaptureFixture[str]) -> None:
    # The check is about the engine serving; judge health is only reported.
    class DownJudge:
        async def health_check(self) -> bool:
            return False

    async with serving(AuditServicer(audit_graph=None, provider=DownJudge())) as port:
        code = await _run(["--target", f"127.0.0.1:{port}"], {})

    assert code == 0
    assert "healthy=False (judge=down)" in capsys.readouterr().out


def test_exits_one_when_nothing_answers(capsys: pytest.CaptureFixture[str]) -> None:
    port = _unused_port()

    code = healthcheck.main(["--timeout", "2"], {"GRPC_PORT": str(port)})

    assert code == 1
    assert f"engine unhealthy: 127.0.0.1:{port}: UNAVAILABLE" in capsys.readouterr().err


def test_module_entry_point_exit_code() -> None:
    port = _unused_port()

    completed = subprocess.run(
        [sys.executable, "-m", "truthtable.healthcheck", "--timeout", "2"],
        env={"GRPC_PORT": str(port), "PYTHONPATH": str(BACKEND_DIR / "src")},
        capture_output=True,
        text=True,
        timeout=60,
    )

    assert completed.returncode == 1, completed.stderr
    assert "engine unhealthy" in completed.stderr


@pytest.mark.parametrize(
    "environ, expected",
    [
        ({}, "127.0.0.1:50051"),
        ({"GRPC_HOST": "0.0.0.0", "GRPC_PORT": "6000"}, "127.0.0.1:6000"),
        ({"GRPC_HOST": "::"}, "127.0.0.1:50051"),
        ({"GRPC_HOST": "[::]"}, "127.0.0.1:50051"),
        ({"GRPC_HOST": "127.0.0.1", "GRPC_PORT": " "}, "127.0.0.1:50051"),
        ({"GRPC_HOST": "engine"}, "engine:50051"),
        ({"GRPC_HOST": "::1"}, "[::1]:50051"),
        ({"GRPC_HOST": "[::1]", "GRPC_PORT": "7000"}, "[::1]:7000"),
    ],
)
def test_engine_target(environ: Dict[str, str], expected: str) -> None:
    assert healthcheck.engine_target(environ) == expected


@pytest.mark.parametrize(
    "env_token, file_token, expected",
    [
        ("env", "file", "env"),
        ("", "file", "file"),
        ("", None, ""),
    ],
)
def test_internal_token(
    tmp_path: Path, env_token: str, file_token: Optional[str], expected: str
) -> None:
    token_file = tmp_path / "internal.token"
    if file_token is not None:
        token_file.write_text(f"{file_token}\n", encoding="utf-8")
    environ = {
        "TRUTHTABLE_INTERNAL_TOKEN": env_token,
        "TRUTHTABLE_INTERNAL_TOKEN_FILE": str(token_file),
    }

    assert healthcheck.internal_token(environ) == expected


def test_default_token_file_is_the_shared_volume() -> None:
    assert healthcheck.DEFAULT_INTERNAL_TOKEN_FILE == "/var/lib/trustagent-internal/internal.token"
