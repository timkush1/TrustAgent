"""
Shared HTTP plumbing for the judge providers.

`request_json` sends one attempt and turns every failure into a typed
ProviderError (errors.py): transport errors, non-2xx statuses (with the
upstream error message and Retry-After), and 2xx answers that are not JSON.
The Content-Type check matters for OpenAI-compatible gateways: a wrong base
URL on OpenRouter answers `200 text/html` from the site's catch-all page,
which must be an error, never an empty judge answer.

Retrying is the caller's job (retry.call_with_retry), so a provider can adapt
the request between attempts.
"""

import email.utils
import json
import time
from typing import Any, Dict, Mapping, Optional

import httpx

from .errors import (
    AUTH,
    BAD_CONTENT_TYPE,
    BAD_RESPONSE,
    CONNECTION,
    CREDITS,
    HTTP_ERROR,
    INVALID,
    NOT_FOUND,
    OVERLOADED,
    RATE_LIMITED,
    TIMEOUT,
    TOO_LARGE,
    UNAVAILABLE,
    BudgetError,
    ProviderConnectionError,
    ProviderError,
)

# Upstream error messages are cut to this many characters in ProviderError.
MAX_ERROR_MESSAGE_CHARS = 300

# OpenRouter's transient 402: the request would exceed the key's in-flight budget.
OPENROUTER_IN_FLIGHT_BUDGET = "openrouter_in_flight_budget"

_STATUS_KINDS: Dict[int, str] = {
    400: INVALID,
    401: AUTH,
    402: CREDITS,
    403: AUTH,
    404: NOT_FOUND,
    408: TIMEOUT,
    413: TOO_LARGE,
    422: INVALID,
    429: RATE_LIMITED,
    529: OVERLOADED,
}

JsonObject = Dict[str, Any]


def status_kind(status: int) -> str:
    """ProviderError.kind for an HTTP error status."""
    if status in _STATUS_KINDS:
        return _STATUS_KINDS[status]
    if 500 <= status <= 599:
        return UNAVAILABLE
    return HTTP_ERROR


def status_retryable(status: int) -> bool:
    """Whether an HTTP error status is transient in principle (408, 429, 5xx)."""
    return status in (408, 429) or 500 <= status <= 599


def parse_retry_after(headers: Mapping[str, str], now: Optional[float] = None) -> Optional[float]:
    """
    Seconds the server asks us to wait: `retry-after-ms` (OpenAI), else
    `retry-after` as delay-seconds or an HTTP-date. None when absent or invalid.
    """
    raw_ms = headers.get("retry-after-ms")
    if raw_ms is not None:
        try:
            value = float(raw_ms) / 1000.0
            if value >= 0:
                return value
        except ValueError:
            pass
    raw = headers.get("retry-after")
    if raw is None:
        return None
    raw = raw.strip()
    try:
        seconds = float(raw)
        return seconds if seconds >= 0 else None
    except ValueError:
        pass
    try:
        when = email.utils.parsedate_to_datetime(raw)
    except (TypeError, ValueError):
        return None
    if when is None:
        return None
    current = time.time() if now is None else now
    return max(0.0, when.timestamp() - current)


def _truncate(text: str) -> str:
    text = " ".join(text.split())
    if len(text) <= MAX_ERROR_MESSAGE_CHARS:
        return text
    return text[: MAX_ERROR_MESSAGE_CHARS - 1] + "…"


def _error_body(response: httpx.Response) -> Any:
    try:
        return response.json()
    except (ValueError, UnicodeDecodeError):
        return None


def error_message(body: Any, fallback: str = "") -> str:
    """
    The human-readable message of an upstream error envelope:
    `{"error": {"message": ...}}` (OpenAI, Anthropic, OpenRouter),
    `{"error": "..."}` (Ollama), `{"message": ...}` or `{"detail": ...}`.
    """
    if isinstance(body, dict):
        error = body.get("error")
        if isinstance(error, dict) and isinstance(error.get("message"), str):
            return _truncate(error["message"])
        if isinstance(error, str):
            return _truncate(error)
        for key in ("message", "detail"):
            value = body.get(key)
            if isinstance(value, str):
                return _truncate(value)
            if value is not None:
                return _truncate(json.dumps(value, ensure_ascii=False))
    return _truncate(fallback)


def _limit_source(body: Any) -> str:
    if isinstance(body, dict):
        error = body.get("error")
        if isinstance(error, dict):
            metadata = error.get("metadata")
            if isinstance(metadata, dict) and isinstance(metadata.get("limit_source"), str):
                return str(metadata["limit_source"])
    return ""


def error_from_status(
    provider: str,
    status: int,
    body: Any,
    *,
    headers: Optional[Mapping[str, str]] = None,
    text: str = "",
) -> ProviderError:
    """The ProviderError for an HTTP error answer (status, parsed body, headers)."""
    kind = status_kind(status)
    retry_after = parse_retry_after(headers or {})
    message = f"{provider} returned HTTP {status}"
    detail = error_message(body, fallback=text)
    if detail:
        message = f"{message}: {detail}"
    if status == 402:
        transient = _limit_source(body) == OPENROUTER_IN_FLIGHT_BUDGET
        return BudgetError(
            message,
            kind=CREDITS,
            provider=provider,
            status=status,
            retryable=transient,
            retry_after_s=retry_after if transient else None,
        )
    return ProviderError(
        message,
        kind=kind,
        provider=provider,
        status=status,
        retryable=status_retryable(status),
        retry_after_s=retry_after,
    )


def error_from_response(provider: str, response: httpx.Response) -> ProviderError:
    """The ProviderError for a non-2xx httpx response."""
    body = _error_body(response)
    text = "" if body is not None else response.text
    return error_from_status(
        provider, response.status_code, body, headers=response.headers, text=text
    )


def error_from_exception(provider: str, exc: httpx.HTTPError, where: str) -> ProviderError:
    """The ProviderError for an httpx transport failure (no response received)."""
    if isinstance(exc, httpx.TimeoutException) and not isinstance(exc, httpx.ConnectTimeout):
        return ProviderError(
            f"{provider} request to {where} timed out ({type(exc).__name__})",
            kind=TIMEOUT,
            provider=provider,
            retryable=True,
        )
    return ProviderConnectionError(
        f"Cannot connect to {provider} at {where} ({type(exc).__name__}: {exc})",
        kind=CONNECTION,
        provider=provider,
        retryable=True,
    )


def is_json_content_type(content_type: str) -> bool:
    """application/json, or any application/*+json media type."""
    media_type = content_type.split(";", 1)[0].strip().lower()
    return media_type == "application/json" or (
        media_type.startswith("application/") and media_type.endswith("+json")
    )


def check_json_response(provider: str, response: httpx.Response) -> JsonObject:
    """
    The JSON object of a 2xx answer.

    Raises:
        ProviderError: bad_content_type when the answer is not JSON (e.g. an HTML
            page), bad_response when the body is not a JSON object.
    """
    content_type = response.headers.get("content-type", "")
    if not is_json_content_type(content_type):
        raise ProviderError(
            f"{provider} answered HTTP {response.status_code} with Content-Type "
            f"{content_type or '(none)'!r} instead of JSON; check the base URL "
            "(an OpenRouter base URL must not include a web page path)",
            kind=BAD_CONTENT_TYPE,
            provider=provider,
            status=response.status_code,
        )
    try:
        data = response.json()
    except (ValueError, UnicodeDecodeError) as exc:
        raise ProviderError(
            f"{provider} answered with invalid JSON: {exc}",
            kind=BAD_RESPONSE,
            provider=provider,
            status=response.status_code,
        ) from exc
    if not isinstance(data, dict):
        raise ProviderError(
            f"{provider} answered with a JSON {type(data).__name__}, not an object",
            kind=BAD_RESPONSE,
            provider=provider,
            status=response.status_code,
        )
    return data


async def request_json(
    client: httpx.AsyncClient,
    method: str,
    url: str,
    *,
    provider: str,
    json_body: Optional[JsonObject] = None,
    timeout_s: Optional[float] = None,
) -> JsonObject:
    """
    One HTTP attempt that must answer 2xx with a JSON object.

    Args:
        client: The provider's client (base URL, headers and default timeout).
        method: HTTP method.
        url: Path (or URL) relative to the client's base URL.
        provider: Provider name for error messages.
        json_body: Request body.
        timeout_s: Per-attempt timeout; None uses the client's default.

    Raises:
        ProviderError: For every failure (see errors.py for the kinds).
    """
    timeout: Any = httpx.USE_CLIENT_DEFAULT if timeout_s is None else timeout_s
    try:
        response = await client.request(method, url, json=json_body, timeout=timeout)
    except httpx.HTTPError as exc:
        raise error_from_exception(provider, exc, str(client.base_url)) from exc
    if not response.is_success:
        raise error_from_response(provider, response)
    return check_json_response(provider, response)


def first_text(value: Any) -> str:
    """A message content that may be null or a list of content parts, as text."""
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        parts = []
        for part in value:
            if isinstance(part, dict) and isinstance(part.get("text"), str):
                parts.append(part["text"])
            elif isinstance(part, str):
                parts.append(part)
        return "".join(parts)
    return ""


def bad_response(provider: str, what: str) -> ProviderError:
    """A ProviderError for a JSON answer missing the expected fields."""
    return ProviderError(
        f"{provider} answer is missing {what}", kind=BAD_RESPONSE, provider=provider
    )


def object_field(data: JsonObject, key: str) -> JsonObject:
    """data[key] when it is a JSON object, else {}."""
    value = data.get(key)
    return value if isinstance(value, dict) else {}


def int_field(data: JsonObject, key: str) -> int:
    """data[key] as a non-negative int (0 when missing or not a number)."""
    value = data.get(key)
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return 0
    return max(0, int(value))


def float_field(data: JsonObject, key: str) -> Optional[float]:
    """data[key] as a float, or None when missing or not a number."""
    value = data.get(key)
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    return float(value)
