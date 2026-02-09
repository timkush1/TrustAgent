"""
Score Calculator Node

This node aggregates claim verification results into a final faithfulness score.

The faithfulness score indicates how much of the LLM's response is supported
by the provided context. A score of 1.0 means fully faithful, 0.0 means
completely unsupported (hallucination).
"""

import logging
from typing import List

from ..state import AuditState, ClaimVerification, VerificationStatus

logger = logging.getLogger(__name__)


def calculate_faithfulness_score(verifications: List[ClaimVerification]) -> float:
    """
    Calculate faithfulness score from claim verifications.
    
    Scoring logic:
    - SUPPORTED claim: 1.0 points
    - PARTIALLY_SUPPORTED: 0.5 points
    - UNSUPPORTED: 0.0 points
    - UNKNOWN: 0.3 points (conservative estimate)
    
    Final score is the weighted average, taking into account confidence levels.
    
    Args:
        verifications: List of claim verifications
        
    Returns:
        Faithfulness score between 0.0 and 1.0
    """
    if not verifications:
        logger.warning("No verifications to score")
        return 0.0
    
    # Assign base scores
    score_map = {
        VerificationStatus.SUPPORTED: 1.0,
        VerificationStatus.PARTIALLY_SUPPORTED: 0.5,
        VerificationStatus.UNSUPPORTED: 0.0,
        VerificationStatus.UNKNOWN: 0.3,
    }
    
    # Calculate weighted score
    total_weight = 0.0
    weighted_sum = 0.0
    
    for verification in verifications:
        status = verification["status"]
        confidence = verification["confidence"]
        
        # Weight by confidence (claims we're more confident about count more)
        weight = max(confidence, 0.1)  # Minimum weight of 0.1
        base_score = score_map.get(status, 0.0)
        
        weighted_sum += base_score * weight
        total_weight += weight
    
    # Compute average
    if total_weight == 0:
        return 0.0
    
    final_score = weighted_sum / total_weight
    
    logger.debug(
        f"Calculated faithfulness score: {final_score:.3f} "
        f"from {len(verifications)} claims"
    )
    
    return final_score


def detect_hallucination(verifications: List[ClaimVerification]) -> bool:
    """
    Determine if hallucination is detected.
    
    We consider it a hallucination if:
    - Any claim is UNSUPPORTED with high confidence (>0.7), OR
    - More than 30% of claims are UNSUPPORTED or PARTIALLY_SUPPORTED
    
    Args:
        verifications: List of claim verifications
        
    Returns:
        True if hallucination detected
    """
    if not verifications:
        return False
    
    # Check for high-confidence unsupported claims
    for verification in verifications:
        if (verification["status"] == VerificationStatus.UNSUPPORTED and 
            verification["confidence"] > 0.7):
            logger.info(
                f"Hallucination detected: High-confidence unsupported claim: "
                f"{verification['claim'][:50]}..."
            )
            return True
    
    # Check percentage of problematic claims
    problematic = sum(
        1 for v in verifications 
        if v["status"] in [VerificationStatus.UNSUPPORTED, VerificationStatus.PARTIALLY_SUPPORTED]
    )
    
    problematic_ratio = problematic / len(verifications)
    
    if problematic_ratio > 0.3:
        logger.info(
            f"Hallucination detected: {problematic_ratio:.1%} of claims "
            f"are unsupported or partially supported"
        )
        return True
    
    return False


def generate_reasoning_trace(verifications: List[ClaimVerification], score: float) -> str:
    """
    Generate human-readable explanation of the audit results.
    
    Args:
        verifications: List of claim verifications
        score: Calculated faithfulness score
        
    Returns:
        Reasoning trace as formatted string
    """
    # Count by status
    status_counts = {
        VerificationStatus.SUPPORTED: 0,
        VerificationStatus.PARTIALLY_SUPPORTED: 0,
        VerificationStatus.UNSUPPORTED: 0,
        VerificationStatus.UNKNOWN: 0,
    }
    
    for v in verifications:
        status_counts[v["status"]] += 1
    
    total = len(verifications)
    
    # Build trace
    trace_lines = [
        f"Faithfulness Score: {score:.2f}/1.00",
        f"",
        f"Total Claims Analyzed: {total}",
        f"  ✓ Supported: {status_counts[VerificationStatus.SUPPORTED]} ({status_counts[VerificationStatus.SUPPORTED]/total*100:.1f}%)",
        f"  ⚠ Partially Supported: {status_counts[VerificationStatus.PARTIALLY_SUPPORTED]} ({status_counts[VerificationStatus.PARTIALLY_SUPPORTED]/total*100:.1f}%)",
        f"  ✗ Unsupported: {status_counts[VerificationStatus.UNSUPPORTED]} ({status_counts[VerificationStatus.UNSUPPORTED]/total*100:.1f}%)",
        f"  ? Unknown: {status_counts[VerificationStatus.UNKNOWN]} ({status_counts[VerificationStatus.UNKNOWN]/total*100:.1f}%)",
        f"",
    ]
    
    # Add details for problematic claims
    unsupported_claims = [v for v in verifications if v["status"] == VerificationStatus.UNSUPPORTED]
    if unsupported_claims:
        trace_lines.append("Unsupported Claims:")
        for i, v in enumerate(unsupported_claims[:5], 1):  # Show max 5
            trace_lines.append(f"  {i}. {v['claim'][:100]}...")
        
        if len(unsupported_claims) > 5:
            trace_lines.append(f"  ... and {len(unsupported_claims) - 5} more")
        trace_lines.append("")
    
    return "\n".join(trace_lines)


class ScorerNode:
    """
    LangGraph node for score calculation.
    
    This node:
    1. Takes claim_verifications from state
    2. Calculates faithfulness score
    3. Detects hallucinations
    4. Generates reasoning trace
    5. Updates state with final results
    
    Usage in graph:
        node = ScorerNode()
        graph.add_node("score", node.run)
    """
    
    async def run(self, state: AuditState) -> AuditState:
        """
        Execute score calculation.
        
        Args:
            state: Current graph state
            
        Returns:
            Updated state with final scores and reasoning
        """
        logger.info(f"Calculating scores for request {state['request_id']}")
        
        verifications = state["claim_verifications"]
        
        # Calculate faithfulness score
        score = calculate_faithfulness_score(verifications)
        
        # Detect hallucinations
        hallucination = detect_hallucination(verifications)
        
        # Generate reasoning trace
        reasoning = generate_reasoning_trace(verifications, score)
        
        # Update state
        state["faithfulness_score"] = score
        state["hallucination_detected"] = hallucination
        state["reasoning_trace"] = reasoning
        
        logger.info(
            f"Audit complete: score={score:.3f}, hallucination={hallucination}"
        )
        
        return state
