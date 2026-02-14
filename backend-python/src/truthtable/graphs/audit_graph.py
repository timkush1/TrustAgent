"""
Audit Graph

This module defines the LangGraph workflow for auditing LLM responses.

The graph orchestrates four nodes in sequence:
1. Decompose -> Extract claims from response
2. Retrieve  -> Fetch relevant context from Qdrant knowledge base (NEW!)
3. Verify    -> Check each claim against retrieved context
4. Score     -> Calculate faithfulness score

LangGraph manages the state flow automatically.

KEY CHANGE: Previously this was decompose -> verify -> score, but the
verify step had NO context to check against (context_docs was always empty).
By adding the "retrieve" step, we now populate context_docs with real
knowledge from Qdrant before verification begins.
"""

import logging
from typing import Optional

from langgraph.graph import StateGraph, END

from .state import AuditState
from .nodes import DecomposerNode, RetrieverNode, VerifierNode, ScorerNode
from ..providers.base import LLMProvider
from ..vectorstore.embeddings import EmbeddingService
from ..vectorstore.qdrant_store import QdrantStore

logger = logging.getLogger(__name__)


def build_audit_graph(
    provider: LLMProvider,
    embedding_service: Optional[EmbeddingService] = None,
    qdrant_store: Optional[QdrantStore] = None,
) -> StateGraph:
    """
    Build the audit workflow graph.

    Workflow (with RAG):
        START -> decompose -> retrieve -> verify -> score -> END

    Workflow (without RAG, fallback):
        START -> decompose -> verify -> score -> END

    If embedding_service or qdrant_store is None, the retrieve step
    is skipped (for backward compatibility / testing without Qdrant).

    Args:
        provider: LLM provider to use for decompose and verify nodes
        embedding_service: Embedding service for vector search (optional)
        qdrant_store: Qdrant vector store for knowledge retrieval (optional)

    Returns:
        Compiled StateGraph ready to execute
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

    # Conditionally add the retrieve node
    has_retrieval = embedding_service is not None and qdrant_store is not None

    if has_retrieval:
        retriever = RetrieverNode(
            embedding_service=embedding_service,
            qdrant_store=qdrant_store,
        )
        workflow.add_node("retrieve", retriever.run)

        # Flow: decompose -> retrieve -> verify -> score -> END
        workflow.set_entry_point("decompose")
        workflow.add_edge("decompose", "retrieve")
        workflow.add_edge("retrieve", "verify")
        workflow.add_edge("verify", "score")
        workflow.add_edge("score", END)

        logger.info("Audit graph built WITH retrieval (Qdrant enabled)")
    else:
        # Flow without retrieval: decompose -> verify -> score -> END
        workflow.set_entry_point("decompose")
        workflow.add_edge("decompose", "verify")
        workflow.add_edge("verify", "score")
        workflow.add_edge("score", END)

        logger.warning(
            "Audit graph built WITHOUT retrieval. "
            "Claims will be verified against empty context. "
            "Set QDRANT_URL to enable knowledge retrieval."
        )

    # Compile the graph
    app = workflow.compile()

    logger.info("Audit graph compiled successfully")

    return app


async def run_audit(
    graph: StateGraph,
    request_id: str,
    user_query: str,
    llm_response: str,
    context_docs: list[str],
) -> AuditState:
    """
    Execute the audit workflow.

    Args:
        graph: Compiled audit graph
        request_id: Unique identifier for this audit
        user_query: The user's question
        llm_response: The LLM's answer to audit
        context_docs: Context documents from RAG (may be empty;
                     the retrieve node will populate from Qdrant)

    Returns:
        Final AuditState with all results
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
        "reasoning_trace": None,
    }

    # Execute graph
    final_state = await graph.ainvoke(initial_state)

    logger.info(
        f"Audit complete for {request_id}: "
        f"score={final_state['faithfulness_score']:.3f}"
    )

    return final_state
