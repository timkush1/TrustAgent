#!/usr/bin/env python3
"""Check a static demo recording before it is published (SPEC §2.13, §0 row 19, risk 19).

    python3 scripts/check_recording.py frontend-react/demo-data/simulated
    python3 scripts/check_recording.py DIR --require-recorded   # the P6 launch gate: refuse mock

A recording is manifest.json (trustagent.recording/v1) plus traces/<trace_id>.json (api.v1
TraceDetail). This script refuses a recording that could mislead a reader:

- The manifest must validate against contracts/schema/recording.v1.schema.json, every trace file
  against api.v1 TraceDetail, every manifest trace against TraceSummary and every event (wrapped in
  its WS v2 envelope) against WsEvent.
- kind "recorded" needs a decision backend that is neither mock nor off, a judge that is neither a
  fallback nor the mock, an answer_source on every trace, and the recorded label.
- kind "mock" must carry the fixed label "Simulated (mock checks + mock judge): not model output",
  roi.simulated true, and nothing that claims to be measured: no `label: "measured"`, no
  `cost_source` other than simulated, free_local or unknown, no calibration state "measured", no
  `simulated: false`. Every trace must come from the mock decision backend (or none).
- The directory must be consistent: one trace file per manifest trace (no extras), matching ids and
  answer sources, totals and answer-source counts that add up, events in time order that name
  known traces.
- No credential or e-mail address may appear anywhere (the recording is public). An address at a
  reserved example domain (RFC 2606 and RFC 6761: example.com/.net/.org, .example, .test, .invalid,
  .localhost, localhost) is not a real one and passes, as the Go sanitizer keeps those hosts
  (backend-go/internal/recording/sanitize.go reservedHost).
- The directory holds a recording and nothing else: manifest.json, traces/*.json and an optional
  .gitkeep. Everything in it is published (the dataset plugin copies it to dist/demo/), so a stray
  file would be too.
- The two dataset directories (D110) keep their kind: frontend-react/demo-data/simulated holds the
  simulated tape (kind mock, `npm run recording`), frontend-react/demo-data/recorded holds only a
  real recording (kind recorded, `make record-demo`). The rule follows the directory's name, so a
  copy elsewhere is checked by its content alone.

--require-recorded (the release gate: pages.yml on a v* tag, record_demo.py before it swaps a new
recording in) also refuses a mock recording and a missing directory, and says how to make one.

Exit status: 0 ok, 1 a check failed (with --require-recorded, also no recording at all), 2 usage
or environment error (for example jsonschema missing: pip install jsonschema).
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any, Iterator

ROOT = Path(__file__).resolve().parent.parent
SCHEMA_DIR = ROOT / "contracts" / "schema"
SCHEMA_BASE = "https://github.com/timkush1/TrustAgent/contracts/schema/"

MOCK_LABEL = "Simulated (mock checks + mock judge): not model output"
RECORDED_LABEL = re.compile(r"^Recorded .+ · replayed, not live$")
MOCK_COST_SOURCES = {"simulated", "free_local", "unknown"}

# SPEC §7.5 redaction patterns, plus private keys, JWTs and e-mail addresses.
SECRET_PATTERNS = [
    ("an sk- API key", re.compile(r"sk-[A-Za-z0-9_-]{8,}")),
    ("a bearer token", re.compile(r"Bearer\s+[A-Za-z0-9._~+/=-]{8,}")),
    ("an x-api-key header", re.compile(r"x-api-key:\s*\S+", re.IGNORECASE)),
    ("a private key", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")),
    ("a JWT", re.compile(r"eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}")),
    ("an e-mail address", re.compile(r"[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})")),
]

# The dataset directories (D110) and the kind each may hold.
DATASET_KINDS = {"simulated": "mock", "recorded": "recorded"}
# What a recording directory may hold besides manifest.json and traces/*.json.
ALLOWED_EXTRAS = {".gitkeep"}
RECORD_HINT = (
    "make one with `make record-demo CONFIRM=1` (the owner's OpenRouter key, about $1; "
    "docs/JEV-ACCESS.md): it writes frontend-react/demo-data/recorded/"
)

RESERVED_DOMAINS = ("example.com", "example.net", "example.org")
RESERVED_TLDS = (".example", ".test", ".invalid", ".localhost")


def reserved_host(host: str) -> bool:
    """A name under the reserved example domains (RFC 2606, RFC 6761), or localhost.

    Mirrors reservedHost in backend-go/internal/recording/sanitize.go.
    """
    h = host.lower().rstrip(".")
    if h == "localhost":
        return True
    if any(h == d or h.endswith("." + d) for d in RESERVED_DOMAINS):
        return True
    return h.endswith(RESERVED_TLDS)


class Problems:
    def __init__(self) -> None:
        self.items: list[str] = []

    def add(self, where: str, message: str) -> None:
        self.items.append(f"{where}: {message}")


def load_json(path: Path, problems: Problems) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        problems.add(str(path), f"cannot read JSON ({exc})")
        return None


# ------------------------------------------------------------------------------ schema validation


class Schemas:
    """Validators for recording.v1 and the api.v1 definitions, resolving refs to contracts/schema."""

    def __init__(self) -> None:
        try:
            import jsonschema
        except ImportError:
            print(
                "check_recording: the jsonschema package is required (pip install jsonschema)",
                file=sys.stderr,
            )
            sys.exit(2)
        self._js = jsonschema
        self.docs: dict[str, Any] = {}
        for path in SCHEMA_DIR.glob("*.schema.json"):
            doc = json.loads(path.read_text(encoding="utf-8"))
            self.docs[doc.get("$id", SCHEMA_BASE + path.name)] = doc
        self._cache: dict[str, Any] = {}

    def _validator(self, uri: str, fragment: str = "") -> Any:
        key = uri + "#" + fragment
        if key in self._cache:
            return self._cache[key]
        root = self.docs[uri]
        schema: Any = root if not fragment else {"$ref": uri + "#" + fragment}
        cls = self._js.validators.validator_for(root)
        try:  # jsonschema >= 4.18
            from referencing import Registry, Resource

            registry = Registry().with_resources(
                (u, Resource.from_contents(d)) for u, d in self.docs.items()
            )
            validator = cls(schema, registry=registry)
        except ImportError:  # older jsonschema: RefResolver with a store
            resolver = self._js.RefResolver(base_uri=uri, referrer=root, store=dict(self.docs))
            validator = cls(schema, resolver=resolver)
        self._cache[key] = validator
        return validator

    def errors(self, instance: Any, schema_file: str, definition: str = "") -> Iterator[str]:
        uri = SCHEMA_BASE + schema_file
        fragment = f"/$defs/{definition}" if definition else ""
        validator = self._validator(uri, fragment)
        for err in sorted(validator.iter_errors(instance), key=lambda e: list(e.absolute_path)):
            path = "/".join(str(p) for p in err.absolute_path) or "(root)"
            yield f"{path}: {err.message[:300]}"


# ------------------------------------------------------------------------------ walkers


def walk(value: Any, path: str = "") -> Iterator[tuple[str, str, Any]]:
    """Yields (path, key, value) for every member of every object, depth first."""
    if isinstance(value, dict):
        for k, v in value.items():
            here = f"{path}/{k}"
            yield here, k, v
            yield from walk(v, here)
    elif isinstance(value, list):
        for i, v in enumerate(value):
            yield from walk(v, f"{path}/{i}")


def strings(value: Any, path: str = "") -> Iterator[tuple[str, str]]:
    if isinstance(value, str):
        yield path, value
    elif isinstance(value, dict):
        for k, v in value.items():
            yield from strings(v, f"{path}/{k}")
    elif isinstance(value, list):
        for i, v in enumerate(value):
            yield from strings(v, f"{path}/{i}")


# ------------------------------------------------------------------------------ checks


def check_honesty(doc: Any, where: str, kind: str, problems: Problems) -> None:
    """Mock data never claims to be measured, reported or real."""
    if kind != "mock":
        return
    for path, key, value in walk(doc):
        if key == "label" and value == "measured":
            problems.add(where, f"{path} is labelled measured, but this is a mock recording")
        elif key == "cost_source" and isinstance(value, str) and value not in MOCK_COST_SOURCES:
            problems.add(where, f"{path} is {value!r}; mock costs are simulated")
        elif key == "calibration_state" and value == "measured":
            problems.add(where, f"{path} claims a measured calibration in a mock recording")
        elif key == "simulated" and value is False:
            problems.add(where, f"{path} says simulated: false in a mock recording")
        elif key == "backend" and isinstance(value, str) and value not in ("mock", "off"):
            problems.add(where, f"{path} names decision backend {value!r} in a mock recording")


def check_secrets(doc: Any, where: str, problems: Problems) -> None:
    for path, text in strings(doc):
        for name, pattern in SECRET_PATTERNS:
            if name == "an e-mail address":
                found = any(not reserved_host(m.group(1)) for m in pattern.finditer(text))
            else:
                found = pattern.search(text) is not None
            if found:
                problems.add(where, f"{path or '(root)'} contains {name}; sanitize the recording")


def check_manifest_rules(m: dict[str, Any], problems: Problems, require_recorded: bool) -> None:
    where = "manifest.json"
    kind = m.get("kind")
    backend = (m.get("decision_model") or {}).get("backend")
    judge = m.get("judge") or {}
    traces = m.get("traces") or []
    if kind == "recorded":
        if backend in ("mock", "off"):
            problems.add(where, f"kind recorded needs a real decision backend, not {backend!r}")
        if judge.get("fallback_active"):
            problems.add(where, "kind recorded needs a judge that is not on its fallback")
        if str(judge.get("provider", "")).lower() == "mock":
            problems.add(where, "kind recorded cannot have the mock judge")
        missing = [t.get("trace_id") for t in traces if t.get("answer_source") is None]
        if missing:
            problems.add(
                where, f"kind recorded needs answer_source on every trace ({len(missing)} missing)"
            )
        if not RECORDED_LABEL.match(str(m.get("label", ""))):
            problems.add(where, "the recorded label must read 'Recorded … · replayed, not live'")
    elif kind == "mock":
        if m.get("label") != MOCK_LABEL:
            problems.add(where, f"a mock recording must be labelled {MOCK_LABEL!r}")
        if backend != "mock":
            problems.add(
                where, f"a mock recording comes from the mock decision backend, not {backend!r}"
            )
        if (m.get("roi") or {}).get("simulated") is not True:
            problems.add(where, "a mock recording needs roi.simulated true")
        if require_recorded:
            problems.add(
                where,
                "kind mock: this is the simulated tape (mock checks, mock judge), and "
                "--require-recorded accepts only a real recording (kind recorded); " + RECORD_HINT,
            )
    else:
        problems.add(where, f"unknown kind {kind!r}")

    totals = m.get("totals") or {}
    if totals.get("traces") != len(traces):
        problems.add(
            where, f"totals.traces is {totals.get('traces')}, but {len(traces)} traces are listed"
        )
    sources = m.get("answer_sources") or {}
    for src in ("scripted", "generated"):
        n = sum(1 for t in traces if t.get("answer_source") == src)
        if sources.get(src) != n:
            problems.add(
                where, f"answer_sources.{src} is {sources.get(src)}, but {n} traces are {src}"
            )
    ids = [t.get("trace_id") for t in traces]
    if len(set(ids)) != len(ids):
        problems.add(where, "a trace is listed twice")
    known = set(ids)
    last = -1
    for i, ev in enumerate(m.get("events") or []):
        t = ev.get("t_ms", 0)
        if isinstance(t, int) and t < last:
            problems.add(where, f"events/{i} is out of time order")
        last = t if isinstance(t, int) else last
        if ev.get("trace_id") is not None and ev.get("trace_id") not in known:
            problems.add(where, f"events/{i} names an unknown trace {ev.get('trace_id')}")


def check_events(m: dict[str, Any], schemas: Schemas, problems: Problems) -> None:
    for i, ev in enumerate(m.get("events") or []):
        if ev.get("type") == "hb":
            continue
        envelope = {
            "v": 2,
            "seq": i + 1,
            "epoch": "recording",
            "type": ev.get("type"),
            "ts": m.get("recorded_at"),
            "data": ev.get("data"),
        }
        if ev.get("trace_id") is not None:
            envelope["trace_id"] = ev["trace_id"]
        errs = list(schemas.errors(envelope, "api.v1.schema.json", "WsEvent"))
        if errs:
            problems.add(
                f"manifest.json events/{i} ({ev.get('type')})",
                "does not match any WsEvent: " + errs[0],
            )


def check_layout(directory: Path, kind: Any, problems: Problems) -> None:
    """Only a recording in the directory, and the dataset directories keep their kind (D110)."""
    for entry in sorted(directory.iterdir(), key=lambda p: p.name):
        if entry.name == "manifest.json" and entry.is_file():
            continue
        if entry.name == "traces" and entry.is_dir():
            for item in sorted(entry.iterdir(), key=lambda p: p.name):
                if not (item.is_file() and item.suffix == ".json"):
                    problems.add(
                        f"traces/{item.name}",
                        "is not a trace file; everything here is published (dist/demo/)",
                    )
            continue
        if entry.name in ALLOWED_EXTRAS and entry.is_file():
            continue
        problems.add(
            entry.name,
            "is not part of a recording (manifest.json, traces/); everything here is published "
            "(dist/demo/)",
        )
    expected = DATASET_KINDS.get(directory.resolve().name)
    if expected is None or kind == expected:
        return
    if expected == "mock":
        problems.add(
            "manifest.json",
            f"demo-data/simulated holds the simulated tape (kind mock), not kind {kind!r}; a real "
            "recording belongs in demo-data/recorded (make record-demo)",
        )
    else:
        problems.add(
            "manifest.json",
            f"demo-data/recorded holds only a real recording (kind recorded), not kind {kind!r}; "
            "the simulated tape lives in demo-data/simulated (npm run recording)",
        )


def check_dir(directory: Path, require_recorded: bool) -> Problems:
    problems = Problems()
    schemas = Schemas()
    manifest_path = directory / "manifest.json"
    m = load_json(manifest_path, problems)
    if not isinstance(m, dict):
        if not problems.items:
            problems.add("manifest.json", "is not a JSON object")
        return problems

    for err in schemas.errors(m, "recording.v1.schema.json"):
        problems.add("manifest.json (recording.v1)", err)
    for i, t in enumerate(m.get("traces") or []):
        for err in schemas.errors(t, "api.v1.schema.json", "TraceSummary"):
            problems.add(f"manifest.json traces/{i} (TraceSummary)", err)
    check_events(m, schemas, problems)
    check_manifest_rules(m, problems, require_recorded)
    check_layout(directory, m.get("kind"), problems)
    kind = str(m.get("kind"))
    check_honesty(m, "manifest.json", kind, problems)
    check_secrets(m, "manifest.json", problems)

    traces_dir = directory / "traces"
    listed = {t.get("trace_id"): t for t in m.get("traces") or [] if isinstance(t, dict)}
    files = {p.stem: p for p in traces_dir.glob("*.json")} if traces_dir.is_dir() else {}
    for extra in sorted(set(files) - set(listed)):
        problems.add(f"traces/{extra}.json", "is not listed in the manifest")
    for trace_id, summary in listed.items():
        path = files.get(trace_id)
        where = f"traces/{trace_id}.json"
        if path is None:
            problems.add(where, "is missing")
            continue
        detail = load_json(path, problems)
        if not isinstance(detail, dict):
            continue
        for err in schemas.errors(detail, "api.v1.schema.json", "TraceDetail"):
            problems.add(f"{where} (TraceDetail)", err)
        if detail.get("trace_id") != trace_id:
            problems.add(where, f"holds trace {detail.get('trace_id')}")
        if detail.get("answer_source") != summary.get("answer_source"):
            problems.add(where, "answer_source differs from the manifest")
        if detail.get("labels"):
            problems.add(where, "published trace files carry no human labels")
        check_honesty(detail, where, kind, problems)
        check_secrets(detail, where, problems)
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument(
        "directory", type=Path, help="recording directory (manifest.json + traces/)"
    )
    parser.add_argument(
        "--require-recorded", action="store_true", help="refuse a mock recording (launch gate)"
    )
    args = parser.parse_args(argv)
    if not (args.directory / "manifest.json").is_file():
        if args.require_recorded:
            # The release gate found no recording: a failed check, with what to do about it.
            print(
                f"check_recording: no recording in {args.directory} (manifest.json not found); "
                f"--require-recorded needs a real recording: {RECORD_HINT}",
                file=sys.stderr,
            )
            return 1
        print(f"check_recording: {args.directory}/manifest.json not found", file=sys.stderr)
        return 2
    problems = check_dir(args.directory, args.require_recorded)
    if problems.items:
        for p in problems.items[:200]:
            print(f"FAIL {p}")
        if len(problems.items) > 200:
            print(f"... and {len(problems.items) - 200} more")
        print(
            f"check_recording: {len(problems.items)} problem(s) in {args.directory}",
            file=sys.stderr,
        )
        return 1
    m = json.loads((args.directory / "manifest.json").read_text(encoding="utf-8"))
    print(
        f"check_recording: ok: {args.directory} · kind {m['kind']} · {len(m['traces'])} traces · \"{m['label']}\""
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
