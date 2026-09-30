"""Unit tests for scripts/setup_url.py (docker is faked; no browser is launched)."""

from __future__ import annotations

import io
import sys
import unittest
from pathlib import Path
from typing import Dict, List, Mapping, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import setup_url  # noqa: E402

URL = "http://localhost:3000/app/setup#token=abc_DEF-123"
VALID = (
    "The setup URL is valid until 2026-10-01T12:00:00Z and works once; it creates a new admin key."
)
ADMIN_EXISTS = (
    "An admin credential already exists: sign in with it, or run with -force to mint a "
    "recovery token."
)


class FakeDocker:
    """`docker ps` finds the proxies of ``running``; `docker exec` answers ``exec_result``."""

    def __init__(
        self,
        running: Mapping[str, str],
        exec_result: Tuple[int, str, str] = (0, f"SETUP: open {URL}\n", VALID + "\n"),
    ) -> None:
        self.running = dict(running)
        self.exec_result = exec_result
        self.calls: List[List[str]] = []

    def __call__(self, argv: Sequence[str], timeout: float) -> Tuple[int, str, str]:
        argv = list(argv)
        self.calls.append(argv)
        if argv[:2] == ["docker", "ps"]:
            project = next(
                a.split("=", 2)[2]
                for a in argv
                if a.startswith("label=com.docker.compose.project=")
            )
            container = self.running.get(project)
            return 0, (container + "\n") if container else "", ""
        if argv[:2] == ["docker", "exec"]:
            return self.exec_result
        raise AssertionError(f"unexpected command {argv}")

    def execs(self) -> List[List[str]]:
        return [c for c in self.calls if c[:2] == ["docker", "exec"]]


class Opener:
    def __init__(self, works: bool = True) -> None:
        self.works = works
        self.opened: List[str] = []

    def __call__(self, url: str) -> bool:
        self.opened.append(url)
        return self.works


def run_main(
    args: Sequence[str],
    docker: FakeDocker,
    opener: Optional[Opener] = None,
    env: Optional[Dict[str, str]] = None,
) -> Tuple[int, str, str, Opener]:
    opener = opener or Opener()
    out, err = io.StringIO(), io.StringIO()
    code = setup_url.main(
        list(args), run=docker, opener=opener, environ=env or {}, stdout=out, stderr=err
    )
    return code, out.getvalue(), err.getvalue(), opener


class ParseTest(unittest.TestCase):
    def test_reads_the_first_setup_line(self) -> None:
        stdout = f"noise\n  SETUP: open {URL}  \nSETUP: open http://other\n"
        self.assertEqual(setup_url.setup_line_url(stdout), URL)

    def test_no_setup_line(self) -> None:
        self.assertIsNone(setup_url.setup_line_url("SETUP: open \nsomething else\n"))

    def test_default_projects_start_with_the_up_project(self) -> None:
        self.assertEqual(
            setup_url.default_projects({}),
            ["truthtable", "truthtable-demo", "truthtable-e2e"],
        )
        self.assertEqual(
            setup_url.default_projects({"COMPOSE_PROJECT_NAME": "truthtable-dx"}),
            ["truthtable-dx", "truthtable", "truthtable-demo", "truthtable-e2e"],
        )


class MintTest(unittest.TestCase):
    def test_setup_url_uses_the_running_stack(self) -> None:
        # Only the demo runs: `make setup-url` finds it instead of the make-up project.
        docker = FakeDocker({"truthtable-demo": "c0ffee"})
        code, out, err, _ = run_main([], docker)
        self.assertEqual(code, 0)
        self.assertEqual(out.splitlines()[0], f"SETUP: open {URL}")
        self.assertIn("valid until", out)
        self.assertEqual(
            docker.execs(), [["docker", "exec", "c0ffee", "/proxy", "-new-setup-token"]]
        )

    def test_the_first_listed_project_wins(self) -> None:
        docker = FakeDocker({"truthtable": "up1", "truthtable-demo": "demo1"})
        run_main([], docker)
        self.assertEqual(docker.execs()[0][2], "up1")

    def test_url_only_prints_the_url_alone(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"})
        code, out, err, _ = run_main(["--url-only", "--project", "truthtable-demo"], docker)
        self.assertEqual((code, out), (0, URL + "\n"))
        self.assertIn("valid until", err)

    def test_force_mints_a_recovery_token(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"})
        run_main(["--project", "truthtable-demo", "--force"], docker)
        self.assertEqual(docker.execs()[0][-2:], ["-new-setup-token", "-force"])

    def test_no_running_proxy_is_a_usage_error(self) -> None:
        docker = FakeDocker({})
        code, out, err, _ = run_main(["--url-only", "--project", "truthtable-demo"], docker)
        self.assertEqual((code, out), (2, ""))
        self.assertIn("no running TrustAgent proxy", err)
        self.assertIn("make demo", err)

    def test_admin_exists_fails_without_open(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"}, (1, "", ADMIN_EXISTS + "\n"))
        code, out, err, _ = run_main(["--url-only", "--project", "truthtable-demo"], docker)
        self.assertEqual((code, out), (1, ""))
        self.assertIn("an admin already exists", err)
        self.assertIn("SETUP_FORCE=1", err)


class OpenTest(unittest.TestCase):
    ARGS = ["--open", "--project", "truthtable-demo", "--dashboard-url", "http://localhost:3000"]

    def test_opens_the_setup_link(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"})
        code, out, _, opener = run_main(self.ARGS, docker)
        self.assertEqual(code, 0)
        self.assertEqual(opener.opened, [URL])
        self.assertIn(f"Setup (one-time link: it creates your admin key): {URL}", out)

    def test_print_only_and_ci_never_open(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"})
        _, out, _, opener = run_main(self.ARGS + ["--print-only"], docker)
        self.assertEqual(opener.opened, [])
        self.assertIn(URL, out)
        _, _, _, opener = run_main(self.ARGS, docker, env={"CI": "true"})
        self.assertEqual(opener.opened, [])

    def test_opens_the_dashboard_when_an_admin_exists(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"}, (1, "", ADMIN_EXISTS))
        code, out, _, opener = run_main(self.ARGS, docker)
        self.assertEqual(code, 0)
        self.assertEqual(opener.opened, ["http://localhost:3000"])
        self.assertIn("an admin already exists", out)
        self.assertIn("Dashboard: http://localhost:3000", out)

    def test_opens_the_dashboard_when_minting_fails(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"}, (1, "", "the database cannot be opened"))
        code, out, _, opener = run_main(self.ARGS, docker)
        self.assertEqual(code, 0)
        self.assertIn("no setup link was minted: the database cannot be opened", out)
        self.assertEqual(opener.opened, ["http://localhost:3000"])

    def test_a_failed_launcher_is_reported_not_fatal(self) -> None:
        docker = FakeDocker({"truthtable-demo": "c0ffee"})
        code, out, _, _ = run_main(self.ARGS, docker, opener=Opener(works=False))
        self.assertEqual(code, 0)
        self.assertIn("open the URL above manually", out)


if __name__ == "__main__":
    unittest.main()
