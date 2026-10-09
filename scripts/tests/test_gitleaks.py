"""Static checks of .gitleaks.toml: what the secret scan allows stays narrow.

The "gitleaks (secrets)" job (.github/workflows/security.yml) runs gitleaks over the whole history
with this file. These tests pin its allow-lists without the gitleaks binary: the default rules stay
on, only the test paths and the detector vectors are allowed by path, and the regex allow-lists
accept the project's fake-key conventions, in test files only, but never a realistic key. Standard
library only (tomllib, Python 3.11).
"""

from __future__ import annotations

import re
import secrets
import string
import tomllib
import unittest
from pathlib import Path
from typing import Any, Dict, List, Optional

ROOT = Path(__file__).resolve().parents[2]
CONFIG: Dict[str, Any] = tomllib.loads((ROOT / ".gitleaks.toml").read_text(encoding="utf-8"))
WORKFLOW = (ROOT / ".github" / "workflows" / "security.yml").read_text(encoding="utf-8")


def allowlists() -> List[Dict[str, Any]]:
    return list(CONFIG.get("allowlists") or [])


def allowed(target: str, text: str, path: Optional[str] = None) -> bool:
    """Whether a regex allow-list accepts `text` found in `path` (None: a path no list names).

    An entry with paths and condition AND needs both its regex and one of its paths to match.
    """
    for entry in allowlists():
        if entry.get("regexTarget", "secret") != target:
            continue
        if not any(re.search(pattern, text) for pattern in entry.get("regexes") or []):
            continue
        paths = entry.get("paths") or []
        if not paths or entry.get("condition", "OR") != "AND":
            return True
        if path is not None and any(re.search(p, path) for p in paths):
            return True
    return False


def realistic(prefix: str, length: int) -> str:
    """A random key-shaped string (not a real key) of the given vendor prefix."""
    alphabet = string.ascii_letters + string.digits
    return prefix + "".join(secrets.choice(alphabet) for _ in range(length))


class GitleaksConfigTest(unittest.TestCase):
    def test_the_default_rules_stay_on(self) -> None:
        self.assertIs(CONFIG["extend"]["useDefault"], True)
        self.assertNotIn("rules", CONFIG, "no rule is redefined or switched off here")

    def test_only_tests_and_detector_vectors_are_allowed_by_path(self) -> None:
        unconditional = [
            path
            for entry in allowlists()
            if entry.get("condition", "OR") == "OR"
            for path in entry.get("paths") or []
        ]
        self.assertEqual(
            sorted(unconditional),
            sorted(
                [
                    r"_test\.go$",
                    r"(^|/)tests/unit/",
                    r"^frontend-react/e2e/",
                    r"^contracts/vectors/detectors/",
                ]
            ),
        )
        for path in unconditional:
            for source in ("backend-go/cmd/proxy/main.go", "scripts/doctor.py", ".env.example"):
                self.assertIsNone(re.search(path, source), f"{path} would allow {source}")

    def test_commit_scoped_entries_need_both_the_commit_and_the_path(self) -> None:
        for entry in allowlists():
            if entry.get("commits"):
                self.assertEqual(entry.get("condition"), "AND", entry["description"])
                self.assertTrue(entry.get("paths"), entry["description"])
                for commit in entry["commits"]:
                    self.assertRegex(commit, r"^[0-9a-f]{40}$")

    def test_fake_key_conventions_pass_in_tests_only_and_realistic_keys_never(self) -> None:
        tests = (
            "frontend-react/src/data/control/securityPlane.test.ts",
            "frontend-react/src/components/common/composites.test.tsx",
            "scripts/tests/test_service_env.py",
        )
        elsewhere = (
            "frontend-react/src/data/control/securityPlane.ts",
            "scripts/doctor.py",
            "docs/SECURITY.md",
            ".env.example",
            None,
        )
        for fake in (
            "sk-test-0123456789abcdef",
            "sk-or-v1-e2e-nocredits-0123456789",
            "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm",
            "tk_live_x",
        ):
            for path in tests:
                self.assertTrue(allowed("secret", fake, path), f"{fake} in {path}")
            for other in elsewhere:
                self.assertFalse(allowed("secret", fake, other), f"{fake} would pass in {other}")
        for real in (
            "sk-or-v1-" + secrets.token_hex(32),
            realistic("sk-ant-api03-", 93),
            realistic("sk-proj-", 48),
            "tk_live_" + secrets.token_hex(16),
            # A fake prefix inside a longer value is not a fake key.
            "xsk-test-" + secrets.token_hex(8),
        ):
            for path in tests + elsewhere:
                self.assertFalse(allowed("secret", real, path), "a realistic key would be allowed")

    def test_calibration_digests_pass_and_keys_next_to_them_do_not(self) -> None:
        digest = secrets.token_hex(32)
        for line in (
            f'  "calibration_key": "{digest}",',
            f"    calibration_key: '{digest}',",
        ):
            self.assertTrue(allowed("line", line), line)
        for line in (
            f'  "api_key": "{digest}",',
            f'  "calibration_key": "sk-or-v1-{digest}",',
            f'  "calibration_key": "{digest.upper()}",',
        ):
            self.assertFalse(allowed("line", line), line)

    def test_the_workflow_scans_the_whole_history_with_this_config(self) -> None:
        # The job runs up to the next job (a line indented by exactly two spaces).
        job = re.split(r"\n  [a-z-]+:\n", WORKFLOW.split("\n  gitleaks:\n", 1)[1], maxsplit=1)[0]
        self.assertIn("name: gitleaks (secrets)", job)
        self.assertIn("fetch-depth: 0", job)
        self.assertIn("sha256sum -c", job)
        self.assertRegex(job, r"gitleaks git --config \.gitleaks\.toml --redact")


if __name__ == "__main__":
    unittest.main()
