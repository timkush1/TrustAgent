"""Static checks of trivy-secret.yaml: what Trivy's secret scan allows stays narrow.

The "Trivy (filesystem scan)" job (.github/workflows/security.yml) points TRIVY_SECRET_CONFIG at
this file. These tests pin it without the Trivy binary, the way test_gitleaks.py pins
.gitleaks.toml: no built-in rule or built-in allow rule is switched off, and the only path allowed
is the detector vectors'. The file is read as text (its layout is regular), so the tests stay
standard library only.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path
from typing import List

ROOT = Path(__file__).resolve().parents[2]
CONFIG = (ROOT / "trivy-secret.yaml").read_text(encoding="utf-8")
WORKFLOW = (ROOT / ".github" / "workflows" / "security.yml").read_text(encoding="utf-8")


def code(text: str) -> List[str]:
    """The lines that are not comments or blank."""
    return [
        line for line in text.splitlines() if line.strip() and not line.lstrip().startswith("#")
    ]


def allowed_paths() -> List[str]:
    """The `path:` regex of every allow rule, unquoted."""
    return [
        m.group(1).strip().strip("'\"")
        for line in code(CONFIG)
        if (m := re.match(r"^\s+path:\s*(.+)$", line))
    ]


class TrivySecretConfigTest(unittest.TestCase):
    def test_only_allow_rules_are_configured(self) -> None:
        # No `rules`, `disable-rules`, `disable-allow-rules` or `enable-builtin-rules`: the built-in
        # rules and the built-in allow rules stay exactly as Trivy ships them.
        top_level = [line.split(":", 1)[0] for line in code(CONFIG) if not line.startswith(" ")]
        self.assertEqual(top_level, ["allow-rules"])
        self.assertEqual(len(re.findall(r"(?m)^  - id: ", CONFIG)), 1, "a single allow rule")
        self.assertNotRegex(
            CONFIG, r"(?m)^\s+regex:", "an allow rule by match would apply anywhere"
        )

    def test_only_the_detector_vectors_are_allowed(self) -> None:
        self.assertEqual(allowed_paths(), [r"^contracts/vectors/detectors/[^/]+\.json$"])
        (pattern,) = allowed_paths()
        for path in (
            "contracts/vectors/detectors/secret-aws-and-github-tokens-in-text-order.json",
            "contracts/vectors/detectors/secret-only-the-pasted-match-is-excused.json",
        ):
            self.assertRegex(path, pattern)
        for path in (
            "backend-go/cmd/proxy/main.go",
            "contracts/vectors/gate/secret.json",
            "contracts/vectors/detectors/nested/key.json",
            "contracts/vectors/detectors/secret.json.bak",
            "docs/contracts/vectors/detectors/secret.json",
            ".env.example",
            "scripts/doctor.py",
        ):
            self.assertNotRegex(path, pattern, f"{path} would be allowed")

    def test_the_workflow_scans_secrets_with_this_config(self) -> None:
        job = re.split(r"\n  [a-z-]+:\n", WORKFLOW.split("\n  trivy:\n", 1)[1], maxsplit=1)[0]
        self.assertIn("name: Trivy (filesystem scan)", job)
        self.assertRegex(job, r"(?m)^\s+TRIVY_SECRET_CONFIG: trivy-secret\.yaml$")
        # The secret scanner stays on: no input narrows the scanners or skips files or folders.
        for narrowing in ("scanners:", "skip-files:", "skip-dirs:", "TRIVY_SCANNERS", "TRIVY_SKIP"):
            self.assertNotIn(narrowing, "\n".join(code(job)), narrowing)


if __name__ == "__main__":
    unittest.main()
