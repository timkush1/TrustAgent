"""
Fact Verifier Node

This node verifies each claim against the provided context using
Natural Language Inference (NLI).

NLI is a technique where we ask: "Given this context, does this claim logically follow?"
Possible answers:
- ENTAILMENT: The claim is supported by the context
- CONTRADICTION: The claim conflicts with the context  
- NEUTRAL: Can't determine (claim not addressed in context)
"""

import json
import logging
from typing import List

from ...providers.base import LLMProvider, CompletionRequest
from ..state import AuditState, ClaimVerification, VerificationStatus

logger = logging.getLogger(__name__)


# Prompt for NLI-based verification
VERIFIER_SYSTEM_PROMPT = """You are a fact verification expert. Given a claim and context documents, determine if the claim is supported by the context.

Your task:
1. Read the context carefully
2. Determine if the claim is supported, contradicted, or not addressed
3. Find specific evidence (quotes) that support your determination

Classification:
- SUPPORTED: The claim is backed by the context
- UNSUPPORTED: The claim contradicts the context OR has no supporting evidence
- PARTIALLY_SUPPORTED: Some aspects are supported, others are not

Output format (JSON only, no markdown):
{
  "status": "SUPPORTED" | "UNSUPPORTED" | "PARTIALLY_SUPPORTED",
  "confidence": 0.95,
  "evidence": ["quote from context 1", "quote from context 2"],
  "reasoning": "Brief explanation"
}"""


def create_verifier_prompt(claim: str, context_docs: List[str]) -> str:
    """
    Create the verification prompt.
    
    Args:
        claim: The claim to verify
        context_docs: List of context documents
        
    Returns:
        Formatted prompt
    """
    # Combine context docs
    context_text = "\n\n".join([
        f"[Document {i+1}]\n{doc}"
        for i, doc in enumerate(context_docs)
    ])
    
    return f"""Verify this claim against the context:

<claim>
{claim}
</claim>

<context>
{context_text}
</context>

Return ONLY the JSON object, nothing else."""


async def verify_claim(
    claim: str,
    context_docs: List[str],
    provider: LLMProvider
) -> ClaimVerification:
    """
    Verify a single claim against context.
    
    Args:
        claim: The claim to verify
        context_docs: Context documents to verify against
        provider: LLM provider
        
    Returns:
        ClaimVerification with results
    """
    try:
        # Create verification request
        request = CompletionRequest(
            messages=provider.create_messages(
                system_prompt=VERIFIER_SYSTEM_PROMPT,
                user_message=create_verifier_prompt(claim, context_docs)
            ),
            model=provider.model,
            temperature=0.0,  # Deterministic
            max_tokens=512
        )
        
        # Get verification from LLM
        response = await provider.complete(request)
        
        # Parse JSON response
        result = json.loads(response.content.strip())
        
        # Map status string to enum
        status_str = result["status"].upper()
        status = VerificationStatus[status_str]
        
        # Build verification object
        verification: ClaimVerification = {
            "claim": claim,
            "status": status,
            "confidence": float(result.get("confidence", 0.5)),
            "evidence": result.get("evidence", [])
        }
        
        logger.debug(
            f"Verified claim: {claim[:50]}... -> {status.value} "
            f"(confidence: {verification['confidence']:.2f})"
        )
        
        return verification
        
    except (json.JSONDecodeError, KeyError, ValueError) as e:
        logger.error(f"Failed to parse verification result: {e}")
        logger.error(f"LLM output was: {response.content}")
        
        # Fallback: mark as unknown with low confidence
        return {
            "claim": claim,
            "status": VerificationStatus.UNKNOWN,
            "confidence": 0.0,
            "evidence": []
        }
    
    except Exception as e:
        logger.error(f"Verification failed for claim '{claim[:50]}...': {e}")
        return {
            "claim": claim,
            "status": VerificationStatus.UNKNOWN,
            "confidence": 0.0,
            "evidence": []
        }


async def verify_all_claims(
    claims: List[str],
    context_docs: List[str],
    provider: LLMProvider
) -> List[ClaimVerification]:
    """
    Verify all claims against context.
    
    Args:
        claims: List of claims to verify
        context_docs: Context documents
        provider: LLM provider
        
    Returns:
        List of ClaimVerification results
    """
    logger.info(f"Verifying {len(claims)} claims against {len(context_docs)} context docs")
    
    # Verify each claim
    # Note: In production, you might want to batch these or use asyncio.gather
    # for parallel processing, but sequential is simpler and avoids rate limits
    verifications = []
    for i, claim in enumerate(claims, 1):
        logger.debug(f"Verifying claim {i}/{len(claims)}")
        verification = await verify_claim(claim, context_docs, provider)
        verifications.append(verification)
    
    # Log summary
    supported = sum(1 for v in verifications if v["status"] == VerificationStatus.SUPPORTED)
    unsupported = sum(1 for v in verifications if v["status"] == VerificationStatus.UNSUPPORTED)
    partial = sum(1 for v in verifications if v["status"] == VerificationStatus.PARTIALLY_SUPPORTED)
    
    logger.info(
        f"Verification complete: {supported} supported, {unsupported} unsupported, "
        f"{partial} partially supported"
    )
    
    return verifications


class VerifierNode:
    """
    LangGraph node for claim verification.
    
    This node:
    1. Takes claims and context_docs from state
    2. Verifies each claim using NLI
    3. Updates state.claim_verifications with results
    
    Usage in graph:
        node = VerifierNode(provider=ollama_provider)
        graph.add_node("verify", node.run)
    """
    
    def __init__(self, provider: LLMProvider):
        """
        Initialize the verifier node.
        
        Args:
            provider: LLM provider for verification
        """
        self.provider = provider
    
    async def run(self, state: AuditState) -> AuditState:
        """
        Execute claim verification.
        
        Args:
            state: Current graph state
            
        Returns:
            Updated state with claim_verifications populated
        """
        logger.info(f"Verifying claims for request {state['request_id']}")
        
        # Verify all claims
        verifications = await verify_all_claims(
            claims=state["claims"],
            context_docs=state["context_docs"],
            provider=self.provider
        )
        
        # Update state
        state["claim_verifications"] = verifications
        
        logger.info(f"Verified {len(verifications)} claims")
        
        return state
