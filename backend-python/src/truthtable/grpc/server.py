"""
gRPC Server Implementation

Implements the AuditService defined in evaluator.proto
"""

import logging
import uuid
import time
from typing import Dict

import grpc
from grpc import aio

from .. import __version__
from .pb import evaluator_pb2
from .pb import evaluator_pb2_grpc
from ..graphs.audit_graph import run_audit
from ..graphs.state import VerificationStatus, AuditState
from ..metrics import (
    AUDITS_TOTAL,
    AUDIT_DURATION,
    FAITHFULNESS_SCORE,
    HALLUCINATIONS_DETECTED,
    ACTIVE_AUDITS,
    CLAIMS_TOTAL,
)

logger = logging.getLogger(__name__)

# In-memory storage for audit results (for demo - use Redis in production)
_audit_results: Dict[str, AuditState] = {}

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
    - SubmitAudit: Start async audit (returns immediately)
    - GetAuditResult: Retrieve completed audit results
    - HealthCheck: Service health status
    """

    def __init__(
        self, audit_graph, provider=None, qdrant_store=None, embedding_service=None, ingestor=None
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
        """
        self.audit_graph = audit_graph
        self.provider = provider
        self.qdrant_store = qdrant_store
        self.embedding_service = embedding_service
        self.ingestor = ingestor
        self._version = __version__
        logger.info("AuditServicer initialized")

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
            _audit_results[audit_id] = result

            # Record metrics
            score = result.get("faithfulness_score", 0.0)
            AUDITS_TOTAL.labels(status="success").inc()
            AUDIT_DURATION.observe(duration)
            FAITHFULNESS_SCORE.observe(score)
            if result.get("hallucination_detected"):
                HALLUCINATIONS_DETECTED.inc()
            for cv in result.get("claim_verifications", []):
                status_val = cv.get("status", "unknown")
                if hasattr(status_val, "value"):
                    status_val = status_val.value
                CLAIMS_TOTAL.labels(status=status_val).inc()

            logger.info(f"Audit {audit_id} completed with score: {score:.2f}")

            return evaluator_pb2.AuditSubmission(
                audit_id=audit_id, status="completed", queue_position=0
            )

        except Exception as e:
            logger.error(f"Audit failed: {e}", exc_info=True)
            AUDITS_TOTAL.labels(status="error").inc()
            # Store error result
            _audit_results[audit_id] = {"error": str(e), "request_id": request.request_id}
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

        # Map VerificationStatus to proto enum
        status_map = {
            VerificationStatus.SUPPORTED: evaluator_pb2.VERIFICATION_STATUS_SUPPORTED,
            VerificationStatus.UNSUPPORTED: evaluator_pb2.VERIFICATION_STATUS_UNSUPPORTED,
            VerificationStatus.PARTIALLY_SUPPORTED: evaluator_pb2.VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
            VerificationStatus.UNKNOWN: evaluator_pb2.VERIFICATION_STATUS_UNSPECIFIED,
            "supported": evaluator_pb2.VERIFICATION_STATUS_SUPPORTED,
            "unsupported": evaluator_pb2.VERIFICATION_STATUS_UNSUPPORTED,
            "partially_supported": evaluator_pb2.VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
            "unknown": evaluator_pb2.VERIFICATION_STATUS_UNSPECIFIED,
        }

        # Build claim verifications
        claims = []
        if result.get("claim_verifications"):
            for cv in result["claim_verifications"]:
                status_val = cv.get("status", "unknown")
                if hasattr(status_val, "value"):
                    status_val = status_val.value

                claims.append(
                    evaluator_pb2.ClaimVerification(
                        claim=cv.get("claim", ""),
                        status=status_map.get(
                            status_val, evaluator_pb2.VERIFICATION_STATUS_UNSPECIFIED
                        ),
                        confidence=cv.get("confidence", 0.0),
                        evidence=cv.get("evidence", []),
                    )
                )

        # Determine grade based on score
        score = result.get("faithfulness_score", 0.0)
        if score >= 0.9:
            grade = evaluator_pb2.TRUST_GRADE_A if hasattr(evaluator_pb2, "TRUST_GRADE_A") else 1
        elif score >= 0.7:
            grade = evaluator_pb2.TRUST_GRADE_B if hasattr(evaluator_pb2, "TRUST_GRADE_B") else 2
        elif score >= 0.5:
            grade = evaluator_pb2.TRUST_GRADE_C if hasattr(evaluator_pb2, "TRUST_GRADE_C") else 3
        else:
            grade = evaluator_pb2.TRUST_GRADE_D if hasattr(evaluator_pb2, "TRUST_GRADE_D") else 4

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

        # Check LLM provider
        if self.provider:
            try:
                dependencies["ollama"] = await self.provider.health_check()
            except Exception:
                dependencies["ollama"] = False
        else:
            dependencies["ollama"] = True  # Assume ok if no provider

        # Check Qdrant vector store
        if self.qdrant_store:
            try:
                dependencies["qdrant"] = self.qdrant_store.health_check()
            except Exception:
                dependencies["qdrant"] = False

        # Overall health
        healthy = all(dependencies.values()) if dependencies else True

        return evaluator_pb2.HealthResponse(
            healthy=healthy, version=self._version, dependencies=dependencies
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
