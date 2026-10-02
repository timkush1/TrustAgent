"""
Proxy-to-engine authentication (SPEC sections 2.2 and 7.5).

Every RPC from the proxy carries the metadata `x-trustagent-internal: <token>`.
The token is TRUTHTABLE_INTERNAL_TOKEN, or the contents of
TRUTHTABLE_INTERNAL_TOKEN_FILE (the proxy generates the file into a volume the
engine mounts read-only).

- `InternalTokenInterceptor` rejects any RPC without the right token with
  UNAUTHENTICATED once a token is known. Without a token every RPC is
  accepted, which `check_bind` allows only on a loopback address.
- `check_bind` is the startup rule: the engine refuses to listen on a
  non-loopback address (e.g. 0.0.0.0 in compose) without a token. It waits up
  to 30 s for the token file first, because the proxy may start after the
  engine and write it then.
- The file is re-read when it changes, so a token the proxy rotates is picked
  up without restarting the engine. Once a token is known the engine keeps
  requiring one: a token file that disappears or reads empty (e.g. caught
  mid-rewrite) keeps the last token instead of opening the engine to
  unauthenticated calls.

Token values are never logged.
"""

import asyncio
import hmac
import ipaddress
import logging
import time
from pathlib import Path
from typing import Any, Awaitable, Callable, Optional, Tuple

import grpc
from grpc import aio

logger = logging.getLogger(__name__)

INTERNAL_TOKEN_METADATA = "x-trustagent-internal"  # nosec B105 - a metadata key, not a secret
DEFAULT_TOKEN_FILE = (  # nosec B105 - where the token lives, not the token
    "/var/lib/trustagent-internal/internal.token"
)
TOKEN_WAIT_S = 30.0
TOKEN_POLL_S = 0.5

# A changed token file is noticed within this many seconds.
_FILE_RECHECK_S = 1.0

# "No token configured".
_NO_TOKEN = ""  # nosec B105 - the absence of a token, not a secret

# What identifies one version of the token file: inode (an atomic rename
# replaces it), modification time and size.
_FileStamp = Tuple[int, int, int]


class InternalTokenError(RuntimeError):
    """The engine may not start: non-loopback bind without an internal token."""


def is_loopback_host(host: str) -> bool:
    """Whether a bind host only accepts local connections (127.0.0.0/8, ::1, localhost)."""
    value = host.strip().strip("[]").lower()
    if value == "localhost":
        return True
    try:
        return ipaddress.ip_address(value).is_loopback
    except ValueError:
        return False  # "", 0.0.0.0-style wildcards are IPs; any other name is not loopback


class InternalTokenSource:
    """
    The current internal token: the explicit token, else the token file's
    contents (re-read when the file changes). "" when there is none yet.

    Once the file has yielded a token, a missing or empty file keeps that
    token: the engine never drops back to accepting unauthenticated calls
    while it runs (it may be listening on a non-loopback address).
    """

    def __init__(
        self,
        token: str = _NO_TOKEN,
        path: Optional[Path] = None,
        clock: Callable[[], float] = time.monotonic,
        on_token: Optional[Callable[[str], None]] = None,
    ):
        self._token = token.strip()
        self.path = path
        self._clock = clock
        # Told every token the source yields, a rotated file's included (the
        # engine registers them with its log redactor, D42).
        self._on_token = on_token
        if self._token and on_token is not None:
            on_token(self._token)
        self._file_token = _NO_TOKEN
        self._file_stamp: Optional[_FileStamp] = None
        self._checked_at: Optional[float] = None

    @property
    def from_env(self) -> bool:
        return bool(self._token)

    def _stamp(self) -> Optional[_FileStamp]:
        if self.path is None:
            return None
        try:
            stat = self.path.stat()
        except OSError:
            return None
        return (stat.st_ino, stat.st_mtime_ns, stat.st_size)

    def _read_file(self) -> str:
        if self.path is None:
            return _NO_TOKEN
        try:
            return self.path.read_text(encoding="utf-8").strip()
        except OSError:
            return _NO_TOKEN

    def refresh(self) -> str:
        """Re-read the token file now if it changed; return the current token."""
        if self._token:
            return self._token
        self._checked_at = self._clock()
        stamp = self._stamp()
        if stamp == self._file_stamp:
            return self._file_token
        self._file_stamp = stamp
        token = self._read_file() if stamp is not None else _NO_TOKEN
        if token:
            if token != self._file_token:
                if self._on_token is not None:
                    self._on_token(token)
                replaced = bool(self._file_token)
                logger.info(
                    f"Internal token {'reloaded' if replaced else 'loaded'} from {self.path}"
                )
                self._file_token = token
        elif self._file_token:
            logger.warning(
                f"Internal token file {self.path} is missing or empty; "
                "keeping the last token (RPCs still need it)"
            )
        return self._file_token

    def current(self) -> str:
        """The current token (the file is checked at most once per second)."""
        if self._token:
            return self._token
        if self._checked_at is None or self._clock() - self._checked_at >= _FILE_RECHECK_S:
            return self.refresh()
        return self._file_token

    def matches(self, presented: str) -> bool:
        """Constant-time comparison of a presented token with the current one."""
        expected = self.current()
        if expected and hmac.compare_digest(presented.encode(), expected.encode()):
            return True
        # The proxy may have just rotated the file: check once more, now.
        if not self._token:
            refreshed = self.refresh()
            return bool(refreshed) and hmac.compare_digest(presented.encode(), refreshed.encode())
        return False


def token_source(
    token: str, token_file: str, on_token: Optional[Callable[[str], None]] = None
) -> InternalTokenSource:
    """
    The token source for TRUTHTABLE_INTERNAL_TOKEN and TRUTHTABLE_INTERNAL_TOKEN_FILE
    (`on_token` is told every token it yields, see InternalTokenSource).
    """
    path = Path(token_file).expanduser() if token_file.strip() else None
    return InternalTokenSource(token=token, path=path, on_token=on_token)


async def check_bind(
    host: str,
    source: InternalTokenSource,
    *,
    wait_s: float = TOKEN_WAIT_S,
    poll_s: float = TOKEN_POLL_S,
    sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    clock: Callable[[], float] = time.monotonic,
) -> None:
    """
    Enforce the startup rule: a non-loopback bind needs an internal token.

    Waits up to `wait_s` for the token file on a non-loopback host.

    Raises:
        InternalTokenError: If `host` is not loopback and no token appeared.
    """
    if source.refresh():
        return
    if is_loopback_host(host):
        logger.info(
            f"No internal token configured; accepting unauthenticated RPCs on loopback {host}"
        )
        return
    if source.path is None:
        raise InternalTokenError(
            f"refusing to listen on {host} without an internal token: set "
            "TRUTHTABLE_INTERNAL_TOKEN or TRUTHTABLE_INTERNAL_TOKEN_FILE, or bind "
            "GRPC_HOST=127.0.0.1"
        )
    logger.info(
        f"Waiting up to {wait_s:g}s for the internal token file {source.path} "
        f"(needed to listen on {host})"
    )
    deadline = clock() + wait_s
    while clock() < deadline:
        await sleep(poll_s)
        if source.refresh():
            return
    raise InternalTokenError(
        f"refusing to listen on {host} without an internal token: set "
        "TRUTHTABLE_INTERNAL_TOKEN or TRUTHTABLE_INTERNAL_TOKEN_FILE (the proxy writes "
        f"{source.path or DEFAULT_TOKEN_FILE}), or bind GRPC_HOST=127.0.0.1"
    )


def _presented_token(handler_call_details: Any) -> str:
    for item in handler_call_details.invocation_metadata or ():
        key, value = item[0], item[1]
        if key == INTERNAL_TOKEN_METADATA:
            return value.decode() if isinstance(value, bytes) else str(value)
    return ""


async def _deny(request: Any, context: Any) -> None:
    await context.abort(
        grpc.StatusCode.UNAUTHENTICATED,
        f"missing or invalid {INTERNAL_TOKEN_METADATA} metadata",
    )


# Every AuditService RPC is unary-unary; a rejected call never reaches its
# handler, so the request is not deserialized.
_DENY_HANDLER = grpc.unary_unary_rpc_method_handler(_deny)


class InternalTokenInterceptor(aio.ServerInterceptor):  # type: ignore[misc]
    """Rejects RPCs without the internal token (UNAUTHENTICATED) once one is known."""

    def __init__(self, source: InternalTokenSource):
        self.source = source

    async def intercept_service(
        self, continuation: Callable[[Any], Awaitable[Any]], handler_call_details: Any
    ) -> Any:
        if not self.source.current():
            return await continuation(handler_call_details)
        if self.source.matches(_presented_token(handler_call_details)):
            return await continuation(handler_call_details)
        logger.warning(f"Rejected {handler_call_details.method}: missing or invalid internal token")
        return _DENY_HANDLER
