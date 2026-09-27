"""Shared fixtures for end-to-end tests.

These tests exercise the real running stack (Go proxy + Python gRPC engine +
Qdrant). They are opt-in — excluded from the default `pytest` run via the
`e2e` marker (see pyproject `addopts`) and run explicitly with `make test-e2e`
or `pytest -m e2e`. Each fixture SKIPS (not fails) when its service is
unreachable, so the suite is safe to collect on any machine.
"""

import urllib.request

import pytest

PROXY_URL = "http://localhost:8080"
GRPC_ADDR = "localhost:50051"


def _http_ok(url: str, timeout: int = 5) -> bool:
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            return resp.status == 200
    except Exception:
        return False


@pytest.fixture(scope="session")
def proxy_url() -> str:
    """The Go proxy base URL, or skip if it isn't running."""
    if not _http_ok(f"{PROXY_URL}/health"):
        pytest.skip(f"Go proxy not reachable at {PROXY_URL} (start: make dev-go)")
    return PROXY_URL


@pytest.fixture(scope="session")
def grpc_stub():
    """A connected AuditService stub, or skip if the engine isn't running."""
    grpc = pytest.importorskip("grpc")
    from truthtable.grpc.pb import evaluator_pb2, evaluator_pb2_grpc

    channel = grpc.insecure_channel(GRPC_ADDR)
    stub = evaluator_pb2_grpc.AuditServiceStub(channel)
    try:
        stub.HealthCheck(evaluator_pb2.HealthRequest(), timeout=5)
    except Exception:
        channel.close()
        pytest.skip(f"audit engine not reachable at {GRPC_ADDR} (start: make dev-python)")
    yield stub
    channel.close()
