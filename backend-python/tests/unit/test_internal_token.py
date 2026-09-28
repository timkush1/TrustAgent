"""
Proxy-to-engine internal token (SPEC sections 2.2 and 7.5): every RPC carries
`x-trustagent-internal`; the interceptor rejects a missing or wrong token with
UNAUTHENTICATED once a token is known; the engine refuses a non-loopback bind
without one after waiting up to 30 s for the token file.
"""

import asyncio
from pathlib import Path
from typing import AsyncIterator, List, Optional

import grpc
import pytest
from grpc import aio

from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer, create_server
from truthtable.healthcheck import check as healthcheck
from truthtable.internal_auth import (
    INTERNAL_TOKEN_METADATA,
    InternalTokenError,
    InternalTokenInterceptor,
    InternalTokenSource,
    check_bind,
    is_loopback_host,
    token_source,
)

TOKEN = "3f1e0c9a-internal-token"
RPC_TIMEOUT_S = 5


class FakeClock:
    def __init__(self) -> None:
        self.now = 0.0

    def __call__(self) -> float:
        return self.now


# ---------------------------------------------------------------------------
# Token source
# ---------------------------------------------------------------------------


def test_env_token_wins_over_the_file(tmp_path: Path) -> None:
    path = tmp_path / "internal.token"
    path.write_text("from-file\n", encoding="utf-8")
    source = token_source(" from-env ", str(path))
    assert source.current() == "from-env"
    assert source.from_env


def test_file_token_is_read_and_trimmed(tmp_path: Path) -> None:
    path = tmp_path / "internal.token"
    path.write_text(f"{TOKEN}\n", encoding="utf-8")
    assert token_source("", str(path)).current() == TOKEN


def test_missing_or_empty_file_means_no_token(tmp_path: Path) -> None:
    assert token_source("", str(tmp_path / "missing")).current() == ""
    empty = tmp_path / "empty.token"
    empty.write_text("  \n", encoding="utf-8")
    assert token_source("", str(empty)).current() == ""
    assert token_source("", "").current() == ""


def test_a_rotated_file_is_picked_up(tmp_path: Path) -> None:
    path = tmp_path / "internal.token"
    path.write_text("first", encoding="utf-8")
    clock = FakeClock()
    source = InternalTokenSource(path=path, clock=clock)
    assert source.current() == "first"

    path.write_text("second-token", encoding="utf-8")
    assert source.current() == "first"  # re-checked at most once per second
    clock.now += 1.5
    assert source.current() == "second-token"


def test_matches_rechecks_the_file_immediately(tmp_path: Path) -> None:
    path = tmp_path / "internal.token"
    path.write_text("first", encoding="utf-8")
    source = InternalTokenSource(path=path, clock=FakeClock())
    assert source.matches("first")

    path.write_text("rotated-token", encoding="utf-8")
    assert source.matches("rotated-token")
    assert not source.matches("first")
    assert not source.matches("")


@pytest.mark.parametrize(
    "host, loopback",
    [
        ("127.0.0.1", True),
        ("127.0.0.2", True),
        ("::1", True),
        ("[::1]", True),
        ("localhost", True),
        ("0.0.0.0", False),
        ("::", False),
        ("", False),
        ("engine", False),
        ("10.0.0.5", False),
    ],
)
def test_is_loopback_host(host: str, loopback: bool) -> None:
    assert is_loopback_host(host) is loopback


# ---------------------------------------------------------------------------
# Startup rule
# ---------------------------------------------------------------------------


async def test_loopback_bind_needs_no_token(tmp_path: Path) -> None:
    await check_bind("127.0.0.1", token_source("", str(tmp_path / "missing")), wait_s=0)


async def test_non_loopback_bind_with_a_token_starts() -> None:
    await check_bind("0.0.0.0", token_source(TOKEN, ""), wait_s=0)


async def test_non_loopback_bind_waits_for_the_token_file(tmp_path: Path) -> None:
    path = tmp_path / "internal.token"
    clock = FakeClock()
    sleeps: List[float] = []

    async def sleep(delay: float) -> None:
        sleeps.append(delay)
        clock.now += delay
        if len(sleeps) == 3:
            path.write_text(TOKEN, encoding="utf-8")  # the proxy writes it

    source = InternalTokenSource(path=path, clock=clock)
    await check_bind("0.0.0.0", source, sleep=sleep, clock=clock)

    assert sleeps == [0.5, 0.5, 0.5]
    assert source.current() == TOKEN


async def test_non_loopback_bind_without_a_token_is_refused(tmp_path: Path) -> None:
    clock = FakeClock()
    sleeps: List[float] = []

    async def sleep(delay: float) -> None:
        sleeps.append(delay)
        clock.now += delay

    source = InternalTokenSource(path=tmp_path / "missing", clock=clock)
    with pytest.raises(InternalTokenError, match="refusing to listen on 0.0.0.0"):
        await check_bind("0.0.0.0", source, sleep=sleep, clock=clock)

    assert sum(sleeps) == pytest.approx(30.0)  # waited the full 30 s


# ---------------------------------------------------------------------------
# Interceptor over a real in-process server
# ---------------------------------------------------------------------------


async def _serve(source: InternalTokenSource) -> AsyncIterator[str]:
    servicer = AuditServicer(audit_graph=None)
    server = create_server(
        servicer, host="127.0.0.1", port=0, interceptors=[InternalTokenInterceptor(source)]
    )
    # create_server does not report the ephemeral port it bound; add one we know.
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    try:
        yield f"127.0.0.1:{port}"
    finally:
        await server.stop(grace=None)


@pytest.fixture
async def guarded_target() -> AsyncIterator[str]:
    async for target in _serve(token_source(TOKEN, "")):
        yield target


async def _health(target: str, token: Optional[str]) -> pb.HealthResponse:
    metadata = ((INTERNAL_TOKEN_METADATA, token),) if token is not None else None
    async with aio.insecure_channel(target) as channel:
        stub = pb_grpc.AuditServiceStub(channel)
        return await stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S, metadata=metadata)


async def test_rpc_with_the_token_is_served(guarded_target: str) -> None:
    response = await _health(guarded_target, TOKEN)
    assert response.version


@pytest.mark.parametrize("token", [None, "", "wrong-token"])
async def test_rpc_without_the_right_token_is_unauthenticated(
    guarded_target: str, token: Optional[str]
) -> None:
    with pytest.raises(aio.AioRpcError) as exc_info:
        await _health(guarded_target, token)
    assert exc_info.value.code() == grpc.StatusCode.UNAUTHENTICATED
    assert TOKEN not in (exc_info.value.details() or "")


async def test_every_rpc_is_guarded(guarded_target: str) -> None:
    async with aio.insecure_channel(guarded_target) as channel:
        stub = pb_grpc.AuditServiceStub(channel)
        for call, message in (
            (stub.Evaluate, pb.AuditRequest(request_id="r")),
            (stub.GetEngineInfo, pb.EngineInfoRequest()),
            (stub.SubmitAudit, pb.AuditRequest(request_id="r")),
        ):
            with pytest.raises(aio.AioRpcError) as exc_info:
                await call(message, timeout=RPC_TIMEOUT_S)
            assert exc_info.value.code() == grpc.StatusCode.UNAUTHENTICATED


async def test_without_a_token_every_rpc_is_accepted(tmp_path: Path) -> None:
    async for target in _serve(token_source("", str(tmp_path / "missing"))):
        response = await _health(target, None)
        assert response.version


async def test_healthcheck_tool_sends_the_token(guarded_target: str) -> None:
    # `python -m truthtable.healthcheck` (a blocking client) sends the same token.
    response = await asyncio.to_thread(healthcheck, guarded_target, RPC_TIMEOUT_S, TOKEN)
    assert response.version

    with pytest.raises(grpc.RpcError) as exc_info:
        await asyncio.to_thread(healthcheck, guarded_target, RPC_TIMEOUT_S, "")
    assert exc_info.value.code() == grpc.StatusCode.UNAUTHENTICATED
