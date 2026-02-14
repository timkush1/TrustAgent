"""
Context Retriever Node

This node retrieves relevant documents from the Qdrant knowledge base
to provide context for claim verification.

THIS IS THE KEY MISSING PIECE that makes the system actually work.

This is the 'R' in RAG (Retrieval-Augmented Generation):
1. Take the claims extracted by the decomposer
2. For each claim, find relevant knowledge in Qdrant
3. Combine all retrieved documents into context_docs
4. The verifier then checks claims against these documents

Without this node, the verifier has no context and cannot properly
determine if claims are supported or not.

Pipeline position:
    decompose -> [RETRIEVE] -> verify -> score
                  ^^^^^^^^
                  YOU ARE HERE
"""

import logging
from typing import List, Set

from ...vectorstore.embeddings import EmbeddingService
from ...vectorstore.qdrant_store import QdrantStore
from ..state import AuditState

logger = logging.getLogger(__name__)


class RetrieverNode:
    """
    LangGraph node for context retrieval from Qdrant.

    This node:
    1. Takes claims from state (set by DecomposerNode)
    2. Also uses user_query for additional retrieval signal
    3. Embeds each claim and the query using sentence-transformers
    4. Searches Qdrant for relevant knowledge
    5. Deduplicates and updates state.context_docs

    Usage in graph:
        node = RetrieverNode(embedding_service=..., qdrant_store=...)
        graph.add_node("retrieve", node.run)
    """

    def __init__(
        self,
        embedding_service: EmbeddingService,
        qdrant_store: QdrantStore,
        top_k_per_claim: int = 3,
        score_threshold: float = 0.3,
    ):
        """
        Initialize the retriever node.

        Args:
            embedding_service: Service for generating text embeddings
            qdrant_store: Qdrant client for vector search
            top_k_per_claim: How many documents to retrieve per claim
            score_threshold: Minimum similarity score to include a document.
                           Lower = more results but less relevant.
                           Higher = fewer results but more relevant.
                           0.3 is a good starting point for MiniLM.
        """
        self.embedding_service = embedding_service
        self.qdrant_store = qdrant_store
        self.top_k_per_claim = top_k_per_claim
        self.score_threshold = score_threshold

    async def run(self, state: AuditState) -> AuditState:
        """
        Retrieve relevant context for claims.

        Strategy:
        1. Search for the original user query (broad context)
        2. Search for each individual claim (specific context)
        3. Deduplicate by text content
        4. Store in state.context_docs

        Args:
            state: Current graph state (must have claims populated)

        Returns:
            Updated state with context_docs populated
        """
        request_id = state["request_id"]
        claims = state.get("claims") or []
        user_query = state.get("user_query", "")

        logger.info(
            f"Retrieving context for request {request_id}: "
            f"{len(claims)} claims + user query"
        )

        # Collect all search queries: user_query + each claim
        search_texts: List[str] = []
        if user_query:
            search_texts.append(user_query)
        search_texts.extend(claims)

        if not search_texts:
            logger.warning(f"No claims or query to search for in request {request_id}")
            state["context_docs"] = []
            return state

        # Generate embeddings for all search texts at once (batch is faster)
        vectors = self.embedding_service.embed(search_texts)

        # Search Qdrant for each vector, collecting unique documents
        seen_texts: Set[str] = set()
        context_docs: List[str] = []

        for i, (text, vector) in enumerate(zip(search_texts, vectors)):
            label = "query" if i == 0 and user_query else f"claim {i}"

            results = self.qdrant_store.search(
                query_vector=vector,
                top_k=self.top_k_per_claim,
                score_threshold=self.score_threshold,
            )

            for result in results:
                doc_text = result["text"]
                if doc_text not in seen_texts:
                    seen_texts.add(doc_text)
                    context_docs.append(doc_text)
                    logger.debug(
                        f"  Retrieved for {label} (score={result['score']:.3f}): "
                        f"{doc_text[:60]}..."
                    )

        # Update state with retrieved context
        state["context_docs"] = context_docs

        logger.info(
            f"Retrieved {len(context_docs)} unique context documents "
            f"for request {request_id}"
        )

        return state
