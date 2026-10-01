"""
Downloads for the benchmark loaders: HTTPS only, written atomically, hashed.

The SHA-256 of every source file is recorded in the prepared dataset's
manifest, so a run names exactly which bytes it was drawn from even when the
upstream revision is a moving branch.
"""

import hashlib
import json
import os
import tempfile
import urllib.request
from pathlib import Path
from typing import Any, Callable, Mapping, Optional

DOWNLOAD_TIMEOUT_S = 120.0
CHUNK_BYTES = 1 << 20

# (url, headers) -> response body; tests pass a fake instead of the network.
Fetcher = Callable[[str, Mapping[str, str]], bytes]


def _require_https(url: str) -> None:
    if not url.startswith("https://"):
        raise ValueError(f"refusing a non-HTTPS download: {url}")


def http_get(url: str, headers: Mapping[str, str]) -> bytes:
    """GET `url` over HTTPS and return the body."""
    _require_https(url)
    # _require_https above: only https:// URLs are opened.
    request = urllib.request.Request(url, headers=dict(headers))  # nosec B310
    with urllib.request.urlopen(request, timeout=DOWNLOAD_TIMEOUT_S) as response:  # nosec B310
        body: bytes = response.read()
    return body


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(CHUNK_BYTES), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_atomic(path: Path, data: bytes) -> None:
    """Write `data` to `path` via a temporary file in the same directory."""
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
        os.replace(tmp, path)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def download(
    url: str,
    dest: Path,
    *,
    fetch: Optional[Fetcher] = None,
    headers: Optional[Mapping[str, str]] = None,
    refresh: bool = False,
) -> str:
    """
    Download `url` to `dest` unless it is already there (or `refresh`), and
    return the file's SHA-256.
    """
    _require_https(url)
    if refresh or not dest.exists():
        body = (fetch or http_get)(url, dict(headers or {}))
        write_atomic(dest, body)
    return sha256_file(dest)


def get_json(
    url: str, *, fetch: Optional[Fetcher] = None, headers: Optional[Mapping[str, str]] = None
) -> Any:
    """GET `url` and parse the JSON body."""
    _require_https(url)
    return json.loads((fetch or http_get)(url, dict(headers or {})).decode("utf-8"))


def combined_digest(hashes: Mapping[str, str]) -> str:
    """One digest over named file hashes (sorted by name)."""
    digest = hashlib.sha256()
    for name in sorted(hashes):
        digest.update(f"{name}\0{hashes[name]}\n".encode())
    return digest.hexdigest()


def digest_rows(rows: Any) -> str:
    """SHA-256 of rows as canonical JSON."""
    canonical = json.dumps(rows, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()
