"""
Fakes for provider tests: a scripted httpx transport and a sleep recorder.

The HTTP providers accept `transport=` (an httpx transport) and `sleep=` (the
retry backoff). `ScriptedTransport` answers each request with the next scripted
reply (an httpx.Response, a callable building one, or an exception to raise)
and records every request, so tests exercise the real clients, retry loop and
error mapping without any network.
"""

import json
from typing import Any, Callable, Dict, List, Mapping, Optional, Union

import httpx

Reply = Union[httpx.Response, Callable[[httpx.Request], httpx.Response], Exception]


def json_reply(
    body: Any, status: int = 200, headers: Optional[Mapping[str, str]] = None
) -> httpx.Response:
    """A JSON answer (Content-Type application/json)."""
    return httpx.Response(status, json=body, headers=dict(headers or {}))


def text_reply(
    text: str, status: int = 200, content_type: str = "text/html; charset=utf-8"
) -> httpx.Response:
    """A non-JSON answer (an HTML page by default)."""
    return httpx.Response(status, text=text, headers={"content-type": content_type})


class ScriptedTransport(httpx.MockTransport):
    """An httpx transport replaying `replies` in order; the last one repeats."""

    def __init__(self, *replies: Reply):
        if not replies:
            raise ValueError("at least one reply is needed")
        self.replies: List[Reply] = list(replies)
        self.requests: List[httpx.Request] = []
        super().__init__(self._handle)

    def _handle(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        index = min(len(self.requests), len(self.replies)) - 1
        reply = self.replies[index]
        if isinstance(reply, Exception):
            raise reply
        if callable(reply) and not isinstance(reply, httpx.Response):
            return reply(request)
        return reply

    @property
    def calls(self) -> int:
        return len(self.requests)

    def body(self, index: int = -1) -> Dict[str, Any]:
        """The JSON body of a recorded request."""
        content = self.requests[index].content
        return json.loads(content) if content else {}

    def path(self, index: int = -1) -> str:
        return self.requests[index].url.path


class SleepRecorder:
    """An awaitable no-op sleep that records the requested delays."""

    def __init__(self) -> None:
        self.delays: List[float] = []

    async def __call__(self, delay: float) -> None:
        self.delays.append(delay)


def connect_error(message: str = "Connection refused") -> httpx.ConnectError:
    return httpx.ConnectError(message)
