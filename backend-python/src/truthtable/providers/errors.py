"""
Typed judge-provider errors.

Every provider raises a ProviderError (a RuntimeError, so existing callers that
catch RuntimeError or Exception keep working) whose `kind` classifies the
failure the same way for every backend:

| kind               | Cause                                                    | Retried by default |
|--------------------|----------------------------------------------------------|--------------------|
| connection         | the host could not be reached (DNS, refused, reset)      | yes                |
| timeout            | the attempt timed out (client side, or HTTP 408)         | yes                |
| rate_limited       | HTTP 429                                                 | yes                |
| overloaded         | HTTP 529 (Anthropic "overloaded_error")                  | yes                |
| unavailable        | any other HTTP 5xx                                       | yes                |
| credits            | HTTP 402: out of credits or over a spend limit           | only in-flight 402 |
| auth               | HTTP 401/403                                             | no                 |
| invalid            | HTTP 400/422 (the request was rejected)                  | no                 |
| not_found          | HTTP 404 (wrong base URL or model)                       | no                 |
| too_large          | HTTP 413                                                 | no                 |
| bad_content_type   | a 2xx answer that is not JSON (e.g. an HTML catch-all)   | no                 |
| bad_response       | a JSON answer without the expected fields                | no                 |
| http_error         | any other non-2xx status                                 | no                 |

Whether a retry actually happens is decided by the RetryPolicy (retry.py);
Ollama, for instance, retries only connection errors and 5xx. Messages never
contain credentials: only the provider name, the status and the upstream
error message (truncated).
"""

from typing import Optional

# ProviderError.kind values (see the module docstring).
CONNECTION = "connection"
TIMEOUT = "timeout"
RATE_LIMITED = "rate_limited"
OVERLOADED = "overloaded"
UNAVAILABLE = "unavailable"
CREDITS = "credits"
AUTH = "auth"
INVALID = "invalid"
NOT_FOUND = "not_found"
TOO_LARGE = "too_large"
BAD_CONTENT_TYPE = "bad_content_type"
BAD_RESPONSE = "bad_response"
HTTP_ERROR = "http_error"


class ProviderError(RuntimeError):
    """
    A judge provider call failed.

    Attributes:
        kind: Failure class (module constants, e.g. "rate_limited").
        provider: Provider name ("anthropic", "openrouter", ...).
        status: HTTP status, or None when no response was received.
        retryable: Whether the failure is transient in principle; the retry
            policy decides whether this provider retries it.
        retry_after_s: Server-requested delay (Retry-After), if any.
    """

    def __init__(
        self,
        message: str,
        *,
        kind: str,
        provider: str,
        status: Optional[int] = None,
        retryable: bool = False,
        retry_after_s: Optional[float] = None,
    ) -> None:
        super().__init__(message)
        self.kind = kind
        self.provider = provider
        self.status = status
        self.retryable = retryable
        self.retry_after_s = retry_after_s

    def __repr__(self) -> str:
        return (
            f"{type(self).__name__}(kind={self.kind!r}, provider={self.provider!r}, "
            f"status={self.status!r}, retryable={self.retryable!r})"
        )


class ProviderConnectionError(ProviderError, ConnectionError):
    """The provider could not be reached (also a ConnectionError, for legacy callers)."""


class BudgetError(ProviderError):
    """
    HTTP 402: the account is out of credits or over a spend limit.

    OpenRouter also answers 402 when a request would exceed the key's in-flight
    budget (`error.metadata.limit_source == "openrouter_in_flight_budget"`);
    only that case is transient (`retryable`), and it honours Retry-After.
    """
