"""
Engine healthcheck: ``python -m truthtable.healthcheck``.

Exits 0 when the local engine's gRPC server answers HealthCheck, 1 otherwise.
The compose engine healthcheck runs it (SPEC section 3.4). It checks that the
engine is up and serving; the judge's own health is reported in the response
(and printed) but does not fail the check, so a judge outage never stops the
proxy from starting.

The target is GRPC_HOST:GRPC_PORT, with a wildcard bind address (0.0.0.0, ::)
checked on loopback. The internal token (TRUTHTABLE_INTERNAL_TOKEN, else the
TRUTHTABLE_INTERNAL_TOKEN_FILE contents) is sent as x-trustagent-internal
metadata when available. Only grpc and the generated stubs are imported, so
the check stays fast.
"""

import argparse
import os
import sys
from pathlib import Path
from typing import Mapping, Optional, Sequence

import grpc

from truthtable.grpc.pb import evaluator_pb2, evaluator_pb2_grpc

DEFAULT_PORT = 50051
DEFAULT_TIMEOUT_S = 3.0
INTERNAL_TOKEN_METADATA = "x-trustagent-internal"  # nosec B105 - a metadata key, not a secret
DEFAULT_INTERNAL_TOKEN_FILE = (  # nosec B105 - where the token lives, not the token
    "/var/lib/trustagent-internal/internal.token"
)

_WILDCARD_HOSTS = {"", "0.0.0.0", "::", "[::]"}  # nosec B104 - bind addresses, never dialled


def engine_target(environ: Mapping[str, str]) -> str:
    """host:port of the local engine, from GRPC_HOST and GRPC_PORT."""
    host = environ.get("GRPC_HOST", "").strip()
    port = environ.get("GRPC_PORT", "").strip() or str(DEFAULT_PORT)
    if host in _WILDCARD_HOSTS:
        host = "127.0.0.1"
    elif ":" in host and not host.startswith("["):
        host = f"[{host}]"  # bare IPv6 literal
    return f"{host}:{port}"


def internal_token(environ: Mapping[str, str]) -> str:
    """The proxy-to-engine token, or "" when none is configured."""
    token = environ.get("TRUTHTABLE_INTERNAL_TOKEN", "").strip()
    if token:
        return token
    path = Path(environ.get("TRUTHTABLE_INTERNAL_TOKEN_FILE") or DEFAULT_INTERNAL_TOKEN_FILE)
    try:
        return path.read_text(encoding="utf-8").strip()
    except OSError:
        return ""


def check(  # nosec B107 - "" means no token is sent
    target: str, timeout: float, token: str = ""
) -> evaluator_pb2.HealthResponse:
    """
    Call HealthCheck on `target`.

    Raises:
        grpc.RpcError: If the engine does not answer within `timeout` seconds.
    """
    metadata = ((INTERNAL_TOKEN_METADATA, token),) if token else None
    with grpc.insecure_channel(target) as channel:
        stub = evaluator_pb2_grpc.AuditServiceStub(channel)
        response: evaluator_pb2.HealthResponse = stub.HealthCheck(
            evaluator_pb2.HealthRequest(), timeout=timeout, metadata=metadata
        )
        return response


def main(argv: Optional[Sequence[str]] = None, environ: Optional[Mapping[str, str]] = None) -> int:
    env = os.environ if environ is None else environ
    parser = argparse.ArgumentParser(
        prog="python -m truthtable.healthcheck",
        description="Exit 0 if the local engine answers its gRPC HealthCheck, 1 otherwise.",
    )
    parser.add_argument(
        "--target",
        help="engine host:port (default: GRPC_HOST:GRPC_PORT, loopback for a wildcard host)",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_S,
        help=f"seconds to wait for the answer (default {DEFAULT_TIMEOUT_S:g})",
    )
    args = parser.parse_args(argv)

    target = args.target or engine_target(env)
    try:
        response = check(target, args.timeout, internal_token(env))
    except grpc.RpcError as exc:
        code = exc.code() if isinstance(exc, grpc.Call) else None
        details = exc.details() if isinstance(exc, grpc.Call) else str(exc)
        print(
            f"engine unhealthy: {target}: {code.name if code else 'error'}: {details}",
            file=sys.stderr,
        )
        return 1

    dependencies = ", ".join(
        f"{name}={'ok' if ok else 'down'}" for name, ok in sorted(response.dependencies.items())
    )
    print(
        f"engine ok: {target} version {response.version} healthy={response.healthy}"
        + (f" ({dependencies})" if dependencies else "")
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
