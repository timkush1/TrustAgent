"""Static checks of the Grafana dashboards against SPEC Appendix B and F15.

Every query selects its series by `job` (the proxy's and the engine's scrape
jobs in config/prometheus.yml), so a name can never be counted twice, and uses
only the renamed metrics: trustagent_gateway_* from the proxy (job="proxy")
and trustagent_engine_* from the engine (job="engine"). Every metric a query
names is one the code registers: the gateway's in backend-go/internal/metrics,
the engine's in backend-python/src/truthtable/metrics.py, read as text so a
renamed or removed metric fails here instead of leaving an empty panel.
Standard library only.
"""

from __future__ import annotations

import json
import re
import unittest
from pathlib import Path
from typing import Any, Dict, Iterator, List, Set, Tuple

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
GO_METRICS = ROOT / "backend-go" / "internal" / "metrics"
ENGINE_METRICS = ROOT / "backend-python" / "src" / "truthtable" / "metrics.py"
# The series a histogram exposes besides its name.
HISTOGRAM_SERIES = ("_bucket", "_sum", "_count")


def registered_metrics() -> Dict[str, str]:
    """Every metric name the gateway and the engine register, mapped to its type."""
    found: Dict[str, str] = {}
    namespace = re.search(
        r'const namespace = "([a-z_]+)"', (GO_METRICS / "metrics.go").read_text(encoding="utf-8")
    )
    assert namespace is not None, "backend-go/internal/metrics/metrics.go declares no namespace"
    for path in sorted(GO_METRICS.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8")
        for ctor in re.finditer(r"promauto\.New(Counter|Gauge|Histogram|Summary)(?:Vec)?\(", text):
            name = re.compile(r'Name:\s*"(\w+)"').search(text, ctor.end())
            assert name is not None, f"{path.name}: a metric without a Name"
            found[f"{namespace.group(1)}_{name.group(1)}"] = ctor.group(1).lower()
    engine = ENGINE_METRICS.read_text(encoding="utf-8")
    for match in re.finditer(
        r'\b(Counter|Gauge|Histogram|Summary)\(\s*"(trustagent_engine_\w+)"', engine
    ):
        found[match.group(2)] = match.group(1).lower()
    return found


def exposed_series(registered: Dict[str, str]) -> Set[str]:
    """The series names a scrape shows for the registered metrics."""
    names: Set[str] = set()
    for name, kind in registered.items():
        names.add(name)
        if kind in ("histogram", "summary"):
            names.update(name + suffix for suffix in HISTOGRAM_SERIES)
    return names


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

    def test_every_queried_metric_is_registered(self) -> None:
        registered = registered_metrics()
        # The reader found both sides (a broken pattern would make every check below vacuous).
        self.assertEqual(registered.get("trustagent_gateway_cost_write_dropped_total"), "counter")
        self.assertEqual(registered.get("trustagent_gateway_overhead_seconds"), "histogram")
        self.assertEqual(registered.get("trustagent_engine_audits_total"), "counter")
        series = exposed_series(registered)
        for path in DASHBOARDS:
            dashboard = json.loads(path.read_text(encoding="utf-8"))
            for title, expr in queries(dashboard):
                for name in metric_names(expr):
                    self.assertIn(
                        name, series, f"{path.name} {title!r}: {name} is not registered ({expr})"
                    )

    def test_the_spend_row_shows_the_ledger_and_the_budgets(self) -> None:
        dashboard = json.loads((GRAFANA / "dashboards" / "trustagent.json").read_text("utf-8"))
        items = dashboard["panels"]
        start = next(i for i, p in enumerate(items) if p["type"] == "row" and "Spend" in p["title"])
        row: List[Dict[str, Any]] = []
        for panel in items[start + 1 :]:
            if panel["type"] == "row":
                break
            row.append(panel)
        exprs = [t["expr"] for p in row for t in p.get("targets") or []]
        compact = [re.sub(r"\s+", "", e) for e in exprs]
        for wanted in (
            'sumby(kind)(trustagent_gateway_spend_usd_today{job="proxy"})',
            'sumby(kind)(trustagent_gateway_spend_reserved_usd{job="proxy"})',
            'sumby(component)(increase(trustagent_gateway_jev_cost_usd_total{job="proxy"}[24h]))',
            'sum(trustagent_gateway_uniform_samples_today{job="proxy"})',
            'trustagent_gateway_audit_jobs_total{job="proxy",outcome="llm_budget"}',
            'trustagent_gateway_cost_write_dropped_total{job="proxy"}',
        ):
            self.assertTrue(any(wanted in e for e in compact), wanted)
        ids = [p["id"] for p in panels(items)]
        self.assertEqual(len(ids), len(set(ids)), "panel ids are unique")
        for panel in row:
            self.assertTrue(panel.get("description"), f"{panel['title']}: says what it counts")
            if panel.get("fieldConfig", {}).get("defaults", {}).get("unit") == "currencyUSD":
                # Honest numbers: a dollar figure says where it comes from.
                self.assertRegex(panel["description"], r"(?i)real spend|estimated|budget held")


if __name__ == "__main__":
    unittest.main()
