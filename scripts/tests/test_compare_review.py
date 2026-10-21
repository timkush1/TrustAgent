"""Unit tests for scripts/compare_review.py (S-5): canned pages, no network."""

from __future__ import annotations

import contextlib
import io
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Dict, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import compare_review as cr  # noqa: E402

DAY = "2026-10-21"
COMPARE = {
    "schema": "trustagent.compare/v1",
    "columns": [
        {"id": "inline", "label": "Runs inline"},
        {"id": "self_hosted", "label": "Self-hosted"},
    ],
    "rows": [
        {
            "id": "observability_judge",
            "name": "LLM-as-judge in observability tools",
            "cells": {
                "inline": {
                    "value": "no",
                    "vendor": "Langfuse",
                    "text": "Judges run asynchronously in its own worker on ingested traces.",
                    "source_url": "https://docs.example.com/judge",
                    "as_of": "2026-10-10",
                    "reviewed": False,
                },
                "self_hosted": {
                    "value": "yes",
                    "vendor": "Langfuse",
                    "text": "Open source and self-hosted with Docker.",
                    "source_url": "https://docs.example.com/gone",
                    "as_of": "2026-10-10",
                    "reviewed": False,
                },
            },
        }
    ],
}
CLAIMS = {
    "claims": [
        {
            "id": "kept",
            "display": "95.5%",
            "source_url": "https://vendor.example.com/post",
            "reverify": "check",
        },
        {
            "id": "lost",
            "display": "171 ms median",
            "source_url": "https://vendor.example.com/post",
            "reverify": "check",
        },
        {
            "id": "plain",
            "display": "$0.05 / 1,000",
            "source_url": "https://vendor.example.com/post",
        },
    ]
}
PAGES: Dict[str, Tuple[str, str]] = {
    "https://docs.example.com/judge": (
        "200",
        "<html><head><style>p{}</style><script>var x = 'Judges run inline';</script></head><body>"
        "<nav>Docs Pricing</nav><h1>LLM as a judge</h1><p>Set up an evaluator in minutes.</p>"
        "<p>Evaluators run asynchronously in a separate worker on ingested traces, so nothing "
        "runs in your application &amp; nothing slows it down.</p></body></html>",
    ),
    "https://docs.example.com/gone": ("404", ""),
    "https://vendor.example.com/post": (
        "200",
        "<p>On our faithfulness test Jev scored 95.5% accuracy overall.</p>",
    ),
}


def make_root() -> Path:
    root = Path(tempfile.mkdtemp(prefix="compare-review-"))
    (root / "contracts" / "claims").mkdir(parents=True)
    (root / cr.COMPARE_PATH).write_text(json.dumps(COMPARE), encoding="utf-8")
    (root / cr.CLAIMS_PATH).write_text(json.dumps(CLAIMS), encoding="utf-8")
    return root


class TextTest(unittest.TestCase):
    def test_visible_text_drops_scripts_styles_and_tags(self) -> None:
        text = cr.visible_text(PAGES["https://docs.example.com/judge"][1])
        self.assertNotIn("var x", text)
        self.assertNotIn("p{}", text)
        self.assertNotIn("<", text)
        self.assertIn("nothing runs in your application & nothing slows it down", text)

    def test_the_best_sentence_shares_the_most_words(self) -> None:
        text = cr.visible_text(PAGES["https://docs.example.com/judge"][1])
        found, score = cr.best_sentence(
            "Judges run asynchronously in its own worker on ingested traces.", text
        )
        self.assertTrue(
            found and found.startswith("Evaluators run asynchronously in a separate worker")
        )
        self.assertGreater(score, 0.6)
        self.assertEqual(cr.best_sentence("", text), (None, 0.0))

    def test_figures(self) -> None:
        self.assertEqual(cr.figures("95.5%"), ["95.5%"])
        self.assertEqual(cr.figures("171 ms median"), ["171"])
        self.assertEqual(cr.figures("$14.30 / 1,000"), ["$14.30", "1,000"])

    def test_only_https_is_fetched(self) -> None:
        self.assertEqual(cr.http_fetch("http://example.com/")[0], "error: not an https URL")


class ReviewTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = make_root()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.calls: list = []

    def fetch(self, url: str) -> Tuple[str, str]:
        self.calls.append(url)
        return PAGES[url]

    def run_main(self, *argv: str) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = cr.main(list(argv), root=self.root, fetcher=self.fetch, today=DAY)
        return code, out.getvalue(), err.getvalue()

    def test_the_table_quotes_the_source_and_the_outputs_follow(self) -> None:
        code, out, _ = self.run_main("--fetch")
        self.assertEqual(code, 1, "a 404 source and an unconfirmed claim fail the check")
        self.assertEqual(sorted(self.calls), sorted(PAGES))
        self.assertIn("| LLM-as-judge in observability tools: Runs inline | no · Langfuse |", out)
        self.assertIn("“Evaluators run asynchronously in a separate worker on ingested traces", out)
        self.assertIn("(the page could not be read)", out)
        self.assertIn("| kept | 95.5% | https://vendor.example.com/post | confirmed |", out)
        self.assertIn(
            "| lost | 171 ms median | https://vendor.example.com/post | not found on its source (171) |",
            out,
        )
        self.assertNotIn("| plain |", out, "only claims marked reverify")

        out_dir = self.root / cr.DEFAULT_DIR
        rows = (out_dir / "compare-fetch.tsv").read_text(encoding="utf-8").splitlines()
        self.assertEqual(rows[0], "cell\turl\thttp_status\tdate")
        self.assertEqual(len(rows), 3, "one row per cell, its current source only")
        self.assertIn(
            f"observability_judge.self_hosted\thttps://docs.example.com/gone\t404\t{DAY}", rows
        )

        proposal = json.loads((out_dir / "proposal.json").read_text(encoding="utf-8"))
        self.assertEqual(proposal["as_of"], {"observability_judge.inline": DAY})
        self.assertEqual(
            proposal["drop"],
            [{"cell": "observability_judge.self_hosted", "why": "source answered 404"}],
        )
        self.assertEqual(proposal["claims"]["kept"]["state"], "confirmed")
        self.assertEqual(proposal["claims"]["lost"]["state"], "not_found")

    def test_the_cache_prints_the_same_table_without_the_network(self) -> None:
        _, first, _ = self.run_main("--fetch")
        self.calls.clear()
        code, again, _ = self.run_main()
        self.assertEqual(self.calls, [])
        self.assertEqual(code, 1)
        self.assertEqual(again, first)

    def test_an_empty_cache_says_to_fetch(self) -> None:
        code, out, _ = self.run_main("--cache", str(self.root / "empty"))
        self.assertEqual(code, 1)
        self.assertIn("error: not in the cache (run with --fetch)", out)

    def test_every_source_read_and_confirmed_passes(self) -> None:
        compare = json.loads(json.dumps(COMPARE))
        compare["rows"][0]["cells"].pop("self_hosted")
        (self.root / cr.COMPARE_PATH).write_text(json.dumps(compare), encoding="utf-8")
        claims = {"claims": [CLAIMS["claims"][0]]}
        (self.root / cr.CLAIMS_PATH).write_text(json.dumps(claims), encoding="utf-8")
        code, _, err = self.run_main("--fetch")
        self.assertEqual(code, 0, err)

    def test_the_real_contracts_are_readable(self) -> None:
        compare = json.loads((cr.ROOT / cr.COMPARE_PATH).read_text(encoding="utf-8"))
        claims = json.loads((cr.ROOT / cr.CLAIMS_PATH).read_text(encoding="utf-8"))
        pages = {
            c["source_url"]: cr.Page(c["source_url"], "200", c["text"])
            for r in compare["rows"]
            for c in r["cells"].values()
            if c.get("source_url")
        }
        for c in claims["claims"]:
            if c.get("source_url"):
                pages.setdefault(c["source_url"], cr.Page(c["source_url"], "200", c["display"]))
        lines, tsv, proposal, _ = cr.review(compare, claims, pages, DAY)
        cells = sum(len(r["cells"]) for r in compare["rows"])
        self.assertEqual(len(tsv) - 1, cells)
        self.assertEqual(len(proposal["as_of"]), cells)


if __name__ == "__main__":
    unittest.main()
