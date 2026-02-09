"""
Audit Graph

This module defines the LangGraph workflow for auditing LLM responses.

The graph orchestrates three nodes in sequence:
1. Decompose → Extract claims from response
2. Verify → Check each claim against context  
3. Score → Calculate faithfulness score

LangGraph manages the state flow automatically.
"""

import logging
from langgraph.graph import StateGraph, END

from .state import AuditState
from .nodes import DecomposerNode, VerifierNode, ScorerNode
from ..providers.base import LLMProvider

logger = logging.getLogger(__name__)


def build_audit_graph(provider: LLMProvider) -> StateGraph:
    """
    Build the audit workflow graph.
    
    Workflow:
        START → decompose → verify → score → END
    
    Args:
        provider: LLM provider to use for all nodes
        
    Returns:
        Compiled StateGraph ready to execute
        
    Usage:
        graph = build_audit_graph(ollama_provider)
        result = await graph.ainvoke(initial_state)
    """
    logger.info("Building audit graph")
    
    # Create nodes
    decomposer = DecomposerNode(provider=provider)
    verifier = VerifierNode(provider=provider)
    scorer = ScorerNode()
    
    # Initialize graph with state schema
    workflow = StateGraph(AuditState)
    
    # Add nodes to graph
    workflow.add_node("decompose", decomposer.run)
    workflow.add_node("verify", verifier.run)
    workflow.add_node("score", scorer.run)
    
    # Define edges (execution flow)
    workflow.set_entry_point("decompose")
    workflow.add_edge("decompose", "verify")
    workflow.add_edge("verify", "score")
    workflow.add_edge("score", END)
    
    # Compile the graph
    app = workflow.compile()
    
    logger.info("Audit graph compiled successfully")
    
    return app


async def run_audit(
    graph: StateGraph,
    request_id: str,
    user_query: str,
    llm_response: str,
    context_docs: list[str]
) -> AuditState:
    """
    Execute the audit workflow.
    
    Args:
        graph: Compiled audit graph
        request_id: Unique identifier for this audit
        user_query: The user's question
        llm_response: The LLM's answer to audit
        context_docs: Context documents from RAG
        
    Returns:
        Final AuditState with all results
        
    Example:
        graph = build_audit_graph(provider)
        result = await run_audit(
            graph=graph,
            request_id="abc-123",
            user_query="What is the capital of France?",
            llm_response="Paris is the capital of France.",
            context_docs=["France is a country in Europe. Its capital is Paris."]
        )
        
        print(f"Score: {result['faithfulness_score']}")
        print(f"Hallucination: {result['hallucination_detected']}")
    """
    logger.info(f"Starting audit for request {request_id}")
    
    # Create initial state
    initial_state: AuditState = {
        "request_id": request_id,
        "user_query": user_query,
        "llm_response": llm_response,
        "context_docs": context_docs,
        "claims": None,
        "claim_verifications": None,
        "faithfulness_score": None,
        "hallucination_detected": None,
        "reasoning_trace": None
    }
    
    # Execute graph
    final_state = await graph.ainvoke(initial_state)
    
    logger.info(
        f"Audit complete for {request_id}: "
        f"score={final_state['faithfulness_score']:.3f}"
    )
    
    return final_state
