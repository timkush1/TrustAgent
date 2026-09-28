"""
gRPC Server Implementation

Implements the AuditService defined in proto/evaluator.proto (docs/v2/SPEC.md
section 2.2).
"""

import asyncio
import logging
import math
import uuid
import time
from collections import OrderedDict
from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Set, Tuple

import grpc
from grpc import aio

from .. import __version__
from .pb import evaluator_pb2
from .pb import evaluator_pb2_grpc
from ..engine_info import EngineContext, engine_info, judge_info
from ..graphs.audit_graph import AuditStepError, run_audit
from ..graphs.state import (
    DECIDED_STATUSES,
    AuditOutcome,
    AuditState,
    AuditVerdict,
    ClaimVerification,
    VerificationStatus,
    coerce_status,
)
from ..providers.costs import CallCost, cost_ledger
from ..providers.mock import FixtureNotFoundError, mock_judge_scope
from ..scoring.aggregate import SCORING_VERSION
from ..metrics import (
    AUDITS_TOTAL,
    AUDIT_DURATION,
    FAITHFULNESS_SCORE,
    HALLUCINATIONS_DETECTED,
    ACTIVE_AUDITS,
    CLAIMS_TOTAL,
)

logger = logging.getLogger(__name__)

# HealthCheck asks whether the engine is serving; the judge's health is only
# reported. Its probe is bounded so a slow or unreachable judge (a provider
# health check may wait up to 10 s) shows as judge=False instead of running the
# RPC past the deadline of `python -m truthtable.healthcheck` (3 s by default)
# and marking a serving engine unhealthy.
JUDGE_HEALTH_TIMEOUT_S = 2.0

# Qdrant probes (health, point count) are synchronous client calls; they run in
# a worker thread, bounded like the judge probe.
QDRANT_PROBE_TIMEOUT_S = 1.0

# Engine concurrency defaults (TRUTHTABLE_ENGINE_MAX_RPCS / _MAX_AUDITS, C-H13).
DEFAULT_MAX_RPCS = 64
DEFAULT_MAX_AUDITS = 16

# AuditResult.error_kind values that mean the judge itself failed.
_JUDGE_FAILURE_KINDS = frozenset({"judge_unavailable", "decomposer_failed"})

NO_JUDGE_TRACE = (
    "Not assessed: the engine has no audit judge configured (LLM_PROVIDER); "
    "no LLM call was made."
)

# TrustGrade cutoffs (D18): A>=0.9, B>=0.7, C>=0.5, D>=0.3, F<0.3. They must
# match the TrustGrade comments in proto/evaluator.proto.
_GRADE_CUTOFFS: Tuple[Tuple[float, int], ...] = (
    (0.9, evaluator_pb2.TRUST_GRADE_A),
    (0.7, evaluator_pb2.TRUST_GRADE_B),
    (0.5, evaluator_pb2.TRUST_GRADE_C),
    (0.3, evaluator_pb2.TRUST_GRADE_D),
)

# Legacy (v1) claim mapping for SubmitAudit/GetAuditResult pollers: UNKNOWN and
# NOT_CHECKED are sent as UNSPECIFIED, never as the v2-only values 5 and 6.
_LEGACY_CLAIM_STATUS: Dict[VerificationStatus, int] = {
    VerificationStatus.SUPPORTED: evaluator_pb2.VERIFICATION_STATUS_SUPPORTED,
    VerificationStatus.CONTRADICTED: evaluator_pb2.VERIFICATION_STATUS_CONTRADICTED,
    VerificationStatus.UNSUPPORTED: evaluator_pb2.VERIFICATION_STATUS_UNSUPPORTED,
    VerificationStatus.PARTIALLY_SUPPORTED: evaluator_pb2.VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
    VerificationStatus.UNKNOWN: evaluator_pb2.VERIFICATION_STATUS_UNSPECIFIED,
    VerificationStatus.NOT_CHECKED: evaluator_pb2.VERIFICATION_STATUS_UNSPECIFIED,
}

# v2 claim mapping (Evaluate): every status has its own value (D6).
_V2_CLAIM_STATUS: Dict[VerificationStatus, int] = {
    VerificationStatus.SUPPORTED: evaluator_pb2.VERIFICATION_STATUS_SUPPORTED,
    VerificationStatus.CONTRADICTED: evaluator_pb2.VERIFICATION_STATUS_CONTRADICTED,
    VerificationStatus.UNSUPPORTED: evaluator_pb2.VERIFICATION_STATUS_UNSUPPORTED,
    VerificationStatus.PARTIALLY_SUPPORTED: evaluator_pb2.VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
    VerificationStatus.UNKNOWN: evaluator_pb2.VERIFICATION_STATUS_UNKNOWN,
    VerificationStatus.NOT_CHECKED: evaluator_pb2.VERIFICATION_STATUS_NOT_CHECKED,
}

_AUDIT_VERDICT: Dict[AuditVerdict, int] = {
    AuditVerdict.GROUNDED: evaluator_pb2.AUDIT_VERDICT_GROUNDED,
    AuditVerdict.HALLUCINATED: evaluator_pb2.AUDIT_VERDICT_HALLUCINATED,
    AuditVerdict.UNVERIFIABLE: evaluator_pb2.AUDIT_VERDICT_UNVERIFIABLE,
    AuditVerdict.NO_CLAIMS: evaluator_pb2.AUDIT_VERDICT_NO_CLAIMS,
    AuditVerdict.NOT_ASSESSED: evaluator_pb2.AUDIT_VERDICT_NOT_ASSESSED,
}

# Claim span offsets for a claim not located in the answer (SPEC section 2.1:
# "-1 means unaligned"). Aligned spans come from the decomposer (text/spans.py):
# UTF-16 code units, half-open.
UNALIGNED_SPAN = -1

# span_method values sent for an aligned span (an unaligned one sends "").
_SPAN_METHODS = frozenset({"exact", "normalized", "fuzzy"})

# The optional floats of AuditScores, set only when the scorer computed them.
_SCORE_FLOATS: Tuple[str, ...] = (
    "trust_score",
    "faithfulness",
    "contradiction_rate",
    "unverified_rate",
    "coverage",
    "accuracy_addressed",
    "max_contradiction",
    "p_hallucinated",
)
_SCORE_COUNTS: Tuple[str, ...] = ("claims_total", "claims_decided", "claims_unknown")

# The scorer's letter grade (from TRUTHTABLE_SCORING_GRADE_CUTOFFS) on the wire.
_GRADE_BY_LETTER: Dict[str, int] = {
    "A": evaluator_pb2.TRUST_GRADE_A,
    "B": evaluator_pb2.TRUST_GRADE_B,
    "C": evaluator_pb2.TRUST_GRADE_C,
    "D": evaluator_pb2.TRUST_GRADE_D,
    "F": evaluator_pb2.TRUST_GRADE_F,
}

_AUDIT_STATUS: Dict[AuditOutcome, int] = {
    AuditOutcome.COMPLETED: evaluator_pb2.AUDIT_STATUS_COMPLETED,
    AuditOutcome.DEGRADED: evaluator_pb2.AUDIT_STATUS_DEGRADED,
    AuditOutcome.FAILED: evaluator_pb2.AUDIT_STATUS_FAILED,
}


def grade_for_score(score: float) -> int:
    """
    TrustGrade for a legacy score (cutoffs documented on the proto TrustGrade).
    Evaluate sends the scorer's grade instead (configurable cutoffs).
    """
    for cutoff, grade in _GRADE_CUTOFFS:
        if score >= cutoff:
            return grade
    return evaluator_pb2.TRUST_GRADE_F


def _failure_kind(exc: BaseException) -> str:
    """AuditResult.error_kind for an exception raised by the audit graph."""
    cause: Optional[BaseException] = exc
    seen: Set[int] = set()
    while cause is not None and id(cause) not in seen:
        if isinstance(cause, FixtureNotFoundError):
            return "mock_fixture_missing"
        seen.add(id(cause))
        cause = cause.__cause__ or cause.__context__
    if isinstance(exc, AuditStepError):
        return exc.error_kind
    if isinstance(exc, TimeoutError):
        return "timeout"
    return "internal"


def _record_claim_metrics(verifications: Iterable[ClaimVerification]) -> None:
    for cv in verifications:
        CLAIMS_TOTAL.labels(status=coerce_status(cv.get("status")).value).inc()


def _finite(value: Any) -> Optional[float]:
    """`value` as a finite float, or None."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    number = float(value)
    return number if math.isfinite(number) else None


def _claim_span(cv: Mapping[str, Any]) -> Tuple[int, int, str]:
    """(start, end, method) of a claim's span; (-1, -1, "") when it is not aligned."""
    start, end = cv.get("span_start"), cv.get("span_end")
    method = str(cv.get("span_method") or "")
    if (
        isinstance(start, int)
        and isinstance(end, int)
        and 0 <= start <= end
        and method in _SPAN_METHODS
    ):
        return start, end, method
    return UNALIGNED_SPAN, UNALIGNED_SPAN, ""


def _v2_claim(index: int, cv: ClaimVerification) -> evaluator_pb2.ClaimVerification:
    """
    v2 ClaimVerification: UNKNOWN=5 and NOT_CHECKED=6 keep their own values
    (D6); the span, quote, reasoning, decided_by and verdict distribution come
    from the decomposer's and verifier's records when they made them.
    """
    status = coerce_status(cv.get("status"))
    # The v2 keys are optional extras of the record (a v1 caller has none).
    record: Mapping[str, Any] = cv
    span_start, span_end, span_method = _claim_span(record)
    claim = evaluator_pb2.ClaimVerification(
        index=index,
        claim=cv.get("claim", ""),
        status=_V2_CLAIM_STATUS[status],
        confidence=float(cv.get("confidence", 0.0)),
        evidence=list(cv.get("evidence") or []),
        reasoning=str(record.get("reasoning") or ""),
        quote=str(record.get("quote") or ""),
        decided_by=str(
            record.get("decided_by") or ("llm" if status in DECIDED_STATUSES else "none")
        ),
        probability_source=str(record.get("probability_source") or ""),
        span_start=span_start,
        span_end=span_end,
        span_method=span_method,
    )
    probabilities = record.get("verdict_probabilities")
    if isinstance(probabilities, Mapping):
        for key, value in probabilities.items():
            number = _finite(value)
            if number is not None:
                claim.verdict_probabilities[str(key)] = number
    return claim


def _cost_record(call: CallCost) -> evaluator_pb2.CostRecord:
    """proto CostRecord of one judge call (an unknown cost is sent as 0, labelled unknown)."""
    return evaluator_pb2.CostRecord(
        component=call.component,
        kind=call.kind,
        provider=call.provider,
        model=call.model,
        input_tokens=call.input_tokens,
        output_tokens=call.output_tokens,
        cost_usd=call.cost_usd if call.cost_usd is not None else 0.0,
        cost_source=call.cost_source,
        latency_ms=call.latency_ms,
        status=call.status,
    )


def _context_truncated(request: evaluator_pb2.AuditRequest) -> bool:
    """Whether the proxy already cut the evidence it sent (context_trimmed)."""
    if request.HasField("evidence_summary") and request.evidence_summary.truncated:
        return True
    return any(doc.truncated for doc in request.context)


# In-memory storage for audit results. Results are written by SubmitAudit and
# read once by the Go proxy's immediately-following GetAuditResult, so they are
# short-lived. Bounded (LRU: evict oldest) to prevent unbounded growth; a
# Redis-backed store is the multi-replica production upgrade.
_MAX_STORED_RESULTS = 1000
_audit_results: "OrderedDict[str, AuditState]" = OrderedDict()


def _store_result(audit_id: str, result: AuditState) -> None:
    _audit_results[audit_id] = result
    _audit_results.move_to_end(audit_id)
    while len(_audit_results) > _MAX_STORED_RESULTS:
        _audit_results.popitem(last=False)  # evict oldest


# KB claim status <-> proto enum mapping
_KB_STATUS_TO_PROTO = {
    "accepted": evaluator_pb2.KB_CLAIM_STATUS_ACCEPTED,
    "quarantined": evaluator_pb2.KB_CLAIM_STATUS_QUARANTINED,
}
_PROTO_TO_KB_STATUS = {v: k for k, v in _KB_STATUS_TO_PROTO.items()}


class AuditServicer(evaluator_pb2_grpc.AuditServiceServicer):
    """
    Implementation of the AuditService gRPC service.

    Handles:
    - Evaluate: Run an audit and return its v2 AuditResult (the proxy's only audit call)
    - SubmitAudit: Run an audit and store its result (legacy, deprecated)
    - GetAuditResult: Retrieve a stored result with the legacy v1 mapping (deprecated)
    - HealthCheck: Service health status
    - IngestDocuments, ListKBClaims, ListConflicts, GetKBStats: knowledge base

    - GetEngineInfo: What the engine runs with (judge, knowledge, limits)

    The other v2 RPCs (RetrieveContext, ApplyRuntimeConfig, DraftEvaluator)
    are not overridden yet, so they inherit the generated handlers and return
    UNIMPLEMENTED until their phase lands.
    """

    def __init__(
        self,
        audit_graph,
        provider=None,
        qdrant_store=None,
        embedding_service=None,
        ingestor=None,
        *,
        engine: Optional[EngineContext] = None,
        max_audits: Optional[int] = None,
    ):
        """
        Initialize the servicer.

        Args:
            audit_graph: Compiled LangGraph workflow
            provider: LLM provider for health checks (optional)
            qdrant_store: Qdrant store for health checks (optional)
            embedding_service: Embedding service for document ingestion (optional)
            ingestor: ClaimIngestor for claim-level (Gate-1 gated) ingestion;
                      None falls back to legacy chunk ingestion
            engine: Startup facts reported by GetEngineInfo and HealthCheck (the
                    resolved judge, settings, knowledge state). Without it the
                    servicer reports built-in defaults.
            max_audits: Concurrent audits (default: engine.max_audits, else 16)
        """
        self.audit_graph = audit_graph
        self.provider = provider
        self.qdrant_store = qdrant_store
        self.embedding_service = embedding_service
        self.ingestor = ingestor
        self.engine = engine
        self._version = __version__
        if max_audits is None:
            max_audits = engine.max_audits if engine is not None else DEFAULT_MAX_AUDITS
        self.max_audits = max_audits
        # Audits beyond max_audits queue here instead of loading the judge (C-H13).
        self._audit_slots = asyncio.Semaphore(max_audits)
        self._judge_healthy: Optional[bool] = engine.judge.healthy if engine else None
        logger.info(f"AuditServicer initialized (max {max_audits} concurrent audits)")

    @property
    def _no_judge(self) -> bool:
        """The engine was started without a judge (LLM_PROVIDER=none, or none found)."""
        return self.engine is not None and not self.engine.has_judge

    async def Evaluate(
        self, request: evaluator_pb2.AuditRequest, context
    ) -> evaluator_pb2.AuditResult:
        """
        Run an audit to completion and return its v2 result (SPEC section 2.2).

        At most `max_audits` audits run at once; later calls wait for a slot.
        Without a judge the audit is NOT_ASSESSED (no_judge) with no LLM call.
        """
        if self._no_judge:
            return self._not_assessed_without_judge(request)
        async with self._audit_slots:
            result = await self._evaluate(request, context)
        if self.engine is not None:
            # JudgeInfo.healthy describes this audit: false only when it failed
            # because the judge did not answer.
            judge_failed = result.error_kind in _JUDGE_FAILURE_KINDS
            result.judge.CopyFrom(judge_info(self.engine.judge, not judge_failed))
        return result

    def _not_assessed_without_judge(
        self, request: evaluator_pb2.AuditRequest
    ) -> evaluator_pb2.AuditResult:
        trace_id = request.trace_id or request.request_id
        result = evaluator_pb2.AuditResult(
            audit_id=request.audit_id or str(uuid.uuid4()),
            request_id=request.request_id or trace_id,
            trace_id=trace_id,
            status=evaluator_pb2.AUDIT_STATUS_COMPLETED,
            verdict=evaluator_pb2.AUDIT_VERDICT_NOT_ASSESSED,
            not_assessed_reason="no_judge",
            reasoning_trace=NO_JUDGE_TRACE,
            completed_at_ms=int(time.time() * 1000),
            scoring_version=SCORING_VERSION,
        )
        if request.HasField("gate"):
            result.gate.CopyFrom(request.gate)
        if request.HasField("evidence_summary"):
            result.evidence_summary.CopyFrom(request.evidence_summary)
        result.step_timings["total_ms"] = 0
        if self.engine is not None:
            result.judge.CopyFrom(judge_info(self.engine.judge, None))
        AUDITS_TOTAL.labels(status="success").inc()
        logger.info(f"Audit {result.audit_id}: NOT_ASSESSED (no_judge)")
        return result

    async def _evaluate(
        self, request: evaluator_pb2.AuditRequest, context
    ) -> evaluator_pb2.AuditResult:
        """
        Run an audit to completion and return its v2 result (SPEC section 2.2).

        Audit failures are results (status FAILED with error_kind), not RPC
        errors, so the proxy can persist them like any other outcome.
        """
        started = time.monotonic()
        audit_id = request.audit_id or str(uuid.uuid4())
        trace_id = request.trace_id or request.request_id
        request_id = request.request_id or trace_id
        options = request.options

        logger.info(
            f"Evaluate: trace {trace_id} audit {audit_id} "
            f"({len(request.context)} evidence documents)"
        )

        ACTIVE_AUDITS.inc()
        try:
            state: Optional[AuditState] = None
            failure: Optional[Tuple[str, str]] = None
            with mock_judge_scope(options.mock_scenario) as mock_judge, cost_ledger() as ledger:
                try:
                    state = await run_audit(
                        graph=self.audit_graph,
                        request_id=request_id,
                        user_query=request.query,
                        llm_response=request.response,
                        context_docs=[doc.content for doc in request.context],
                        context_sources=[doc.source for doc in request.context],
                        knowledge_enabled=options.knowledge_enabled,
                        unsupported_policy=options.unsupported_policy,
                        context_truncated=_context_truncated(request),
                        extract_claims_without_evidence=options.extract_claims_without_evidence,
                        llm_budget_exhausted=options.llm_budget_exhausted,
                    )
                except Exception as exc:
                    logger.error(f"Audit {audit_id} failed: {exc}", exc_info=True)
                    failure = (_failure_kind(exc), str(exc) or type(exc).__name__)
            if mock_judge.misses:
                # A mock miss inside the verifier is swallowed into an UNKNOWN
                # claim; the audit still fails rather than scoring a fixture gap.
                failure = ("mock_fixture_missing", "; ".join(mock_judge.misses))

            duration = time.monotonic() - started
            result = self._evaluate_result(
                request, audit_id, trace_id, request_id, state, failure, int(duration * 1000)
            )
            # Every judge call, a failed audit's included (the money is spent).
            result.costs.extend(_cost_record(call) for call in ledger.calls)
            self._record_evaluate_metrics(result, state, duration)

            logger.info(
                f"Audit {audit_id}: {evaluator_pb2.AuditStatus.Name(result.status)} "
                f"{evaluator_pb2.AuditVerdict.Name(result.verdict)}"
                + (f" ({result.error_kind})" if result.error_kind else "")
            )
            return result
        finally:
            ACTIVE_AUDITS.dec()

    def _evaluate_result(
        self,
        request: evaluator_pb2.AuditRequest,
        audit_id: str,
        trace_id: str,
        request_id: str,
        state: Optional[AuditState],
        failure: Optional[Tuple[str, str]],
        total_ms: int,
    ) -> evaluator_pb2.AuditResult:
        """Map a finished audit (or its failure) onto the v2 AuditResult."""
        view: Mapping[str, Any] = state if state is not None else {}
        result = evaluator_pb2.AuditResult(
            audit_id=audit_id,
            request_id=request_id,
            trace_id=trace_id,
            completed_at_ms=int(time.time() * 1000),
            processing_time_ms=total_ms,
            scoring_version=str(view.get("scoring_version") or SCORING_VERSION),
        )
        # Echo what the proxy decided and sent, so the result is self-contained.
        if request.HasField("gate"):
            result.gate.CopyFrom(request.gate)
        if request.HasField("evidence_summary"):
            result.evidence_summary.CopyFrom(request.evidence_summary)

        timings = dict(view.get("step_timings") or {})
        timings["total_ms"] = total_ms
        result.step_timings.update({key: int(value) for key, value in timings.items()})

        verifications: Sequence[ClaimVerification] = view.get("claim_verifications") or []
        result.claims.extend(_v2_claim(index, cv) for index, cv in enumerate(verifications))

        verdict_value = state.get("verdict") if state is not None else None
        if failure is None and state is not None:
            if state.get("audit_status") == AuditOutcome.FAILED.value:
                unknown = sum(
                    1
                    for cv in verifications
                    if coerce_status(cv.get("status")) is VerificationStatus.UNKNOWN
                )
                failure = (
                    state.get("error_kind") or "internal",
                    f"the judge could not decide {unknown} of {len(verifications)} claims",
                )
            elif verdict_value is None:
                failure = ("internal", "the audit finished without a verdict")
        if failure is not None or state is None or verdict_value is None:
            error_kind, message = failure or ("internal", "the audit produced no result")
            result.status = evaluator_pb2.AUDIT_STATUS_FAILED
            result.error_kind = error_kind
            result.error = message
            result.reasoning_trace = f"Audit failed ({error_kind}): {message}"
            return result

        verdict = AuditVerdict(verdict_value)
        result.status = _AUDIT_STATUS[AuditOutcome(state.get("audit_status") or "completed")]
        result.verdict = _AUDIT_VERDICT[verdict]
        result.not_assessed_reason = state.get("not_assessed_reason") or ""
        result.degraded_reasons.extend(state.get("degraded_reasons") or [])
        result.reasoning_trace = state.get("reasoning_trace") or ""
        result.hallucination_detected = verdict is AuditVerdict.HALLUCINATED
        if verdict is AuditVerdict.NOT_ASSESSED:
            return result

        # Scores are absent when not computable (SPEC section 3.2): the scorer
        # leaves out what it cannot compute (e.g. the trust score of an
        # UNVERIFIABLE audit with nothing addressed), and none is invented here.
        result.unsupported_policy = state.get("unsupported_policy") or ""
        self._set_scores(result, state.get("scores"), verifications)
        trust_score = result.scores.trust_score if result.scores.HasField("trust_score") else None
        # Legacy field 4: the trust score, or 0 when there is none.
        result.faithfulness_score = trust_score if trust_score is not None else 0.0
        grade = state.get("grade")
        if trust_score is not None and isinstance(grade, str):
            result.grade = _GRADE_BY_LETTER.get(grade, evaluator_pb2.TRUST_GRADE_UNSPECIFIED)
        return result

    @staticmethod
    def _set_scores(
        result: evaluator_pb2.AuditResult,
        scores: Any,
        verifications: Sequence[ClaimVerification],
    ) -> None:
        """AuditResult.scores from the scorer's AuditScores (counts only without them)."""
        if isinstance(scores, Mapping):
            for name in _SCORE_FLOATS:
                value = _finite(scores.get(name))
                if value is not None:
                    setattr(result.scores, name, value)
            for name in _SCORE_COUNTS:
                count = scores.get(name)
                setattr(result.scores, name, int(count) if isinstance(count, int) else 0)
            return
        statuses: List[VerificationStatus] = [
            coerce_status(cv.get("status")) for cv in verifications
        ]
        result.scores.claims_total = len(statuses)
        result.scores.claims_decided = sum(1 for s in statuses if s in DECIDED_STATUSES)
        result.scores.claims_unknown = sum(1 for s in statuses if s is VerificationStatus.UNKNOWN)

    @staticmethod
    def _record_evaluate_metrics(
        result: evaluator_pb2.AuditResult, state: Optional[AuditState], duration: float
    ) -> None:
        failed = result.status == evaluator_pb2.AUDIT_STATUS_FAILED
        AUDITS_TOTAL.labels(status="error" if failed else "success").inc()
        AUDIT_DURATION.observe(duration)
        if result.scores.HasField("trust_score"):
            FAITHFULNESS_SCORE.observe(result.scores.trust_score)
        if result.verdict == evaluator_pb2.AUDIT_VERDICT_HALLUCINATED:
            HALLUCINATIONS_DETECTED.inc()
        _record_claim_metrics((state or {}).get("claim_verifications") or [])

    async def SubmitAudit(
        self, request: evaluator_pb2.AuditRequest, context
    ) -> evaluator_pb2.AuditSubmission:
        """
        Submit an audit request and process synchronously (legacy; shares the
        Evaluate concurrency limit).
        """
        async with self._audit_slots:
            return await self._submit_audit(request, context)

    async def _submit_audit(
        self, request: evaluator_pb2.AuditRequest, context
    ) -> evaluator_pb2.AuditSubmission:
        """Run an audit and store its result for GetAuditResult."""
        audit_id = str(uuid.uuid4())
        start_time = time.time()

        logger.info(f"Received audit request: {request.request_id} -> audit_id: {audit_id}")

        ACTIVE_AUDITS.inc()
        try:
            # Extract context documents
            context_docs = [doc.content for doc in request.context]

            # Run the audit
            result = await run_audit(
                graph=self.audit_graph,
                request_id=request.request_id,
                user_query=request.query,
                llm_response=request.response,
                context_docs=context_docs,
            )

            # Store result for later retrieval
            result["audit_id"] = audit_id
            duration = time.time() - start_time
            result["processing_time_ms"] = int(duration * 1000)
            _store_result(audit_id, result)

            # Record metrics
            score = result.get("faithfulness_score") or 0.0
            AUDITS_TOTAL.labels(status="success").inc()
            AUDIT_DURATION.observe(duration)
            FAITHFULNESS_SCORE.observe(score)
            if result.get("hallucination_detected"):
                HALLUCINATIONS_DETECTED.inc()
            _record_claim_metrics(result.get("claim_verifications") or [])

            logger.info(f"Audit {audit_id} completed with score: {score:.2f}")

            return evaluator_pb2.AuditSubmission(
                audit_id=audit_id, status="completed", queue_position=0
            )

        except Exception as e:
            logger.error(f"Audit failed: {e}", exc_info=True)
            AUDITS_TOTAL.labels(status="error").inc()
            # Store error result
            _store_result(audit_id, {"error": str(e), "request_id": request.request_id})
            return evaluator_pb2.AuditSubmission(
                audit_id=audit_id, status="failed", queue_position=0
            )
        finally:
            ACTIVE_AUDITS.dec()

    async def GetAuditResult(
        self, request: evaluator_pb2.AuditResultRequest, context
    ) -> evaluator_pb2.AuditResult:
        """
        Get audit result by ID.
        """
        audit_id = request.audit_id
        logger.info(f"Audit result requested: {audit_id}")

        if audit_id not in _audit_results:
            context.set_code(grpc.StatusCode.NOT_FOUND)
            context.set_details(f"Audit {audit_id} not found")
            return evaluator_pb2.AuditResult()

        result = _audit_results[audit_id]

        # Check if it was an error
        if "error" in result:
            return evaluator_pb2.AuditResult(
                audit_id=audit_id,
                request_id=result.get("request_id", ""),
                status=evaluator_pb2.AUDIT_STATUS_FAILED,
                reasoning_trace=f"Error: {result['error']}",
            )

        # Legacy (v1) mapping for SubmitAudit/GetAuditResult pollers (SPEC
        # section 2.2): UNKNOWN and NOT_CHECKED claims are UNSPECIFIED, DEGRADED
        # and NOT_ASSESSED audits are COMPLETED, and a NOT_ASSESSED or NO_CLAIMS
        # audit scores 0.
        claims = [
            evaluator_pb2.ClaimVerification(
                claim=cv.get("claim", ""),
                status=_LEGACY_CLAIM_STATUS[coerce_status(cv.get("status"))],
                confidence=cv.get("confidence", 0.0),
                evidence=cv.get("evidence", []),
            )
            for cv in result.get("claim_verifications") or []
        ]

        # The legacy grade always follows the score (v1 pollers expect one).
        score = result.get("faithfulness_score") or 0.0
        grade = grade_for_score(score)

        # Convert step_timings to map<string, int64>
        step_timings = {}
        raw_timings = result.get("step_timings", {})
        if isinstance(raw_timings, dict):
            step_timings = {k: int(v) for k, v in raw_timings.items()}

        return evaluator_pb2.AuditResult(
            audit_id=audit_id,
            request_id=result.get("request_id", ""),
            status=evaluator_pb2.AUDIT_STATUS_COMPLETED,
            faithfulness_score=score,
            grade=grade,
            claims=claims,
            reasoning_trace=result.get("reasoning_trace", ""),
            completed_at_ms=int(time.time() * 1000),
            processing_time_ms=result.get("processing_time_ms", 0),
            hallucination_detected=result.get("hallucination_detected", False),
            step_timings=step_timings,
        )

    async def _probe_judge(self) -> Optional[bool]:
        """The judge's health (bounded); None without a judge to probe."""
        if not self.provider:
            return None
        try:
            healthy = bool(
                await asyncio.wait_for(self.provider.health_check(), JUDGE_HEALTH_TIMEOUT_S)
            )
        except Exception:  # includes the probe timing out
            healthy = False
        self._judge_healthy = healthy
        return healthy

    async def _probe_qdrant(self) -> Tuple[bool, int]:
        """(reachable, points) of the knowledge store, off the event loop and bounded."""
        if not self.qdrant_store:
            return False, 0
        try:
            reachable = bool(
                await asyncio.wait_for(
                    asyncio.to_thread(self.qdrant_store.health_check), QDRANT_PROBE_TIMEOUT_S
                )
            )
            points = 0
            if reachable:
                points = int(
                    await asyncio.wait_for(
                        asyncio.to_thread(self.qdrant_store.count), QDRANT_PROBE_TIMEOUT_S
                    )
                )
            return reachable, points
        except Exception:
            return False, 0

    async def HealthCheck(
        self, request: evaluator_pb2.HealthRequest, context
    ) -> evaluator_pb2.HealthResponse:
        """
        Health check endpoint.

        Dependencies are reported under the keys `judge` and, with a knowledge
        store, `qdrant` (D10). `healthy` is true when all of them are.
        """
        logger.debug("Health check requested")

        dependencies: Dict[str, bool] = {}
        judge_healthy = await self._probe_judge()
        if judge_healthy is not None:
            dependencies["judge"] = judge_healthy
        else:
            # No judge: unhealthy when the engine was started without one,
            # otherwise (a bare servicer) assumed fine.
            dependencies["judge"] = not self._no_judge

        kb_points = 0
        if self.qdrant_store:
            dependencies["qdrant"], kb_points = await self._probe_qdrant()

        response = evaluator_pb2.HealthResponse(
            healthy=all(dependencies.values()),
            version=self._version,
            dependencies=dependencies,
            kb_claims=kb_points,
            knowledge_enabled=self.qdrant_store is not None,
        )
        if self.engine is not None:
            judge = self.engine.judge
            response.judge_provider = judge.effective
            response.judge_model = judge.model
            response.judge_fallback_active = judge.fallback_active
            response.knowledge_enabled = self.engine.knowledge.enabled
        return response

    async def GetEngineInfo(
        self, request: evaluator_pb2.EngineInfoRequest, context
    ) -> evaluator_pb2.EngineInfo:
        """
        What the engine runs with (SPEC section 2.2): version, judge (with a
        fresh bounded health probe), knowledge, verifier, scoring settings,
        warnings and the concurrency limits the proxy sizes its workers by.
        """
        judge_healthy = await self._probe_judge()
        qdrant_reachable, kb_points = await self._probe_qdrant()
        if self.engine is not None:
            info = engine_info(
                self.engine,
                judge_healthy=judge_healthy,
                qdrant_reachable=qdrant_reachable,
                kb_claims=kb_points,
            )
        else:
            info = evaluator_pb2.EngineInfo(version=self._version)
        info.max_audits = self.max_audits
        if not info.max_rpcs:
            info.max_rpcs = DEFAULT_MAX_RPCS
        return info

    async def IngestDocuments(
        self, request: evaluator_pb2.IngestRequest, context
    ) -> evaluator_pb2.IngestResponse:
        """
        Ingest documents into the knowledge base.

        With a ClaimIngestor configured (the default when RAG is enabled),
        documents are decomposed into atomic claims that must pass the Gate-1
        entailment check; otherwise falls back to legacy chunk ingestion.
        """
        logger.info(f"Ingesting {len(request.documents)} documents")

        if not self.embedding_service or not self.qdrant_store:
            context.set_code(grpc.StatusCode.UNAVAILABLE)
            context.set_details("Embedding service or Qdrant store not configured")
            return evaluator_pb2.IngestResponse(
                documents_ingested=0,
                status="unavailable: embedding service or vector store not configured",
            )

        try:
            await self.qdrant_store.aensure_collection()

            if self.ingestor is not None:
                documents = []
                for doc in request.documents:
                    meta = dict(doc.metadata) if doc.metadata else {}
                    if doc.id:
                        meta["doc_id"] = doc.id
                    documents.append({"content": doc.content, "metadata": meta})

                report = await self.ingestor.ingest_documents(documents)

                claim_results = [
                    evaluator_pb2.ClaimIngestResult(
                        claim_id=r.claim_id,
                        claim=r.claim,
                        source_doc_id=r.source_doc_id,
                        status=_KB_STATUS_TO_PROTO[r.status],
                        entailment_score=r.entailment_score,
                        conflicts_with=r.conflicts_with,
                    )
                    for r in report.claim_results
                ]
                logger.info(
                    f"Claim-level ingest: {report.accepted} accepted, "
                    f"{report.quarantined} quarantined, {report.conflicts_detected} conflicts"
                )
                return evaluator_pb2.IngestResponse(
                    documents_ingested=report.documents,
                    status="success",
                    claim_results=claim_results,
                    claims_accepted=report.accepted,
                    claims_quarantined=report.quarantined,
                    conflicts_detected=report.conflicts_detected,
                )

            # Legacy chunk-level ingestion (no LLM available for Gate-1).
            texts = [doc.content for doc in request.documents]
            metadata = []
            for doc in request.documents:
                meta = dict(doc.metadata) if doc.metadata else {}
                if doc.id:
                    meta["doc_id"] = doc.id
                metadata.append(meta)

            # Off the event loop (D8): encoding and the Qdrant upsert block.
            vectors = await self.embedding_service.aembed(texts)
            count = await self.qdrant_store.aupsert_documents(
                texts=texts, vectors=vectors, metadata=metadata
            )

            logger.info(f"Successfully ingested {count} documents (legacy chunk mode)")
            return evaluator_pb2.IngestResponse(documents_ingested=count, status="success")

        except Exception as e:
            logger.error(f"Document ingestion failed: {e}", exc_info=True)
            return evaluator_pb2.IngestResponse(documents_ingested=0, status=f"error: {str(e)}")

    # ------------------------------------------------------------------
    # Knowledge-base queries (VERITAS-lite)
    # ------------------------------------------------------------------

    async def _kb_claims(self) -> List[Dict[str, Any]]:
        """All claim points from the store, newest first (paged, off the event loop; D8, D9)."""
        points: List[Dict[str, Any]] = await self.qdrant_store.ascroll_points(
            must={"kind": "claim"}
        )
        points.sort(key=lambda p: p.get("ingested_at_ms", 0), reverse=True)
        return points

    @staticmethod
    def _to_kb_claim(point: Dict) -> evaluator_pb2.KBClaim:
        return evaluator_pb2.KBClaim(
            claim_id=point["id"],
            claim=point.get("text", ""),
            source_doc_id=str(point.get("source_doc_id", "")),
            source_excerpt=point.get("source_excerpt", ""),
            status=_KB_STATUS_TO_PROTO.get(
                point.get("kb_status", ""), evaluator_pb2.KB_CLAIM_STATUS_UNSPECIFIED
            ),
            entailment_score=float(point.get("entailment_score", 0.0)),
            conflicts_with=[str(c) for c in point.get("conflicts_with", [])],
            ingested_at_ms=int(point.get("ingested_at_ms", 0)),
        )

    async def ListKBClaims(
        self, request: evaluator_pb2.ListKBClaimsRequest, context
    ) -> evaluator_pb2.ListKBClaimsResponse:
        if not self.qdrant_store:
            context.set_code(grpc.StatusCode.UNAVAILABLE)
            context.set_details("Vector store not configured")
            return evaluator_pb2.ListKBClaimsResponse()

        points = await self._kb_claims()
        if request.status_filter != evaluator_pb2.KB_CLAIM_STATUS_UNSPECIFIED:
            wanted = _PROTO_TO_KB_STATUS[request.status_filter]
            points = [p for p in points if p.get("kb_status") == wanted]

        total = len(points)
        limit = min(request.limit or 50, 200)
        offset = max(request.offset, 0)
        page = points[offset : offset + limit]

        return evaluator_pb2.ListKBClaimsResponse(
            claims=[self._to_kb_claim(p) for p in page], total=total
        )

    async def ListConflicts(
        self, request: evaluator_pb2.ListConflictsRequest, context
    ) -> evaluator_pb2.ListConflictsResponse:
        if not self.qdrant_store:
            context.set_code(grpc.StatusCode.UNAVAILABLE)
            context.set_details("Vector store not configured")
            return evaluator_pb2.ListConflictsResponse()

        points = {p["id"]: p for p in await self._kb_claims()}
        seen: set = set()
        pairs = []
        for point in points.values():
            for other_id in point.get("conflicts_with", []):
                key = tuple(sorted((point["id"], str(other_id))))
                if key in seen or str(other_id) not in points:
                    continue
                seen.add(key)
                pairs.append(
                    evaluator_pb2.ConflictPair(
                        claim_a=self._to_kb_claim(point),
                        claim_b=self._to_kb_claim(points[str(other_id)]),
                    )
                )

        total = len(pairs)
        limit = min(request.limit or 50, 200)
        return evaluator_pb2.ListConflictsResponse(conflicts=pairs[:limit], total=total)

    async def GetKBStats(
        self, request: evaluator_pb2.KBStatsRequest, context
    ) -> evaluator_pb2.KBStatsResponse:
        if not self.qdrant_store:
            context.set_code(grpc.StatusCode.UNAVAILABLE)
            context.set_details("Vector store not configured")
            return evaluator_pb2.KBStatsResponse()

        points = await self._kb_claims()
        accepted = sum(1 for p in points if p.get("kb_status") == "accepted")
        quarantined = sum(1 for p in points if p.get("kb_status") == "quarantined")

        seen: set = set()
        ids = {p["id"] for p in points}
        for point in points:
            for other_id in point.get("conflicts_with", []):
                if str(other_id) in ids:
                    seen.add(tuple(sorted((point["id"], str(other_id)))))

        return evaluator_pb2.KBStatsResponse(
            total_claims=len(points),
            accepted=accepted,
            quarantined=quarantined,
            conflict_pairs=len(seen),
        )


def create_server(
    servicer: AuditServicer,
    host: str = "127.0.0.1",
    port: int = 50051,
    max_rpcs: int = DEFAULT_MAX_RPCS,
    interceptors: Sequence[Any] = (),
) -> aio.Server:
    """
    Create and configure the gRPC server.

    Args:
        servicer: AuditServicer instance
        host: Host to bind to. Loopback by default; the engine binds a
              non-loopback address (0.0.0.0 in compose, where the port stays
              on the internal network) only with an internal token.
        port: Port to listen on
        max_rpcs: Concurrent RPCs (TRUTHTABLE_ENGINE_MAX_RPCS); further calls
                  fail fast with RESOURCE_EXHAUSTED instead of piling up (C-H13)
        interceptors: Server interceptors (the internal-token check)

    Returns:
        Configured gRPC server
    """
    logger.info(f"Creating gRPC server (max {max_rpcs} concurrent RPCs)")

    server = aio.server(interceptors=list(interceptors), maximum_concurrent_rpcs=max_rpcs)
    evaluator_pb2_grpc.add_AuditServiceServicer_to_server(servicer, server)
    server.add_insecure_port(f"{host}:{port}")

    logger.info(f"gRPC server configured on {host}:{port}")

    return server
