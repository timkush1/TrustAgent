"""
Origin binding of judge credentials (SPEC section 7.5, P4-PLAN D34 and D78).

A credential may only be sent to its allowed origins. Origins are canonical
`scheme://host[:port]`: the scheme and host lowercased, the default port of the
scheme left out, never a bare host. The gateway pushes each credential with
its allowed origins; the engine intersects them with its own table, so a push
can narrow where a secret goes but never widen it:

| Provider   | Engine origins                                                  |
|------------|-----------------------------------------------------------------|
| openrouter | https://openrouter.ai, plus the origin of TRUTHTABLE_OPENROUTER_BASE_URL |
| anthropic  | https://api.anthropic.com                                       |
| openai     | https://api.openai.com                                          |

Every other provider takes no pushed credential.
"""

from typing import Dict, FrozenSet, Tuple

from ..config import Settings
from ..providers.anthropic import ANTHROPIC_BASE_URL
from ..providers.openai import OPENAI_BASE_URL
from ..providers.origins import canonical_origin, canonical_origins
from ..providers.openrouter import OPENROUTER_BASE_URL, normalize_base_url

__all__ = [
    "VENDOR_ORIGINS",
    "canonical_origin",
    "canonical_origins",
    "engine_origins",
    "provider_base_url",
]

# The engine's own vendor table (the origins a keyed judge may call).
VENDOR_ORIGINS: Dict[str, Tuple[str, ...]] = {
    "openrouter": ("https://openrouter.ai",),
    "anthropic": ("https://api.anthropic.com",),
    "openai": ("https://api.openai.com",),
}


def provider_base_url(provider: str, settings: Settings) -> str:
    """The API root the engine calls for a keyed provider (never a pushed URL)."""
    if provider == "openrouter":
        return normalize_base_url(settings.openrouter_base_url or OPENROUTER_BASE_URL)
    if provider == "anthropic":
        return ANTHROPIC_BASE_URL
    if provider == "openai":
        return OPENAI_BASE_URL
    raise ValueError(f"{provider!r} takes no pushed credential")


def engine_origins(provider: str, settings: Settings) -> FrozenSet[str]:
    """The engine's own allowed origins for `provider` (empty when it takes no credential)."""
    origins = set(VENDOR_ORIGINS.get(provider, ()))
    if provider == "openrouter" and settings.openrouter_base_url:
        try:
            origins.add(canonical_origin(settings.openrouter_base_url))
        except ValueError:
            pass  # config validation already rejects a malformed URL
    return frozenset(origins)
