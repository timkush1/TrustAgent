"""
Claim Decomposer Node

This node breaks down an LLM response into atomic, verifiable claims.

Why decompose? Consider this LLM response:
  "Paris is the capital of France and was founded in 250 BC by Julius Caesar."

This contains TWO claims:
  1. "Paris is the capital of France" ← TRUE
  2. "Paris was founded in 250 BC by Julius Caesar" ← FALSE

By decomposing, we can verify each claim independently and catch
partial hallucinations that might otherwise go undetected.
"""

import logging
from typing import List

from ...providers.base import LLMProvider, CompletionRequest
from ..state import AuditState

logger = logging.getLogger(__name__)


# Prompt engineering for claim extraction
DECOMPOSER_SYSTEM_PROMPT = """You are a claim extraction expert. Your job is to break down text into atomic, verifiable claims.

Rules:
1. Each claim should be a single factual statement
2. Claims should be self-contained (understandable without context)
3. Extract ALL claims, even implicit ones
4. Do not add information not present in the original text
5. Do not evaluate truth - just extract claims

Output format:
Return ONLY a JSON array of strings, like:
["claim 1", "claim 2", "claim 3"]

No explanations, no markdown, just the JSON array."""


def create_decomposer_prompt(response: str) -> str:
    """
    Create the user message for claim decomposition.
    
    Args:
        response: The LLM response to decompose
        
    Returns:
        Formatted prompt for the decomposer
    """
    return f"""Extract all factual claims from this text:

<text>
{response}
</text>

Remember: Return ONLY the JSON array of claims, nothing else."""


async def decompose_claims(
    llm_response: str,
    provider: LLMProvider
) -> List[str]:
    """
    Decompose an LLM response into atomic claims.
    
    Args:
        llm_response: The text to decompose
        provider: LLM provider to use for extraction
        
    Returns:
        List of extracted claims
        
    Raises:
        RuntimeError: If decomposition fails
    """
    try:
        # Create the completion request
        request = CompletionRequest(
            messages=provider.create_messages(
                system_prompt=DECOMPOSER_SYSTEM_PROMPT,
                user_message=create_decomposer_prompt(llm_response)
            ),
            model=provider.model,
            temperature=0.0,  # Deterministic for consistency
            max_tokens=1024
        )
        
        # Get the LLM to extract claims
        response = await provider.complete(request)
        
        # Parse the JSON array from response
        import json
        claims = json.loads(response.content.strip())
        
        if not isinstance(claims, list):
            raise ValueError("Expected JSON array of claims")
        
        # Filter out empty or very short claims
        claims = [c for c in claims if c and len(c.strip()) > 5]
        
        logger.info(f"Extracted {len(claims)} claims from response")
        return claims
        
    except json.JSONDecodeError as e:
        logger.error(f"Failed to parse claims as JSON: {e}")
        logger.error(f"LLM output was: {response.content}")
        
        # Fallback: treat the whole response as one claim
        logger.warning("Falling back to treating entire response as single claim")
        return [llm_response]
    
    except Exception as e:
        logger.error(f"Claim decomposition failed: {e}")
        raise RuntimeError(f"Failed to decompose claims: {e}") from e


class DecomposerNode:
    """
    LangGraph node for claim decomposition.
    
    This node:
    1. Takes the llm_response from state
    2. Uses an LLM to extract atomic claims
    3. Updates state.claims with the results
    
    Usage in graph:
        node = DecomposerNode(provider=ollama_provider)
        graph.add_node("decompose", node.run)
    """
    
    def __init__(self, provider: LLMProvider):
        """
        Initialize the decomposer node.
        
        Args:
            provider: LLM provider for claim extraction
        """
        self.provider = provider
    
    async def run(self, state: AuditState) -> AuditState:
        """
        Execute claim decomposition.
        
        Args:
            state: Current graph state
            
        Returns:
            Updated state with claims populated
        """
        logger.info(f"Decomposing claims for request {state['request_id']}")
        
        # Extract claims from the LLM response
        claims = await decompose_claims(
            llm_response=state["llm_response"],
            provider=self.provider
        )
        
        # Update state
        state["claims"] = claims
        
        logger.info(f"Decomposed into {len(claims)} claims")
        for i, claim in enumerate(claims, 1):
            logger.debug(f"  Claim {i}: {claim[:100]}...")
        
        return state
