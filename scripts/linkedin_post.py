#!/usr/bin/env python3
"""The LinkedIn launch post as plain text, filled from the measured run and the recorded tape
(D213; standard library only).

    python3 scripts/linkedin_post.py                 # make linkedin-post
    python3 scripts/linkedin_post.py --clip          # make linkedin-post CLIP=1 (WSL clipboard)
    python3 scripts/linkedin_post.py --roles         # with the optional "open to roles" line
    python3 scripts/linkedin_post.py --restricted    # the mode-R post: no fast-check figures

The post's words live in docs/launch/linkedin-post.md, in the ```text block under "## The post"
(or "## The post, mode R" with --restricted). This script fills its placeholders and prints the
result; nobody pastes a figure by hand.

Placeholders:
  {featured.claim}     the answer's own words for the featured trace's contradicted claim: the
                       answer's sentence that holds the claim's span, else the quote when it is
                       a phrase of 4 words or more, else the claim
  {featured.evidence}  the evidence that contradicts it (the document's words)
  {featured.source}    where that answer came from, said plainly (D227): a scripted answer is "a
                       support-bot answer scripted for the demo", a generated one names the model
                       that wrote it; never "my support bot" (the owner runs no support bot)
  {featured.outcome}   what TrustAgent did with it: "flagged it", "blocked it", "replaced it with
                       a fallback answer"
  {gate.conditions}    how the gate latency was taken (D205): measured.json
                       sources.gate_latency.conditions (connection, requests at a time, where the
                       run ran, snapshot, n)
  {cost.conditions}    what the fast check's cost was measured on (D229): measured.json
                       sources.fast_check_usd_per_1k.conditions (the dataset, n, the input tokens
                       per answer); a cost per answer grows with the documents' length
  {same_checks.judge}  the LLM judge the same-checks ratio compares with, by name ("Claude Haiku
                       4.5"), from measured.json sources.llm_same_checks_usd_per_1k.model
  {measured:KEY}       a value of contracts/claims/measured.json, formatted as the README shows it
                       (ms with " ms", USD with "$", a fraction as a percentage, a multiplier bare)
  {claim:ID}           a claim's display and label chip from value-claims.v1.json, e.g.
                       "76% at 0.5 [third-party]"
  {run.date}           the measured run's date; {run.ids} its run ids; {snapshot} its snapshot.
                       The post says its figures come from one benchmark run, so {run.date} is
                       missing when measured.json's values come from several (a load run, which
                       the post does not quote, is not counted)
  {agreement}          a paragraph built here: our RAGTruth-QA accuracy and balanced accuracy, each
                       with its 95% interval and n per class; the thresholds the bench flags at
                       (each bench evaluator's warn_at, read from contracts/evaluators) and where
                       the groundedness one came from; then Arize's figures for Jev, at 0.5 and at
                       its tuned threshold, said not to be like for like (D212, D233). When any
                       part is missing the paragraph is left out (the agreement then lives in the
                       README only) and a note says so.
  {roles}              at the start of a line: the line is printed only with --roles

The featured trace is the recorded tape's ``featured.trace_id``
(frontend-react/demo-data/recorded/); only a tape with kind "recorded" whose fast-check, judge and
generated-answer hosts are vendor hosts counts (D203), and only a measured run with mock:false. Every missing value is listed and the exit status is 1: the post is never
printed with a gap or a made-up figure.

The post's first line must show before LinkedIn's "...see more" cut on a phone: it names
TrustAgent and stays within OPENING_MAX characters once filled, or the script refuses (exit 2).
The whole post stays within MAX_POST_CHARS (LinkedIn takes 3,000; the rest is room for a last
edit in the composer), or the script refuses (exit 2): LinkedIn would refuse or cut it.

Exit status: 0 printed (or copied), 1 a value is missing, 2 usage error or a malformed template.
"""

from __future__ import annotations

import argparse
import io
import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence, TextIO, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
import clip as clipboard  # noqa: E402
from release_notes import Refused, format_value, tape_hosts_problem, vendor_hosts  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
TEMPLATE_PATH = "docs/launch/linkedin-post.md"
MEASURED_PATH = "contracts/claims/measured.json"
CLAIMS_PATH = "contracts/claims/value-claims.v1.json"
RECORDED_DIR = "frontend-react/demo-data/recorded"
POST_HEADING = "## The post"
RESTRICTED_HEADING = "## The post, mode R"
# D233: Arize's figures for Jev on RAGTruth, at a 0.5 threshold, at its tuned one, and that
# threshold. Neither accuracy is like for like with ours (one question, worded differently).
ARIZE_AT_HALF_ID = "arize_ragtruth_accuracy_at_default_threshold"
ARIZE_TUNED_ID = "arize_ragtruth_jev_accuracy"
ARIZE_THRESHOLD_ID = "arize_ragtruth_tuned_threshold"
# The evaluators the bench's fast check runs (backend-python/evals/judges.py DEFAULT_EVALUATORS);
# the bench flags an answer when any of them reaches its warn_at.
BENCH_EVALUATORS = ("groundedness", "contradiction")
EVALUATOR_PATH = "contracts/evaluators/{name}.v1.json"
CHIPS = {
    "measured": "measured",
    "estimate": "estimate",
    "third_party": "third-party",
    "vendor": "vendor",
    "simulated": "simulated",
}
PLACEHOLDER = re.compile(r"\{([a-z][a-z0-9_.:]*)\}")
ROLES_PREFIX = "{roles}"
# What a phone shows before "...see more" (about 140-150 characters; LinkedIn does not publish it).
OPENING_MAX = 150
# LinkedIn takes at most 3,000 characters in a post; the filled post keeps 200 of them spare, so a
# longer snapshot name or a last edit in the composer never pushes it over (D229).
LINKEDIN_MAX_CHARS = 3000
MAX_POST_CHARS = 2800
# How each answer source is said (D227): a scripted demo answer is never presented as a real bot's.
# The scripted answers were written with the agents that built the demo, so "scripted", not "I wrote".
SCRIPTED_SOURCE = "a support-bot answer scripted for the demo"
GENERATED_SOURCE = "a support-bot answer that {model} wrote for the demo"
OUTCOMES = {
    "flag": "flagged it",
    "block": "blocked it",
    "fallback": "replaced it with a fallback answer",
}
MIN_QUOTE_WORDS = 4
SENTENCE = re.compile(r"[^.!?\n]+(?:[.!?]+|$)")


class UsageError(Exception):
    """A bad argument, an unreadable file or a malformed template."""


@dataclass
class Sources:
    """Everything a post is filled from, read once."""

    measured: Dict[str, Any]
    claims: Dict[str, Dict[str, Any]]
    manifest: Optional[Dict[str, Any]]
    trace: Optional[Dict[str, Any]]
    problems: List[str] = field(default_factory=list)
    # Each bench evaluator's warn_at as the post says it ("0.8"); None when one is unreadable.
    thresholds: Optional[Dict[str, str]] = None


# ------------------------------------------------------------------------------ reading


def _read_json(path: Path) -> Optional[Any]:
    if not path.is_file():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise UsageError(f"{path}: {exc}") from None


def template(text: str, heading: str) -> str:
    """The ```text block that follows ``heading`` (the first one before the next ## heading)."""
    m = re.search(rf"(?m)^{re.escape(heading)}\s*$", text)
    if not m:
        raise UsageError(f"{TEMPLATE_PATH} has no '{heading}' section")
    rest = text[m.end() :]
    nxt = re.search(r"(?m)^## ", rest)
    section = rest[: nxt.start()] if nxt else rest
    block = re.search(r"(?ms)^```text\n(.*?)^```\s*$", section)
    if not block:
        raise UsageError(f"{TEMPLATE_PATH}: '{heading}' has no ```text block")
    return block.group(1)


def read_thresholds(root: Path) -> Optional[Dict[str, str]]:
    """Each bench evaluator's warn_at, as the post says it ("0.8"), or None when one is
    unreadable (D233: the post never names a cutoff the bench did not use)."""
    out: Dict[str, str] = {}
    for name in BENCH_EVALUATORS:
        try:
            spec = _read_json(root / EVALUATOR_PATH.format(name=name))
        except UsageError:
            return None
        outcomes = spec.get("outcomes") if isinstance(spec, dict) else None
        warn_at = outcomes.get("warn_at") if isinstance(outcomes, dict) else None
        if not isinstance(warn_at, (int, float)) or isinstance(warn_at, bool):
            return None
        out[name] = f"{warn_at:g}"
    return out


def read_sources(root: Path, need_tape: bool) -> Sources:
    measured = _read_json(root / MEASURED_PATH) or {}
    claims_doc = _read_json(root / CLAIMS_PATH) or {}
    claims = {
        c["id"]: c for c in claims_doc.get("claims") or [] if isinstance(c, dict) and "id" in c
    }
    src = Sources(
        measured=measured,
        claims=claims,
        manifest=None,
        trace=None,
        thresholds=read_thresholds(root),
    )
    if not need_tape:
        return src
    manifest = _read_json(root / RECORDED_DIR / "manifest.json")
    if not isinstance(manifest, dict):
        src.problems.append(
            f"no recorded tape ({RECORDED_DIR}/manifest.json): run make record-demo"
        )
        return src
    if manifest.get("kind") != "recorded":
        src.problems.append(
            f"{RECORDED_DIR}/manifest.json is kind {manifest.get('kind')!r}, not recorded"
        )
        return src
    # D203: the hosts are the evidence; a tape from mock-upstream is simulated whatever it says.
    try:
        problem = tape_hosts_problem(manifest, vendor_hosts(root))
    except Refused as exc:
        problem = str(exc)
    if problem:
        src.problems.append(problem)
        return src
    src.manifest = manifest
    featured = manifest.get("featured") or {}
    trace_id = featured.get("trace_id") if isinstance(featured, dict) else None
    if not trace_id:
        src.problems.append("the recorded manifest names no featured trace (featured.trace_id)")
        return src
    if not re.fullmatch(r"[0-9a-f-]{36}", str(trace_id)):
        src.problems.append(f"featured.trace_id {trace_id!r} is not a trace id")
        return src
    trace = _read_json(root / RECORDED_DIR / "traces" / f"{trace_id}.json")
    if not isinstance(trace, dict):
        src.problems.append(f"the featured trace {trace_id} is not in {RECORDED_DIR}/traces/")
        return src
    src.trace = trace
    return src


# ------------------------------------------------------------------------------ values


def measured_run(src: Sources) -> bool:
    return src.measured.get("status") == "measured" and src.measured.get("mock") is False


def measured_value(src: Sources, key: str) -> Optional[float]:
    if not measured_run(src):
        return None
    entry = (src.measured.get("values") or {}).get(key) or {}
    value = entry.get("value")
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def show(value: float, unit: str) -> str:
    """A measured value as the README shows it (scripts/check-claims.mjs formatting)."""
    text = format_value(value, unit)
    if unit == "ms":
        return f"{text} ms"
    if unit == "usd_per_1k":
        return f"${text}"
    return text


def _utf16_index(text: str, offset: int) -> int:
    """The str index of a UTF-16 offset (spans are UTF-16, half-open)."""
    units = 0
    for index, char in enumerate(text):
        if units >= offset:
            return index
        units += 2 if ord(char) > 0xFFFF else 1
    return len(text)


def answer_text(trace: Dict[str, Any]) -> str:
    """The answer the claims' spans point into: body.response_text, else the preview."""
    body = trace.get("body") if isinstance(trace.get("body"), dict) else {}
    for text in (body.get("response_text"), trace.get("response_preview")):
        if isinstance(text, str) and text.strip():
            return text
    return ""


def span_sentence(answer: str, span: Dict[str, Any]) -> Optional[str]:
    """The answer's sentence that holds the span's start, or None."""
    start = span.get("start")
    if not answer or not isinstance(start, int) or isinstance(start, bool) or start < 0:
        return None
    at = _utf16_index(answer, start)
    if at >= len(answer):
        return None
    for m in SENTENCE.finditer(answer):
        if m.start() <= at < m.end():
            sentence = m.group(0).strip()
            return sentence or None
    return None


def featured_claim(trace: Dict[str, Any]) -> Tuple[Optional[str], Optional[str]]:
    """(the answer's words, the contradicting evidence) of the first CONTRADICTED claim that has a
    span in the answer (D209's rule), or (None, None). The words are the answer's sentence that
    holds the span; else the quote, when it is a phrase rather than a fragment ("60 days"); else
    the claim."""
    claims = ((trace.get("audit_detail") or {}).get("claims")) or []
    answer = answer_text(trace)
    for claim in claims:
        if not isinstance(claim, dict) or claim.get("status") != "CONTRADICTED":
            continue
        if not isinstance(claim.get("span"), dict):
            continue
        quote = str(claim.get("quote") or "").strip()
        words = span_sentence(answer, claim["span"]) or ""
        if not words and len(quote.split()) >= MIN_QUOTE_WORDS:
            words = quote
        if not words:
            words = str(claim.get("claim") or quote).strip()
        evidence = ""
        for span in claim.get("evidence_spans") or []:
            if isinstance(span, dict) and str(span.get("text") or "").strip():
                evidence = str(span["text"]).strip()
                break
        if not evidence:
            evidence = next(
                (str(e).strip() for e in claim.get("evidence") or [] if str(e).strip()), ""
            )
        return (words.rstrip(".") or None), (evidence.rstrip(".") or None)
    return None, None


def featured_source(trace: Dict[str, Any]) -> Optional[str]:
    """Where the featured answer came from, said plainly (D227), or None when the trace does not
    say. A scripted answer was written for the demo; a generated one names its model."""
    source = trace.get("answer_source")
    if source == "scripted":
        return SCRIPTED_SOURCE
    model = str(trace.get("model") or "").strip()
    if source in ("generated", "live") and model:
        return GENERATED_SOURCE.format(model=model.split("/")[-1])
    return None


def featured_outcome(trace: Dict[str, Any]) -> Optional[str]:
    return OUTCOMES.get(str(trace.get("action") or ""))


def gate_conditions(src: Sources) -> Optional[str]:
    """measured.json sources.gate_latency.conditions (D205), from a measured run only."""
    if not measured_run(src):
        return None
    text = ((src.measured.get("sources") or {}).get("gate_latency") or {}).get("conditions")
    return text.strip() if isinstance(text, str) and text.strip() else None


# How a cost the vendor did not report is said (the README says "Measured tokens × vendor price").
COMPUTED_COSTS = (
    ("fast_check_usd_per_1k", "the fast check's cost is tokens × price"),
    ("llm_same_checks_usd_per_1k", "the judge's cost is tokens × price"),
)
BOTH_COMPUTED = "both costs are tokens × price"


def cost_conditions(src: Sources) -> Optional[str]:
    """measured.json sources.fast_check_usd_per_1k.conditions (D229), from a measured run only;
    a cost computed from tokens × price, not reported by the vendor, says so."""
    if not measured_run(src):
        return None
    sources = src.measured.get("sources") or {}
    text = (sources.get("fast_check_usd_per_1k") or {}).get("conditions")
    if not isinstance(text, str) or not text.strip():
        return None
    computed = [
        words
        for key, words in COMPUTED_COSTS
        if (sources.get(key) or {}).get("cost_source") == "computed"
    ]
    if len(computed) == len(COMPUTED_COSTS):
        # Both computed: said once, so the post keeps its room under MAX_POST_CHARS.
        computed = [BOTH_COMPUTED]
    return "; ".join([text.strip(), *computed])


def judge_name(model: str) -> str:
    """A judge model as a reader knows it: "anthropic/claude-haiku-4.5" is "Claude Haiku 4.5";
    any other model keeps its own id (without the vendor prefix)."""
    bare = model.strip().split("/")[-1]
    m = re.fullmatch(r"claude-([a-z]+)-(\d+)[.-](\d+)(?:-\d{8})?", bare)
    if m:
        return f"Claude {m.group(1).title()} {m.group(2)}.{m.group(3)}"
    return bare


def same_checks_judge(src: Sources) -> Optional[str]:
    """The judge the same-checks ratio compares with (D229), from a measured run only."""
    if not measured_run(src):
        return None
    entry = (src.measured.get("sources") or {}).get("llm_same_checks_usd_per_1k") or {}
    model = entry.get("model")
    return judge_name(model) if isinstance(model, str) and model.strip() else None


def claim_text(src: Sources, claim_id: str) -> Optional[str]:
    claim = src.claims.get(claim_id)
    if not claim or claim.get("label") == "measured" or not claim.get("display"):
        return None  # measured figures come in through {measured:KEY}
    return f"{claim['display']} [{CHIPS.get(claim['label'], claim['label'])}]"


def _interval(src: Sources, key: str, unit: str) -> Optional[Tuple[str, Tuple[int, int]]]:
    """(the 95% interval's words, (n hallucinated, n faithful)) of a measured value, or None."""
    entry = (src.measured.get("intervals") or {}).get(key)
    if not isinstance(entry, dict):
        return None
    lo, hi, n_pos, n_neg = (entry.get(k) for k in ("lo", "hi", "n_pos", "n_neg"))
    if not all(isinstance(v, (int, float)) for v in (lo, hi)) or not all(
        isinstance(v, int) for v in (n_pos, n_neg)
    ):
        return None
    return f"95% interval {show(lo, unit)} to {show(hi, unit)}", (n_pos, n_neg)


def _answers(counts: Tuple[int, int]) -> str:
    return f"{counts[0]} hallucinated and {counts[1]} faithful answers"


def arize_figures(src: Sources) -> Optional[str]:
    """Arize's two figures for Jev with their chips: "76% at 0.5 [third-party], 87% at a tuned
    0.80 [third-party]" (D233), or None when a claim is missing."""
    at_half = claim_text(src, ARIZE_AT_HALF_ID)
    tuned = src.claims.get(ARIZE_TUNED_ID) or {}
    threshold = src.claims.get(ARIZE_THRESHOLD_ID) or {}
    chip = CHIPS.get(str(tuned.get("label")))
    if at_half is None or not chip or not tuned.get("display") or not threshold.get("display"):
        return None
    return f"{at_half}, {tuned['display']} at a tuned {threshold['display']} [{chip}]"


def agreement(src: Sources) -> Tuple[Optional[str], List[str]]:
    """(the paragraph, what is missing). D212, D233: never alone; each figure with its 95%
    interval and n per class (said once when both figures share the sample); the thresholds the
    bench flags at, and where the groundedness one came from; then Arize's figures for Jev, said
    not to be like for like."""
    missing = []
    accuracy = measured_value(src, "ragtruth_qa_accuracy")
    balanced = measured_value(src, "ragtruth_qa_agreement")
    acc_int = _interval(src, "ragtruth_qa_accuracy", "fraction")
    bal_int = _interval(src, "ragtruth_qa_agreement", "fraction")
    thresholds = src.thresholds
    arize = arize_figures(src)
    for name, value in (
        ("ragtruth_qa_accuracy", accuracy),
        ("ragtruth_qa_agreement", balanced),
        ("the interval of ragtruth_qa_accuracy (with n per class)", acc_int),
        ("the interval of ragtruth_qa_agreement (with n per class)", bal_int),
        ("the bench thresholds (warn_at in contracts/evaluators)", thresholds),
        (f"Arize's figures ({ARIZE_AT_HALF_ID}, {ARIZE_TUNED_ID}, {ARIZE_THRESHOLD_ID})", arize),
    ):
        if value is None:
            missing.append(name)
    if missing:
        return None, missing
    # For the type checker: every part is present.
    assert accuracy is not None and balanced is not None and acc_int and bal_int
    assert thresholds is not None and arize is not None
    if acc_int[1] == bal_int[1]:
        sample = f"On a RAGTruth-QA sample ({_answers(acc_int[1])})"
        # The second interval follows the first: "95% interval" is said once.
        acc_words, bal_words = acc_int[0], bal_int[0].replace("95% interval ", "", 1)
    else:
        sample = "On a RAGTruth-QA sample"
        acc_words = f"{acc_int[0]}, {_answers(acc_int[1])}"
        bal_words = f"{bal_int[0]}, {_answers(bal_int[1])}"
    text = (
        f"{sample}, the fast check matched the human labels on {show(accuracy, 'fraction')} of "
        f"answers ({acc_words}) [measured]; balanced accuracy {show(balanced, 'fraction')} "
        f"({bal_words}) [measured]. It flags at groundedness {thresholds['groundedness']} "
        f"(Arize's tuned cutoff on RAGTruth itself, which may favour my figure) or contradiction "
        f"{thresholds['contradiction']}. Arize asked Jev one question, worded differently from "
        f"mine: {arize}. Not like for like; RAGTruth is public, so Jev may have seen it in "
        f"training."
    )
    return text, []


def benchmark_runs(src: Sources) -> List[str]:
    """The benchmark runs measured.json's values come from: its ``runs`` without the load runs
    (the proxy overhead, system ``proxy``, which bench-publish folds in and the post never quotes).
    evals/report.py takes each value from the newest run that measures it, so after a second
    published run the values can come from two runs."""
    values = src.measured.get("values") or {}
    load = {
        str(entry.get("run_id"))
        for entry in values.values()
        if isinstance(entry, dict) and entry.get("system") == "proxy" and entry.get("run_id")
    }
    return [str(r) for r in src.measured.get("runs") or [] if r and str(r) not in load]


def several_runs_problem(src: Sources) -> Optional[str]:
    """Why {run.date} cannot be filled: the post says its figures come from one benchmark run."""
    runs = benchmark_runs(src)
    if len(runs) <= 1:
        return None
    return (
        f"{MEASURED_PATH} takes its values from {len(runs)} benchmark runs ({', '.join(runs)}), "
        "but the post says they come from one benchmark run on {run.date}: publish from one run, "
        f"or reword that sentence in {TEMPLATE_PATH}"
    )


def run_facts(src: Sources) -> Dict[str, Optional[str]]:
    if not measured_run(src):
        return {"run.date": None, "run.ids": None, "snapshot": None}
    generated = str(src.measured.get("generated_at") or "")
    date = generated[:10] if re.match(r"\d{4}-\d{2}-\d{2}", generated) else None
    if several_runs_problem(src):
        date = None
    runs = [str(r) for r in src.measured.get("runs") or [] if r]
    snap = src.measured.get("snapshot")
    if isinstance(snap, list):
        snap_text: Optional[str] = " and ".join(str(s) for s in snap) if snap else None
    else:
        snap_text = str(snap) if snap else None
    return {"run.date": date, "run.ids": ", ".join(runs) or None, "snapshot": snap_text}


# ------------------------------------------------------------------------------ the post


def fill(tpl: str, src: Sources, roles: bool) -> Tuple[str, List[str], List[str]]:
    """(the post, missing values, notes). Raises UsageError on an unknown placeholder."""
    missing: List[str] = list(src.problems)
    notes: List[str] = []
    facts = run_facts(src)
    words, evidence = featured_claim(src.trace) if src.trace else (None, None)
    source = featured_source(src.trace) if src.trace else None
    outcome = featured_outcome(src.trace) if src.trace else None
    if src.trace is not None and words is None:
        missing.append("the featured trace has no CONTRADICTED claim with a span and its evidence")
    if src.trace is not None and source is None:
        missing.append(
            "the featured trace does not say where its answer came from (answer_source, model)"
        )
    if src.trace is not None and outcome is None:
        missing.append(
            f"the featured trace's action {src.trace.get('action')!r} is not flag, block or fallback"
        )

    def value_of(name: str) -> Optional[str]:
        if name == "featured.claim":
            return words
        if name == "featured.evidence":
            return evidence
        if name == "featured.source":
            return source
        if name == "featured.outcome":
            return outcome
        if name == "gate.conditions":
            return gate_conditions(src)
        if name == "cost.conditions":
            return cost_conditions(src)
        if name == "same_checks.judge":
            return same_checks_judge(src)
        if name in facts:
            return facts[name]
        if name.startswith("measured:"):
            key = name.split(":", 1)[1]
            entry = (src.measured.get("values") or {}).get(key)
            if not isinstance(entry, dict):
                raise UsageError(f"{{{name}}} names no value in {MEASURED_PATH}")
            value = measured_value(src, key)
            return None if value is None else show(value, str(entry.get("unit", "")))
        if name.startswith("claim:"):
            claim_id = name.split(":", 1)[1]
            if claim_id not in src.claims:
                raise UsageError(f"{{{name}}} names no claim in {CLAIMS_PATH}")
            text = claim_text(src, claim_id)
            if text is None:
                raise UsageError(f"{{{name}}}: a measured claim goes in as {{measured:KEY}}")
            return text
        raise UsageError(f"unknown placeholder {{{name}}} in {TEMPLATE_PATH}")

    out_lines: List[str] = []
    for line in tpl.splitlines():
        if line.startswith(ROLES_PREFIX):
            if not roles:
                continue
            line = line[len(ROLES_PREFIX) :]
        if line.strip() == "{agreement}":
            paragraph, lacking = agreement(src)
            if paragraph is None:
                notes.append(
                    "the agreement paragraph is left out (the README shows it): missing "
                    + "; ".join(lacking)
                )
                continue
            out_lines.append(paragraph)
            continue

        def sub(m: "re.Match[str]") -> str:
            name = m.group(1)
            if name in ("agreement", "roles"):
                raise UsageError(f"{{{name}}} must stand at the start of its own line")
            value = value_of(name)
            if value is None:
                reason = (several_runs_problem(src) if name == "run.date" else None) or name
                if reason not in missing:
                    missing.append(reason)
                return m.group(0)
            return value

        out_lines.append(PLACEHOLDER.sub(sub, line).rstrip())
    text = "\n".join(out_lines)
    text = re.sub(r"\n{3,}", "\n\n", text).strip() + "\n"
    if not missing:
        check_honesty(text, src)
    return text, missing, notes


def post_length(text: str) -> int:
    """The post's length as LinkedIn counts it (UTF-16 code units: a character outside the BMP,
    an emoji, counts twice)."""
    return len(text.encode("utf-16-le")) // 2


def check_honesty(text: str, src: Sources) -> None:
    """The filled post fits LinkedIn (MAX_POST_CHARS), its opening shows before "...see more",
    and a demo answer is never presented as the output of a support bot the owner runs (D213,
    D227, D229). Raises UsageError."""
    length = post_length(text)
    if length > MAX_POST_CHARS:
        raise UsageError(
            f"the post is {length:,} characters: LinkedIn takes {LINKEDIN_MAX_CHARS:,} and this "
            f"command keeps it within {MAX_POST_CHARS:,}, so a longer value or a last edit still "
            f"fits; cut {length - MAX_POST_CHARS:,} or more from the template in {TEMPLATE_PATH}"
        )
    opening = text.split("\n", 1)[0]
    if post_length(opening) > OPENING_MAX or "TrustAgent" not in opening:
        raise UsageError(
            f"the first line must name TrustAgent within {OPENING_MAX} characters so a phone "
            f"shows it before '...see more' (it has {post_length(opening)}): {opening!r}"
        )
    if re.search(r"\bmy (support )?bot\b", text, re.IGNORECASE):
        raise UsageError(
            "the post says 'my bot': the featured answer comes from the demo, not from a bot the "
            "owner runs; say where it came from with {featured.source} (D227)"
        )


def build(root: Path, restricted: bool, roles: bool) -> Tuple[str, List[str], List[str]]:
    try:
        md = (root / TEMPLATE_PATH).read_text(encoding="utf-8")
    except OSError as exc:
        raise UsageError(f"{TEMPLATE_PATH}: {exc}") from None
    tpl = template(md, RESTRICTED_HEADING if restricted else POST_HEADING)
    names = set(PLACEHOLDER.findall(tpl))
    need_tape = any(n.startswith("featured.") for n in names)
    src = read_sources(root, need_tape=need_tape)
    if any(
        n.startswith(("measured:", "run.", "gate.", "cost.", "same_checks."))
        or n in ("snapshot", "agreement")
        for n in names
    ):
        if not measured_run(src):
            src.problems.insert(
                0,
                f"{MEASURED_PATH} is not a measured run (status {src.measured.get('status')!r}, "
                f"mock {src.measured.get('mock')!r}): make bench-publish first",
            )
    return fill(tpl, src, roles)


def main(
    argv: Optional[Sequence[str]] = None,
    root: Path = ROOT,
    stdout: Optional[TextIO] = None,
    stderr: Optional[TextIO] = None,
    which: clipboard.Which = clipboard.shutil.which,
) -> int:
    out = stdout if stdout is not None else sys.stdout
    err = stderr if stderr is not None else sys.stderr
    parser = argparse.ArgumentParser(prog="linkedin_post.py", description=__doc__.split("\n", 1)[0])
    parser.add_argument(
        "--clip", action="store_true", help="copy it to the Windows clipboard (WSL)"
    )
    parser.add_argument("--roles", action="store_true", help="include the optional roles line")
    parser.add_argument(
        "--restricted", action="store_true", help="the mode-R post (no fast-check figures)"
    )
    parser.add_argument("--root", type=Path, default=root, help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    try:
        text, missing, notes = build(args.root, args.restricted, args.roles)
    except UsageError as exc:
        print(f"linkedin-post: {exc}", file=err)
        return 2
    for note in notes:
        print(f"linkedin-post: note: {note}", file=err)
    if missing:
        print("linkedin-post: not printed, values are missing:", file=err)
        for item in missing:
            print(f"  - {item}", file=err)
        return 1
    if not args.clip:
        out.write(text)
        return 0
    buffer = io.BytesIO()
    try:
        where = clipboard.copy(text.encode("utf-8"), out=buffer, which=which)
    except clipboard.ClipError as exc:
        print(f"linkedin-post: {exc}", file=err)
        return 1
    if where == "stdout":
        out.write(buffer.getvalue().decode("utf-8"))
        print("linkedin-post: no clip.exe here, so the post went to stdout", file=err)
    else:
        print(
            f"linkedin-post: copied the post ({len(text)} characters) to the Windows clipboard",
            file=err,
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
