"""
Evaluator Studio "Draft with AI" (SPEC sections 3.2 and 7.3; P4-PLAN D34, D54).

The gateway's `POST /api/studio/draft` reaches the engine's DraftEvaluator,
which asks the judge to turn a plain-English description into one
evaluator-draft.v1 draft with `contracts/prompts/evaluator-drafter.v1.json`:

1. The prompt's system text, then its user text with the description, route
   and existing evaluator ids filled in. The user's text is data: hidden
   characters are stripped, every value is capped at its `max_chars`, and a
   data tag inside a value is escaped, so it cannot close its block and
   inject instructions.
2. The answer must be one JSON object (code fences are tolerated) that passes
   the evaluator-draft.v1 JSON Schema (draft 2020-12) and the seven hard
   rules (studio/hard_rules.py), with the request's existing ids as the only
   evaluators it may reference.
3. Otherwise the judge gets at most one repair turn (`call.max_repairs`): its
   previous answer and every error, one per line as `<path>: <message>`.

The result carries the last draft (even an invalid one, with its errors: the
gateway validates again and never saves or attaches a draft) and one
`studio_draft` cost record for all attempts. Without a judge it is
`NoJudgeError` (FAILED_PRECONDITION `no_judge`).

The mock judge (simulated checks) answers with the prompt's first example
draft at no cost, labelled `simulated`.
"""

import json
import re
from dataclasses import dataclass, field
from functools import lru_cache
from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Tuple

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError, best_match
from referencing import Registry, Resource

from ..contracts import load_contract
from ..providers.base import CompletionRequest, LLMProvider, Message, MessageRole
from ..providers.costs import STUDIO_DRAFT, CallCost, combine_calls, complete_recorded, cost_ledger
from ..providers.limiter import unwrap
from ..providers.mock import MockLLMProvider, mock_answer_scope
from ..providers.swappable import SwappableProvider
from ..security import sanitize_text, strip_code_fences
from .hard_rules import hard_rule_errors

DRAFTER_PROMPT = "prompts/evaluator-drafter.v1.json"
DRAFT_SCHEMA = "schema/evaluator-draft.v1.schema.json"
EVALUATOR_SCHEMA = "schema/evaluator.v1.schema.json"

# The data blocks of the user text; their tags are escaped inside values.
DATA_TAGS = ("description", "route_name", "route_description", "existing_evaluators")
_DATA_TAG = re.compile(r"<\s*(/?)\s*(" + "|".join(DATA_TAGS) + r")\s*>", re.IGNORECASE)

_PLACEHOLDER = re.compile(r"\{\{([a-z_]+)\}\}")
_EVALUATOR_ID = re.compile(r"^[a-z][a-z0-9_]{1,47}$")

# Errors listed in one repair turn (the rest are summarised).
MAX_LISTED_ERRORS = 20
MAX_ERROR_CHARS = 300


class NoJudgeError(RuntimeError):
    """The engine has no judge to draft with."""


@dataclass(frozen=True)
class DraftRequest:
    """DraftEvaluatorRequest."""

    description: str
    route_name: str = ""
    route_description: str = ""
    existing_ids: Sequence[str] = ()
    model_override: str = ""


@dataclass
class DraftResult:
    """
    DraftEvaluatorResponse.

    Attributes:
        spec_json: The last draft as compact JSON ("" when no answer was a JSON object).
        errors: Its schema and hard-rule errors (empty: the draft is valid).
        cost: One studio_draft record for every attempt (None: no call was made).
        model: The model that answered.
        attempts: Judge calls made (1, or 2 with the repair turn).
    """

    spec_json: str
    errors: List[str] = field(default_factory=list)
    cost: Optional[CallCost] = None
    model: str = ""
    attempts: int = 0

    @property
    def valid(self) -> bool:
        return bool(self.spec_json) and not self.errors

    @property
    def simulated(self) -> bool:
        return self.cost is not None and self.cost.cost_source == "simulated"


@lru_cache(maxsize=1)
def drafter_prompt() -> Mapping[str, Any]:
    """contracts/prompts/evaluator-drafter.v1.json."""
    prompt: Mapping[str, Any] = load_contract(DRAFTER_PROMPT)
    return prompt


@lru_cache(maxsize=1)
def draft_validator() -> Draft202012Validator:
    """A draft 2020-12 validator of evaluator-draft.v1 (with evaluator.v1 for its $refs)."""
    draft = load_contract(DRAFT_SCHEMA)
    evaluator = load_contract(EVALUATOR_SCHEMA)
    registry: Registry = Registry().with_resources(
        [
            (draft["$id"], Resource.from_contents(draft)),
            (evaluator["$id"], Resource.from_contents(evaluator)),
        ]
    )
    Draft202012Validator.check_schema(draft)
    return Draft202012Validator(draft, registry=registry)


def json_path(parts: Iterable[Any]) -> str:
    """A jsonschema error location as a lint.ts-style path (`a.b[0].c`; "" for the root)."""
    path = ""
    for part in parts:
        if isinstance(part, int):
            path += f"[{part}]"
        else:
            path += f".{part}" if path else str(part)
    return path


def _schema_message(error: ValidationError) -> str:
    # A oneOf/anyOf failure says little on its own; its closest branch says what to fix.
    if error.context:
        closest = best_match(error.context)
        if closest is not None:
            error = closest
    message = error.message
    if len(message) > MAX_ERROR_CHARS:
        message = message[: MAX_ERROR_CHARS - 3] + "..."
    return f"{json_path(error.absolute_path) or '(root)'}: {message}"


def schema_errors(document: Any) -> List[str]:
    """Every evaluator-draft.v1 schema error of `document`, ordered by path."""
    errors = sorted(draft_validator().iter_errors(document), key=lambda e: list(map(str, e.path)))
    return [_schema_message(error) for error in errors]


def parse_draft(text: str) -> Tuple[Optional[Dict[str, Any]], List[str]]:
    """
    The JSON object of an answer and its parse error, if any. Code fences and
    text around one object are tolerated; anything else is an error.
    """
    stripped = strip_code_fences(text)
    candidates = [stripped]
    start, end = stripped.find("{"), stripped.rfind("}")
    if 0 <= start < end and (start, end) != (0, len(stripped) - 1):
        candidates.append(stripped[start : end + 1])
    error = "the answer is empty"
    for candidate in candidates:
        if not candidate:
            continue
        try:
            document = json.loads(candidate)
        except json.JSONDecodeError as exc:
            error = (
                f"the answer is not valid JSON ({exc.msg} at line {exc.lineno} column {exc.colno})"
            )
            continue
        if not isinstance(document, dict):
            return None, [f"(root): expected one JSON object, got {type(document).__name__}"]
        return document, []
    return None, [f"(root): {error}"]


def check_draft(document: Any, existing_ids: Iterable[str]) -> List[str]:
    """The schema errors of a draft, then its hard-rule errors (existing ids as the registry)."""
    errors = schema_errors(document)
    errors.extend(str(e) for e in hard_rule_errors(document, registry=existing_ids))
    return errors


def _escape_tags(text: str) -> str:
    return _DATA_TAG.sub(
        lambda m: f"&lt;{m.group(1)}{m.group(2)}&gt;",
        text,
    )


def _value(raw: str, spec: Mapping[str, Any]) -> str:
    """One user-supplied variable: sanitized, capped, its data tags escaped."""
    text = sanitize_text(raw or "", max_chars=int(spec.get("max_chars", 2000))).strip()
    if not text:
        return str(spec.get("empty", ""))
    return _escape_tags(text)


def _render(lines: Sequence[str], values: Mapping[str, str]) -> str:
    # One pass: a value that itself contains {{name}} is not expanded again.
    return _PLACEHOLDER.sub(lambda m: values.get(m.group(1), m.group(0)), "\n".join(lines))


def existing_evaluator_ids(ids: Iterable[str]) -> List[str]:
    """The well-formed evaluator ids of a request, de-duplicated in order."""
    seen: List[str] = []
    for raw in ids:
        evaluator_id = raw.strip()
        if _EVALUATOR_ID.match(evaluator_id) and evaluator_id not in seen:
            seen.append(evaluator_id)
    return seen


def render_user_text(request: DraftRequest, existing_ids: Sequence[str]) -> str:
    """The drafter prompt's user text for `request`."""
    prompt = drafter_prompt()
    variables: Mapping[str, Mapping[str, Any]] = prompt["variables"]
    values = {
        "description": _value(request.description, variables["description"]),
        "route_name": _value(request.route_name, variables["route_name"]),
        "route_description": _value(request.route_description, variables["route_description"]),
        "existing_ids": _value(", ".join(existing_ids), variables["existing_ids"]),
    }
    return _render(prompt["user"], values)


def render_repair_text(errors: Sequence[str]) -> str:
    """The repair turn listing `errors`, capped at the prompt's `errors.max_chars`."""
    prompt = drafter_prompt()
    listed = list(errors[:MAX_LISTED_ERRORS])
    if len(errors) > MAX_LISTED_ERRORS:
        listed.append(f"... and {len(errors) - MAX_LISTED_ERRORS} more")
    text = sanitize_text(
        "\n".join(listed), max_chars=int(prompt["variables"]["errors"]["max_chars"])
    )
    return _render(prompt["repair"], {"errors": text})


def simulated_draft() -> str:
    """What the mock judge answers: the prompt's first example draft."""
    return json.dumps(drafter_prompt()["examples"][0]["output"], ensure_ascii=False)


class EvaluatorDrafter:
    """Drafts evaluators through the judge (see the module docstring)."""

    def __init__(self, prompt: Optional[Mapping[str, Any]] = None) -> None:
        self.prompt = prompt if prompt is not None else drafter_prompt()
        call = self.prompt["call"]
        self.temperature = float(call["temperature"])
        self.max_tokens = int(call["max_tokens"])
        self.max_repairs = int(call.get("max_repairs", 1))

    async def draft(self, provider: Optional[LLMProvider], request: DraftRequest) -> DraftResult:
        """
        Draft one evaluator (see the module docstring).

        Raises:
            NoJudgeError: Without a judge.
            ProviderError (or another provider exception): When the first call
                fails. A failing repair call returns the first draft instead.
        """
        judge = unwrap(provider) if provider is not None else None
        if provider is None or judge is None:
            raise NoJudgeError("no judge is configured")
        if isinstance(judge, SwappableProvider) and judge.current is None:
            raise NoJudgeError("no judge is configured")
        existing_ids = existing_evaluator_ids(request.existing_ids)
        model = request.model_override.strip() or provider.model
        messages = [
            Message(MessageRole.SYSTEM, "\n".join(self.prompt["system"])),
            Message(MessageRole.USER, render_user_text(request, existing_ids)),
        ]
        result = DraftResult(spec_json="", model=model)
        with cost_ledger() as ledger:
            for attempt in range(1 + self.max_repairs):
                call = CompletionRequest(
                    messages=list(messages),
                    model=model,
                    temperature=self.temperature,
                    max_tokens=self.max_tokens,
                )
                try:
                    if isinstance(judge, MockLLMProvider):
                        with mock_answer_scope(simulated_draft()):
                            response = await complete_recorded(provider, call, STUDIO_DRAFT)
                    else:
                        response = await complete_recorded(provider, call, STUDIO_DRAFT)
                except Exception:
                    if attempt == 0:
                        raise
                    break  # keep the first draft and its errors; the call is booked
                result.attempts = attempt + 1
                result.model = response.model or model
                document, errors = parse_draft(response.content)
                if document is not None:
                    errors = check_draft(document, existing_ids)
                    result.spec_json = json.dumps(
                        document, ensure_ascii=False, separators=(",", ":")
                    )
                elif result.spec_json:
                    # The repair broke the JSON: keep the earlier draft and its errors.
                    break
                if errors and response.truncated:
                    errors.append(
                        "(root): the answer was cut off at the token limit; keep every field "
                        "short so the whole object fits"
                    )
                result.errors = errors
                if not errors:
                    break
                messages.append(Message(MessageRole.ASSISTANT, response.content))
                messages.append(Message(MessageRole.USER, render_repair_text(errors)))
        result.cost = combine_calls(ledger.calls, STUDIO_DRAFT)
        return result
