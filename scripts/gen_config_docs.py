#!/usr/bin/env python3
"""Generate docs/CONFIGURATION.md and .env.example from the key registry (standard library only).

``contracts/config/keys.v1.json`` is the single configuration registry (SPEC §2.7); the Go
loader, the Python loader and this script all read it. This script renders two files from it:

- ``docs/CONFIGURATION.md``: every key, one table per group, in the registry's order (groups
  as ``groups`` lists them, keys as ``keys`` lists them);
- ``.env.example``: only the keys that are not ``advanced`` (at most 15), each commented out
  at its default under a short description. Secrets are always left empty.
  ``scripts/configure.py --ensure`` copies it to a new ``.env``.

Usage::

    python3 scripts/gen_config_docs.py            # write both files (only those that changed)
    python3 scripts/gen_config_docs.py --check    # exit 1 with a unified diff when one is stale

The registry is checked against ``contracts/schema/keys.v1.schema.json`` first (a small
validator for exactly the JSON Schema keywords that schema uses), then against the rules the
output depends on. Output is deterministic: no timestamps, no host paths, LF line endings.

Exit codes: 0 written or up to date, 1 ``--check`` found drift, 2 invalid registry or schema,
usage error, or a file that cannot be read or written.
"""

from __future__ import annotations

import argparse
import difflib
import json
import math
import re
import sys
import textwrap
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence

REPO_ROOT = Path(__file__).resolve().parent.parent
REGISTRY_REL = "contracts/config/keys.v1.json"
SCHEMA_REL = "contracts/schema/keys.v1.schema.json"
DOCS_REL = "docs/CONFIGURATION.md"
ENV_EXAMPLE_REL = ".env.example"

# SPEC §2.7: .env.example lists at most 15 keys (the ones that are not advanced).
MAX_BASIC_KEYS = 15
# .env.example comment lines stay within this width ("# " included).
ENV_WIDTH = 80

# Plain-language names for the registry's service ids (schema: ConfigKey.services).
SERVICE_TEXT: Dict[str, str] = {
    "proxy": "the Go gateway",
    "engine": "the Python audit engine",
    "compose": "docker compose and make",
    "dashboard": "the dashboard container (at start)",
    "build": "the dashboard build (baked into the bundle)",
    "tools": "setup and demo tools (trafficgen, doctor, configure)",
}

# How each value kind is written (schema: ConfigKey.kind).
KIND_TEXT: Dict[str, str] = {
    "string": "Text",
    "int": "Integer",
    "float": "Number",
    "bool": "`true` or `false`",
    "duration": "Duration (Go syntax, e.g. `30s`, `5m`)",
    "bytes": "Byte count",
    "url": "URL",
    "enum": "One of",
    "list": "Comma-separated list",
    "path": "File path",
    "key_list": "Comma-separated `label=key` pairs",
}


class RegistryError(Exception):
    """The registry or its schema cannot be rendered; ``problems`` says why."""

    def __init__(self, problems: Sequence[str]) -> None:
        super().__init__("; ".join(problems))
        self.problems = list(problems)


class SchemaError(Exception):
    """The schema uses something the built-in validator does not implement."""


# ----------------------------------------------------------------------------- schema check

# Keywords that only annotate; they never reject an instance.
_ANNOTATIONS = frozenset(
    {"$schema", "$id", "$comment", "$defs", "title", "description", "default", "examples"}
)
_JSON_TYPES = frozenset({"object", "array", "string", "number", "integer", "boolean", "null"})


def _json_type_ok(value: Any, name: str) -> bool:
    if name == "object":
        return isinstance(value, dict)
    if name == "array":
        return isinstance(value, list)
    if name == "string":
        return isinstance(value, str)
    if name == "boolean":
        return isinstance(value, bool)
    if name == "null":
        return value is None
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return False
    if name == "integer":
        return isinstance(value, int) or (isinstance(value, float) and value.is_integer())
    return True  # number


def _same_json(a: Any, b: Any) -> bool:
    """JSON equality: 1 == 1.0, but true != 1."""
    if isinstance(a, bool) or isinstance(b, bool):
        return type(a) is type(b) and a == b
    if isinstance(a, dict) and isinstance(b, dict):
        return a.keys() == b.keys() and all(_same_json(a[k], b[k]) for k in a)
    if isinstance(a, list) and isinstance(b, list):
        return len(a) == len(b) and all(_same_json(x, y) for x, y in zip(a, b))
    return bool(type(a) is type(b) or (_is_number(a) and _is_number(b))) and a == b


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def _resolve_ref(ref: str, root: Mapping[str, Any]) -> Mapping[str, Any]:
    if not ref.startswith("#/"):
        raise SchemaError(f"only local $ref values are supported, got {ref!r}")
    node: Any = root
    for part in ref[2:].split("/"):
        part = part.replace("~1", "/").replace("~0", "~")
        if not isinstance(node, dict) or part not in node:
            raise SchemaError(f"$ref {ref!r} does not resolve")
        node = node[part]
    if not isinstance(node, dict):
        raise SchemaError(f"$ref {ref!r} is not a schema object")
    return node


def validate(
    instance: Any, schema: Mapping[str, Any], root: Mapping[str, Any], path: str = ""
) -> List[str]:
    """Return one message per violation of ``schema`` (JSON pointer path first).

    Implements the JSON Schema 2020-12 keywords the registry schema uses and raises
    ``SchemaError`` on any other keyword, so a constraint is never skipped silently.
    """
    where = path or "/"
    errors: List[str] = []
    for keyword in schema:
        if keyword not in _ANNOTATIONS and keyword not in _KEYWORDS:
            raise SchemaError(f"unsupported JSON Schema keyword {keyword!r} at {where}")

    if "$ref" in schema:
        errors += validate(instance, _resolve_ref(schema["$ref"], root), root, path)

    if "type" in schema:
        names = schema["type"] if isinstance(schema["type"], list) else [schema["type"]]
        unknown = [n for n in names if n not in _JSON_TYPES]
        if unknown:
            raise SchemaError(f"unknown type {unknown[0]!r} at {where}")
        if not any(_json_type_ok(instance, n) for n in names):
            errors.append(f"{where}: expected {' or '.join(names)}")
            return errors  # the remaining keywords assume the right type

    if "const" in schema and not _same_json(instance, schema["const"]):
        errors.append(f"{where}: must be {json.dumps(schema['const'])}")
    if "enum" in schema and not any(_same_json(instance, v) for v in schema["enum"]):
        allowed = ", ".join(json.dumps(v) for v in schema["enum"])
        errors.append(f"{where}: {json.dumps(instance)} is not one of {allowed}")
    if "anyOf" in schema:
        if not any(not validate(instance, sub, root, path) for sub in schema["anyOf"]):
            errors.append(f"{where}: matches none of the anyOf alternatives")

    if isinstance(instance, str):
        if "minLength" in schema and len(instance) < schema["minLength"]:
            errors.append(f"{where}: shorter than {schema['minLength']} characters")
        if "maxLength" in schema and len(instance) > schema["maxLength"]:
            errors.append(f"{where}: longer than {schema['maxLength']} characters")
        if "pattern" in schema and re.search(schema["pattern"], instance) is None:
            errors.append(f"{where}: {instance!r} does not match {schema['pattern']}")

    if _is_number(instance):
        if "minimum" in schema and instance < schema["minimum"]:
            errors.append(f"{where}: below the minimum {schema['minimum']}")
        if "maximum" in schema and instance > schema["maximum"]:
            errors.append(f"{where}: above the maximum {schema['maximum']}")

    if isinstance(instance, list):
        if "minItems" in schema and len(instance) < schema["minItems"]:
            errors.append(f"{where}: needs at least {schema['minItems']} item(s)")
        if "maxItems" in schema and len(instance) > schema["maxItems"]:
            errors.append(f"{where}: allows at most {schema['maxItems']} item(s)")
        if schema.get("uniqueItems"):
            for i, item in enumerate(instance):
                if any(_same_json(item, other) for other in instance[:i]):
                    errors.append(f"{where}: item {i} is a duplicate")
        if "items" in schema:
            for i, item in enumerate(instance):
                errors += validate(item, schema["items"], root, f"{path}/{i}")

    if isinstance(instance, dict):
        for name in schema.get("required", []):
            if name not in instance:
                errors.append(f"{where}: missing required property {name!r}")
        properties = schema.get("properties", {})
        extra = schema.get("additionalProperties", True)
        for name, value in instance.items():
            child = f"{path}/{name}"
            if name in properties:
                errors += validate(value, properties[name], root, child)
            elif extra is False:
                errors.append(f"{where}: unknown property {name!r}")
            elif isinstance(extra, dict):
                errors += validate(value, extra, root, child)
    return errors


_KEYWORDS = frozenset(
    {
        "$ref",
        "type",
        "const",
        "enum",
        "anyOf",
        "minLength",
        "maxLength",
        "pattern",
        "minimum",
        "maximum",
        "minItems",
        "maxItems",
        "uniqueItems",
        "items",
        "required",
        "properties",
        "additionalProperties",
    }
)


# ----------------------------------------------------------------------------- registry model


@dataclass(frozen=True)
class Group:
    id: str
    title: str
    description: str
    keys: List[Dict[str, Any]]


@dataclass(frozen=True)
class Registry:
    groups: List[Group]  # registry order; groups without keys are left out
    total: int

    def basic_keys(self) -> List[Dict[str, Any]]:
        """Keys that are not advanced (the .env.example set), in registry order."""
        return [k for g in self.groups for k in g.keys if not k["advanced"]]


def _schema_enum(schema: Mapping[str, Any], prop: str) -> List[str]:
    """The enum of ConfigKey.<prop> (or of its items) in the registry schema."""
    try:
        node = schema["$defs"]["ConfigKey"]["properties"][prop]
    except (KeyError, TypeError):
        raise RegistryError([f"{SCHEMA_REL}: ConfigKey.{prop} is not defined"]) from None
    node = node.get("items", node)
    if not isinstance(node.get("enum"), list):
        raise RegistryError([f"{SCHEMA_REL}: ConfigKey.{prop} has no enum"])
    return list(node["enum"])


def build_registry(data: Any, schema: Mapping[str, Any]) -> Registry:
    """Validate ``data`` against ``schema`` and the generator's rules; group its keys."""
    try:
        problems = validate(data, schema, schema)
    except SchemaError as exc:
        raise RegistryError([f"{SCHEMA_REL}: {exc}"]) from None
    if problems:
        raise RegistryError([f"{REGISTRY_REL}: {p}" for p in problems])

    # The generator describes every service and kind the schema allows.
    for prop, known in (("services", SERVICE_TEXT), ("kind", KIND_TEXT)):
        missing = [v for v in _schema_enum(schema, prop) if v not in known]
        if missing:
            raise RegistryError(
                [
                    f"{SCHEMA_REL}: ConfigKey.{prop} value(s) {', '.join(missing)} are not "
                    "described in scripts/gen_config_docs.py"
                ]
            )

    problems = []
    group_ids = [g["id"] for g in data["groups"]]
    for gid in sorted({g for g in group_ids if group_ids.count(g) > 1}):
        problems.append(f"group id {gid} is declared twice")
    seen: Dict[str, str] = {}
    for key in data["keys"]:
        for name in [key["name"], *key["aliases"]]:
            if name in seen:
                problems.append(f"{name} is declared twice ({seen[name]} and {key['name']})")
            seen.setdefault(name, key["name"])
        if key["group"] not in group_ids:
            problems.append(f"{key['name']}: unknown group {key['group']}")
        if key["kind"] == "enum" and not key.get("enum"):
            problems.append(f"{key['name']}: kind enum needs an enum list")
        if key["secret"] and key["default"] not in ("", None):
            problems.append(f"{key['name']}: a secret cannot ship a default")
    basic = [k["name"] for k in data["keys"] if not k["advanced"]]
    if len(basic) > MAX_BASIC_KEYS:
        problems.append(
            f".env.example would list {len(basic)} keys (advanced: false); "
            f"at most {MAX_BASIC_KEYS} are allowed"
        )
    if problems:
        raise RegistryError([f"{REGISTRY_REL}: {p}" for p in problems])

    groups = []
    for g in data["groups"]:
        keys = [k for k in data["keys"] if k["group"] == g["id"]]
        if keys:
            groups.append(Group(g["id"], g["title"], g.get("description", ""), keys))
    return Registry(groups=groups, total=len(data["keys"]))


def load_registry(root: Path) -> Registry:
    """Read and validate the registry and its schema under ``root``."""
    documents = []
    for rel in (REGISTRY_REL, SCHEMA_REL):
        try:
            with open(root / rel, encoding="utf-8") as handle:
                documents.append(json.load(handle))
        except OSError as exc:
            raise RegistryError([f"{rel}: cannot read: {exc.strerror or exc}"]) from None
        except ValueError as exc:
            raise RegistryError([f"{rel}: invalid JSON: {exc}"]) from None
    data, schema = documents
    if not isinstance(schema, dict):
        raise RegistryError([f"{SCHEMA_REL}: not a JSON object"])
    return build_registry(data, schema)


# ----------------------------------------------------------------------------- formatting


def _number(value: Any) -> str:
    if isinstance(value, float) and math.isfinite(value) and value.is_integer():
        return str(int(value))
    return str(value)


def _range(key: Mapping[str, Any]) -> str:
    low, high = key.get("min"), key.get("max")
    if low is not None and high is not None:
        return f"from {_number(low)} to {_number(high)}"
    if low is not None:
        return f"at least {_number(low)}"
    if high is not None:
        return f"at most {_number(high)}"
    return ""


# Registry key names (and TRUTHTABLE_* style wildcards) inside prose become code spans.
_IDENTIFIER = re.compile(
    r"(?<![\w`])([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+(?:_\*)?|[A-Z][A-Z0-9]*_\*)(?![\w`*])"
)


_ENTITY = re.compile(r"&(?=#?[A-Za-z0-9]+;)")


def _md_text(text: str) -> str:
    """Escape prose for a Markdown table cell: no HTML, emphasis, strikethrough or column breaks."""
    text = re.sub(r"\s+", " ", text)
    for char, escaped in (("\\", "\\\\"), ("|", "\\|"), ("*", "\\*"), ("~", "\\~")):
        text = text.replace(char, escaped)
    return _ENTITY.sub("&amp;", text).replace("<", "&lt;")


def _md_code(text: str) -> str:
    """A code span that is safe inside a table cell."""
    text = " ".join(text.split()).replace("|", "\\|")
    fence = "``" if "`" in text else "`"
    pad = " " if fence == "``" or text.startswith(" ") or text.endswith(" ") else ""
    return f"{fence}{pad}{text}{pad}{fence}"


def md_prose(text: str) -> str:
    """Escape prose and format the environment-variable names in it as code."""
    parts = _IDENTIFIER.split(" ".join(text.split()))
    # re.split with one group: even indexes are prose, odd indexes are identifiers.
    return "".join(_md_code(p) if i % 2 else _md_text(p) for i, p in enumerate(parts))


def _code_list(values: Sequence[str]) -> str:
    return ", ".join(_md_code(v) for v in values)


def md_default(key: Mapping[str, Any]) -> str:
    default, note = key["default"], key.get("default_note")
    if default is None:
        cell = "*none*"
    elif default == "":
        cell = "*empty*"
    else:
        cell = _md_code(default)
    if note:
        cell += "<br>" + md_prose(note)
    if "compose_default" in key:
        cell += "<br>compose: " + _md_code(key["compose_default"])
    return cell


def md_details(key: Mapping[str, Any]) -> str:
    """Value syntax, allowed values, shorthands and runtime behaviour, as one line."""
    kind = key["kind"]
    if kind == "enum":
        syntax = f"One of {_code_list(key['enum'])}"
    elif kind == "list" and key.get("enum"):
        syntax = f"Comma-separated list of {_code_list(key['enum'])}"
    else:
        syntax = KIND_TEXT[kind]
    bounds = _range(key)
    if bounds:
        syntax += f", {bounds}"
    sentences = [syntax + "."]
    if key.get("shorthands"):
        pairs = [f"{_md_code(s)} = {_md_code(v)}" for s, v in key["shorthands"].items()]
        sentences.append(f"Shorthands: {', '.join(pairs)}.")
    if key.get("runtime") == "settable":
        sentences.append("Also editable while running (dashboard Settings).")
    elif key.get("runtime") == "seed":
        sentences.append("Seeds a value that is then edited in the dashboard or policy file.")
    return " ".join(sentences)


def _yes(flag: bool) -> str:
    return "yes" if flag else "no"


def _restart(key: Mapping[str, Any]) -> str:
    if not key["restart"]:
        return "no"
    return "rebuild" if "build" in key["services"] else "yes"


def _slug(title: str) -> str:
    """The anchor GitHub gives a Markdown heading."""
    return re.sub(r"[^\w\- ]", "", title.strip().lower()).replace(" ", "-")


# ----------------------------------------------------------------------------- renderers

# The fixed top of docs/CONFIGURATION.md; str.format fields come from the registry.
_MD_INTRO = """\
<!--
  GENERATED FILE: do not edit by hand.
  Source: {registry} (schema: {schema}).
  Regenerate with `make config-docs` (python3 scripts/gen_config_docs.py);
  `make config-docs-check` and CI fail when this file and the registry disagree.
-->

# Configuration reference

TrustAgent is configured with environment variables. This page lists every one of them:
{total} keys in {groups} groups, generated from the key registry
[`{registry}`](../{registry}).

**You rarely need this page.** The demo (`make demo`) needs no settings at all, and a
normal install needs one key, `OPENROUTER_API_KEY` (see [JEV-ACCESS.md](JEV-ACCESS.md)).
The {basic} settings most people ever change are in [`.env.example`](../.env.example);
every other key is marked *advanced* and has a safe default.

## How settings are read

- **Where to put them.** In `.env` at the repository root. `make up` and `make demo`
  create it from `.env.example` when it is missing and fill in the generated passwords.
- **Which value wins at start.** Built-in default, then `.env`, then the process
  environment. A variable that is already set in the environment is never overwritten
  by `.env`.
- **Settings you can change while running** (marked below) also have a value in the
  dashboard. A non-empty environment variable always wins over it and shows there as
  read-only (*managed by environment*).
- **Durations** use Go syntax in both services: `800ms`, `30s`, `5m`, `2160h`.
- **Mistakes are reported, not guessed.** An invalid value produces a warning with a
  hint, and an unknown `TRUTHTABLE_*` name gets a *did you mean …?* suggestion. With
  `TRUTHTABLE_STRICT_CONFIG=true` (the demo, CI and e2e set it) any warning stops the
  service.
- **Aliases** are older or alternative names. When both are set, the canonical name wins.

## Reading the tables

| Column | Meaning |
|---|---|
| Key | The variable name, plus any aliases. |
| Default | The built-in value. *empty* means unset; *none* means there is no fixed value \
(it is generated or derived, as noted). `compose:` is the value the shipped \
`docker-compose.yml` or image sets. |
| Services | Who reads it: {services}. |
| Advanced | `no` = listed in `.env.example`. `yes` = add it to `.env` yourself when you \
need it. |
| Secret | `yes` = never logged, never sent to the browser, masked in the dashboard. |
| Restart | `yes` = takes effect after a restart (`make up` again); `rebuild` = after \
rebuilding the dashboard image; `no` = applied while running. |
| Description | What it does, then the value syntax. |

## Groups
"""


def render_markdown(registry: Registry) -> str:
    services = "; ".join(f"`{sid}` = {text}" for sid, text in SERVICE_TEXT.items())
    out: List[str] = _MD_INTRO.format(
        registry=REGISTRY_REL,
        schema=SCHEMA_REL,
        total=registry.total,
        groups=len(registry.groups),
        basic=len(registry.basic_keys()),
        services=services,
    ).splitlines()
    out.append("")
    for group in registry.groups:
        count = len(group.keys)
        out.append(
            f"- [{group.title}](#{_slug(group.title)}): {count} key{'s' if count != 1 else ''}"
        )
    for group in registry.groups:
        out += ["", f"## {group.title}", ""]
        if group.description:
            out += [md_prose(group.description), ""]
        out += [
            "| Key | Default | Services | Advanced | Secret | Restart | Description |",
            "|---|---|---|---|---|---|---|",
        ]
        for key in group.keys:
            name = _md_code(key["name"])
            if key["aliases"]:
                label = "alias" if len(key["aliases"]) == 1 else "aliases"
                name += f"<br>{label} {_code_list(key['aliases'])}"
            cells = [
                name,
                md_default(key),
                ", ".join(key["services"]),
                _yes(key["advanced"]),
                _yes(key["secret"]),
                _restart(key),
                md_prose(key["description"]) + "<br>" + md_details(key),
            ]
            out.append("| " + " | ".join(cells) + " |")
    return "\n".join(out) + "\n"


def _comment(text: str) -> List[str]:
    return textwrap.wrap(
        " ".join(text.split()),
        width=ENV_WIDTH,
        initial_indent="# ",
        subsequent_indent="# ",
        break_long_words=False,
        break_on_hyphens=False,
    )


def env_comment(key: Mapping[str, Any]) -> str:
    """The description of one key in .env.example (plain text, wrapped by the caller)."""
    text = str(key["description"]).strip()
    if key["secret"]:
        text += " Secret."
    if key["kind"] == "enum":
        text += f" One of: {', '.join(key['enum'])}."
    elif key["kind"] == "list" and key.get("enum"):
        text += f" Comma-separated, from: {', '.join(key['enum'])}."
    if key.get("shorthands"):
        pairs = [f"{s} ({v})" for s, v in key["shorthands"].items()]
        text += f" Shorthands: {', '.join(pairs)}."
    if key.get("default_note"):
        text += f" Default: {key['default_note'].rstrip('.')}."
    return text


def env_value(value: str) -> str:
    """A value as .env syntax: quoted only when compose or the loaders could misread it.

    Single quotes keep a value literal for compose and scripts/envfile.py alike; a value that
    itself holds a single quote gets double quotes with backslash escapes.
    """
    if not re.search(r"[\s#'\"$\\`]", value):
        return value
    if "'" not in value:
        return f"'{value}'"
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def render_env_example(registry: Registry) -> str:
    out: List[str] = [
        "# TrustAgent settings (.env). NEVER commit .env.",
        "#",
        f"# Generated from {REGISTRY_REL} by scripts/gen_config_docs.py",
        "# (make config-docs): to change this template, change the registry.",
        "#",
        "# `make up` and `make demo` create .env from this file when it is missing",
        "# (scripts/configure.py --ensure) and add the generated values the stack needs:",
        "# POSTGRES_PASSWORD, GRAFANA_ADMIN_PASSWORD, TRAFFICGEN_API_KEY and its",
        "# TRUTHTABLE_API_KEYS entry. The demo (`make demo`) needs no keys at all; for",
        "# real checks, set OPENROUTER_API_KEY (docs/JEV-ACCESS.md).",
        "#",
        "# These are the common settings, commented out at their defaults: uncomment a",
        "# line to change it. Every other key is advanced; docs/CONFIGURATION.md lists",
        "# them all with their defaults, and any of them can be added here the same way.",
        "# Precedence: built-in default < .env < process environment. A value set here",
        "# wins over the dashboard, which then shows it as read-only.",
    ]
    for group in registry.groups:
        keys = [k for k in group.keys if not k["advanced"]]
        if not keys:
            continue
        out += ["", f"# ---------- {group.title} ----------"]
        for key in keys:
            value = "" if key["secret"] or key["default"] is None else env_value(key["default"])
            out += ["", *_comment(env_comment(key)), f"# {key['name']}={value}"]
    return "\n".join(out) + "\n"


def render(registry: Registry) -> Dict[str, str]:
    """Every generated file, by path relative to the repository root."""
    return {DOCS_REL: render_markdown(registry), ENV_EXAMPLE_REL: render_env_example(registry)}


# ----------------------------------------------------------------------------- CLI


def _read(path: Path) -> Optional[str]:
    """The file's text with universal newlines, or None when it does not exist."""
    try:
        with open(path, encoding="utf-8") as handle:
            return handle.read()
    except FileNotFoundError:
        return None


def _write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(text)


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="gen_config_docs.py",
        description=(f"Generate {DOCS_REL} and {ENV_EXAMPLE_REL} from {REGISTRY_REL} (SPEC §2.7)."),
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="write nothing; exit 1 and print a unified diff when a generated file is stale",
    )
    parser.add_argument(
        "--root",
        type=Path,
        default=REPO_ROOT,
        help="repository root to read the registry from and write into (default: %(default)s)",
    )
    args = parser.parse_args(argv)
    root: Path = args.root

    try:
        files = render(load_registry(root))
    except RegistryError as exc:
        for problem in exc.problems:
            print(f"gen_config_docs.py: {problem}", file=sys.stderr)
        return 2

    stale = []
    for rel, text in files.items():
        try:
            current = _read(root / rel)
        except (OSError, UnicodeDecodeError) as exc:
            print(f"gen_config_docs.py: cannot read {rel}: {exc}", file=sys.stderr)
            return 2
        if current == text:
            continue
        stale.append(rel)
        if args.check:
            diff = difflib.unified_diff(
                (current or "").splitlines(keepends=True),
                text.splitlines(keepends=True),
                fromfile=f"a/{rel} (committed)" if current is not None else "/dev/null",
                tofile=f"b/{rel} (generated from {REGISTRY_REL})",
            )
            sys.stdout.writelines(diff)
            continue
        try:
            _write(root / rel, text)
        except OSError as exc:
            print(f"gen_config_docs.py: cannot write {rel}: {exc}", file=sys.stderr)
            return 2
        print(f"gen_config_docs.py: wrote {rel}")

    if args.check and stale:
        print(
            f"gen_config_docs.py: {', '.join(stale)} out of date with {REGISTRY_REL}; "
            "run `make config-docs` (python3 scripts/gen_config_docs.py) and commit the result",
            file=sys.stderr,
        )
        return 1
    if not stale:
        print(f"gen_config_docs.py: {', '.join(files)} up to date")
    return 0


if __name__ == "__main__":
    sys.exit(main())
