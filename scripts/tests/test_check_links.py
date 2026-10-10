"""Unit tests for scripts/check_links.py (CO-21): relative files, heading anchors, code skipped."""

from __future__ import annotations

import io
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import check_links as cl  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]


class CheckLinksTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(tempfile.mkdtemp(prefix="links-"))
        self.addCleanup(shutil.rmtree, self.root)
        (self.root / "docs" / "images").mkdir(parents=True)
        (self.root / "docs" / "images" / "a.png").write_bytes(b"png")
        (self.root / "docs" / "GUIDE.md").write_text(
            "# Guide\n\n## 2. Modes\n\n## Tier 0: the instant checks\n\n## Notes\n\n## Notes\n\n"
            '<a id="custom-anchor"></a>\n\n```\n## Not a heading\n```\n',
            encoding="utf-8",
        )

    def run_on(self, readme: str) -> tuple[int, str]:
        (self.root / "README.md").write_text(readme, encoding="utf-8")
        out, err = io.StringIO(), io.StringIO()
        code = cl.main(["--root", str(self.root)], out=out, err=err)
        return code, err.getvalue()

    def test_good_links_pass(self) -> None:
        code, err = self.run_on(
            "# Top\n\n[guide](docs/GUIDE.md), [modes](docs/GUIDE.md#2-modes), "
            "[tier](docs/GUIDE.md#tier-0-the-instant-checks), [again](docs/GUIDE.md#notes-1), "
            "[custom](docs/GUIDE.md#custom-anchor), [top](#top), ![img](docs/images/a.png), "
            "[site](https://example.com/x), [mail](mailto:a@b.c)\n"
            '<img src="docs/images/a.png"> <source srcset="docs/images/a.png 2x, docs/images/a.png 1x">\n'
        )
        self.assertEqual(code, 0, err)

    def test_broken_links_are_named_with_their_line(self) -> None:
        code, err = self.run_on(
            "# Top\n\n[missing](docs/NOPE.md)\n[anchor](docs/GUIDE.md#nope)\n[here](#nowhere)\n"
            '<img src="docs/images/b.png">\n[out](../../etc/passwd)\n[code heading](docs/GUIDE.md#not-a-heading)\n'
        )
        self.assertEqual(code, 1)
        self.assertIn("README.md:3: docs/NOPE.md: no such file", err)
        self.assertIn("README.md:4: docs/GUIDE.md#nope: no heading #nope", err)
        self.assertIn("README.md:5: #nowhere: no heading #nowhere", err)
        self.assertIn("README.md:6: docs/images/b.png: no such file", err)
        self.assertIn("README.md:7: ../../etc/passwd leaves the repository", err)
        self.assertIn("README.md:8: docs/GUIDE.md#not-a-heading: no heading", err)

    def test_code_is_not_scanned(self) -> None:
        code, err = self.run_on("# Top\n\n`[x](nope.md)`\n\n```md\n[y](nope.md)\n```\n")
        self.assertEqual(code, 0, err)

    def test_slug_follows_github(self) -> None:
        self.assertEqual(cl.slug("Tier 0: the instant checks"), "tier-0-the-instant-checks")
        self.assertEqual(cl.slug("13. Load test"), "13-load-test")
        self.assertEqual(cl.slug("`make demo` and [links](x.md)"), "make-demo-and-links")

    def test_the_repository_links_resolve(self) -> None:
        out, err = io.StringIO(), io.StringIO()
        self.assertEqual(cl.main([], out=out, err=err), 0, err.getvalue())


if __name__ == "__main__":
    unittest.main()
