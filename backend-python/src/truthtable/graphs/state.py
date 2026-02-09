"""
LangGraph State Definitions

Defines the state schema for the audit workflow.
LangGraph uses TypedDict to define what data flows through the graph.
"""

from typing import TypedDict, List, Optional
from enum import Enum


class VerificationStatus(str, Enum):
    """Status of a claim verification."""
    SUPPORTED = "supported"                # Claim is backed by context
    UNSUPPORTED = "unsupported"            # Claim contradicts or lacks evidence
    PARTIALLY_SUPPORTED = "partially_supported"  # Some evidence exists
    UNKNOWN = "unknown"                    # Not yet verified


class ClaimVerification(TypedDict):
    """Verification result for a single claim."""
    claim: str                             # The extracted claim
    status: VerificationStatus             # Verification outcome
    confidence: float                      # Confidence score (0.0-1.0)
    evidence: List[str]                    # Supporting/refuting snippets


class AuditState(TypedDict):
    """
    State that flows through the audit graph.
    
    Each node in the graph reads from and writes to this state.
    LangGraph automatically manages state updates.
    
    Input fields (provided initially):
        request_id: Unique identifier for tracking
        user_query: The question asked
        llm_response: The answer to verify
        context_docs: Retrieved documents from RAG
    
    Intermediate fields (populated by nodes):
        claims: Extracted claims from the response
        claim_verifications: Verification results per claim
    
    Output fields (final results):
        faithfulness_score: How much is supported (0.0-1.0)
        hallucination_detected: True if any unsupported claims
        reasoning_trace: Explanation of findings
    """
    
    # Input
    request_id: str
    user_query: str
    llm_response: str
    context_docs: List[str]
    
    # Intermediate
    claims: Optional[List[str]]
    claim_verifications: Optional[List[ClaimVerification]]
    
    # Output
    faithfulness_score: Optional[float]
    hallucination_detected: Optional[bool]
    reasoning_trace: Optional[str]
