"""Static checks of the Grafana dashboards against SPEC Appendix B and F15.

Every query selects its series by `job` (the proxy's and the engine's scrape
jobs in config/prometheus.yml), so a name can never be counted twice, and uses
only the renamed metrics: trustagent_gateway_* from the proxy (job="proxy")
and trustagent_engine_* from the engine (job="engine"). Standard library only.
"""

from __future__ import annotations

import json
import re
import unittest
from pathlib import Path
from typing import Any, Dict, Iterator, List, Tuple

ROOT = Path(__file__).resolve().parents[2]
GRAFANA = ROOT / "config" / "grafana"
DASHBOARDS = sorted((GRAFANA / "dashboards").glob("*.json"))
PROMETHEUS = (ROOT / "config" / "prometheus.yml").read_text(encoding="utf-8")
DATASOURCES = (GRAFANA / "datasources" / "prometheus.yml").read_text(encoding="utf-8")

# The scrape job each metric prefix comes from.
JOB_FOR_PREFIX = {"trustagent_gateway_": "proxy", "trustagent_engine_": "engine"}
# PromQL words that are not metric names: grouping and set operators, modifiers.
KEYWORDS = frozenset(
    {"by", "without", "on", "ignoring", "group_left", "group_right", "and", "or", "unless", "bool"}
)
_IDENT = re.compile(r"[A-Za-z_:][A-Za-z0-9_:]*")


def panels(items: List[Dict[str, Any]]) -> Iterator[Dict[str, Any]]:
    """Every panel, including those nested in collapsed rows."""
    for panel in items:
        yield panel
        yield from panels(panel.get("panels") or [])


def queries(dashboard: Dict[str, Any]) -> Iterator[Tuple[str, str]]:
    """(panel title, PromQL expression) of every target."""
    for panel in panels(dashboard.get("panels") or []):
        for target in panel.get("targets") or []:
            if "expr" in target:
                yield str(panel.get("title")), str(target["expr"])


def metric_names(expr: str) -> List[str]:
    """The metric names an expression selects (functions and grouping labels left out)."""
    text = re.sub(r"\{[^}]*\}", " ", expr)  # label matchers, with their strings
    text = re.sub(r"\[[^\]]*\]", " ", text)  # range durations
    text = re.sub(
        r"\b(by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)", " ", text
    )  # grouping labels
    names: List[str] = []
    for match in _IDENT.finditer(text):
        if text[match.end() :].lstrip().startswith("("):
            continue  # a function or an aggregation
        if match.group(0) in KEYWORDS:
            continue
        names.append(match.group(0))
    return names


def selectors(expr: str, name: str) -> List[str]:
    """The label matchers of each occurrence of ``name``; '' for a bare name."""
    found: List[str] = []
    for match in re.finditer(rf"\b{re.escape(name)}\b", expr):
        rest = expr[match.end() :]
        matchers = re.match(r"\s*\{([^}]*)\}", rest)
        found.append(matchers.group(1) if matchers else "")
    return found


class GrafanaTest(unittest.TestCase):
    def test_there_is_a_dashboard(self) -> None:
        self.assertTrue(DASHBOARDS)

    def test_every_query_filters_by_job_and_uses_the_renamed_metrics(self) -> None:
        for path in DASHBOARDS:
            dashboard = json.loads(path.read_text(encoding="utf-8"))
            exprs = list(queries(dashboard))
            self.assertTrue(exprs, path.name)
            for title, expr in exprs:
                names = metric_names(expr)
                self.assertTrue(names, f"{path.name} {title!r}: no metric in {expr}")
                for name in names:
                    prefix = next((p for p in JOB_FOR_PREFIX if name.startswith(p)), None)
                    self.assertIsNotNone(
                        prefix,
                        f"{path.name} {title!r}: {name} is neither trustagent_gateway_* "
                        "nor trustagent_engine_*",
                    )
                    assert prefix is not None
                    job = JOB_FOR_PREFIX[prefix]
                    for matchers in selectors(expr, name):
                        self.assertRegex(
                            matchers,
                            rf'(^|,)\s*job\s*=\s*"{job}"',
                            f'{path.name} {title!r}: {name} needs job="{job}" in {expr}',
                        )

    def test_the_jobs_exist_in_the_scrape_config(self) -> None:
        for job in JOB_FOR_PREFIX.values():
            self.assertRegex(PROMETHEUS, rf"job_name:\s*'{job}'")

    def test_panels_use_the_provisioned_datasource(self) -> None:
        match = re.search(r"^\s+uid:\s*(\S+)\s*$", DATASOURCES, re.MULTILINE)
        assert match is not None, "config/grafana/datasources/prometheus.yml sets no uid"
        uid = match.group(1)
        for path in DASHBOARDS:
            dashboard = json.loads(path.read_text(encoding="utf-8"))
            for panel in panels(dashboard.get("panels") or []):
                if panel.get("type") == "row":
                    continue
                self.assertEqual(
                    panel.get("datasource"), {"type": "prometheus", "uid": uid}, panel["title"]
                )

    def test_the_parser(self) -> None:
        expr = (
            "histogram_quantile(0.95, sum by (le, mode) (rate("
            'trustagent_gateway_overhead_seconds_bucket{job="proxy", route!="x"}[5m])))'
        )
        self.assertEqual(metric_names(expr), ["trustagent_gateway_overhead_seconds_bucket"])
        self.assertEqual(
            selectors(expr, "trustagent_gateway_overhead_seconds_bucket"),
            ['job="proxy", route!="x"'],
        )
        ratio = 'sum(rate(a_total[5m])) / sum(rate(b_total{job="engine"}[5m]))'
        self.assertEqual(metric_names(ratio), ["a_total", "b_total"])
        self.assertEqual(selectors(ratio, "a_total"), [""])


if __name__ == "__main__":
    unittest.main()
