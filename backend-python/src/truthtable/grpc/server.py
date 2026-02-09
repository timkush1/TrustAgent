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

from .pb import evaluator_pb2
from .pb import evaluator_pb2_grpc
from ..graphs.audit_graph import run_audit
from ..graphs.state import VerificationStatus, AuditState

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
    
    def __init__(self, audit_graph, provider=None):
        """
        Initialize the servicer.
        
        Args:
            audit_graph: Compiled LangGraph workflow
            provider: LLM provider for health checks (optional)
        """
        self.audit_graph = audit_graph
        self.provider = provider
        self._version = "0.1.0"
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
            result["processing_time_ms"] = int((time.time() - start_time) * 1000)
            _audit_results[audit_id] = result
            
            logger.info(f"Audit {audit_id} completed with score: {result.get('faithfulness_score', 0):.2f}")
            
            return evaluator_pb2.AuditSubmission(
                audit_id=audit_id,
                status="completed",
                queue_position=0
            )
            
        except Exception as e:
            logger.error(f"Audit failed: {e}", exc_info=True)
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
            VerificationStatus.PARTIALLY_SUPPORTED: evaluator_pb2.VERIFICATION_STATUS_UNSUPPORTED,
            VerificationStatus.UNKNOWN: evaluator_pb2.VERIFICATION_STATUS_UNSPECIFIED,
            "supported": evaluator_pb2.VERIFICATION_STATUS_SUPPORTED,
            "unsupported": evaluator_pb2.VERIFICATION_STATUS_UNSUPPORTED,
            "partially_supported": evaluator_pb2.VERIFICATION_STATUS_UNSUPPORTED,
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
        
        return evaluator_pb2.AuditResult(
            audit_id=audit_id,
            request_id=result.get("request_id", ""),
            status=evaluator_pb2.AUDIT_STATUS_COMPLETED,
            faithfulness_score=score,
            grade=grade,
            claims=claims,
            reasoning_trace=result.get("reasoning_trace", ""),
            completed_at_ms=int(time.time() * 1000),
            processing_time_ms=result.get("processing_time_ms", 0)
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
        
        # Overall health
        healthy = all(dependencies.values()) if dependencies else True
        
        return evaluator_pb2.HealthResponse(
            healthy=healthy,
            version=self._version,
            dependencies=dependencies
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
