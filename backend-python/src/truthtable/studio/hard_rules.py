"""
Hard rules of evaluator specs (SPEC section 2.8; D54).

The Go reference (`internal/decision/lint.go`) and the TypeScript port
(`src/lib/decision/lint.ts`) run the hard rules and the lints; the engine
ports only the seven hard-rule codes, so the Studio drafter can ask the judge
to repair a draft that breaks one before the gateway sees it. Codes, paths and
conditions follow lint.ts; `contracts/vectors/lint` pins them.

| Code                          | Fires when                                                     |
|-------------------------------|----------------------------------------------------------------|
| `unresolved_state_reference`  | a backticked reference is neither a declared state field nor `rules.<declared param>` with `rules` declared |
| `no_id_reference`             | question text names a question id (`<id>__v<n>`)               |
| `unknown_risk_weight_option`  | a `risk_weights` key is not an option of the choice question  |
| `risk_level_out_of_range`     | a `risk_levels` value is not below the number of score levels |
| `threshold_order`             | not `escalate_from < warn_at <= critical_at`                   |
| `unknown_evaluator_reference` | a `requires_corroboration`/`implies` id is not in the registry |
| `unknown_detector`            | a `det:` reference names no detector                           |

The rules read the spec defensively: a part with the wrong shape (which the
JSON Schema reports) is skipped rather than raising.
"""

import json
import re
from dataclasses import dataclass
from functools import lru_cache
from typing import Any, FrozenSet, Iterable, List, Mapping, Optional, Sequence, Tuple

from ..contracts import contract_path

UNRESOLVED_STATE_REFERENCE = "unresolved_state_reference"
NO_ID_REFERENCE = "no_id_reference"
UNKNOWN_RISK_WEIGHT_OPTION = "unknown_risk_weight_option"
RISK_LEVEL_OUT_OF_RANGE = "risk_level_out_of_range"
THRESHOLD_ORDER = "threshold_order"
UNKNOWN_EVALUATOR_REFERENCE = "unknown_evaluator_reference"
UNKNOWN_DETECTOR = "unknown_detector"

ERROR_CODES: Tuple[str, ...] = (
    UNRESOLVED_STATE_REFERENCE,
    NO_ID_REFERENCE,
    UNKNOWN_RISK_WEIGHT_OPTION,
    RISK_LEVEL_OUT_OF_RANGE,
    THRESHOLD_ORDER,
    UNKNOWN_EVALUATOR_REFERENCE,
    UNKNOWN_DETECTOR,
)

DETECTOR_RULES = "rules/detectors.v1.json"

# Float tolerance of the threshold comparisons (lint.ts EPS).
EPS = 1e-9

# ASCII word boundaries, as in ECMAScript and RE2.
_QUESTION_ID = re.compile(r"\b[a-z][a-z0-9_]{1,47}__v[0-9]+\b", re.ASCII)
_BACKTICKED = re.compile(r"`([^`]*)`")


@dataclass(frozen=True)
class RuleError:
    """One broken hard rule: its code, where (lint.ts path) and why."""

    code: str
    path: str
    message: str

    def __str__(self) -> str:
        return f"{self.path or '(root)'}: {self.message} [{self.code}]"


@lru_cache(maxsize=1)
def detector_ids() -> FrozenSet[str]:
    """The detector ids of contracts/rules/detectors.v1.json."""
    with open(contract_path(DETECTOR_RULES), encoding="utf-8") as f:
        return frozenset(json.load(f)["detectors"])


def _number(value: Any) -> Optional[float]:
    """`value` as a number; None for anything else (booleans included)."""
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return float(value)
    return None


def _show(value: Any) -> str:
    """A number as JSON writes it (0.6, not 0.6000000000000001; 1, not 1.0 for an int)."""
    return json.dumps(value)


def _mapping(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _strings(value: Any) -> List[str]:
    return [item for item in value if isinstance(item, str)] if isinstance(value, list) else []


def _texts(value: Any, path: str, out: List[Tuple[str, str]]) -> List[Tuple[str, str]]:
    """Every string of a text tree (strings, arrays, objects) in document order, with its path."""
    if isinstance(value, str):
        out.append((path, value))
    elif isinstance(value, list):
        for index, item in enumerate(value):
            _texts(item, f"{path}[{index}]", out)
    elif isinstance(value, Mapping):
        for key, item in value.items():
            _texts(item, f"{path}.{key}", out)
    return out


def hard_rule_errors(
    spec: Any,
    *,
    registry: Optional[Iterable[str]] = None,
    detectors: Optional[Iterable[str]] = None,
) -> List[RuleError]:
    """
    The hard rules `spec` breaks, in lint.ts order.

    Args:
        spec: An evaluator.v1 spec or evaluator-draft.v1 draft (parsed JSON).
        registry: Evaluator ids that requires_corroboration and implies may
            name; None skips that check (as lint.ts without a registry).
        detectors: Detector ids for `det:` references (default: the contracts rules).
    """
    errors: List[RuleError] = []
    if not isinstance(spec, Mapping):
        return errors
    state_fields = _strings(spec.get("state_fields"))
    params = _mapping(spec.get("params"))
    question = _mapping(spec.get("question"))
    question_type = question.get("type")
    criteria = question.get("criteria")
    question_texts = _texts(question.get("instructions"), "question.instructions", [])
    _texts(criteria, "question.criteria", question_texts)

    for path, text in question_texts:
        for match in _BACKTICKED.finditer(text):
            ref = match.group(1)
            if ref.startswith("rules."):
                ok = "rules" in state_fields and ref[len("rules.") :] in params
            else:
                ok = ref in state_fields
            if not ok:
                errors.append(
                    RuleError(
                        UNRESOLVED_STATE_REFERENCE,
                        path,
                        f"`{ref}` is neither a declared state field nor rules.<declared param>",
                    )
                )
    for path, text in question_texts:
        found = _QUESTION_ID.search(text)
        if found is not None:
            errors.append(
                RuleError(
                    NO_ID_REFERENCE,
                    path,
                    f"mentions the question id {found.group(0)}; questions are answered "
                    "independently",
                )
            )

    signal = _mapping(spec.get("signal"))
    risk_weights = signal.get("risk_weights")
    if question_type == "choice" and isinstance(risk_weights, Mapping):
        options = set(criteria) if isinstance(criteria, Mapping) else set()
        for key in risk_weights:
            if key not in options:
                errors.append(
                    RuleError(
                        UNKNOWN_RISK_WEIGHT_OPTION,
                        f"signal.risk_weights.{key}",
                        f"{key} is not an option of the question",
                    )
                )
    risk_levels = signal.get("risk_levels")
    if question_type == "score" and isinstance(risk_levels, list) and isinstance(criteria, list):
        for level in risk_levels:
            number = _number(level)
            if number is not None and number >= len(criteria):
                errors.append(
                    RuleError(
                        RISK_LEVEL_OUT_OF_RANGE,
                        "signal.risk_levels",
                        f"level {_show(level)} does not exist: the question has {len(criteria)} levels",
                    )
                )

    outcomes = _mapping(spec.get("outcomes"))
    escalation = _mapping(spec.get("escalation"))
    warn_at = _number(outcomes.get("warn_at"))
    critical_at = _number(outcomes.get("critical_at"))
    escalate_from = _number(escalation.get("escalate_from"))
    if warn_at is not None:
        if escalate_from is not None and not escalate_from < warn_at - EPS:
            errors.append(
                RuleError(
                    THRESHOLD_ORDER,
                    "escalation.escalate_from",
                    f"escalate_from {_show(escalation.get('escalate_from'))} must be below "
                    f"warn_at {_show(outcomes.get('warn_at'))}",
                )
            )
        if critical_at is not None and not warn_at <= critical_at + EPS:
            errors.append(
                RuleError(
                    THRESHOLD_ORDER,
                    "outcomes.critical_at",
                    f"critical_at {_show(outcomes.get('critical_at'))} must be at least "
                    f"warn_at {_show(outcomes.get('warn_at'))}",
                )
            )

    known = frozenset(registry) if registry is not None else None
    detector_set = frozenset(detectors) if detectors is not None else detector_ids()
    references: Sequence[Tuple[str, List[str]]] = (
        ("outcomes.requires_corroboration", _strings(outcomes.get("requires_corroboration"))),
        ("outcomes.implies", _strings(outcomes.get("implies"))),
    )
    for path, ids in references:
        for ref in ids:
            if ref.startswith("det:"):
                if ref[len("det:") :] not in detector_set:
                    errors.append(RuleError(UNKNOWN_DETECTOR, path, f"{ref} is not a detector"))
                continue
            if known is not None and ref not in known:
                errors.append(
                    RuleError(UNKNOWN_EVALUATOR_REFERENCE, path, f"{ref} is not a known evaluator")
                )
    return errors
