"""
gRPC Server Implementation

Implements the AuditService defined in proto/evaluator.proto (docs/v2/SPEC.md
section 2.2).
"""

import asyncio
import logging
import uuid
import time
from collections import OrderedDict
from typing import Any, Dict, Iterable, Optional, Sequence, Set, Tuple

import grpc
from grpc import aio

from .. import __version__
from .pb import evaluator_pb2
from .pb import evaluator_pb2_grpc
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
from ..providers.mock import FixtureNotFoundError, mock_judge_scope
from ..metrics import (
    AUDITS_TOTAL,
    AUDIT_DURATION,
    FAITHFULNESS_SCORE,
    HALLUCINATIONS_DETECTED,
    ACTIVE_AUDITS,
    CLAIMS_TOTAL,
)

logger = logging.getLogger(__name__)

# Evaluate results carry the v1 scorer's numbers until scoring v2 lands (SPEC
# section 3.2, scoring/aggregate.py); scoring_version tells consumers which.
SCORING_VERSION = "v1"

# HealthCheck asks whether the engine is serving; the judge's health is only
# reported. Its probe is bounded so a slow or unreachable judge (a provider
# health check may wait up to 10 s) shows as judge=False instead of running the
# RPC past the deadline of `python -m truthtable.healthcheck` (3 s by default)
# and marking a serving engine unhealthy.
JUDGE_HEALTH_TIMEOUT_S = 2.0

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
# "-1 means unaligned"). The S0 decomposer does not align claims; spans arrive
# with decomposer v2 (text/spans.py).
UNALIGNED_SPAN = -1

# Verdicts that come with a trust score (NO_CLAIMS and NOT_ASSESSED have none).
_SCORED_VERDICTS = frozenset(
    {AuditVerdict.GROUNDED, AuditVerdict.HALLUCINATED, AuditVerdict.UNVERIFIABLE}
)

_AUDIT_STATUS: Dict[AuditOutcome, int] = {
    AuditOutcome.COMPLETED: evaluator_pb2.AUDIT_STATUS_COMPLETED,
    AuditOutcome.DEGRADED: evaluator_pb2.AUDIT_STATUS_DEGRADED,
    AuditOutcome.FAILED: evaluator_pb2.AUDIT_STATUS_FAILED,
}


def grade_for_score(score: float) -> int:
    """TrustGrade for a trust score (cutoffs documented on the proto TrustGrade)."""
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


def _v2_claim(index: int, cv: ClaimVerification) -> evaluator_pb2.ClaimVerification:
    """v2 ClaimVerification: UNKNOWN=5 and NOT_CHECKED=6 keep their own values (D6)."""
    status = coerce_status(cv.get("status"))
    # quote is an optional extra a decomposer may add (decomposer v2 locates claims, P2).
    extras: Dict[str, Any] = dict(cv)
    return evaluator_pb2.ClaimVerification(
        index=index,
        claim=cv.get("claim", ""),
        status=_V2_CLAIM_STATUS[status],
        confidence=float(cv.get("confidence", 0.0)),
        evidence=list(cv.get("evidence") or []),
        reasoning=cv.get("reasoning", ""),
        quote=str(extras.get("quote") or ""),
        decided_by="llm" if status in DECIDED_STATUSES else "none",
        span_start=UNALIGNED_SPAN,
        span_end=UNALIGNED_SPAN,
    )


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

    The other v2 RPCs (GetEngineInfo, RetrieveContext, ApplyRuntimeConfig,
    DraftEvaluator) are not overridden yet, so they inherit the generated
    handlers and return UNIMPLEMENTED until their phase lands.
    """

    def __init__(
        self,
        audit_graph,
        provider=None,
        qdrant_store=None,
        embedding_service=None,
        ingestor=None,
        judge_provider: str = "",
        judge_fallback_active: bool = False,
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
            judge_provider: Name of the judge in use (HealthResponse.judge_provider,
                      e.g. "anthropic", "ollama", "mock"); "" when not known
            judge_fallback_active: Whether the configured judge was unavailable and
                      the engine runs on its fallback judge instead
        """
        self.audit_graph = audit_graph
        self.provider = provider
        self.qdrant_store = qdrant_store
        self.embedding_service = embedding_service
        self.ingestor = ingestor
        self.judge_provider = judge_provider
        self.judge_fallback_active = judge_fallback_active
        self._version = __version__
        logger.info("AuditServicer initialized")

    async def Evaluate(
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
            with mock_judge_scope(options.mock_scenario) as mock_judge:
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
        result = evaluator_pb2.AuditResult(
            audit_id=audit_id,
            request_id=request_id,
            trace_id=trace_id,
            completed_at_ms=int(time.time() * 1000),
            processing_time_ms=total_ms,
            scoring_version=SCORING_VERSION,
        )
        # Echo what the proxy decided and sent, so the result is self-contained.
        if request.HasField("gate"):
            result.gate.CopyFrom(request.gate)
        if request.HasField("evidence_summary"):
            result.evidence_summary.CopyFrom(request.evidence_summary)

        timings = dict((state or {}).get("step_timings") or {})
        timings["total_ms"] = total_ms
        result.step_timings.update({key: int(value) for key, value in timings.items()})

        verifications: Sequence[ClaimVerification] = (state or {}).get("claim_verifications") or []
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

        # Scores are absent when not computable: no trust score without verdict
        # claims, and none at all for NOT_ASSESSED (SPEC section 3.2).
        result.unsupported_policy = state.get("unsupported_policy") or ""
        statuses = [coerce_status(cv.get("status")) for cv in verifications]
        result.scores.claims_total = len(statuses)
        result.scores.claims_decided = sum(1 for s in statuses if s in DECIDED_STATUSES)
        result.scores.claims_unknown = sum(1 for s in statuses if s is VerificationStatus.UNKNOWN)
        if verdict in _SCORED_VERDICTS:
            trust_score = float(state.get("faithfulness_score") or 0.0)
            result.scores.trust_score = trust_score
            result.faithfulness_score = trust_score
            result.grade = grade_for_score(trust_score)
        return result

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
        Submit an audit request and process synchronously.

        In a production system, this would queue the job and return immediately.
        For simplicity, we process synchronously here.
        """
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

    async def HealthCheck(
        self, request: evaluator_pb2.HealthRequest, context
    ) -> evaluator_pb2.HealthResponse:
        """
        Health check endpoint.
        """
        logger.debug("Health check requested")

        # Check dependencies
        dependencies = {}

        # Check the audit judge (whichever provider it is; the v1 key was "ollama")
        if self.provider:
            try:
                dependencies["judge"] = bool(
                    await asyncio.wait_for(self.provider.health_check(), JUDGE_HEALTH_TIMEOUT_S)
                )
            except Exception:  # includes the probe timing out
                dependencies["judge"] = False
        else:
            dependencies["judge"] = True  # Assume ok if no provider

        # Check Qdrant vector store
        if self.qdrant_store:
            try:
                dependencies["qdrant"] = self.qdrant_store.health_check()
            except Exception:
                dependencies["qdrant"] = False

        # Overall health
        healthy = all(dependencies.values()) if dependencies else True

        return evaluator_pb2.HealthResponse(
            healthy=healthy,
            version=self._version,
            dependencies=dependencies,
            judge_provider=self.judge_provider,
            judge_model=str(getattr(self.provider, "model", "") or ""),
            judge_fallback_active=self.judge_fallback_active,
            knowledge_enabled=self.qdrant_store is not None,
        )

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
            self.qdrant_store.ensure_collection()

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

            vectors = self.embedding_service.embed(texts)
            count = self.qdrant_store.upsert_documents(
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

    def _kb_claims(self) -> list:
        """All claim points from the store, newest first."""
        points = self.qdrant_store.scroll_points(must={"kind": "claim"})
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

        points = self._kb_claims()
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

        points = {p["id"]: p for p in self._kb_claims()}
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

        points = self._kb_claims()
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
    # 0.0.0.0 is intentional: reachable only on the internal Docker network,
    # not published to the host (see docker-compose.yml).
    servicer: AuditServicer,
    host: str = "0.0.0.0",  # nosec B104
    port: int = 50051,
    max_workers: int = 10,
) -> aio.Server:
    """
    Create and configure the gRPC server.

    Args:
        servicer: AuditServicer instance
        host: Host to bind to
        port: Port to listen on
        max_workers: Maximum concurrent requests

    Returns:
        Configured gRPC server
    """
    logger.info(f"Creating gRPC server (max_workers={max_workers})")

    # Create server
    server = aio.server()

    # Register servicer
    evaluator_pb2_grpc.add_AuditServiceServicer_to_server(servicer, server)

    # Bind to address
    server.add_insecure_port(f"{host}:{port}")

    logger.info(f"gRPC server configured on {host}:{port}")

    return server
