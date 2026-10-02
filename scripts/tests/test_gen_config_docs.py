"""Unit tests for scripts/gen_config_docs.py (standard library only; no Docker, no network)."""

from __future__ import annotations

import contextlib
import copy
import io
import json
import re
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock
from typing import Any, Dict, List, Optional, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import gen_config_docs as gen  # noqa: E402
from configure import ensure  # noqa: E402
from envfile import EnvFile, parse_line, parse_value  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
REAL_SCHEMA: Dict[str, Any] = json.loads((ROOT / gen.SCHEMA_REL).read_text(encoding="utf-8"))
REAL_REGISTRY: Dict[str, Any] = json.loads((ROOT / gen.REGISTRY_REL).read_text(encoding="utf-8"))

# The fifteen keys SPEC §2.7 lists for .env.example.
SPEC_ENV_EXAMPLE_KEYS = {
    "OPENROUTER_API_KEY",
    "TRUTHTABLE_JEV_BACKEND",
    "TRUTHTABLE_JEV_MODEL",
    "ANTHROPIC_API_KEY",
    "LLM_PROVIDER",
    "TRUTHTABLE_UPSTREAM_URL",
    "TRUTHTABLE_UPSTREAM_API_KEY",
    "TRUTHTABLE_API_KEYS",
    "TRUTHTABLE_ADMIN_KEYS",
    "TRUTHTABLE_GATE_DEFAULT_MODE",
    "TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY",
    "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY",
    "TRUTHTABLE_PUBLIC_URL",
    "COMPOSE_PROFILES",
    "BIND_ADDR",
}

_EXAMPLE_LINE = re.compile(r"^# ([A-Z][A-Z0-9_]*)=(.*)$")


def key(name: str, group: str = "core", **fields: Any) -> Dict[str, Any]:
    """A schema-valid registry entry; keyword arguments override its fields."""
    entry: Dict[str, Any] = {
        "name": name,
        "aliases": [],
        "services": ["proxy"],
        "kind": "string",
        "default": "",
        "secret": False,
        "advanced": True,
        "restart": True,
        "group": group,
        "description": f"What {name} does.",
    }
    entry.update(fields)
    return entry


def registry(keys: List[Dict[str, Any]], groups: Optional[List[Dict[str, Any]]] = None):
    return {
        "version": "1",
        "groups": groups
        or [
            {"id": "core", "title": "Core", "description": "Core settings."},
            {"id": "judge", "title": "Engine: judge", "description": "The judge."},
        ],
        "keys": keys,
    }


def example_assignments(text: str) -> List[Tuple[str, str]]:
    """The commented-out ``# KEY=value`` lines of a rendered .env.example."""
    return [(m.group(1), m.group(2)) for m in map(_EXAMPLE_LINE.match, text.splitlines()) if m]


def table_rows(markdown: str) -> Dict[str, List[str]]:
    """Key name -> unescaped-enough cells of every key row in the rendered tables."""
    rows: Dict[str, List[str]] = {}
    for line in markdown.splitlines():
        if not line.startswith("| `"):
            continue
        cells = [c.strip() for c in re.split(r"(?<!\\)\|", line)[1:-1]]
        rows[cells[0].split("`")[1]] = cells
    return rows


class ValidatorTest(unittest.TestCase):
    def test_real_registry_is_valid_against_real_schema(self) -> None:
        self.assertEqual(gen.validate(REAL_REGISTRY, REAL_SCHEMA, REAL_SCHEMA), [])

    def check(self, instance: Any, schema: Dict[str, Any]) -> List[str]:
        return gen.validate(instance, schema, schema)

    def test_types_distinguish_booleans_from_numbers(self) -> None:
        self.assertEqual(self.check(3, {"type": "integer"}), [])
        self.assertEqual(self.check(3.0, {"type": "integer"}), [])
        self.assertTrue(self.check(True, {"type": "integer"}))
        self.assertTrue(self.check(1, {"type": "boolean"}))
        self.assertEqual(self.check(None, {"type": ["string", "null"]}), [])
        self.assertTrue(self.check(3.5, {"type": "integer"}))

    def test_object_rules_report_json_pointer_paths(self) -> None:
        schema = {
            "type": "object",
            "additionalProperties": False,
            "required": ["a", "b"],
            "properties": {"a": {"type": "string", "pattern": "^[a-z]+$"}},
        }
        errors = self.check({"a": "ABC", "c": 1}, schema)
        self.assertIn("/: missing required property 'b'", errors)
        self.assertIn("/: unknown property 'c'", errors)
        self.assertTrue(any(e.startswith("/a: 'ABC' does not match") for e in errors), errors)

    def test_arrays_refs_enums_and_any_of(self) -> None:
        schema = {
            "$defs": {"Id": {"enum": ["x", "y"]}},
            "type": "array",
            "minItems": 1,
            "uniqueItems": True,
            "items": {"$ref": "#/$defs/Id"},
        }
        self.assertEqual(self.check(["x", "y"], schema), [])
        self.assertIn("/: needs at least 1 item(s)", self.check([], schema))
        self.assertIn("/: item 1 is a duplicate", self.check(["x", "x"], schema))
        self.assertTrue(any(e.startswith("/1: ") for e in self.check(["x", "z"], schema)))
        any_of = {"anyOf": [{"type": "string"}, {"type": "null"}]}
        self.assertEqual(self.check(None, any_of), [])
        self.assertTrue(self.check(1, any_of))
        self.assertTrue(self.check("2", {"const": "1"}))
        self.assertEqual(self.check(1.0, {"const": 1}), [])
        self.assertTrue(self.check(True, {"const": 1}))

    def test_string_and_number_bounds(self) -> None:
        self.assertTrue(self.check("", {"minLength": 1}))
        self.assertTrue(self.check("abcd", {"maxLength": 3}))
        self.assertTrue(self.check(0, {"minimum": 1}))
        self.assertTrue(self.check(9, {"maximum": 8}))
        self.assertEqual(self.check("abc", {"minLength": 1, "maxLength": 3}), [])

    def test_unknown_keyword_or_remote_ref_is_refused(self) -> None:
        with self.assertRaises(gen.SchemaError):
            self.check("x", {"format": "email"})
        with self.assertRaises(gen.SchemaError):
            self.check("x", {"$ref": "other.json#/x"})
        with self.assertRaises(gen.SchemaError):
            self.check("x", {"$ref": "#/$defs/missing"})


class BuildRegistryTest(unittest.TestCase):
    def build(self, data: Dict[str, Any]) -> gen.Registry:
        return gen.build_registry(data, REAL_SCHEMA)

    def assert_problem(self, data: Dict[str, Any], fragment: str) -> None:
        with self.assertRaises(gen.RegistryError) as ctx:
            self.build(data)
        self.assertTrue(any(fragment in p for p in ctx.exception.problems), ctx.exception.problems)

    def test_groups_follow_group_order_and_keys_follow_registry_order(self) -> None:
        data = registry(
            [key("B_JUDGE", "judge"), key("Z_CORE"), key("A_JUDGE", "judge"), key("M_CORE")]
        )
        built = self.build(data)
        self.assertEqual([g.id for g in built.groups], ["core", "judge"])
        self.assertEqual([k["name"] for k in built.groups[0].keys], ["Z_CORE", "M_CORE"])
        self.assertEqual([k["name"] for k in built.groups[1].keys], ["B_JUDGE", "A_JUDGE"])
        self.assertEqual(built.total, 4)

    def test_group_without_keys_is_left_out(self) -> None:
        built = self.build(registry([key("ONLY_CORE")]))
        self.assertEqual([g.id for g in built.groups], ["core"])

    def test_schema_violation_is_reported_with_its_path(self) -> None:
        data = registry([key("BAD_KEY", kind="number")])
        self.assert_problem(data, "/keys/0/kind")
        data = registry([key("BAD_KEY")])
        del data["keys"][0]["restart"]
        self.assert_problem(data, "missing required property 'restart'")

    def test_cross_field_rules(self) -> None:
        self.assert_problem(registry([key("DUP_KEY"), key("DUP_KEY")]), "declared twice")
        self.assert_problem(
            registry([key("A_KEY", aliases=["B_KEY"]), key("B_KEY")]), "declared twice"
        )
        self.assert_problem(registry([key("LOST_KEY", "nowhere")]), "unknown group nowhere")
        self.assert_problem(
            registry([key("SECRET_KEY", secret=True, default="sk-x")]), "cannot ship a default"
        )
        self.assert_problem(registry([key("ENUM_KEY", kind="enum")]), "needs an enum list")
        groups = [{"id": "core", "title": "Core"}, {"id": "core", "title": "Again"}]
        self.assert_problem(registry([key("A_KEY")], groups), "group id core is declared twice")

    def test_at_most_fifteen_basic_keys(self) -> None:
        keys = [key(f"BASIC_{i}", advanced=False) for i in range(gen.MAX_BASIC_KEYS)]
        self.assertEqual(len(self.build(registry(keys)).basic_keys()), gen.MAX_BASIC_KEYS)
        keys.append(key("BASIC_EXTRA", advanced=False))
        self.assert_problem(registry(keys), "at most 15 are allowed")

    def test_group_titles_need_their_own_heading_anchor(self) -> None:
        clash = [{"id": "core", "title": "Core"}, {"id": "judge", "title": "core!"}]
        self.assert_problem(registry([key("A_KEY")], clash), "anchor #core of 'Core'")
        fixed = [{"id": "core", "title": "Groups"}]
        self.assert_problem(registry([key("A_KEY")], fixed), "anchor #groups of 'Groups'")
        empty = [{"id": "core", "title": "?!"}]
        self.assert_problem(registry([key("A_KEY")], empty), "empty heading anchor")

    def test_schema_values_the_generator_cannot_describe_are_refused(self) -> None:
        schema = copy.deepcopy(REAL_SCHEMA)
        schema["$defs"]["ConfigKey"]["properties"]["services"]["items"]["enum"].append("robot")
        with self.assertRaises(gen.RegistryError) as ctx:
            gen.build_registry(registry([key("A_KEY")]), schema)
        self.assertIn("robot", str(ctx.exception))


class RenderMarkdownTest(unittest.TestCase):
    def render(self, keys: List[Dict[str, Any]]) -> str:
        return gen.render_markdown(gen.build_registry(registry(keys), REAL_SCHEMA))

    def test_generated_header_contents_and_group_tables(self) -> None:
        md = self.render([key("CORE_KEY"), key("JUDGE_KEY", "judge")])
        self.assertTrue(md.startswith("<!--\n  GENERATED FILE: do not edit by hand."))
        self.assertIn("2 keys in 2 groups", md)
        self.assertIn("- [Core](#core): 1 key\n", md)
        self.assertIn("- [Engine: judge](#engine-judge): 1 key\n", md)
        self.assertLess(md.index("\n## Core\n"), md.index("\n## Engine: judge\n"))
        self.assertIn(
            "| Key | Default | Services | Advanced | Secret | Restart | Description |", md
        )
        self.assertTrue(md.endswith("\n") and not md.endswith("\n\n"))

    def test_cells_for_every_field(self) -> None:
        md = self.render(
            [
                key(
                    "A_PORT",
                    kind="int",
                    default="8080",
                    min=1,
                    max=65535,
                    aliases=["OLD_PORT"],
                    services=["proxy", "engine"],
                    compose_default="80",
                ),
                key(
                    "A_MODE",
                    kind="enum",
                    default="observe",
                    enum=["observe", "flag"],
                    advanced=False,
                    restart=False,
                    runtime="settable",
                ),
                key("A_SECRET", secret=True, default=None, default_note="generated by configure"),
                key("VITE_THING", services=["build"], default="x"),
                key("A_LIST", kind="list", enum=["a", "b"], runtime="seed"),
                key("A_CAP", kind="float", default="2.00", min=0.0),
                key(
                    "A_URL",
                    kind="url",
                    shorthands={"short": "https://example.com/api"},
                    default_note="empty resolves to https://example.com",
                ),
            ]
        )
        rows = table_rows(md)
        self.assertEqual(
            rows["A_PORT"][:6],
            [
                "`A_PORT`<br>alias `OLD_PORT`",
                "`8080`<br>compose: `80`",
                "proxy, engine",
                "yes",
                "no",
                "yes",
            ],
        )
        self.assertIn("Integer, from 1 to 65535.", rows["A_PORT"][6])
        self.assertEqual(rows["A_MODE"][3:6], ["no", "no", "no"])
        self.assertIn("One of `observe`, `flag`.", rows["A_MODE"][6])
        self.assertIn("Also editable while running", rows["A_MODE"][6])
        self.assertEqual(rows["A_SECRET"][1], "*none*<br>generated by configure")
        self.assertEqual(rows["A_SECRET"][4], "yes")
        self.assertEqual(rows["VITE_THING"][5], "rebuild")
        self.assertIn("Comma-separated list of `a`, `b`.", rows["A_LIST"][6])
        self.assertIn("Seeds a value", rows["A_LIST"][6])
        self.assertIn("Number, at least 0.", rows["A_CAP"][6])
        self.assertEqual(rows["A_URL"][1], "*empty*<br>empty resolves to https://example.com")
        self.assertIn("Shorthands: `short` = `https://example.com/api`.", rows["A_URL"][6])

    def test_prose_is_escaped_and_variable_names_become_code(self) -> None:
        text = "Pipes a|b, *stars*, ~tilde, <tag> &amp; with TRUTHTABLE_X_KEY and TRUTHTABLE_*."
        md = self.render([key("ESCAPED_KEY", description=text, default="x|y")])
        cells = table_rows(md)["ESCAPED_KEY"]
        self.assertEqual(len(cells), 7, "an escaped pipe must not open a new column")
        self.assertEqual(cells[1], "`x\\|y`")
        self.assertIn(
            "Pipes a\\|b, \\*stars\\*, \\~tilde, &lt;tag> &amp;amp; with `TRUTHTABLE_X_KEY` "
            "and `TRUTHTABLE_*`.",
            cells[6],
        )

    def test_code_spans_survive_backticks_pipes_and_empty_values(self) -> None:
        self.assertEqual(gen._md_code("a|b"), "`a\\|b`")
        self.assertEqual(gen._md_code("x`y"), "`` x`y ``")
        self.assertEqual(gen._md_code("``a`"), "``` ``a` ```")
        self.assertEqual(gen._md_code("  "), "*empty*")
        md = self.render([key("A_DIR", default="/x", compose_default="")])
        self.assertEqual(table_rows(md)["A_DIR"][1], "`/x`<br>compose: *empty*")

    def test_runtime_settings_name_the_settings_page(self) -> None:
        keys = [key("LIVE_KEY", restart=False, runtime="settable")]
        md = self.render(keys)
        self.assertIn(
            "Also editable while running in dashboard Settings.", table_rows(md)["LIVE_KEY"][6]
        )
        self.assertIn("`no` = nothing when changed in dashboard Settings;", md)
        self.assertIn("also get a value in dashboard Settings.", md)
        self.assertNotIn("*(from", md, "the Settings page has shipped: no phase tag")

    def test_md_prose_keeps_spaces_around_code_spans(self) -> None:
        self.assertEqual(
            gen.md_prose("an  unknown TRUTHTABLE_* key\n(see LLM_PROVIDER)"),
            "an unknown `TRUTHTABLE_*` key (see `LLM_PROVIDER`)",
        )
        self.assertEqual(gen.md_prose("SPEC and UTF-16 stay prose"), "SPEC and UTF-16 stay prose")

    def test_slugs_match_github_heading_anchors(self) -> None:
        self.assertEqual(gen._slug("Engine link, worker, sampling"), "engine-link-worker-sampling")
        self.assertEqual(gen._slug("Engine: judge"), "engine-judge")
        self.assertEqual(gen._slug("Budgets, Studio"), "budgets-studio")


class RenderEnvExampleTest(unittest.TestCase):
    def render(self, keys: List[Dict[str, Any]]) -> str:
        return gen.render_env_example(gen.build_registry(registry(keys), REAL_SCHEMA))

    def test_only_basic_keys_in_registry_order_with_empty_secrets(self) -> None:
        text = self.render(
            [
                key("JUDGE_BASIC", "judge", advanced=False, default="auto"),
                key("CORE_ADVANCED", default="1"),
                key("CORE_SECRET", advanced=False, secret=True),
                key("CORE_GENERATED", advanced=False, default=None, default_note="derived"),
                key("CORE_BASIC", advanced=False, default="127.0.0.1"),
            ]
        )
        self.assertEqual(
            example_assignments(text),
            [
                ("CORE_SECRET", ""),
                ("CORE_GENERATED", ""),
                ("CORE_BASIC", "127.0.0.1"),
                ("JUDGE_BASIC", "auto"),
            ],
        )
        self.assertNotIn("CORE_ADVANCED", text)
        self.assertLess(text.index("# ---------- Core ----------"), text.index("Engine: judge"))

    def test_every_key_has_a_comment_within_the_width(self) -> None:
        long = "A long description that goes on " * 6
        text = self.render(
            [
                key("A_KEY", advanced=False, description=long, secret=True),
                key("B_KEY", advanced=False, kind="enum", enum=["x", "y"], default="x"),
                key("C_KEY", advanced=False, kind="list", enum=["p", "q"]),
            ]
        )
        lines = text.splitlines()
        for name in ("A_KEY", "B_KEY", "C_KEY"):
            index = lines.index(next(line for line in lines if line.startswith(f"# {name}=")))
            self.assertTrue(lines[index - 1].startswith("# "), name)
            self.assertNotRegex(lines[index - 1], r"^# [A-Z_]+=")
        self.assertTrue(all(len(line) <= gen.ENV_WIDTH for line in lines))
        self.assertIn("Secret.", text)
        self.assertIn("One of: x, y.", text)
        self.assertIn("Comma-separated, from: p, q.", text)

    def test_header_names_the_template_so_a_copied_env_reads_right(self) -> None:
        # configure.py --ensure copies this header into a new .env.
        header = self.render([key("A_KEY", advanced=False)]).split("\n\n", 1)[0]
        self.assertTrue(all(line.startswith("#") for line in header.splitlines()))
        self.assertIn(f"# The template, .env.example, is generated from {gen.REGISTRY_REL}", header)
        self.assertIn("Your own .env is yours to edit.", header)

    def test_values_that_need_quotes_are_quoted(self) -> None:
        self.assertEqual(gen.env_value("http://localhost:3000"), "http://localhost:3000")
        self.assertEqual(gen.env_value("two words"), "'two words'")
        self.assertEqual(gen.env_value("${HOME}/x"), "'${HOME}/x'")
        self.assertEqual(gen.env_value('it\'s "x"'), '"it\'s \\"x\\""')
        for value in ("two words", "${HOME}/x", 'it\'s "x"', "a#b c"):
            self.assertEqual(parse_value(gen.env_value(value)), value)


class RealRegistryTest(unittest.TestCase):
    """The shipped registry and the committed generated files."""

    @classmethod
    def setUpClass(cls) -> None:
        cls.registry = gen.load_registry(ROOT)
        cls.files = gen.render(cls.registry)

    def test_env_example_lists_exactly_the_spec_keys(self) -> None:
        names = [n for n, _ in example_assignments(self.files[gen.ENV_EXAMPLE_REL])]
        self.assertEqual(len(names), len(set(names)))
        self.assertEqual(set(names), SPEC_ENV_EXAMPLE_KEYS)

    def test_env_example_never_carries_a_secret_value(self) -> None:
        secrets = {k["name"] for g in self.registry.groups for k in g.keys if k["secret"]}
        for name, value in example_assignments(self.files[gen.ENV_EXAMPLE_REL]):
            if name in secrets:
                self.assertEqual(value, "", name)

    def test_markdown_lists_every_key_and_alias(self) -> None:
        md = self.files[gen.DOCS_REL]
        rows = table_rows(md)
        self.assertEqual(len(rows), len(REAL_REGISTRY["keys"]))
        for entry in REAL_REGISTRY["keys"]:
            self.assertIn(entry["name"], rows)
            for alias in entry["aliases"]:
                self.assertIn(f"`{alias}`", rows[entry["name"]][0])
            self.assertEqual(len(rows[entry["name"]]), 7, entry["name"])

    def test_output_is_deterministic(self) -> None:
        self.assertEqual(gen.render(gen.load_registry(ROOT)), self.files)

    def test_committed_files_are_current(self) -> None:
        """CI's drift gate: regenerate with `make config-docs` when this fails."""
        for rel, text in self.files.items():
            with open(ROOT / rel, encoding="utf-8") as handle:
                self.assertEqual(
                    handle.read(), text, f"{rel} is stale: run `make config-docs` and commit it"
                )

    def test_configure_turns_the_template_into_a_working_env(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            example = Path(tmp) / ".env.example"
            example.write_text(self.files[gen.ENV_EXAMPLE_REL], encoding="utf-8")
            env = EnvFile.load(Path(tmp) / ".env")
            result = ensure(env, example, volume_exists=lambda _names: False, environ={})
        self.assertTrue(result.created)
        self.assertEqual(result.warnings, [])
        values = env.values()
        for generated in ("POSTGRES_PASSWORD", "GRAFANA_ADMIN_PASSWORD", "TRAFFICGEN_API_KEY"):
            self.assertTrue(values.get(generated), generated)
        self.assertTrue(values["TRUTHTABLE_API_KEYS"].startswith("trafficgen="))
        # Everything the template itself sets is commented out.
        self.assertTrue(
            all(parse_line(line) is None for line in self.files[gen.ENV_EXAMPLE_REL].splitlines())
        )


class MainTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        for rel in (gen.REGISTRY_REL, gen.SCHEMA_REL):
            (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / rel, self.root / rel)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def run_main(self, *args: str) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = gen.main(["--root", str(self.root), *args])
        return code, out.getvalue(), err.getvalue()

    def edit_registry(self, change) -> None:
        path = self.root / gen.REGISTRY_REL
        data = json.loads(path.read_text(encoding="utf-8"))
        change(data)
        path.write_text(json.dumps(data, indent=2), encoding="utf-8")

    def test_write_then_up_to_date_then_check_passes(self) -> None:
        code, out, _ = self.run_main()
        self.assertEqual(code, 0)
        self.assertIn(f"wrote {gen.DOCS_REL}", out)
        self.assertIn(f"wrote {gen.ENV_EXAMPLE_REL}", out)
        for rel in (gen.DOCS_REL, gen.ENV_EXAMPLE_REL):
            self.assertNotIn(b"\r\n", (self.root / rel).read_bytes())
        mtime = (self.root / gen.DOCS_REL).stat().st_mtime_ns
        code, out, _ = self.run_main()
        self.assertEqual((code, (self.root / gen.DOCS_REL).stat().st_mtime_ns), (0, mtime))
        self.assertIn("up to date", out)
        self.assertEqual(self.run_main("--check")[0], 0)

    def test_check_reports_drift_as_a_unified_diff_and_writes_nothing(self) -> None:
        self.run_main()

        def rename(data: Dict[str, Any]) -> None:
            entry = next(k for k in data["keys"] if k["name"] == "BIND_ADDR")
            entry["description"] = "Where published ports listen."

        self.edit_registry(rename)
        before = (self.root / gen.ENV_EXAMPLE_REL).read_text(encoding="utf-8")
        code, out, err = self.run_main("--check")
        self.assertEqual(code, 1)
        self.assertIn(f"--- a/{gen.ENV_EXAMPLE_REL} (committed)", out)
        self.assertIn(f"+++ b/{gen.ENV_EXAMPLE_REL} (generated from", out)
        self.assertIn("+# Where published ports listen.", out)
        self.assertIn("make config-docs", err)
        self.assertEqual((self.root / gen.ENV_EXAMPLE_REL).read_text(encoding="utf-8"), before)

    def test_check_fails_when_a_file_is_missing(self) -> None:
        code, out, _ = self.run_main("--check")
        self.assertEqual(code, 1)
        self.assertIn("--- /dev/null", out)
        self.assertFalse((self.root / gen.DOCS_REL).exists())

    def test_crlf_checkout_is_not_drift(self) -> None:
        self.run_main()
        for rel in (gen.DOCS_REL, gen.ENV_EXAMPLE_REL):
            path = self.root / rel
            path.write_bytes(path.read_bytes().replace(b"\n", b"\r\n"))
        self.assertEqual(self.run_main("--check")[0], 0)

    def test_invalid_registry_exits_2_without_writing(self) -> None:
        self.edit_registry(lambda data: data["keys"][0].update(kind="number"))
        code, _, err = self.run_main()
        self.assertEqual(code, 2)
        self.assertIn("/keys/0/kind", err)
        self.assertFalse((self.root / gen.DOCS_REL).exists())

    def test_unreadable_registry_exits_2(self) -> None:
        (self.root / gen.REGISTRY_REL).write_text("{not json", encoding="utf-8")
        code, _, err = self.run_main("--check")
        self.assertEqual(code, 2)
        self.assertIn("invalid JSON", err)
        (self.root / gen.REGISTRY_REL).unlink()
        code, _, err = self.run_main()
        self.assertEqual(code, 2)
        self.assertIn("cannot read", err)


if __name__ == "__main__":
    unittest.main()
