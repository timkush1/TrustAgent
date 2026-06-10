"""
gRPC Server Implementation

Implements the AuditService defined in evaluator.proto
"""

import logging
import uuid
import time
from typing import Dict, Any

import grpc
from grpc import aio

from .. import __version__
from .pb import evaluator_pb2
from .pb import evaluator_pb2_grpc
from ..graphs.audit_graph import run_audit
from ..graphs.state import VerificationStatus, AuditState
from ..metrics import (
    AUDITS_TOTAL, AUDIT_DURATION, FAITHFULNESS_SCORE,
    HALLUCINATIONS_DETECTED, ACTIVE_AUDITS, CLAIMS_TOTAL,
)

logger = logging.getLogger(__name__)

# In-memory storage for audit results (for demo - use Redis in production)
_audit_results: Dict[str, AuditState] = {}


class AuditServicer(evaluator_pb2_grpc.AuditServiceServicer):
    """
    Implementation of the AuditService gRPC service.
    
    Handles:
    - SubmitAudit: Start async audit (returns immediately)
    - GetAuditResult: Retrieve completed audit results
    - HealthCheck: Service health status
    """
    
    def __init__(self, audit_graph, provider=None, qdrant_store=None, embedding_service=None):
        """
        Initialize the servicer.

        Args:
            audit_graph: Compiled LangGraph workflow
            provider: LLM provider for health checks (optional)
            qdrant_store: Qdrant store for health checks (optional)
            embedding_service: Embedding service for document ingestion (optional)
        """
        self.audit_graph = audit_graph
        self.provider = provider
        self.qdrant_store = qdrant_store
        self.embedding_service = embedding_service
        self._version = __version__
        logger.info("AuditServicer initialized")
    
    async def SubmitAudit(self, request: evaluator_pb2.AuditRequest, context) -> evaluator_pb2.AuditSubmission:
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
                context_docs=context_docs
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
                audit_id=audit_id,
                status="completed",
                queue_position=0
            )

        except Exception as e:
            logger.error(f"Audit failed: {e}", exc_info=True)
            AUDITS_TOTAL.labels(status="error").inc()
            # Store error result
            _audit_results[audit_id] = {
                "error": str(e),
                "request_id": request.request_id
            }
            return evaluator_pb2.AuditSubmission(
                audit_id=audit_id,
                status="failed",
                queue_position=0
            )
        finally:
            ACTIVE_AUDITS.dec()
    
    async def GetAuditResult(self, request: evaluator_pb2.AuditResultRequest, context) -> evaluator_pb2.AuditResult:
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
                reasoning_trace=f"Error: {result['error']}"
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
                if hasattr(status_val, 'value'):
                    status_val = status_val.value
                
                claims.append(evaluator_pb2.ClaimVerification(
                    claim=cv.get("claim", ""),
                    status=status_map.get(status_val, evaluator_pb2.VERIFICATION_STATUS_UNSPECIFIED),
                    confidence=cv.get("confidence", 0.0),
                    evidence=cv.get("evidence", [])
                ))
        
        # Determine grade based on score
        score = result.get("faithfulness_score", 0.0)
        if score >= 0.9:
            grade = evaluator_pb2.TRUST_GRADE_A if hasattr(evaluator_pb2, 'TRUST_GRADE_A') else 1
        elif score >= 0.7:
            grade = evaluator_pb2.TRUST_GRADE_B if hasattr(evaluator_pb2, 'TRUST_GRADE_B') else 2
        elif score >= 0.5:
            grade = evaluator_pb2.TRUST_GRADE_C if hasattr(evaluator_pb2, 'TRUST_GRADE_C') else 3
        else:
            grade = evaluator_pb2.TRUST_GRADE_D if hasattr(evaluator_pb2, 'TRUST_GRADE_D') else 4
        
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
    
    async def HealthCheck(self, request: evaluator_pb2.HealthRequest, context) -> evaluator_pb2.HealthResponse:
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
            healthy=healthy,
            version=self._version,
            dependencies=dependencies
        )


    async def IngestDocuments(self, request: evaluator_pb2.IngestRequest, context) -> evaluator_pb2.IngestResponse:
        """
        Ingest documents into the RAG knowledge base.
        """
        logger.info(f"Ingesting {len(request.documents)} documents")

        if not self.embedding_service or not self.qdrant_store:
            context.set_code(grpc.StatusCode.UNAVAILABLE)
            context.set_details("Embedding service or Qdrant store not configured")
            return evaluator_pb2.IngestResponse(
                documents_ingested=0,
                status="unavailable: embedding service or vector store not configured"
            )

        try:
            texts = [doc.content for doc in request.documents]
            metadata = []
            for doc in request.documents:
                meta = dict(doc.metadata) if doc.metadata else {}
                if doc.id:
                    meta["doc_id"] = doc.id
                metadata.append(meta)

            # Embed the documents
            vectors = self.embedding_service.embed(texts)

            # Store in Qdrant
            self.qdrant_store.ensure_collection()
            count = self.qdrant_store.upsert_documents(
                texts=texts,
                vectors=vectors,
                metadata=metadata,
            )

            logger.info(f"Successfully ingested {count} documents")
            return evaluator_pb2.IngestResponse(
                documents_ingested=count,
                status="success"
            )

        except Exception as e:
            logger.error(f"Document ingestion failed: {e}", exc_info=True)
            return evaluator_pb2.IngestResponse(
                documents_ingested=0,
                status=f"error: {str(e)}"
            )


def create_server(
    servicer: AuditServicer,
    host: str = "0.0.0.0",
    port: int = 50051,
    max_workers: int = 10
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
