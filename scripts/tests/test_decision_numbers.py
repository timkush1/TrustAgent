"""Unit tests for scripts/check_decision_numbers.py (P6 plan section 2.1).

The parsers are proved on small texts in the formats the plans, SPEC appendices and merge bodies
use; the last test runs the check over this repository's documents (no git history needed).
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import check_decision_numbers as cdn  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]

PLAN = """\
| # | Question | Decision | Owner |
|---|---|---|---|
| D99 | One | text | lead |
| **D100** | Two | text | lead |
| D115 capture (`npm run og`) | not a definition | x | y |
"""

SPEC = """\
## Appendix F: P4 errata

- **D92 (decision model):** first text.
- **D76b (lead text):** a suffixed number.
- **D92 (refined):** amends D92, defines nothing new.

## Appendix G: P5 errata

- **D145, D146 (fault channel):** two at once.
| D160 | a row inside an appendix | x |

## Not an appendix

- **D999 (ignored):** outside the appendices.
"""

MERGE = """\
Merge CP-LIVE.

- D154 (CP-LIVE scope): merges the reviewed commit.
- D155, D156 (status): two at once.
  - D157 is only mentioned here.
"""


def ids(defs):
    return sorted(d.ident for d in defs)


class ParserTests(unittest.TestCase):
    def test_plan_rows(self) -> None:
        defs = cdn.plan_definitions(PLAN, "P5", "plan.md")
        self.assertEqual(ids(defs), ["D100", "D99"])
        self.assertEqual(defs[0].where, "plan.md:3")

    def test_spec_bullets_rows_and_amendments(self) -> None:
        defs = cdn.spec_definitions(SPEC, "spec.md")
        self.assertEqual(ids(defs), ["D145", "D146", "D160", "D76b", "D92"])
        phases = {d.ident: d.phase for d in defs}
        self.assertEqual(phases["D92"], "P4")
        self.assertEqual(phases["D146"], "P5")
        self.assertEqual([d.number for d in defs if d.ident == "D76b"], [76])

    def test_commit_lines(self) -> None:
        defs = cdn.commit_definitions([("abc1234", MERGE)])
        self.assertEqual(ids(defs), ["D154", "D155", "D156"])
        self.assertTrue(all(d.where == "commit abc1234" for d in defs))


class CheckTests(unittest.TestCase):
    def d(self, ident: str, phase: str, where: str, kind: str = "plan") -> cdn.Definition:
        return cdn.Definition(
            ident, int(ident[1:].rstrip("abcdefghijklmnopqrstuvwxyz")), phase, where, kind
        )

    def test_clean_set_passes(self) -> None:
        report = cdn.check([self.d("D99", "P5", "a:1"), self.d("D200", "P6", "b:1")])
        self.assertEqual(report.errors, [])

    def test_number_defined_twice_fails(self) -> None:
        report = cdn.check([self.d("D150", "P5", "a:1"), self.d("D150", "P5", "b:9", "spec")])
        self.assertEqual(len(report.errors), 1)
        self.assertIn("D150 is defined 2 times", report.errors[0])

    def test_two_merges_defining_one_number_fail(self) -> None:
        report = cdn.check(
            [
                self.d("D151", "P5", "commit aaa", "commit"),
                self.d("D151", "P5", "commit bbb", "commit"),
                self.d("D151", "P5", "spec:1", "spec"),
            ]
        )
        self.assertTrue(any("defined by 2 merges" in e for e in report.errors))

    def test_merge_copied_into_appendix_is_one_definition(self) -> None:
        report = cdn.check(
            [self.d("D151", "P5", "commit aaa", "commit"), self.d("D151", "P5", "spec:1", "spec")]
        )
        self.assertEqual(report.errors, [])
        self.assertEqual(report.cited_by_commit, [])

    def test_merge_only_number_is_cited_by_commit(self) -> None:
        report = cdn.check([self.d("D161", "P5", "commit ccc", "commit")])
        self.assertEqual(report.errors, [])
        self.assertEqual(report.cited_by_commit, ["D161 (commit ccc)"])

    def test_p5_number_in_the_p6_block_fails(self) -> None:
        report = cdn.check([self.d("D201", "P5", "spec:1", "spec")])
        self.assertTrue(any("reaches the P6 block" in e for e in report.errors))

    def test_p6_number_outside_its_block_fails(self) -> None:
        report = cdn.check([self.d("D173", "P6", "p6:1")])
        self.assertTrue(any("outside D200-D219" in e for e in report.errors))

    def test_ranges(self) -> None:
        self.assertEqual(cdn.ranges([3, 1, 2, 7], ["D76b"]), "D1-D3, D7, D76b")
        self.assertEqual(cdn.ranges([]), "none")


class RepositoryTests(unittest.TestCase):
    def test_repository_documents_pass(self) -> None:
        defs = cdn.collect(ROOT, merges=None)
        report = cdn.check(defs)
        self.assertEqual(report.errors, [])
        phases = {d.phase for d in defs}
        self.assertTrue({"P3", "P4", "P5", "P6"} <= phases, phases)
        p6 = [d.number for d in defs if d.phase == "P6"]
        self.assertEqual(min(p6), 200)
        self.assertLess(max(d.number for d in defs if d.phase == "P5"), 200)


if __name__ == "__main__":
    unittest.main()
