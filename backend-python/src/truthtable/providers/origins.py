"""
Canonical origins (P4-PLAN D78): `scheme://host[:port]` with the scheme and
host lowercased and the scheme's default port left out. Credentials are bound
to origins, never to bare hosts (see truthtable.runtime.origins).
"""

from typing import Dict, Iterable, List, Tuple
from urllib.parse import urlsplit

_DEFAULT_PORTS: Dict[str, int] = {"http": 80, "https": 443}


def canonical_origin(url: str) -> str:
    """
    The canonical origin of an absolute http(s) URL.

    Raises:
        ValueError: If `url` is not an absolute http(s) URL with a host, or has
            userinfo or an invalid port.
    """
    parts = urlsplit(url.strip())
    scheme = parts.scheme.lower()
    if scheme not in _DEFAULT_PORTS:
        raise ValueError(f"not an http(s) URL: {url!r}")
    if parts.username is not None or parts.password is not None:
        raise ValueError("an origin never carries userinfo")
    host = (parts.hostname or "").lower()
    if not host:
        raise ValueError(f"URL without a host: {url!r}")
    port = parts.port  # ValueError for an out-of-range or non-numeric port
    if ":" in host:
        host = f"[{host}]"  # IPv6 literal
    if port is None or port == _DEFAULT_PORTS[scheme]:
        return f"{scheme}://{host}"
    return f"{scheme}://{host}:{port}"


def canonical_origins(urls: Iterable[str]) -> Tuple[List[str], List[str]]:
    """(canonical origins, rejected inputs) of `urls`, de-duplicated in order."""
    origins: List[str] = []
    rejected: List[str] = []
    for url in urls:
        try:
            origin = canonical_origin(url)
        except ValueError:
            rejected.append(url)
            continue
        if origin not in origins:
            origins.append(origin)
    return origins, rejected
