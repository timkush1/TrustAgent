"""Unit tests for scripts/public_demo.py (docker and the edge are faked, except one local server)."""

from __future__ import annotations

import http.server
import io
import json
import os
import stat
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from typing import Dict, List, Mapping, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import public_demo  # noqa: E402
from public_demo import Check, Response, UrlError  # noqa: E402

URL = "https://203-0-113-10.sslip.io"
TRUSTED = "TRUTHTABLE_TRUSTED_PROXIES"


class CheckUrlTest(unittest.TestCase):
    def test_accepts_a_bare_https_origin_and_the_local_rehearsal(self) -> None:
        for value, expected in (
            (URL, ("public", URL)),
            ("https://demo.trustagent.dev", ("public", "https://demo.trustagent.dev")),
            ("http://localhost:28443", ("local", "http://localhost:28443")),
        ):
            self.assertEqual(public_demo.check_url(value), expected, value)

    def test_refuses_what_caddy_or_lets_encrypt_cannot_serve(self) -> None:
        cases = {
            None: "is not set",
            "": "is not set",
            "   ": "is not set",
            f" {URL}": "remove the spaces",
            f"{URL}\n": "remove the spaces",
            "http://demo.trustagent.dev": "not an https address",
            "ftp://demo.trustagent.dev": "not an https address",
            "demo.trustagent.dev": "not an https address",
            # The registry's default: a public host must not keep it.
            "http://localhost:3000": "the only local address is http://localhost:28443",
            "http://127.0.0.1:28443": "the only local address",
            "http://localhost:28443/": "drop the trailing slash",
            f"{URL}/": "no path, query or trailing slash",
            f"{URL}/app": "no path, query or trailing slash",
            f"{URL}?a=1": "no path, query or trailing slash",
            f"{URL}#x": "no path, query or trailing slash",
            f"{URL}:8443": "remove the port",
            f"{URL}:443": "remove the port",
            f"{URL}:abc": "not a number",
            "https://admin@demo.trustagent.dev": "remove the user name",
            "https://203.0.113.10": "not an IP address",
            "https://[2001:db8::1]": "not an IP address",
            "https://localhost": "not a public name",
            "https://intranet": "not a public name",
            "https://demo.local": "not a public name",
            "https://demo.internal": "not a public name",
            "https://Demo.TrustAgent.dev": "exactly as https://demo.trustagent.dev",
            "HTTPS://demo.trustagent.dev": "exactly as https://demo.trustagent.dev",
            "https://-demo.trustagent.dev": "not a valid host name",
            "https://demo_1.trustagent.dev": "not a valid host name",
            "https://demo..trustagent.dev": "not a valid host name",
        }
        for value, reason in cases.items():
            with self.assertRaises(UrlError, msg=value) as caught:
                public_demo.check_url(value)
            self.assertIn(reason, str(caught.exception), value)

    def test_an_ip_address_gets_its_sslip_name_as_the_fix(self) -> None:
        with self.assertRaises(UrlError) as caught:
            public_demo.check_url("https://203.0.113.10")
        self.assertIn("set-url --sslip 203.0.113.10", str(caught.exception))

    def test_sslip_names_a_public_ipv4_address(self) -> None:
        self.assertEqual(public_demo.sslip_url("95.216.10.20"), "https://95-216-10-20.sslip.io")
        self.assertEqual(public_demo.sslip_url(" 49.12.3.4 "), "https://49-12-3-4.sslip.io")
        # Private, loopback, shared and documentation ranges never reach the VM from outside.
        for address in (
            "10.0.0.5",
            "192.168.1.2",
            "127.0.0.1",
            "172.18.0.4",
            "100.64.0.1",
            "203.0.113.10",
        ):
            with self.assertRaises(UrlError, msg=address) as caught:
                public_demo.sslip_url(address)
            self.assertIn("not a public address", str(caught.exception))
        for address in ("2001:db8::1", "demo", "1.2.3"):
            with self.assertRaises(UrlError, msg=address) as caught:
                public_demo.sslip_url(address)
            self.assertIn("public IPv4 address", str(caught.exception))


class EnvFileCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.env = Path(self.tmp.name) / ".env"
        self.out = io.StringIO()
        self.err = io.StringIO()

    def main(self, argv: Sequence[str], environ: Optional[Mapping[str, str]] = None) -> int:
        return public_demo.main(
            argv,
            environ={} if environ is None else environ,
            env_file=self.env,
            run=FakeDocker({}),
            fetch=FakeEdge(),
            sleep=lambda _: None,
            out=self.out,
            err=self.err,
        )


class UrlCommandTest(EnvFileCase):
    def test_prints_the_mode_from_env_then_dotenv(self) -> None:
        self.env.write_text(f"TRUTHTABLE_PUBLIC_URL={URL}\n", encoding="utf-8")
        self.assertEqual(self.main(["url"]), 0)
        self.assertEqual(self.out.getvalue(), "public\n")
        self.out.truncate(0)
        self.out.seek(0)
        # The process environment wins over .env, as in compose.
        local = {"TRUTHTABLE_PUBLIC_URL": "http://localhost:28443"}
        self.assertEqual(self.main(["url"], local), 0)
        self.assertEqual(self.out.getvalue(), "local\n")

    def test_a_missing_or_bad_url_exits_2_with_the_fix(self) -> None:
        self.assertEqual(self.main(["url"]), 2)
        self.assertEqual(self.out.getvalue(), "")
        self.assertIn("set-url --sslip", self.err.getvalue())
        self.assertIn("docs/DEPLOY-DEMO.md", self.err.getvalue())
        self.assertEqual(self.main(["url"], {"TRUTHTABLE_PUBLIC_URL": "http://x.y"}), 2)

    def test_verify_refuses_a_bad_url_before_touching_docker(self) -> None:
        docker = FakeDocker({})
        code = public_demo.main(
            ["verify"], environ={}, env_file=self.env, run=docker, out=self.out, err=self.err
        )
        self.assertEqual(code, 2)
        self.assertEqual(docker.calls, [])


class SetUrlTest(EnvFileCase):
    def test_writes_the_url_and_keeps_every_other_line(self) -> None:
        self.env.write_text("# generated\nPOSTGRES_PASSWORD=x\nTRUTHTABLE_PUBLIC_URL=old\n")
        self.assertEqual(self.main(["set-url", "--sslip", "95.216.10.20"]), 0)
        url = "https://95-216-10-20.sslip.io"
        self.assertEqual(
            self.env.read_text(encoding="utf-8"),
            f"# generated\nPOSTGRES_PASSWORD=x\nTRUTHTABLE_PUBLIC_URL={url}\n",
        )
        if os.name != "nt":
            self.assertEqual(stat.S_IMODE(self.env.stat().st_mode), 0o600)
        self.assertIn(f"TRUTHTABLE_PUBLIC_URL={url} written to .env", self.out.getvalue())

    def test_appends_to_a_new_env_file(self) -> None:
        self.assertEqual(self.main(["set-url", "https://demo.trustagent.dev"]), 0)
        self.assertEqual(
            self.env.read_text(encoding="utf-8"),
            "TRUTHTABLE_PUBLIC_URL=https://demo.trustagent.dev\n",
        )

    def test_a_bad_address_writes_nothing(self) -> None:
        self.assertEqual(self.main(["set-url", "http://demo.trustagent.dev"]), 2)
        self.assertEqual(self.main(["set-url", "--sslip", "10.0.0.1"]), 2)
        self.assertFalse(self.env.exists())

    def test_warns_when_the_shell_overrides_dotenv(self) -> None:
        shell = {"TRUTHTABLE_PUBLIC_URL": "https://other.trustagent.dev"}
        self.assertEqual(self.main(["set-url", URL], shell), 0)
        self.assertIn("also set in this shell", self.err.getvalue())

    def test_url_and_sslip_are_exclusive(self) -> None:
        with self.assertRaises(SystemExit) as caught, _quiet_stderr():
            self.main(["set-url", URL, "--sslip", "203.0.113.10"])
        self.assertEqual(caught.exception.code, 2)


# ---------- verify ----------


class FakeDocker:
    """`docker ps` finds ``containers`` (service -> id); `docker inspect` answers per format."""

    def __init__(
        self,
        containers: Mapping[str, str],
        caddy_ips: str = "172.18.0.6 ",
        trusted: Optional[str] = "172.16.0.0/12",
    ) -> None:
        self.containers = dict(containers)
        self.caddy_ips = caddy_ips
        self.trusted = trusted
        self.calls: List[List[str]] = []

    def __call__(self, argv: Sequence[str], timeout: float) -> Tuple[int, str, str]:
        argv = list(argv)
        self.calls.append(argv)
        if argv[:2] == ["docker", "ps"]:
            service = next(
                a.split("=", 2)[2]
                for a in argv
                if a.startswith("label=com.docker.compose.service=")
            )
            found = self.containers.get(service)
            return 0, (found + "\n") if found else "", ""
        if argv[:2] == ["docker", "inspect"]:
            fmt, container = argv[3], argv[4]
            if "NetworkSettings" in fmt:
                assert container == self.containers["caddy"]
                return 0, self.caddy_ips + "\n", ""
            assert container == self.containers["proxy"]
            line = f"{TRUSTED}={self.trusted}" if self.trusted is not None else ""
            return 0, line + "\n", ""
        raise AssertionError(f"unexpected command {argv}")


RUNNING = {"caddy": "c4ddy", "proxy": "pr0xy"}


def status_body(**overrides: object) -> bytes:
    body: Dict[str, object] = {
        "setup_required": True,
        "demo": True,
        "readonly": True,
        "public_url": URL,
        "persistence": True,
    }
    body.update(overrides)
    return json.dumps(body).encode("utf-8")


class FakeEdge:
    """The public demo's edge as seen from the VM; ``answers`` override paths (full URLs)."""

    def __init__(self, base: str = URL, answers: Optional[Dict[str, Response]] = None) -> None:
        host = base.split("://", 1)[1]
        self.answers: Dict[str, Response] = {
            f"{base}/api/setup/status": Response(
                200, {"content-type": "application/json"}, status_body(public_url=base)
            ),
            f"{base}/v1/models": Response(
                404,
                {"content-type": "text/plain; charset=utf-8"},
                b"Not found: the public demo does not serve the gateway API (/v1).",
            ),
            f"{base}/": Response(200, {"content-type": "text/html"}, b"<!doctype html>"),
        }
        if base.startswith("https://"):
            self.answers[f"http://{host}/"] = Response(308, {"location": f"https://{host}/"})
        self.answers.update(answers or {})
        self.urls: List[str] = []

    def __call__(self, url: str) -> Response:
        self.urls.append(url)
        return self.answers.get(url, Response(0, error="connection refused"))


def by_id(checks: Sequence[Check]) -> Dict[str, Check]:
    return {c.id: c for c in checks}


class CaddyTrustedTest(unittest.TestCase):
    def test_passes_when_caddy_is_inside_the_trusted_networks(self) -> None:
        for trusted in ("172.16.0.0/12", "10.0.0.0/8, 172.18.0.0/16", "172.18.0.6"):
            check = public_demo.check_caddy_trusted("p", FakeDocker(RUNNING, trusted=trusted))
            self.assertTrue(check.ok, (trusted, check))
            self.assertIn("172.18.0.6", check.summary)

    def test_fails_when_visitors_would_share_caddys_address(self) -> None:
        for trusted in ("10.0.0.0/8", "", None, "not-a-cidr"):
            docker = FakeDocker(RUNNING, caddy_ips="192.168.32.4 ", trusted=trusted)
            check = public_demo.check_caddy_trusted("truthtable-demo", docker)
            self.assertFalse(check.ok, trusted)
            self.assertIn("every visitor would share", check.summary)
            self.assertIn("docker network inspect truthtable-demo_default", check.fix)

    def test_reads_only_the_one_variable_from_the_proxy(self) -> None:
        docker = FakeDocker(RUNNING)
        public_demo.check_caddy_trusted("p", docker)
        inspect = [c for c in docker.calls if c[:2] == ["docker", "inspect"]]
        self.assertEqual(len(inspect), 2)
        proxy_format = inspect[1][3]
        self.assertIn(f'"{TRUSTED}"', proxy_format)
        self.assertNotIn("json", proxy_format)
        for call in docker.calls[:2]:
            self.assertIn("label=com.docker.compose.project=p", call)

    def test_fails_when_a_container_is_not_running(self) -> None:
        check = public_demo.check_caddy_trusted("p", FakeDocker({"proxy": "pr0xy"}))
        self.assertFalse(check.ok)
        self.assertEqual(check.summary, "caddy not running in project p")
        check = public_demo.check_caddy_trusted("p", FakeDocker(RUNNING, caddy_ips=""))
        self.assertFalse(check.ok)
        self.assertIn("unknown", check.summary)


class HttpChecksTest(unittest.TestCase):
    def test_a_healthy_public_edge(self) -> None:
        edge = FakeEdge()
        checks = by_id(public_demo.http_checks("public", URL, edge))
        self.assertEqual(
            sorted(checks),
            ["edge.dashboard", "edge.gateway_closed", "edge.http_redirect", "edge.setup_status"],
        )
        self.assertTrue(all(c.ok for c in checks.values()), checks)
        self.assertIn("no admin yet", checks["edge.setup_status"].summary)

    def test_the_local_rehearsal_has_no_redirect_check(self) -> None:
        local = "http://localhost:28443"
        checks = by_id(public_demo.http_checks("local", local, FakeEdge(local)))
        self.assertNotIn("edge.http_redirect", checks)
        self.assertTrue(all(c.ok for c in checks.values()), checks)

    def test_setup_status_needs_demo_read_only_and_this_url(self) -> None:
        for body, reason in (
            (status_body(readonly=False), "read-only is off"),
            (status_body(demo=False), "demo mode is off"),
            (status_body(public_url="http://localhost:3000"), "public_url is"),
            (b"<html>", "did not answer JSON"),
            (b"[]", "did not answer an object"),
        ):
            edge = FakeEdge(answers={f"{URL}/api/setup/status": Response(200, {}, body)})
            check = public_demo.check_setup_status(URL, edge)
            self.assertFalse(check.ok, reason)
            self.assertIn(reason, check.summary)
        edge = FakeEdge(answers={f"{URL}/api/setup/status": Response(502)})
        self.assertIn("HTTP 502", public_demo.check_setup_status(URL, edge).summary)
        edge = FakeEdge(answers={f"{URL}/api/setup/status": Response(0, error="timed out")})
        self.assertIn("no answer (timed out)", public_demo.check_setup_status(URL, edge).summary)
        ok = FakeEdge(
            answers={
                f"{URL}/api/setup/status": Response(200, {}, status_body(setup_required=False))
            }
        )
        self.assertIn("an admin exists", public_demo.check_setup_status(URL, ok).summary)

    def test_the_gateway_must_be_closed_by_the_edge_itself(self) -> None:
        for answer in (
            Response(401, {}, b'{"error":{"code":"unauthorized"}}'),
            Response(404, {}, b'{"error":{"code":"not_found"}}'),  # the proxy's 404, not the edge's
            Response(200, {}, b"{}"),
        ):
            edge = FakeEdge(answers={f"{URL}/v1/models": answer})
            check = public_demo.check_gateway_closed(URL, edge)
            self.assertFalse(check.ok, answer)
            self.assertIn("the gateway API is reachable", check.summary)

    def test_the_dashboard_must_answer_html(self) -> None:
        edge = FakeEdge(answers={f"{URL}/": Response(200, {"content-type": "application/json"})})
        self.assertFalse(public_demo.check_dashboard(URL, edge).ok)
        edge = FakeEdge(answers={f"{URL}/": Response(502, {"content-type": "text/html"})})
        self.assertFalse(public_demo.check_dashboard(URL, edge).ok)

    def test_port_80_must_redirect_to_this_origin(self) -> None:
        host = "http://203-0-113-10.sslip.io/"
        for answer in (
            Response(0, error="connection refused"),
            Response(200, {"content-type": "text/html"}),
            Response(308, {"location": "https://elsewhere.example.org/"}),
        ):
            edge = FakeEdge(answers={host: answer})
            check = public_demo.check_http_redirect(URL, edge)
            self.assertFalse(check.ok, answer)
            self.assertIn("firewall", check.fix)


class Clock:
    def __init__(self) -> None:
        self.now = 0.0
        self.sleeps: List[float] = []

    def __call__(self) -> float:
        return self.now

    def sleep(self, seconds: float) -> None:
        self.sleeps.append(seconds)
        self.now += seconds


class VerifyTest(unittest.TestCase):
    def test_retries_until_the_certificate_is_ready(self) -> None:
        edge = FakeEdge()
        healthy = dict(edge.answers)
        edge.answers.clear()  # every request fails until the edge comes up
        clock = Clock()

        def fetch(url: str) -> Response:
            if len(clock.sleeps) >= 3:
                edge.answers.update(healthy)
            return edge(url)

        checks = public_demo.verify(
            "p", "public", URL, FakeDocker(RUNNING), fetch, 60, clock.sleep, clock
        )
        self.assertTrue(all(c.ok for c in checks), checks)
        self.assertEqual(clock.sleeps, [public_demo.RETRY_EVERY_S] * 3)

    def test_gives_up_at_the_timeout_with_the_last_answers(self) -> None:
        clock = Clock()
        checks = public_demo.verify(
            "p",
            "public",
            URL,
            FakeDocker(RUNNING),
            FakeEdge(answers={f"{URL}/": Response(502)}),
            12,
            clock.sleep,
            clock,
        )
        self.assertFalse(by_id(checks)["edge.dashboard"].ok)
        self.assertTrue(by_id(checks)["edge.setup_status"].ok)
        self.assertLessEqual(clock.now, 12)
        self.assertEqual(len(clock.sleeps), 2)

    def test_main_reports_and_exits_0_or_1(self) -> None:
        out = io.StringIO()
        environ = {"TRUTHTABLE_PUBLIC_URL": URL}
        clock = Clock()
        code = public_demo.main(
            ["verify", "--project", "truthtable-demo", "--timeout", "0"],
            environ=environ,
            env_file=Path(tempfile.gettempdir()) / "trustagent-test-public-demo-no-such.env",
            run=FakeDocker(RUNNING),
            fetch=FakeEdge(),
            sleep=clock.sleep,
            clock=clock,
            out=out,
        )
        self.assertEqual(code, 0, out.getvalue())
        lines = out.getvalue().splitlines()
        self.assertEqual([line.split()[0] for line in lines[:5]], ["PASS"] * 5)
        self.assertIn(f"public demo: {URL} (DEMO MODE: simulated checks, read-only", lines[5])
        self.assertIn("from another network", lines[5])

        out = io.StringIO()
        code = public_demo.main(
            ["verify", "--timeout", "0"],
            environ=environ,
            env_file=Path(tempfile.gettempdir()) / "trustagent-test-public-demo-no-such.env",
            run=FakeDocker(RUNNING, caddy_ips="192.168.0.9"),
            fetch=FakeEdge(),
            sleep=clock.sleep,
            clock=clock,
            out=out,
        )
        self.assertEqual(code, 1)
        self.assertIn("FAIL  edge.caddy_trusted", out.getvalue())
        self.assertIn("fix: set TRUTHTABLE_TRUSTED_PROXIES", out.getvalue())
        self.assertIn("a check failed", out.getvalue())


# ---------- http_fetch against a real local server ----------


class _Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self) -> None:  # noqa: N802 - the stdlib's name
        if self.path == "/redirect":
            self.send_response(308)
            self.send_header("Location", "https://example.org/")
            self.end_headers()
            return
        if self.path == "/missing":
            body = b"Not found: the public demo does not serve the gateway API (/v1)."
            self.send_response(404)
        else:
            body = b"<!doctype html><title>TrustAgent</title>"
            self.send_response(200)
        self.send_header("Content-Type", "text/html" if self.path == "/" else "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args: object) -> None:
        pass


class HttpFetchTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        cls.base = f"http://127.0.0.1:{cls.server.server_address[1]}"
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls) -> None:
        cls.server.shutdown()
        cls.server.server_close()

    def test_answers_are_results_and_redirects_are_not_followed(self) -> None:
        page = public_demo.http_fetch(self.base + "/")
        self.assertEqual((page.status, page.headers["content-type"]), (200, "text/html"))
        self.assertIn(b"TrustAgent", page.body)
        missing = public_demo.http_fetch(self.base + "/missing")
        self.assertEqual(missing.status, 404)
        self.assertIn(public_demo.EDGE_404_TEXT.encode(), missing.body)
        moved = public_demo.http_fetch(self.base + "/redirect")
        self.assertEqual((moved.status, moved.headers["location"]), (308, "https://example.org/"))

    def test_no_answer_is_status_0(self) -> None:
        closed = http.server.HTTPServer(("127.0.0.1", 0), _Handler)
        port = closed.server_address[1]
        closed.server_close()
        answer = public_demo.http_fetch(f"http://127.0.0.1:{port}/", timeout=2)
        self.assertEqual(answer.status, 0)
        self.assertTrue(answer.error)
        self.assertEqual(public_demo.http_fetch("file:///etc/passwd").status, 0)


class _quiet_stderr:
    def __enter__(self) -> None:
        self.saved = sys.stderr
        sys.stderr = io.StringIO()

    def __exit__(self, *exc: object) -> None:
        sys.stderr = self.saved


if __name__ == "__main__":
    unittest.main()
