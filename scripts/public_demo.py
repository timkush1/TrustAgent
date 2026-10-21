#!/usr/bin/env python3
"""Checks for the always-on public demo (D218; docs/DEPLOY-DEMO.md). Standard library only.

    python3 scripts/public_demo.py url                     # check TRUTHTABLE_PUBLIC_URL
    python3 scripts/public_demo.py set-url https://demo.example.org
    python3 scripts/public_demo.py set-url --sslip 203.0.113.10   # https://203-0-113-10.sslip.io
    python3 scripts/public_demo.py verify --project truthtable-demo

``url`` reads TRUTHTABLE_PUBLIC_URL the way compose does (a non-empty process variable, then
``.env``) and accepts two forms:

- ``https://<host>``: a DNS name with nothing after it (no IP address, port, path, query, user or
  trailing slash). Caddy serves that site and gets a Let's Encrypt certificate for it. Prints
  ``public``.
- ``http://localhost:28443``: the local rehearsal (deploy/public-demo/local.yml, plain HTTP on
  127.0.0.1 only). Prints ``local``.

Anything else exits 2 with the reason and the fix. ``make public-demo-up`` runs it first and adds
local.yml for ``local``.

``set-url`` checks an address the same way and writes it to ``.env`` (mode 0600, every other line
kept). ``--sslip IP`` builds the free sslip.io name of a public IPv4 address.

``verify`` runs after ``up --wait``, from the VM itself:

  edge.caddy_trusted    Caddy's address on the compose network is inside the proxy's
                        TRUTHTABLE_TRUSTED_PROXIES, so the proxy counts each visitor's own address
                        (X-Forwarded-For) for its per-IP caps instead of Caddy's
  edge.setup_status     GET <url>/api/setup/status through Caddy reaches the proxy: demo mode and
                        read-only on, public_url is <url>
  edge.gateway_closed   GET <url>/v1/models answers the edge's 404
  edge.dashboard        GET <url>/ is the dashboard's page
  edge.http_redirect    (https only) http://<host>/ redirects to https://<host>/: port 80 is open,
                        which Let's Encrypt renewals need

The HTTP checks are retried until ``--timeout`` (Caddy may still be getting its certificate). A
firewall outside the VM is not seen from the VM: open the URL from another network as well.

Exit codes: 0 every check passed; 1 a check failed; 2 a usage error or a TRUTHTABLE_PUBLIC_URL
that cannot work.
"""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Dict, List, Mapping, Optional, Sequence, TextIO, Tuple, Union

sys.path.insert(0, str(Path(__file__).resolve().parent))
from envfile import DEFAULT_ENV_FILE, EnvFile, resolve  # noqa: E402
from setup_url import Runner, run_command  # noqa: E402

KEY = "TRUTHTABLE_PUBLIC_URL"
TRUSTED_KEY = "TRUTHTABLE_TRUSTED_PROXIES"
# deploy/public-demo/local.yml publishes this port on 127.0.0.1, and Caddy serves this site.
LOCAL_URL = "http://localhost:28443"
EXAMPLE_URL = "https://203-0-113-10.sslip.io"
DOCS = "docs/DEPLOY-DEMO.md"
# The edge's own answer on /v1 (deploy/public-demo/Caddyfile), told apart from the proxy's 404.
EDGE_404_TEXT = "does not serve the gateway API"
# Host names Let's Encrypt never certifies, or that only resolve on this machine.
LOCAL_SUFFIXES = (".localhost", ".local", ".internal", ".test", ".example", ".invalid", ".lan")
LABEL = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?")
DEFAULT_TIMEOUT_S = 180.0
RETRY_EVERY_S = 5.0
HTTP_TIMEOUT_S = 10.0
MAX_BODY = 64 * 1024


class UrlError(ValueError):
    """TRUTHTABLE_PUBLIC_URL cannot serve the public demo; the message says why and what to do."""


def check_url(value: Optional[str]) -> Tuple[str, str]:
    """``(mode, url)`` for a usable TRUTHTABLE_PUBLIC_URL: mode ``public`` or ``local``."""
    if value is None or not value.strip():
        raise UrlError(
            f"{KEY} is not set. Put the demo's https address in .env, for example "
            f"`python3 scripts/public_demo.py set-url --sslip <the VM's IPv4 address>` "
            f"({EXAMPLE_URL}); see {DOCS}."
        )
    url = value
    if url != url.strip():
        raise UrlError(f"{KEY}={url!r}: remove the spaces around the address.")
    if url == LOCAL_URL:
        return "local", url
    if url.rstrip("/") == LOCAL_URL:
        raise UrlError(f"{KEY}={url}: drop the trailing slash ({LOCAL_URL}).")
    parts = urllib.parse.urlsplit(url)
    if parts.scheme == "http" and (parts.hostname or "").startswith(("localhost", "127.")):
        raise UrlError(
            f"{KEY}={url}: the only local address is {LOCAL_URL} (the rehearsal's port in "
            "deploy/public-demo/local.yml)."
        )
    if parts.scheme != "https":
        raise UrlError(
            f"{KEY}={url} is not an https address. Visitors reach the demo over HTTPS: "
            f"https://<host>, for example {EXAMPLE_URL}."
        )
    if parts.username is not None or parts.password is not None:
        raise UrlError(f"{KEY}={url}: remove the user name from the address.")
    try:
        port = parts.port
    except ValueError:
        raise UrlError(f"{KEY}={url}: the port is not a number.") from None
    if port is not None:
        raise UrlError(
            f"{KEY}={url}: remove the port. Caddy serves 443 (and 80 for the redirect and "
            "certificate renewals) and nothing else."
        )
    if parts.path or parts.query or parts.fragment:
        raise UrlError(
            f"{KEY}={url}: use the bare origin https://<host>, with no path, query or trailing "
            "slash (Caddy would serve only that path)."
        )
    origin = f"https://{check_host(parts.netloc.lower(), url)}"
    if url != origin:
        # Caddy, the proxy and the browser's Origin header must all see the same string.
        raise UrlError(f"{KEY}={url}: write it in lowercase, exactly as {origin}.")
    return "public", origin


def check_host(host: str, url: str) -> str:
    """A DNS name Let's Encrypt can certify, else UrlError."""
    candidate = host[1:-1] if host.startswith("[") and host.endswith("]") else host
    try:
        ipaddress.ip_address(candidate)
    except ValueError:
        pass
    else:
        raise UrlError(
            f"{KEY}={url}: use a host name, not an IP address. A free name for an IPv4 address "
            f"is <the address with dashes>.sslip.io: `python3 scripts/public_demo.py set-url "
            f"--sslip {candidate}`."
        )
    if host == "localhost" or "." not in host or host.endswith(LOCAL_SUFFIXES):
        raise UrlError(
            f"{KEY}={url}: {host} is not a public name, so Let's Encrypt cannot certify it. Use "
            f"your domain or <the VM's IPv4 address with dashes>.sslip.io."
        )
    labels = host.split(".")
    if len(host) > 253 or not all(LABEL.fullmatch(label) for label in labels):
        raise UrlError(f"{KEY}={url}: {host} is not a valid host name.")
    return host


def sslip_url(address: str) -> str:
    """``https://a-b-c-d.sslip.io`` for a public IPv4 address, else UrlError."""
    try:
        ip = ipaddress.IPv4Address(address.strip())
    except ValueError:
        raise UrlError(
            f"--sslip {address}: give the VM's public IPv4 address, e.g. 203.0.113.10."
        ) from None
    if not ip.is_global:
        raise UrlError(
            f"--sslip {address}: {ip} is not a public address. Use the IPv4 address your cloud "
            "provider shows for the VM."
        )
    return f"https://{str(ip).replace('.', '-')}.sslip.io"


# ---------- verify ----------


@dataclass
class Check:
    """One verify result."""

    id: str
    ok: bool
    summary: str
    fix: str = ""


@dataclass
class Response:
    """An HTTP answer (redirects are not followed). ``status`` 0 means no answer: see ``error``."""

    status: int
    headers: Dict[str, str] = field(default_factory=dict)
    body: bytes = b""
    error: str = ""


Fetch = Callable[[str], Response]


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """A 3xx answer is the result (edge.http_redirect reads its Location), not a hop."""

    def redirect_request(
        self, req: object, fp: object, code: int, msg: str, headers: object, newurl: str
    ) -> None:
        return None


def http_fetch(url: str, timeout: float = HTTP_TIMEOUT_S) -> Response:
    """GET an http(s) ``url`` without following redirects; certificates are verified."""
    if urllib.parse.urlsplit(url).scheme not in ("http", "https"):
        return Response(0, error=f"not an http(s) URL: {url}")
    opener = urllib.request.build_opener(_NoRedirect())
    request = urllib.request.Request(url, headers={"User-Agent": "trustagent-public-demo-verify"})
    try:
        with opener.open(request, timeout=timeout) as answer:
            headers = {k.lower(): v for k, v in answer.headers.items()}
            return Response(answer.status, headers, answer.read(MAX_BODY))
    except urllib.error.HTTPError as exc:
        try:
            headers = {k.lower(): v for k, v in (exc.headers or {}).items()}
            return Response(exc.code, headers, exc.read(MAX_BODY) if exc.fp else b"")
        finally:
            exc.close()
    except (urllib.error.URLError, OSError, ValueError) as exc:
        reason = getattr(exc, "reason", exc)
        return Response(0, error=str(reason) or type(exc).__name__)


def _answer(r: Response) -> str:
    return f"no answer ({r.error})" if r.status == 0 else f"HTTP {r.status}"


def check_setup_status(url: str, fetch: Fetch) -> Check:
    """The proxy answers through the edge, in demo and read-only mode, for this URL."""
    cid = "edge.setup_status"
    r = fetch(url + "/api/setup/status")
    logs = "make public-demo-logs SERVICE=caddy (and SERVICE=proxy)"
    if r.status != 200:
        return Check(cid, False, f"GET /api/setup/status: {_answer(r)}", logs)
    try:
        body = json.loads(r.body.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        return Check(cid, False, "GET /api/setup/status did not answer JSON: not the proxy", logs)
    if not isinstance(body, dict):
        return Check(cid, False, "GET /api/setup/status did not answer an object", logs)
    problems = []
    if body.get("demo") is not True:
        problems.append("demo mode is off (docker-compose.demo.yml missing?)")
    if body.get("readonly") is not True:
        problems.append("read-only is off (docker-compose.public.yml missing?)")
    if body.get("public_url") != url:
        problems.append(f"the proxy's public_url is {body.get('public_url')!r}, not {url!r}")
    if problems:
        return Check(cid, False, "; ".join(problems), "make public-demo-up")
    admin = (
        "no admin yet (make public-demo-setup-url mints the one-time link)"
        if body.get("setup_required")
        else "an admin exists"
    )
    return Check(cid, True, f"the proxy answers through the edge: demo, read-only; {admin}")


def check_gateway_closed(url: str, fetch: Fetch) -> Check:
    cid = "edge.gateway_closed"
    r = fetch(url + "/v1/models")
    if r.status == 404 and EDGE_404_TEXT in r.body.decode("utf-8", "replace"):
        return Check(cid, True, "/v1 answers 404 at the edge")
    return Check(
        cid,
        False,
        f"GET /v1/models: {_answer(r)}, not the edge's 404: the gateway API is reachable",
        "check that deploy/public-demo/Caddyfile is the one mounted (make public-demo-up)",
    )


def check_dashboard(url: str, fetch: Fetch) -> Check:
    cid = "edge.dashboard"
    r = fetch(url + "/")
    kind = r.headers.get("content-type", "")
    if r.status == 200 and kind.startswith("text/html"):
        return Check(cid, True, "the dashboard answers through the edge")
    return Check(
        cid,
        False,
        f"GET /: {_answer(r)} {kind}".rstrip(),
        "make public-demo-logs SERVICE=dashboard",
    )


def check_http_redirect(url: str, fetch: Fetch) -> Check:
    cid = "edge.http_redirect"
    host = urllib.parse.urlsplit(url).netloc
    r = fetch(f"http://{host}/")
    location = r.headers.get("location", "")
    if r.status in (301, 302, 307, 308) and location.startswith(url + "/"):
        return Check(cid, True, "port 80 redirects to https (Let's Encrypt renewals use it)")
    return Check(
        cid,
        False,
        f"GET http://{host}/: {_answer(r)}, not a redirect to {url}/",
        "open ports 80 and 443 (TCP) and 443 (UDP) in the VM's firewall",
    )


def http_checks(mode: str, url: str, fetch: Fetch) -> List[Check]:
    checks = [
        check_setup_status(url, fetch),
        check_gateway_closed(url, fetch),
        check_dashboard(url, fetch),
    ]
    if mode == "public":
        checks.append(check_http_redirect(url, fetch))
    return checks


def _container(project: str, service: str, run: Runner) -> Optional[str]:
    code, out, _ = run(
        [
            "docker",
            "ps",
            "--quiet",
            "--filter",
            f"label=com.docker.compose.project={project}",
            "--filter",
            f"label=com.docker.compose.service={service}",
            "--filter",
            "status=running",
        ],
        20,
    )
    ids = out.split() if code == 0 else []
    return ids[0] if ids else None


Network = Union[ipaddress.IPv4Network, ipaddress.IPv6Network]


def parse_networks(value: str) -> List[Network]:
    """TRUTHTABLE_TRUSTED_PROXIES as networks (CIDRs or single addresses, comma-separated)."""
    networks: List[Network] = []
    for item in value.replace(" ", ",").split(","):
        if item:
            try:
                networks.append(ipaddress.ip_network(item, strict=False))
            except ValueError:
                continue
    return networks


def check_caddy_trusted(project: str, run: Runner) -> Check:
    """Caddy's address on the compose network is one the proxy trusts X-Forwarded-For from."""
    cid = "edge.caddy_trusted"
    caddy = _container(project, "caddy", run)
    proxy = _container(project, "proxy", run)
    if caddy is None or proxy is None:
        missing = " and ".join(n for n, c in (("caddy", caddy), ("proxy", proxy)) if c is None)
        return Check(
            cid, False, f"{missing} not running in project {project}", "make public-demo-up"
        )
    code, out, err = run(
        [
            "docker",
            "inspect",
            "--format",
            "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}",
            caddy,
        ],
        20,
    )
    addresses = []
    for word in out.split() if code == 0 else []:
        try:
            addresses.append(ipaddress.ip_address(word))
        except ValueError:
            continue
    if not addresses:
        return Check(cid, False, f"Caddy's address is unknown ({err.strip() or out.strip()})")
    # Only this one variable leaves docker: the proxy's environment also holds keys.
    code, out, err = run(
        [
            "docker",
            "inspect",
            "--format",
            '{{range .Config.Env}}{{if eq (index (split . "=") 0) "'
            + TRUSTED_KEY
            + '"}}{{.}}{{end}}{{end}}',
            proxy,
        ],
        20,
    )
    line = out.strip() if code == 0 else ""
    trusted = line.split("=", 1)[1] if line.startswith(TRUSTED_KEY + "=") else ""
    networks = parse_networks(trusted)
    shown = ", ".join(str(a) for a in addresses)
    if any(a in n for a in addresses for n in networks):
        return Check(cid, True, f"the proxy trusts Caddy ({shown} in {trusted})")
    return Check(
        cid,
        False,
        f"Caddy ({shown}) is outside {TRUSTED_KEY} ({trusted or 'empty'}): every visitor would "
        "share Caddy's address and its per-IP caps",
        f"set {TRUSTED_KEY} in .env to the compose network's subnet (docker network inspect "
        f"{project}_default), then make public-demo-up",
    )


def verify(
    project: str,
    mode: str,
    url: str,
    run: Runner,
    fetch: Fetch,
    timeout: float,
    sleep: Callable[[float], None] = time.sleep,
    clock: Callable[[], float] = time.monotonic,
) -> List[Check]:
    """Every check; the HTTP ones are retried until they pass or ``timeout`` runs out."""
    checks = [check_caddy_trusted(project, run)]
    deadline = clock() + timeout
    while True:
        found = http_checks(mode, url, fetch)
        if all(c.ok for c in found) or clock() + RETRY_EVERY_S > deadline:
            return checks + found
        sleep(RETRY_EVERY_S)


def report(checks: Sequence[Check], url: str, mode: str, out: TextIO) -> int:
    width = max(len(c.id) for c in checks)
    for c in checks:
        print(f"{'PASS' if c.ok else 'FAIL'}  {c.id:<{width}}  {c.summary}", file=out)
        if not c.ok and c.fix:
            print(f"      {'':<{width}}  fix: {c.fix}", file=out)
    if not all(c.ok for c in checks):
        print(f"public demo: a check failed ({DOCS}, Troubleshooting)", file=out)
        return 1
    if mode == "public":
        print(
            f"public demo: {url} (DEMO MODE: simulated checks, read-only, no keys). These checks "
            "ran on this machine: open the address from another network too (a phone on mobile "
            "data), which also proves the firewall.",
            file=out,
        )
    else:
        print(f"public demo (local rehearsal): {url}", file=out)
    return 0


# ---------- command line ----------


def main(
    argv: Optional[Sequence[str]] = None,
    environ: Optional[Mapping[str, str]] = None,
    env_file: Optional[Path] = None,
    run: Runner = run_command,
    fetch: Fetch = http_fetch,
    sleep: Callable[[float], None] = time.sleep,
    clock: Callable[[], float] = time.monotonic,
    out: TextIO = sys.stdout,
    err: TextIO = sys.stderr,
) -> int:
    parser = argparse.ArgumentParser(
        description="Checks for the always-on public demo (docs/DEPLOY-DEMO.md)."
    )
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("url", help=f"check {KEY}; print public or local")
    set_url = sub.add_parser("set-url", help=f"check an address and write it to .env as {KEY}")
    given = set_url.add_mutually_exclusive_group(required=True)
    given.add_argument("url", nargs="?", help="https://<host>")
    given.add_argument("--sslip", metavar="IPV4", help="use https://<IPV4 with dashes>.sslip.io")
    check = sub.add_parser("verify", help="check the running public demo through its edge")
    check.add_argument("--project", default="truthtable-demo", help="compose project")
    check.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_S,
        help=f"seconds the HTTP checks are retried (default {DEFAULT_TIMEOUT_S:.0f})",
    )
    args = parser.parse_args(argv)
    path = env_file or DEFAULT_ENV_FILE

    try:
        if args.command == "set-url":
            _, url = check_url(sslip_url(args.sslip) if args.sslip else args.url)
            env = EnvFile.load(path)
            env.set(KEY, url)
            env.write()
            print(f"{KEY}={url} written to {path.name}", file=out)
            shell = (os.environ if environ is None else environ).get(KEY)
            if shell and shell != url:
                print(
                    f"note: {KEY} is also set in this shell ({shell}), which wins over .env: "
                    f"unset {KEY}",
                    file=err,
                )
            return 0
        mode, url = check_url(resolve(KEY, env_file=path, environ=environ))
    except UrlError as exc:
        print(f"public demo: {exc}", file=err)
        return 2

    if args.command == "url":
        print(mode, file=out)
        return 0
    checks = verify(args.project, mode, url, run, fetch, args.timeout, sleep, clock)
    return report(checks, url, mode, out)


if __name__ == "__main__":
    sys.exit(main())
