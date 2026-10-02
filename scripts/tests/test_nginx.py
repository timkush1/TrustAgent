"""Static checks of the dashboard's nginx templates (frontend-react/nginx/templates).

The Connect OpenRouter callback carries a one-time sign-in code in its query string, so nothing
nginx writes may keep it: the callback location logs nothing, and the page it serves sends no
Referer (which the default access-log format records for the next /api request). Standard library
only; the templates are read as text.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path
from typing import Dict

ROOT = Path(__file__).resolve().parents[2]
TEMPLATES = ROOT / "frontend-react" / "nginx" / "templates"
DEFAULT = (TEMPLATES / "default.conf.template").read_text(encoding="utf-8")
HEADERS = (TEMPLATES / "snippets" / "security-headers.conf.template").read_text(encoding="utf-8")


def locations(conf: str) -> Dict[str, str]:
    """Each `location <match> { ... }` block's body, keyed by its match (`= /path`, `/api/`)."""
    found: Dict[str, str] = {}
    for match in re.finditer(r"(?m)^\s*location\s+([^{]+?)\s*\{", conf):
        depth, start = 1, match.end()
        for index in range(start, len(conf)):
            if conf[index] == "{":
                depth += 1
            elif conf[index] == "}":
                depth -= 1
                if depth == 0:
                    found[match.group(1)] = conf[start:index]
                    break
    return found


def directives(body: str) -> list[str]:
    """The directives of a location body, comments left out, whitespace collapsed."""
    lines = [re.sub(r"#.*", "", line).strip() for line in body.splitlines()]
    return [re.sub(r"\s+", " ", line) for line in lines if line]


class NginxTemplateTest(unittest.TestCase):
    def test_the_connect_callback_is_never_logged(self) -> None:
        blocks = locations(DEFAULT)
        self.assertIn("= /connect/callback", blocks)
        body = directives(blocks["= /connect/callback"])
        self.assertIn("access_log off;", body)
        # It is the single-page app, served like `location /`, with the security headers.
        self.assertIn("try_files /index.html =404;", body)
        self.assertIn("include /etc/nginx/conf.d/snippets/security-headers.conf;", body)
        self.assertIn('add_header Cache-Control "no-store" always;', body)
        self.assertFalse(any(d.startswith("proxy_pass") for d in body))

    def test_the_callback_page_sends_no_referer(self) -> None:
        self.assertRegex(HEADERS, r'(?m)^add_header Referrer-Policy "no-referrer" always;$')

    def test_the_exact_match_wins_over_the_app_fallback(self) -> None:
        # `location = ...` is matched before any prefix location, so `location /` (which logs)
        # can never serve the callback; and no regex location could take it either.
        blocks = locations(DEFAULT)
        self.assertIn("/", blocks)
        regexes = [m for m in blocks if m.startswith("~")]
        for match in regexes:
            pattern = match.lstrip("~*").strip()
            self.assertIsNone(re.search(pattern, "/connect/callback"), match)

    def test_the_parser(self) -> None:
        conf = "server {\n  location = /a { x on; }\n  location /b/ {\n    if ($y) { z; }\n  }\n}\n"
        self.assertEqual(locations(conf), {"= /a": " x on; ", "/b/": "\n    if ($y) { z; }\n  "})


if __name__ == "__main__":
    unittest.main()
