"""
The OpenRouter key's spend and limit, from the free `GET /api/v1/key` (P6 M-9, D201).

Every paid P6 step (bench-smoke, calib-pass, bench-lite, record-demo) reads the
key before it spends anything and lowers its own cap to what the key has left
minus a margin:

    effective cap = min(the step's cap, spent so far + limit_remaining - $0.10)

so the key's limit ($8 in P6) ends a step cleanly before it starts, never as a
402 in the middle of a run. A step whose room under that cap is below its
minimum is refused. Steps also end with one `TRUSTAGENT_P6_RESULT {json}` line
(P6 plan section 0.6), which the owner can paste back from their own terminal.

Nothing here prints, logs or returns the key, or the key's OpenRouter label
(it can name the owner): `KeyStatus` keeps only the amounts and the host.

    python -m evals.vendor_key            # "used $0.42 of the $8.00 limit; $7.58 left"
    python -m evals.vendor_key --json     # the same as one JSON object

The key comes from OPENROUTER_API_KEY in the environment or the repository's
.env (the engine's files and precedence); the origin from
TRUTHTABLE_OPENROUTER_BASE_URL (default https://openrouter.ai). A base URL that
points at a stand-in such as mock-upstream is read the same way, and its host is
reported, so a rehearsal never passes for the vendor (D203).

`scripts/record_demo.py` (VEND) imports `read_key_status`, `effective_cap` and
`result_line`.
"""

import argparse
import json
import math
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Dict, Mapping, Optional, Sequence, Tuple

OPENROUTER_ORIGIN = "https://openrouter.ai"
KEY_PATH = "/api/v1/key"
# The default models list returns text-output models only; Jev outputs decisions (Step 0).
MODELS_PATH = "/api/v1/models?output_modalities=all"
KEY_ENV = "OPENROUTER_API_KEY"
BASE_URL_ENV = "TRUTHTABLE_OPENROUTER_BASE_URL"
# D201: a step keeps this much of the key's remaining limit unspent.
LIMIT_MARGIN_USD = 0.10
TIMEOUT_S = 15.0
RESULT_PREFIX = "TRUSTAGENT_P6_RESULT"

# Error kinds of KeyCheckError.
AUTH = "auth"
HTTP = "http"
CONNECTION = "connection"
BAD_RESPONSE = "bad_response"

# fetch(url, headers, timeout_s) -> (status, body). Tests pass a fake; the
# default uses urllib.
Fetch = Callable[[str, Mapping[str, str], float], Tuple[int, bytes]]


class KeyCheckError(RuntimeError):
    """The key check failed. The message never contains the key or its label."""

    def __init__(self, kind: str, message: str) -> None:
        super().__init__(message)
        self.kind = kind


@dataclass(frozen=True)
class KeyStatus:
    """What `GET /api/v1/key` says about the key: amounts and the host only."""

    host: str
    usage_usd: Optional[float]
    limit_usd: Optional[float]  # None: no limit set on the key
    limit_remaining_usd: Optional[float]
    is_free_tier: Optional[bool]

    def to_json(self) -> Dict[str, Any]:
        return {
            "host": self.host,
            "usage_usd": self.usage_usd,
            "limit_usd": self.limit_usd,
            "limit_remaining_usd": self.limit_remaining_usd,
            "is_free_tier": self.is_free_tier,
        }

    def describe(self) -> str:
        """One plain line, e.g. "used $0.42 of the $8.00 limit; $7.58 left"."""
        used = "an unknown amount" if self.usage_usd is None else f"${self.usage_usd:.4f}"
        if self.limit_usd is None:
            return f"used {used}; the key has no limit"
        left = (
            "an unknown amount"
            if self.limit_remaining_usd is None
            else f"${self.limit_remaining_usd:.4f}"
        )
        return f"used {used} of the ${self.limit_usd:.2f} limit; {left} left"


def _urllib_fetch(url: str, headers: Mapping[str, str], timeout_s: float) -> Tuple[int, bytes]:
    scheme = urllib.parse.urlsplit(url).scheme
    if scheme not in ("https", "http"):
        raise KeyCheckError(CONNECTION, f"refusing a {scheme or 'relative'} URL")
    request = urllib.request.Request(url, headers=dict(headers), method="GET")
    try:
        # The scheme is checked above: https for the vendor, http for a local stand-in.
        with urllib.request.urlopen(request, timeout=timeout_s) as response:  # nosec B310
            return int(response.status), response.read()
    except urllib.error.HTTPError as exc:
        return int(exc.code), exc.read() if exc.fp is not None else b""
    except (urllib.error.URLError, OSError) as exc:
        reason = getattr(exc, "reason", exc)
        raise KeyCheckError(CONNECTION, f"{type(exc).__name__}: {reason}") from None


def normalize_origin(base_url: Optional[str]) -> str:
    """The OpenRouter origin of a base URL: no trailing slash and no /api or /api/v1."""
    url = (base_url or OPENROUTER_ORIGIN).strip().rstrip("/")
    for suffix in ("/api/v1", "/api"):
        if url.endswith(suffix):
            url = url[: -len(suffix)]
            break
    return url or OPENROUTER_ORIGIN


def host_of(url: str) -> str:
    """The bare lower-case hostname of a URL ("" when it has none)."""
    return (urllib.parse.urlsplit(url).hostname or "").lower()


def _dotenv(environ: Mapping[str, str]) -> Dict[str, str]:
    """The repository's dotenv values (the engine's files and precedence)."""
    from dotenv import dotenv_values  # a pydantic-settings dependency

    from truthtable.config import env_files

    values: Dict[str, str] = {}
    for path in env_files(environ):
        if path.is_file():
            for name, value in dotenv_values(path).items():
                if value is not None:
                    values[name] = value
    return values


def resolve(environ: Optional[Mapping[str, str]] = None) -> Tuple[Optional[str], str]:
    """
    (the OpenRouter key or None, the OpenRouter origin): the process
    environment wins over the dotenv files, as in the engine.
    """
    env = os.environ if environ is None else environ
    merged = {**_dotenv(env), **env}
    key = (merged.get(KEY_ENV) or "").strip() or None
    return key, normalize_origin(merged.get(BASE_URL_ENV))


def _amount(value: Any) -> Optional[float]:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    number = float(value)
    return number if math.isfinite(number) else None


def read_key_status(
    api_key: str,
    origin: str = OPENROUTER_ORIGIN,
    *,
    fetch: Optional[Fetch] = None,
    timeout_s: float = TIMEOUT_S,
) -> KeyStatus:
    """
    The key's usage and limit (free). Raises KeyCheckError on a refused key,
    an error status, an unreachable host or an unreadable answer.
    """
    if not api_key:
        raise KeyCheckError(AUTH, f"no key: set {KEY_ENV}")
    origin = normalize_origin(origin)
    status, body = (fetch or _urllib_fetch)(
        origin + KEY_PATH, {"Authorization": f"Bearer {api_key}"}, timeout_s
    )
    host = host_of(origin)
    if status in (401, 403):
        raise KeyCheckError(AUTH, f"{host} refused the key (HTTP {status}): check the key")
    if status != 200:
        raise KeyCheckError(HTTP, f"{host} answered the key check with HTTP {status}")
    try:
        document = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise KeyCheckError(BAD_RESPONSE, f"{host} answered the key check with no JSON") from None
    data = document.get("data") if isinstance(document, dict) else None
    if not isinstance(data, dict):
        raise KeyCheckError(BAD_RESPONSE, f"{host} answered the key check without `data`")
    limit = _amount(data.get("limit"))
    remaining = _amount(data.get("limit_remaining"))
    usage = _amount(data.get("usage"))
    if limit is not None and remaining is None and usage is not None:
        remaining = max(0.0, limit - usage)
    free = data.get("is_free_tier")
    return KeyStatus(
        host=host,
        usage_usd=usage,
        limit_usd=limit,
        limit_remaining_usd=remaining,
        is_free_tier=free if isinstance(free, bool) else None,
    )


def model_snapshot(
    model: str,
    origin: str = OPENROUTER_ORIGIN,
    *,
    fetch: Optional[Fetch] = None,
    timeout_s: float = TIMEOUT_S,
) -> Optional[str]:
    """
    The snapshot (canonical slug) OpenRouter lists for `model`, from the free
    models list (no key); None when the model is not listed or the list cannot
    be read.
    """
    try:
        status, body = (fetch or _urllib_fetch)(
            normalize_origin(origin) + MODELS_PATH, {}, timeout_s
        )
        if status != 200:
            return None
        document = json.loads(body.decode("utf-8"))
    except (KeyCheckError, UnicodeDecodeError, json.JSONDecodeError):
        return None
    entries = document.get("data") if isinstance(document, dict) else None
    for entry in entries if isinstance(entries, list) else []:
        if isinstance(entry, dict) and entry.get("id") == model:
            slug = entry.get("canonical_slug")
            return str(slug) if isinstance(slug, str) and slug else None
    return None


@dataclass(frozen=True)
class CapDecision:
    """A step's cap after the key's limit (D201)."""

    requested_usd: float
    cap_usd: float  # The effective total cap (the ledger's spend included)
    spent_usd: float  # Already in the step's ledger
    minimum_usd: float
    lowered: bool
    refused: Optional[str]  # Why the step cannot start, if it cannot

    @property
    def room_usd(self) -> float:
        return max(0.0, self.cap_usd - self.spent_usd)

    def describe(self) -> str:
        line = f"cap ${self.cap_usd:.2f}"
        if self.lowered:
            line += (
                f" (lowered from ${self.requested_usd:.2f}: the key's remaining limit "
                f"minus ${LIMIT_MARGIN_USD:.2f})"
            )
        if self.spent_usd > 0:
            line += f", ${self.spent_usd:.4f} already spent, ${self.room_usd:.4f} left"
        return line


def effective_cap(
    requested_usd: float,
    status: Optional[KeyStatus],
    *,
    spent_usd: float = 0.0,
    minimum_usd: float = 0.0,
    margin_usd: float = LIMIT_MARGIN_USD,
) -> CapDecision:
    """
    The step's cap: `requested_usd`, lowered so that what it may still spend
    (cap - spent) fits in the key's remaining limit minus `margin_usd`.
    Refused when that room is below `minimum_usd`. Without a key status, or
    for a key without a limit, the requested cap stands.
    """
    if requested_usd < 0 or not math.isfinite(requested_usd):
        raise ValueError(f"a cap must be finite and not negative: {requested_usd}")
    cap = float(requested_usd)
    lowered = False
    if status is not None and status.limit_remaining_usd is not None:
        key_room = max(0.0, status.limit_remaining_usd - margin_usd)
        if spent_usd + key_room < cap:
            cap, lowered = spent_usd + key_room, True
    room = max(0.0, cap - spent_usd)
    refused = None
    if room + 1e-12 < minimum_usd:
        refused = (
            f"only ${room:.4f} can be spent (cap ${cap:.2f}, ${spent_usd:.4f} already spent"
            + (
                f", the key has ${status.limit_remaining_usd:.4f} of its limit left"
                if lowered and status is not None and status.limit_remaining_usd is not None
                else ""
            )
            + f"), below this step's minimum ${minimum_usd:.2f}: nothing was spent"
        )
    return CapDecision(
        requested_usd=float(requested_usd),
        cap_usd=round(cap, 6),
        spent_usd=spent_usd,
        minimum_usd=minimum_usd,
        lowered=lowered,
        refused=refused,
    )


def result_line(
    step: str,
    *,
    run_id: Optional[str],
    exit_code: int,
    cost_usd: Optional[float],
    cap_usd: Optional[float],
    key: Optional[KeyStatus],
    label: Optional[str],
    snapshot: Optional[str],
    hosts: Mapping[str, Optional[str]],
    counts: Mapping[str, int],
) -> str:
    """The `TRUSTAGENT_P6_RESULT {json}` line a paid step ends with (P6 plan section 0.6)."""
    document = {
        "step": step,
        "run_id": run_id,
        "exit": exit_code,
        "cost_usd": None if cost_usd is None else round(cost_usd, 6),
        "cap_usd": cap_usd,
        "key_usage_usd": key.usage_usd if key is not None else None,
        "key_limit_remaining_usd": key.limit_remaining_usd if key is not None else None,
        "label": label,
        "snapshot": snapshot,
        "hosts": {name: hosts.get(name) for name in ("decision", "judge", "upstream")},
        "counts": {name: int(counts.get(name, 0)) for name in ("n", "scored", "failed")},
    }
    return f"{RESULT_PREFIX} {json.dumps(document, separators=(',', ':'))}"


def emit_result(line: str, *, journal: Optional[Path] = None) -> None:
    """
    Print a result line, and append it to `journal` (the make targets pass
    tmp/p6/results.log, gitignored), which `make p6-ledger` reads.
    """
    print(line, flush=True)
    if journal is not None:
        journal.parent.mkdir(parents=True, exist_ok=True)
        with open(journal, "a", encoding="utf-8", newline="\n") as f:
            f.write(line + "\n")


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="The OpenRouter key's spend and limit (free)")
    parser.add_argument("--json", action="store_true", help="Print one JSON object")
    args = parser.parse_args(argv)
    key, origin = resolve()
    if key is None:
        print(json.dumps({"key": None}) if args.json else f"no key: {KEY_ENV} is not set")
        return 0
    try:
        status = read_key_status(key, origin)
    except KeyCheckError as exc:
        print(f"key check failed ({exc.kind}): {exc}", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps(status.to_json()))
    else:
        print(f"OpenRouter key at {status.host}: {status.describe()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
