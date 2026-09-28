"""
LangGraph State Definitions

Defines the state schema for the audit workflow.
LangGraph uses TypedDict to define what data flows through the graph.
"""

from typing import Any, TypedDict, List, Dict, NotRequired, Optional
from enum import Enum


class VerificationStatus(str, Enum):
    """Status of a claim verification."""

    SUPPORTED = "supported"  # A relevant document backs the claim
    CONTRADICTED = "contradicted"  # A relevant document conflicts with the claim
    UNSUPPORTED = "unsupported"  # No document addresses the claim (unverifiable)
    PARTIALLY_SUPPORTED = "partially_supported"  # Some evidence exists
    UNKNOWN = "unknown"  # Judge or parse failure
    NOT_CHECKED = "not_checked"  # Extracted, but there was no evidence to check it against


def coerce_status(value: object) -> VerificationStatus:
    """Normalise a claim status (enum or its value); anything unrecognised is UNKNOWN."""
    try:
        return VerificationStatus(value)
    except ValueError:
        return VerificationStatus.UNKNOWN


# Statuses the judge actually decided. UNKNOWN and NOT_CHECKED are excluded from
# the audit verdict (SPEC section 2.2).
DECIDED_STATUSES = frozenset(
    {
        VerificationStatus.SUPPORTED,
        VerificationStatus.CONTRADICTED,
        VerificationStatus.UNSUPPORTED,
        VerificationStatus.PARTIALLY_SUPPORTED,
    }
)


class AuditVerdict(str, Enum):
    """Whole-audit verdict (proto AuditVerdict; UPPER_SNAKE on every surface)."""

    GROUNDED = "GROUNDED"
    HALLUCINATED = "HALLUCINATED"
    UNVERIFIABLE = "UNVERIFIABLE"
    NO_CLAIMS = "NO_CLAIMS"  # The answer made no checkable claim
    NOT_ASSESSED = "NOT_ASSESSED"  # There was nothing to check the answer against


class AuditOutcome(str, Enum):
    """Terminal audit status (proto AuditStatus COMPLETED, DEGRADED or FAILED)."""

    COMPLETED = "completed"
    DEGRADED = "degraded"
    FAILED = "failed"


class NotAssessedReason(str, Enum):
    """Why an audit is NOT_ASSESSED (proto AuditResult.not_assessed_reason)."""

    NO_EVIDENCE = "no_evidence"  # No evidence documents, and knowledge is off
    KNOWLEDGE_NO_HITS = "knowledge_no_hits"  # Knowledge retrieval found nothing


class UnsupportedPolicy(str, Enum):
    """Whether UNSUPPORTED claims count as hallucination or only as unverifiable."""

    HALLUCINATION = "hallucination"
    UNVERIFIABLE = "unverifiable"


class ClaimVerification(TypedDict):
    """Verification result for a single claim."""

    claim: str  # The extracted claim
    status: VerificationStatus  # Verification outcome
    confidence: float  # Confidence score (0.0-1.0)
    evidence: List[str]  # Supporting/refuting snippets
    reasoning: NotRequired[str]  # The judge's one-sentence explanation, when it gave one
    # v2 per-claim record (SPEC section 2.2 ClaimVerification); absent on v1 records.
    index: NotRequired[int]  # Position in the audit's claim list
    quote: NotRequired[str]  # The answer's words that assert the claim (decomposer)
    span_start: NotRequired[int]  # UTF-16 code units, half-open; -1 when unaligned
    span_end: NotRequired[int]
    span_method: NotRequired[str]  # exact|normalized|fuzzy|none
    decided_by: NotRequired[str]  # llm (a verdict) or none (UNKNOWN, NOT_CHECKED)
    # {supported, partially_supported, contradicted, unsupported} -> probability
    verdict_probabilities: NotRequired[Dict[str, float]]
    probability_source: NotRequired[str]  # llm_confidence for the LLM default
    error: NotRequired[bool]  # UNKNOWN because the judge call raised
    error_kind: NotRequired[str]  # e.g. rate_limited, invalid_output


class ExtractedClaim(TypedDict):
    """A claim as the decomposer extracted it, located in the answer."""

    claim: str
    quote: str
    span_start: int  # UTF-16 code units, half-open; -1 when unaligned
    span_end: int
    span_method: str  # exact|normalized|fuzzy|none


class AuditState(TypedDict):
    """
    State that flows through the audit graph.

    Each node in the graph reads from and writes to this state.
    LangGraph automatically manages state updates.

    Input fields (provided initially):
        request_id: Unique identifier for tracking
        user_query: The question asked
        llm_response: The answer to verify
        context_docs: Evidence documents supplied by the caller (or, with the
                      knowledge add-on, retrieved from the knowledge base)
        context_sources: Evidence source of each caller document, parallel to
                      context_docs ("" when the caller did not say)
        knowledge_enabled: Whether knowledge retrieval may supply evidence
        unsupported_policy: Requested UNSUPPORTED handling ("", auto,
                      hallucination or unverifiable); preflight resolves it

    Intermediate fields (populated by nodes):
        claims: Extracted claims from the response
        claim_verifications: Verification results per claim

    Output fields (final results):
        faithfulness_score: Legacy score: scores.trust_score, or 0.0 when absent
        hallucination_detected: Whether the verdict is HALLUCINATED
        reasoning_trace: Explanation of findings
        verdict: AuditVerdict value
        not_assessed_reason: NotAssessedReason value when verdict is NOT_ASSESSED
        audit_status: AuditOutcome value
        degraded_reasons: Why a completed audit is DEGRADED (e.g. unknown_claims)
        error_kind: Why the audit failed (for example judge_unavailable)

    v2 fields (scoring v2, decomposer v2):
        extracted_claims: Quote and UTF-16 span of each claim, parallel to claims
        context_truncated: Input flag: the caller truncated evidence documents
        context_trimmed: The verifier capped the evidence it sent the judge
        scores: The AuditScores of the audit (None when it has none)
        grade: Letter grade of scores.trust_score (None without one)
        scoring_version: "v2"; faithfulness_score then carries trust_score (0 when absent)
    """

    # Input
    request_id: str
    user_query: str
    llm_response: str
    context_docs: List[str]
    context_sources: List[str]
    knowledge_enabled: bool
    unsupported_policy: str

    # Intermediate
    claims: Optional[List[str]]
    claim_verifications: Optional[List[ClaimVerification]]

    # Output
    faithfulness_score: Optional[float]
    hallucination_detected: Optional[bool]
    reasoning_trace: Optional[str]
    verdict: Optional[str]
    not_assessed_reason: Optional[str]
    audit_status: Optional[str]
    degraded_reasons: List[str]
    error_kind: Optional[str]

    # Pipeline observability
    step_timings: Optional[Dict[str, int]]

    # v2 (not set by v1 callers)
    extracted_claims: NotRequired[List[ExtractedClaim]]  # Parallel to claims (decomposer)
    context_truncated: NotRequired[
        bool
    ]  # Input: the caller cut evidence (ContextDocument.truncated)
    context_trimmed: NotRequired[bool]  # The verifier capped the evidence it sent the judge
    scores: NotRequired[Optional[Dict[str, Any]]]  # scoring.AuditScores.to_dict(); None when absent
    grade: NotRequired[Optional[str]]  # A-F from scores.trust_score; None without one
    scoring_version: NotRequired[str]  # "v2"


def add_degraded_reason(state: AuditState, reason: str) -> None:
    """Record why an audit is DEGRADED (once per reason)."""
    reasons = list(state.get("degraded_reasons") or [])
    if reason not in reasons:
        reasons.append(reason)
    state["degraded_reasons"] = reasons
