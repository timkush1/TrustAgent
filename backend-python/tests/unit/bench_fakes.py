"""
Fakes for the benchmark harness tests (evals/): a fake gate-eval executable,
gate-eval output lines, a scripted priced judge, and example builders.

The fake gate-eval is a real executable Python script, so GateJudge's
subprocess plumbing (stdin/stdout JSON Lines, exit codes, stderr, timeouts)
runs for real without Go, Jev or the network. It reads its behaviour from the
JSON file named by its `--fake-config=<path>` argument, and it checks each
input line the way backend-go/cmd/gate-eval does (unknown members are an error
for that line; a response is required), so the harness cannot drift from the
Go CLI's input format unnoticed.
"""

import json
import stat
import sys
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional, Sequence

from evals.examples import LEVEL_RESPONSE, BenchExample
from truthtable.providers.base import (
    CompletionRequest,
    CompletionResponse,
    LLMProvider,
    normalized_usage,
)

FAKE_GATE_EVAL = """#!{python}
import json
import os
import sys
import time

args = sys.argv[1:]
config_path = next(arg.split("=", 1)[1] for arg in args if arg.startswith("--fake-config="))
with open(config_path, encoding="utf-8") as f:
    config = json.load(f)
with open(config["argv_log"], "a", encoding="utf-8") as f:
    f.write(json.dumps(args) + "\\n")
with open(config["env_log"], "a", encoding="utf-8") as f:
    seen = {{name: os.environ.get(name) for name in config.get("env_names", [])}}
    f.write(json.dumps(seen) + "\\n")
if config.get("sleep"):
    time.sleep(config["sleep"])
if config.get("exit"):
    sys.stderr.write(config.get("stderr", "fatal"))
    sys.exit(config["exit"])
# The members backend-go/cmd/gate-eval reads (example.go exampleLine).
MEMBERS = {{
    "id", "query", "user_request", "response", "answer", "system_instructions", "context",
    "evidence", "rules", "max_marker", "scenario", "labels", "label_hallucinated",
    "label_source", "domain", "meta",
}}
EVIDENCE_MEMBERS = {{"id", "source", "text", "untrusted"}}
number = 0
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    number += 1
    item = json.loads(line)
    with open(config["input_log"], "a", encoding="utf-8") as f:
        f.write(line + "\\n")
    unknown = sorted(set(item) - MEMBERS)
    for evidence in item.get("evidence") or []:
        if isinstance(evidence, dict):
            unknown += sorted(set(evidence) - EVIDENCE_MEMBERS)
    if unknown or ("response" not in item and "answer" not in item):
        error = "invalid example: " + (
            f"unknown field {{unknown[0]!r}}" if unknown else "response is required"
        )
        sys.stdout.write(json.dumps({{"id": item.get("id"), "line": number, "error": error,
                                     "skipped": None, "decision": None}}) + "\\n")
        continue
    answer = config["answers"].get(item["id"], config.get("default"))
    if answer is None:
        continue
    sys.stdout.write(json.dumps(dict(id=item["id"], line=number, **answer)) + "\\n")
if config.get("exit_after"):
    sys.exit(config["exit_after"])
"""


class FakeGateEval:
    """A fake gate-eval binary in `directory`, configured through a JSON file."""

    def __init__(self, directory: Path) -> None:
        self.directory = directory
        self.binary = directory / "gate-eval"
        self.binary.write_text(FAKE_GATE_EVAL.format(python=sys.executable), encoding="utf-8")
        self.binary.chmod(self.binary.stat().st_mode | stat.S_IXUSR)
        self.config_path = directory / "fake-gate-eval.json"
        self.argv_log = directory / "argv.jsonl"
        self.input_log = directory / "input.jsonl"
        self.env_log = directory / "env.jsonl"
        self.config: Dict[str, Any] = {
            "argv_log": str(self.argv_log),
            "input_log": str(self.input_log),
            "env_log": str(self.env_log),
            "env_names": ["TRUTHTABLE_POLICY_FILE"],
            "answers": {},
            "default": None,
        }
        self.save()

    @property
    def arg(self) -> str:
        return f"--fake-config={self.config_path}"

    def save(self) -> None:
        self.config_path.write_text(json.dumps(self.config), encoding="utf-8")

    def answer(self, example_id: str, **answer: Any) -> None:
        self.config["answers"][example_id] = answer
        self.save()

    def default(self, **answer: Any) -> None:
        self.config["default"] = answer
        self.save()

    def set(self, **options: Any) -> None:
        self.config.update(options)
        self.save()

    def argv(self) -> List[List[str]]:
        if not self.argv_log.exists():
            return []
        return [json.loads(line) for line in self.argv_log.read_text().splitlines()]

    def envs(self) -> List[Dict[str, Optional[str]]]:
        """The watched environment variables each invocation saw."""
        if not self.env_log.exists():
            return []
        return [json.loads(line) for line in self.env_log.read_text().splitlines()]

    def inputs(self) -> List[Dict[str, Any]]:
        if not self.input_log.exists():
            return []
        return [json.loads(line) for line in self.input_log.read_text().splitlines()]


def gate_line(
    *,
    groundedness: Optional[float] = 0.9,
    contradiction: Optional[float] = 0.2,
    verdict: Optional[str] = "unsupported",
    would_action: str = "flag",
    status: str = "ok",
    deep_audit: str = "high",
    backend: str = "openrouter-decisions",
    snapshot: Optional[str] = "typesafe/jev-1.13-20260917",
    latency_ms: Optional[float] = 180,
    call_latency_ms: Optional[float] = 150,
    cost_usd: float = 0.00004,
    cost_source: str = "reported",
    max_risk: Optional[float] = None,
    skip_reason: Optional[str] = None,
    decision_model: bool = True,
    endpoint_host: Optional[str] = "openrouter.ai",
    connection: str = "warm",
    error_reason: Optional[str] = None,
) -> Dict[str, Any]:
    """
    A gate-eval output line (backend-go/cmd/gate-eval result) with the fields
    the harness reads; `id` and `line` are added by the fake. A real line has
    more (detectors, excluded, compile, ...).
    """

    def evaluator(risk: Optional[float]) -> Dict[str, Any]:
        if risk is None:
            return {"status": "error", "reason": error_reason, "risk_raw": None, "risk": None}
        return {"status": "assessed", "reason": None, "risk_raw": risk, "risk": risk}

    evaluators = {}
    if groundedness is not None or status == "ok":
        evaluators["groundedness@1"] = evaluator(groundedness)
    evaluators["contradiction@1"] = evaluator(contradiction)
    known = [value for value in (groundedness, contradiction) if value is not None]
    usage = {
        "backend": backend,
        "model_requested": "typesafe/jev-1.13",
        "snapshot": snapshot,
        "calls": 1,
        "attempts": 1,
        "hedged": False,
        "latency_ms": call_latency_ms,
        "input_tokens": 950,
        "cost_usd": cost_usd,
        "cost_source": cost_source,
    }
    return {
        "error": None,
        "skipped": None,
        "simulated": backend == "mock",
        "endpoint_host": None if backend == "mock" else endpoint_host,
        "connection": "cold" if backend == "mock" else connection,
        "route": "default",
        "mode": "flag",
        "decision": {
            "status": status,
            "skip_reason": skip_reason,
            "effective_mode": "flag",
            "action": "flag" if would_action != "pass" else "pass",
            "would_action": would_action,
            "enforced": True,
            "verdict": verdict,
            "max_risk": max_risk if max_risk is not None else (max(known) if known else None),
            "calibration": "none",
            "degraded": None if status == "ok" else "error",
            "disagreement": False,
            "deep_audit": deep_audit,
            "reasons": [],
            "evaluators": evaluators,
        },
        "detectors": [],
        "excluded": [],
        "compile": None,
        "decision_model": usage if decision_model else None,
        "cost_usd": cost_usd if decision_model else 0.0,
        "latency_ms": latency_ms,
    }


class ScriptedJudge(LLMProvider):
    """
    A priced judge: `answer(request)` gives the content (an Exception is
    raised). Usage is `tokens` in / `output_tokens` out, priced from the
    price table like a real provider (openrouter anthropic/claude-haiku-4.5:
    $1 / $5 per million tokens).
    """

    provider_name = "openrouter"
    base_url = "https://openrouter.ai"

    def __init__(
        self,
        answer: Callable[[CompletionRequest], Any],
        *,
        model: str = "anthropic/claude-haiku-4.5",
        tokens: int = 1000,
        output_tokens: int = 50,
        reported_cost_usd: Optional[float] = None,
    ) -> None:
        super().__init__(model=model)
        self.answer = answer
        self.tokens = tokens
        self.output_tokens = output_tokens
        self.reported_cost_usd = reported_cost_usd
        self.requests: List[CompletionRequest] = []

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.requests.append(request)
        content = self.answer(request)
        if isinstance(content, Exception):
            raise content
        if not isinstance(content, str):
            content = json.dumps(content)
        return self.priced(
            CompletionResponse(
                content=content,
                model=self.model,
                finish_reason="stop",
                usage=normalized_usage(self.tokens, self.output_tokens),
                provider=self.provider_name,
            ),
            reported_cost_usd=self.reported_cost_usd,
        )

    async def health_check(self) -> bool:
        return True


def example(
    example_id: str,
    *,
    hallucinated: bool = True,
    query: str = "What is the capital of France?",
    response: str = "Lyon is the capital of France.",
    context: Sequence[str] = ("Paris is the capital and largest city of France.",),
    stratum: str = "",
    level: str = LEVEL_RESPONSE,
) -> BenchExample:
    return BenchExample(
        id=example_id,
        dataset="synthetic",
        level=level,
        query=query,
        response=response,
        context=tuple(context),
        label_hallucinated=hallucinated,
        stratum=stratum or f"{int(hallucinated)}|m",
    )
