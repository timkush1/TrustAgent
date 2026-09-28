"""
Scoring v2: from per-claim verdicts to an audit's scores, verdict and status.

SPEC section 3.2 (scoring/aggregate.py) and section 2.2 (status rules); it
fixes D1 (zero claims), D3 (CONTRADICTED and UNSUPPORTED scored alike), D4 (a
judge outage looked like a normal score) and D5 (one contradiction among
supported claims went unflagged).

**Per claim.** Every decided claim (SUPPORTED, PARTIALLY_SUPPORTED,
CONTRADICTED or UNSUPPORTED) carries a distribution (S, P, C, U) over the four
verdicts that sums to 1: its `verdict_probabilities` when the verifier recorded
them, otherwise the LLM default, P(verdict) = confidence with the rest split
evenly over the other three (maximum entropy). With the partial weight w
(TRUTHTABLE_SCORING_PARTIAL_WEIGHT, 0.5):

    s = S + w*P      support
    c = C            contradiction
    u = U + (1-w)*P  unverified          (s + c + u = 1)

UNKNOWN (judge or parse failure) and NOT_CHECKED (nothing to check against)
claims are excluded from every aggregate.

**Aggregates** over the n decided claims:

| Score              | Definition                                                   |
|--------------------|--------------------------------------------------------------|
| faithfulness F     | mean(s)                                                      |
| contradiction_rate | K = mean(c)                                                  |
| unverified_rate    | V = mean(u)                                                  |
| coverage           | 1 - V                                                        |
| accuracy_addressed | sum(s) / (sum(s) + sum(c)); absent when that is below 1e-6   |
| max_contradiction  | max(c)                                                       |
| p_hallucinated     | max(c + u) under `hallucination`, max(c) under `unverifiable` |
| trust_score        | clamp(F - K) under `hallucination`, accuracy_addressed under `unverifiable` |

**Policy.** `unsupported_policy` says what UNSUPPORTED means: `hallucination`
(the request carried the grounding, so an unsupported claim is baseless) or
`unverifiable` (the evidence was retrieved after the fact, so an unsupported
claim is only unchecked). `auto` resolves to `hallucination` when any evidence
document came from the request, otherwise `unverifiable`.

**Flags and verdict.** HALLUCINATED when max(c) >= 0.35 (contradiction), or,
under `hallucination`, when max(u) >= 0.60 or V >= 0.30 (baseless content);
otherwise UNVERIFIABLE under `unverifiable` when coverage < 0.5; otherwise
GROUNDED. An answer without claims is NO_CLAIMS; one whose claims were all
NOT_CHECKED is NOT_ASSESSED. Thresholds compare with a 1e-9 tolerance.

**Status** (SPEC section 2.2): FAILED with error_kind `judge_unavailable` and
no scores when UNKNOWN claims reach `max_unknown_fraction` (0.5) of the checked
(not NOT_CHECKED) claims; DEGRADED when any claim is UNKNOWN or a degraded
reason was recorded; COMPLETED otherwise. Scores are never invented: a score
that cannot be computed is absent (None), and a failed audit has none.
"""

import math
from dataclasses import asdict, dataclass
from typing import Any, Dict, Iterable, List, Mapping, Optional, Protocol, Sequence, Tuple

from ..graphs.state import (
    DECIDED_STATUSES,
    AuditOutcome,
    AuditVerdict,
    UnsupportedPolicy,
    VerificationStatus,
    coerce_status,
)

SCORING_VERSION = "v2"

# Threshold comparisons use this tolerance (SPEC section 2.1 float determinism).
EPS = 1e-9

# accuracy_addressed is absent when sum(s) + sum(c) is below this.
MIN_ADDRESSED_MASS = 1e-6

JUDGE_UNAVAILABLE = "judge_unavailable"

# Degraded reasons (proto AuditResult.degraded_reasons), in reporting order.
DECOMPOSER_FALLBACK = "decomposer_fallback"
UNKNOWN_CLAIMS = "unknown_claims"
CONTEXT_TRIMMED = "context_trimmed"
JUDGE_RETRY_EXHAUSTED = "judge_retry_exhausted"
DEGRADED_REASON_ORDER: Tuple[str, ...] = (
    DECOMPOSER_FALLBACK,
    UNKNOWN_CLAIMS,
    CONTEXT_TRIMMED,
    JUDGE_RETRY_EXHAUSTED,
)

# Keys of a claim's verdict distribution (ClaimVerification.verdict_probabilities).
SUPPORTED_KEY = "supported"
PARTIAL_KEY = "partially_supported"
CONTRADICTED_KEY = "contradicted"
UNSUPPORTED_KEY = "unsupported"
PROBABILITY_KEYS: Tuple[str, ...] = tuple(
    sorted((SUPPORTED_KEY, PARTIAL_KEY, CONTRADICTED_KEY, UNSUPPORTED_KEY))
)

_STATUS_KEY: Dict[VerificationStatus, str] = {
    VerificationStatus.SUPPORTED: SUPPORTED_KEY,
    VerificationStatus.PARTIALLY_SUPPORTED: PARTIAL_KEY,
    VerificationStatus.CONTRADICTED: CONTRADICTED_KEY,
    VerificationStatus.UNSUPPORTED: UNSUPPORTED_KEY,
}

# ClaimVerification.probability_source of the LLM default distribution.
LLM_CONFIDENCE = "llm_confidence"

# Evidence sources that are not the audited request's own evidence (proto
# ContextDocument.source). A caller document without a source is request evidence.
NON_REQUEST_SOURCES = frozenset({"knowledge", "upload"})

GRADES: Tuple[str, ...] = ("A", "B", "C", "D")
FAIL_GRADE = "F"


class ScoringSettings(Protocol):
    """The Settings fields scoring reads (truthtable.config.Settings)."""

    scoring_partial_weight: float
    scoring_contradiction_flag: float
    scoring_unsupported_flag: float
    scoring_unsupported_fraction_flag: float
    scoring_min_coverage: float
    scoring_max_unknown_fraction: float

    @property
    def grade_cutoffs(self) -> Tuple[float, ...]:
        ...


@dataclass(frozen=True)
class ScoringConfig:
    """Scoring thresholds (the TRUTHTABLE_SCORING_* keys; defaults are the registry's)."""

    partial_weight: float = 0.5
    contradiction_flag: float = 0.35
    unsupported_flag: float = 0.60
    unsupported_fraction_flag: float = 0.30
    min_coverage: float = 0.5
    max_unknown_fraction: float = 0.5
    grade_cutoffs: Tuple[float, ...] = (0.9, 0.7, 0.5, 0.3)

    def __post_init__(self) -> None:
        for name in (
            "partial_weight",
            "contradiction_flag",
            "unsupported_flag",
            "unsupported_fraction_flag",
            "min_coverage",
            "max_unknown_fraction",
        ):
            value = getattr(self, name)
            if not 0.0 <= value <= 1.0:
                raise ValueError(f"{name} must be in [0, 1], not {value!r}")
        cutoffs = self.grade_cutoffs
        if len(cutoffs) != len(GRADES) or any(a < b for a, b in zip(cutoffs, cutoffs[1:])):
            raise ValueError(f"grade_cutoffs must be {len(GRADES)} descending values")

    @classmethod
    def from_settings(cls, settings: ScoringSettings) -> "ScoringConfig":
        return cls(
            partial_weight=settings.scoring_partial_weight,
            contradiction_flag=settings.scoring_contradiction_flag,
            unsupported_flag=settings.scoring_unsupported_flag,
            unsupported_fraction_flag=settings.scoring_unsupported_fraction_flag,
            min_coverage=settings.scoring_min_coverage,
            max_unknown_fraction=settings.scoring_max_unknown_fraction,
            grade_cutoffs=tuple(settings.grade_cutoffs),
        )


DEFAULT_SCORING = ScoringConfig()


def clamp(value: float, low: float = 0.0, high: float = 1.0) -> float:
    return min(max(value, low), high)


def resolve_unsupported_policy(requested: str, sources: Sequence[str] = ()) -> UnsupportedPolicy:
    """
    Resolve AuditOptions.unsupported_policy ("", auto, hallucination or
    unverifiable). "" and auto mean hallucination when any evidence document
    came from the request (any source but knowledge or upload), otherwise
    unverifiable.
    """
    value = (requested or "").strip().lower()
    for policy in UnsupportedPolicy:
        if value == policy.value:
            return policy
    if any(source not in NON_REQUEST_SOURCES for source in sources):
        return UnsupportedPolicy.HALLUCINATION
    return UnsupportedPolicy.UNVERIFIABLE


def llm_distribution(status: VerificationStatus, confidence: float) -> Dict[str, float]:
    """
    The default distribution of an LLM verdict: P(verdict) = confidence, the
    rest split evenly over the other three verdicts. Empty for a status that
    is not a verdict (UNKNOWN, NOT_CHECKED).
    """
    key = _STATUS_KEY.get(status)
    if key is None:
        return {}
    confidence = clamp(confidence) if math.isfinite(confidence) else 0.0
    rest = (1.0 - confidence) / 3.0
    return {name: (confidence if name == key else rest) for name in PROBABILITY_KEYS}


def normalized_distribution(probabilities: Mapping[str, Any]) -> Optional[Dict[str, float]]:
    """
    `probabilities` over the four verdict keys, normalized to sum to 1 (summed
    in sorted-key order); None when it has no usable mass. Missing keys are 0;
    negative, non-numeric and non-finite values are ignored.
    """
    values: Dict[str, float] = {}
    for key in PROBABILITY_KEYS:
        raw = probabilities.get(key, 0.0)
        if isinstance(raw, bool) or not isinstance(raw, (int, float)):
            raw = 0.0
        value = float(raw)
        values[key] = value if math.isfinite(value) and value > 0.0 else 0.0
    total = 0.0
    for key in PROBABILITY_KEYS:
        total += values[key]
    if total <= 0.0:
        return None
    return {key: values[key] / total for key in PROBABILITY_KEYS}


@dataclass(frozen=True)
class ClaimScoreInput:
    """What scoring reads from one claim verification."""

    status: VerificationStatus
    confidence: float = 0.0
    probabilities: Optional[Mapping[str, float]] = None

    @classmethod
    def from_verification(cls, verification: Mapping[str, Any]) -> "ClaimScoreInput":
        """From a ClaimVerification record (status, confidence, verdict_probabilities)."""
        try:
            confidence = float(verification.get("confidence") or 0.0)
        except (TypeError, ValueError):
            confidence = 0.0
        probabilities = verification.get("verdict_probabilities")
        return cls(
            status=coerce_status(verification.get("status")),
            confidence=confidence,
            probabilities=probabilities if isinstance(probabilities, Mapping) else None,
        )

    def distribution(self) -> Dict[str, float]:
        """(S, P, C, U) keyed by verdict; empty unless the claim was decided."""
        if self.status not in DECIDED_STATUSES:
            return {}
        if self.probabilities:
            recorded = normalized_distribution(self.probabilities)
            if recorded is not None:
                return recorded
        return llm_distribution(self.status, self.confidence)


@dataclass(frozen=True)
class ClaimComponents:
    """A decided claim's support, contradiction and unverified mass (sum 1)."""

    support: float
    contradiction: float
    unverified: float


def claim_components(distribution: Mapping[str, float], partial_weight: float) -> ClaimComponents:
    partial = distribution.get(PARTIAL_KEY, 0.0)
    return ClaimComponents(
        support=distribution.get(SUPPORTED_KEY, 0.0) + partial_weight * partial,
        contradiction=distribution.get(CONTRADICTED_KEY, 0.0),
        unverified=distribution.get(UNSUPPORTED_KEY, 0.0) + (1.0 - partial_weight) * partial,
    )


@dataclass(frozen=True)
class AuditScores:
    """proto AuditScores: the optional floats are None when not computable."""

    trust_score: Optional[float] = None
    faithfulness: Optional[float] = None
    contradiction_rate: Optional[float] = None
    unverified_rate: Optional[float] = None
    coverage: Optional[float] = None
    accuracy_addressed: Optional[float] = None
    max_contradiction: Optional[float] = None
    p_hallucinated: Optional[float] = None
    claims_total: int = 0
    claims_decided: int = 0
    claims_unknown: int = 0

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)


@dataclass(frozen=True)
class AuditSummary:
    """How an audit ends (SPEC sections 2.2 and 3.2)."""

    status: AuditOutcome
    verdict: Optional[AuditVerdict]
    policy: UnsupportedPolicy
    scores: Optional[AuditScores]
    degraded_reasons: Tuple[str, ...] = ()
    error_kind: Optional[str] = None
    grade: Optional[str] = None
    contradiction_flag: bool = False
    baseless_flag: bool = False
    claims_not_checked: int = 0

    @property
    def hallucination_detected(self) -> bool:
        return self.verdict is AuditVerdict.HALLUCINATED

    @property
    def trust_score(self) -> Optional[float]:
        return self.scores.trust_score if self.scores is not None else None

    @property
    def legacy_score(self) -> float:
        """proto AuditResult.faithfulness_score: trust_score, or 0 when absent."""
        trust = self.trust_score
        return trust if trust is not None else 0.0


def grade_for(trust_score: Optional[float], cutoffs: Sequence[float]) -> Optional[str]:
    """Letter grade of a trust score (A >= cutoffs[0], ... F below the last); None without one."""
    if trust_score is None:
        return None
    for grade, cutoff in zip(GRADES, cutoffs):
        if trust_score >= cutoff - EPS:
            return grade
    return FAIL_GRADE


def ordered_reasons(reasons: Iterable[str]) -> Tuple[str, ...]:
    """Degraded reasons without duplicates, known ones in reporting order first."""
    unique: List[str] = []
    for reason in reasons:
        if reason and reason not in unique:
            unique.append(reason)
    known = [reason for reason in DEGRADED_REASON_ORDER if reason in unique]
    return tuple(known + [reason for reason in unique if reason not in DEGRADED_REASON_ORDER])


def _at_least(value: float, threshold: float) -> bool:
    return value >= threshold - EPS


def aggregate(
    claims: Sequence[ClaimScoreInput],
    policy: UnsupportedPolicy = UnsupportedPolicy.HALLUCINATION,
    config: ScoringConfig = DEFAULT_SCORING,
    degraded_reasons: Iterable[str] = (),
) -> AuditSummary:
    """
    Score an audit's claim verifications (see the module docstring).

    Args:
        claims: One entry per extracted claim, in claim order.
        policy: The resolved unsupported policy.
        config: Scoring thresholds.
        degraded_reasons: Reasons earlier steps recorded (decomposer_fallback,
            context_trimmed, judge_retry_exhausted); unknown_claims is added here.
    """
    reasons = list(degraded_reasons)
    total = len(claims)
    unknown = sum(1 for claim in claims if claim.status is VerificationStatus.UNKNOWN)
    not_checked = sum(1 for claim in claims if claim.status is VerificationStatus.NOT_CHECKED)
    checked = total - not_checked
    decided = [claim for claim in claims if claim.status in DECIDED_STATUSES]
    counts = AuditScores(claims_total=total, claims_decided=len(decided), claims_unknown=unknown)

    if unknown:
        reasons.append(UNKNOWN_CLAIMS)
    final_reasons = ordered_reasons(reasons)
    status = AuditOutcome.DEGRADED if final_reasons else AuditOutcome.COMPLETED

    if total == 0:
        return AuditSummary(status, AuditVerdict.NO_CLAIMS, policy, counts, final_reasons)
    if checked == 0:
        return AuditSummary(
            status,
            AuditVerdict.NOT_ASSESSED,
            policy,
            None,
            final_reasons,
            claims_not_checked=not_checked,
        )
    if not decided or _at_least(unknown / checked, config.max_unknown_fraction):
        return AuditSummary(
            AuditOutcome.FAILED,
            None,
            policy,
            None,
            final_reasons,
            error_kind=JUDGE_UNAVAILABLE,
            claims_not_checked=not_checked,
        )

    components = [
        claim_components(claim.distribution(), config.partial_weight) for claim in decided
    ]
    n = len(components)
    support = sum(part.support for part in components)
    contradiction = sum(part.contradiction for part in components)
    unverified = sum(part.unverified for part in components)

    faithfulness = support / n
    contradiction_rate = contradiction / n
    unverified_rate = unverified / n
    coverage = 1.0 - unverified_rate
    addressed = support + contradiction
    accuracy_addressed = support / addressed if addressed >= MIN_ADDRESSED_MASS else None
    max_contradiction = max(part.contradiction for part in components)
    max_unverified = max(part.unverified for part in components)

    hallucination_policy = policy is UnsupportedPolicy.HALLUCINATION
    if hallucination_policy:
        p_hallucinated = max(part.contradiction + part.unverified for part in components)
        trust_score: Optional[float] = clamp(faithfulness - contradiction_rate)
    else:
        p_hallucinated = max_contradiction
        trust_score = accuracy_addressed

    contradiction_flag = _at_least(max_contradiction, config.contradiction_flag)
    baseless_flag = hallucination_policy and (
        _at_least(max_unverified, config.unsupported_flag)
        or _at_least(unverified_rate, config.unsupported_fraction_flag)
    )
    if contradiction_flag or baseless_flag:
        verdict = AuditVerdict.HALLUCINATED
    elif not hallucination_policy and coverage < config.min_coverage - EPS:
        verdict = AuditVerdict.UNVERIFIABLE
    else:
        verdict = AuditVerdict.GROUNDED

    scores = AuditScores(
        trust_score=trust_score,
        faithfulness=faithfulness,
        contradiction_rate=contradiction_rate,
        unverified_rate=unverified_rate,
        coverage=coverage,
        accuracy_addressed=accuracy_addressed,
        max_contradiction=max_contradiction,
        p_hallucinated=p_hallucinated,
        claims_total=total,
        claims_decided=n,
        claims_unknown=unknown,
    )
    return AuditSummary(
        status,
        verdict,
        policy,
        scores,
        final_reasons,
        grade=grade_for(trust_score, config.grade_cutoffs),
        contradiction_flag=contradiction_flag,
        baseless_flag=baseless_flag,
        claims_not_checked=not_checked,
    )
