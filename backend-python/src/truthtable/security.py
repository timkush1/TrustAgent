"""
Input sanitization and strict LLM-output parsing.

Audited text (user queries, LLM responses, uploaded documents) is untrusted:
it may contain prompt-injection payloads, hidden characters, or adversarially
malformed "JSON". These helpers enforce two rules everywhere:

1. Untrusted text entering a prompt is sanitized (hidden/control characters
   stripped, length capped) and must be wrapped in delimiter tags the system
   prompt declares to be data, never instructions.
2. LLM output never flows onward without strict schema validation - a model
   that was successfully injected produces output that fails validation and
   degrades safely instead of corrupting the audit.
"""

import json
import re
from typing import Any, Optional

# NOTE: character ranges below use \uXXXX escapes, never literal characters —
# literal bidi/zero-width characters in source are themselves a Trojan-Source
# vector (bandit B613).

# Zero-width and bidi-control characters used to hide injection payloads,
# plus C0/C1 control characters (except \n, \r, \t which are legitimate).
_HIDDEN_CHARS = re.compile(
    "["
    "\u200b-\u200f"  # zero-width space/joiners, LRM/RLM
    "\u202a-\u202e"  # bidi embedding/override
    "\u2060-\u2064"  # word joiner, invisible operators
    "\ufeff"  # BOM / zero-width no-break space
    "\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f"  # control chars
    "]"
)

# ```json ... ``` fences that models love to wrap JSON in.
_CODE_FENCE = re.compile(r"^```[a-zA-Z]*\s*|\s*```$")

# Default cap for any single untrusted text entering a prompt.
MAX_PROMPT_TEXT_CHARS = 8000


def sanitize_text(text: str, max_chars: int = MAX_PROMPT_TEXT_CHARS) -> str:
    """Strip hidden/control characters and cap length before prompt insertion."""
    cleaned = _HIDDEN_CHARS.sub("", text)
    if len(cleaned) > max_chars:
        cleaned = cleaned[:max_chars] + " [truncated]"
    return cleaned


def strip_code_fences(text: str) -> str:
    """Remove markdown code fences around an expected-JSON response."""
    return _CODE_FENCE.sub("", text.strip()).strip()


def parse_json_strict(text: str) -> Optional[Any]:
    """Parse LLM output as JSON, tolerating only code fences. None on failure."""
    try:
        return json.loads(strip_code_fences(text))
    except (json.JSONDecodeError, TypeError):
        return None


def validate_claims(parsed: Any, max_claims: int = 20, max_claim_chars: int = 500) -> list[str]:
    """
    Validate decomposer output: must be a JSON array of non-trivial strings.

    Raises ValueError on schema violations (wrong type); silently drops
    individual items that are empty/too short, and caps count and length.
    """
    if not isinstance(parsed, list):
        raise ValueError("Expected a JSON array of claims")

    claims = []
    for item in parsed:
        if not isinstance(item, str):
            raise ValueError("Claims must be strings")
        item = item.strip()
        if len(item) > 5:
            claims.append(item[:max_claim_chars])
        if len(claims) >= max_claims:
            break
    return claims


def validate_verdict(parsed: Any, allowed_statuses: set[str]) -> dict:
    """
    Validate verifier output against the expected schema:
        {"status": <allowed>, "confidence": float, "evidence": [str, ...]}

    Raises ValueError on any violation; clamps confidence to [0, 1] and caps
    evidence list size/length.
    """
    if not isinstance(parsed, dict):
        raise ValueError("Expected a JSON object")

    status = parsed.get("status")
    if not isinstance(status, str) or status.upper() not in allowed_statuses:
        raise ValueError(f"Invalid status: {status!r}")

    try:
        confidence = float(parsed.get("confidence", 0.5))
    except (TypeError, ValueError) as e:
        raise ValueError(f"Invalid confidence: {parsed.get('confidence')!r}") from e
    confidence = min(max(confidence, 0.0), 1.0)

    evidence_raw = parsed.get("evidence", [])
    if not isinstance(evidence_raw, list):
        raise ValueError("Evidence must be a list")
    evidence = [str(item)[:500] for item in evidence_raw[:10]]

    return {"status": status.upper(), "confidence": confidence, "evidence": evidence}
