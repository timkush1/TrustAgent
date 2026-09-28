"""
OpenRouter Provider Implementation (SPEC section 3.2)

OpenRouter serves many models behind one OpenAI-compatible Chat Completions API
at `<base>/api/v1/chat/completions`, so one OPENROUTER_API_KEY runs both the
Jev decision model (in the proxy) and the audit judge (here; default model
OPENROUTER_JUDGE_MODEL = anthropic/claude-haiku-4.5).

Specifics handled here:
- Every request asks for `usage: {include: true}`, so the answer reports the
  call's cost in `usage.cost` (credits, in USD); the cost_source is then
  `reported`, and the price table is only the fallback (`computed`).
- HTTP 402 (no credits, or over a key limit) raises BudgetError. Only the
  in-flight budget 402 (`error.metadata.limit_source ==
  "openrouter_in_flight_budget"`) is transient and retried after Retry-After.
- The answer must be `application/json`: a base URL with a wrong path gets a
  `200 text/html` page from the site, which is an error, not an empty answer.
- Attribution headers identify the app to OpenRouter: `HTTP-Referer`
  (TRUTHTABLE_JEV_APP_URL) and `X-OpenRouter-Title` (TRUTHTABLE_JEV_APP_TITLE).
- The health check is `GET /api/v1/key` (the key's own status, free of charge).

The base URL is TRUTHTABLE_OPENROUTER_BASE_URL (default https://openrouter.ai;
tests point it at a mock upstream). A trailing `/api` or `/api/v1` is accepted
and dropped, since the paths above include it.
"""

import logging
import os
from typing import Any, Dict, Optional

from .base import CompletionRequest
from .openai import OpenAICompatibleProvider
from .transport import JsonObject, float_field

logger = logging.getLogger(__name__)

OPENROUTER_BASE_URL = "https://openrouter.ai"
DEFAULT_OPENROUTER_JUDGE_MODEL = "anthropic/claude-haiku-4.5"
DEFAULT_APP_URL = "https://github.com/timkush1/TrustAgent"
DEFAULT_APP_TITLE = "TrustAgent"

CHAT_PATH = "/api/v1/chat/completions"
KEY_PATH = "/api/v1/key"


def normalize_base_url(base_url: str) -> str:
    """The OpenRouter origin: trailing slashes and an `/api` or `/api/v1` suffix removed."""
    url = (base_url or OPENROUTER_BASE_URL).strip().rstrip("/")
    for suffix in ("/api/v1", "/api"):
        if url.endswith(suffix):
            url = url[: -len(suffix)]
            break
    return url or OPENROUTER_BASE_URL


class OpenRouterProvider(OpenAICompatibleProvider):
    """
    Provider for OpenRouter's OpenAI-compatible Chat Completions API.

    Args:
        model: OpenRouter model id (default anthropic/claude-haiku-4.5)
        api_key: API key; defaults to the OPENROUTER_API_KEY environment variable
        base_url: OpenRouter origin (TRUTHTABLE_OPENROUTER_BASE_URL)
        app_url: HTTP-Referer attribution header (TRUTHTABLE_JEV_APP_URL)
        app_title: X-OpenRouter-Title attribution header (TRUTHTABLE_JEV_APP_TITLE)
        timeout: Request timeout in seconds (per attempt)
        **kwargs: HTTPProvider options (retry_policy, transport, price_table, ...)
    """

    provider_name = "openrouter"
    chat_path = CHAT_PATH

    def __init__(
        self,
        model: str = DEFAULT_OPENROUTER_JUDGE_MODEL,
        api_key: Optional[str] = None,
        base_url: str = OPENROUTER_BASE_URL,
        app_url: str = DEFAULT_APP_URL,
        app_title: str = DEFAULT_APP_TITLE,
        timeout: float = 60.0,
        **kwargs: Any,
    ):
        super().__init__(
            model=model, base_url=normalize_base_url(base_url), timeout=timeout, **kwargs
        )
        self.api_key = api_key or os.environ.get("OPENROUTER_API_KEY", "")
        if not self.api_key:
            raise ValueError("OpenRouter API key missing: set OPENROUTER_API_KEY or pass api_key")
        self.app_url = app_url
        self.app_title = app_title

    def _headers(self) -> Dict[str, str]:
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {self.api_key}",
        }
        if self.app_url:
            headers["HTTP-Referer"] = self.app_url
        if self.app_title:
            headers["X-OpenRouter-Title"] = self.app_title
        return headers

    def _payload(self, request: CompletionRequest) -> JsonObject:
        payload = super()._payload(request)
        payload["usage"] = {"include": True}
        return payload

    def _reported_cost_usd(self, usage: JsonObject) -> Optional[float]:
        cost = float_field(usage, "cost")
        return cost if cost is not None and cost >= 0 else None

    async def health_check(self) -> bool:
        healthy = await self._probe(KEY_PATH)
        if not healthy:
            logger.error("OpenRouter health check failed (GET /api/v1/key)")
        return healthy
