"""
Audit Graph

This module defines the LangGraph workflow for auditing LLM responses.

The graph orchestrates five nodes in sequence:
1. Preflight -> Decide whether there is anything to check the answer against
2. Decompose -> Extract claims from response
3. Retrieve  -> Fetch context from the Qdrant knowledge base (knowledge add-on)
4. Verify    -> Check each claim against the evidence
5. Score     -> Calculate the faithfulness score and classify the audit

Preflight short-circuits an audit with no evidence (no evidence documents and
knowledge off) to NOT_ASSESSED(no_evidence) with zero LLM calls, instead of
verifying every claim against empty context and reporting it UNSUPPORTED.
Skipping happens inside the nodes rather than through conditional edges, so
routing stays uniform: once an audit is NOT_ASSESSED every later node returns
the state unchanged.

Retrieval runs only when the caller enables knowledge and the engine has a
knowledge base; when it finds nothing the audit is NOT_ASSESSED
(knowledge_no_hits) and its claims come back NOT_CHECKED.
"""

import logging
from typing import TYPE_CHECKING, Any, Awaitable, Callable, List, Optional, Sequence

from langgraph.graph import StateGraph, END

from .state import (
    AuditOutcome,
    AuditState,
    AuditVerdict,
    ClaimVerification,
    NotAssessedReason,
    UnsupportedPolicy,
    VerificationStatus,
)
from .nodes import DecomposerNode, VerifierNode, ScorerNode
from ..providers.base import LLMProvider

if TYPE_CHECKING:  # knowledge add-on: imported only when retrieval is configured
    from ..vectorstore.embeddings import EmbeddingService
    from ..vectorstore.qdrant_store import QdrantStore

logger = logging.getLogger(__name__)

GraphStep = Callable[[AuditState], Awaitable[AuditState]]

# Evidence sources that come from the audited request itself (SPEC section 2.2
# ContextDocument.source). Knowledge-base documents ("knowledge", "upload") are
# not request evidence; a caller document without a source is.
NON_REQUEST_SOURCES = frozenset({"knowledge", "upload"})

_NOT_ASSESSED_TRACES = {
    NotAssessedReason.NO_EVIDENCE: (
        "Not assessed: the request carried no evidence documents to check the answer against."
    ),
    NotAssessedReason.KNOWLEDGE_NO_HITS: (
        "Not assessed: the knowledge base had nothing relevant to check the answer against."
    ),
}


class AuditStepError(RuntimeError):
    """A graph step failed; `error_kind` classifies it (AuditResult.error_kind)."""

    def __init__(self, error_kind: str, message: str):
        super().__init__(message)
        self.error_kind = error_kind


def resolve_unsupported_policy(
    requested: str, context_sources: Sequence[str], documents: int
) -> UnsupportedPolicy:
    """
    Resolve AuditOptions.unsupported_policy.

    "hallucination" and "unverifiable" are taken as given. "", "auto" (and any
    unrecognised value) mean hallucination when at least one of the caller's
    documents is request evidence, otherwise unverifiable: an answer checked
    only against the knowledge base is not wrong just because the knowledge
    base does not mention it.
    """
    value = (requested or "").strip().lower()
    if value in {policy.value for policy in UnsupportedPolicy}:
        return UnsupportedPolicy(value)
    if value not in {"", "auto"}:
        logger.warning(f"Unknown unsupported_policy {requested!r}; using auto")

    sources = [context_sources[i] if i < len(context_sources) else "" for i in range(documents)]
    if any(source not in NON_REQUEST_SOURCES for source in sources):
        return UnsupportedPolicy.HALLUCINATION
    return UnsupportedPolicy.UNVERIFIABLE


def is_not_assessed(state: AuditState) -> bool:
    return state.get("verdict") == AuditVerdict.NOT_ASSESSED.value


def mark_not_assessed(state: AuditState, reason: NotAssessedReason) -> AuditState:
    """
    End the audit as NOT_ASSESSED. Claims already extracted come back NOT_CHECKED.

    The v1 fields keep their types for legacy callers: score 0.0, no hallucination.
    """
    unchecked: List[ClaimVerification] = [
        {
            "claim": claim,
            "status": VerificationStatus.NOT_CHECKED,
            "confidence": 0.0,
            "evidence": [],
        }
        for claim in state.get("claims") or []
    ]
    state["claim_verifications"] = unchecked
    state["faithfulness_score"] = 0.0
    state["hallucination_detected"] = False
    state["reasoning_trace"] = _NOT_ASSESSED_TRACES[reason]
    state["verdict"] = AuditVerdict.NOT_ASSESSED.value
    state["not_assessed_reason"] = reason.value
    state["audit_status"] = AuditOutcome.COMPLETED.value
    state["degraded_reasons"] = []
    state["error_kind"] = None
    return state


class PreflightNode:
    """
    First node: resolves the UNSUPPORTED policy and short-circuits audits that
    have nothing to be checked against (NOT_ASSESSED, no_evidence).
    """

    def __init__(self, retrieval_available: bool):
        """
        Args:
            retrieval_available: Whether the graph has a knowledge retrieve step.
        """
        self.retrieval_available = retrieval_available

    async def run(self, state: AuditState) -> AuditState:
        documents = state.get("context_docs") or []
        state["unsupported_policy"] = resolve_unsupported_policy(
            state.get("unsupported_policy") or "",
            state.get("context_sources") or [],
            len(documents),
        ).value

        if documents:
            return state
        if state.get("knowledge_enabled") and self.retrieval_available:
            return state

        logger.info(
            f"Audit {state['request_id']} has no evidence to check against: "
            "NOT_ASSESSED (no_evidence), no LLM call"
        )
        return mark_not_assessed(state, NotAssessedReason.NO_EVIDENCE)


def _unless_not_assessed(step: GraphStep) -> GraphStep:
    """In-node skip: a NOT_ASSESSED audit passes through `step` unchanged."""

    async def run(state: AuditState) -> AuditState:
        if is_not_assessed(state):
            return state
        return await step(state)

    return run


def _failing_as(error_kind: str, step: GraphStep) -> GraphStep:
    """Re-raise any failure of `step` as an AuditStepError of `error_kind`."""

    async def run(state: AuditState) -> AuditState:
        try:
            return await step(state)
        except Exception as exc:
            raise AuditStepError(error_kind, f"{error_kind}: {exc}") from exc

    return run


class KnowledgeRetrieveNode:
    """
    Knowledge retrieval, skipped inside the node unless the caller enabled
    knowledge. A retrieval that yields no documents ends the audit as
    NOT_ASSESSED (knowledge_no_hits) rather than verifying against nothing.
    """

    def __init__(self, retriever: Any):
        self.retriever = retriever

    async def run(self, state: AuditState) -> AuditState:
        if is_not_assessed(state) or not state.get("knowledge_enabled"):
            return state

        state = await self.retriever.run(state)
        if not state.get("context_docs"):
            logger.info(f"Knowledge retrieval found nothing for audit {state['request_id']}")
            return mark_not_assessed(state, NotAssessedReason.KNOWLEDGE_NO_HITS)
        return state


def build_audit_graph(
    provider: LLMProvider,
    embedding_service: Optional["EmbeddingService"] = None,
    qdrant_store: Optional["QdrantStore"] = None,
    hybrid_retriever: Any = None,
    verifier_concurrency: int = 5,
) -> Any:
    """
    Build the audit workflow graph.

    Workflow (with the knowledge add-on):
        START -> preflight -> decompose -> retrieve -> verify -> score -> END

    Workflow (without it):
        START -> preflight -> decompose -> verify -> score -> END

    If embedding_service or qdrant_store is None there is no retrieve step, and
    an audit without evidence documents is NOT_ASSESSED at preflight.

    Args:
        provider: LLM provider to use for decompose and verify nodes
        embedding_service: Embedding service for vector search (optional)
        qdrant_store: Qdrant vector store for knowledge retrieval (optional)
        hybrid_retriever: HybridClaimRetriever for BM25+dense retrieval (optional)
        verifier_concurrency: Max claims verified in parallel

    Returns:
        Compiled StateGraph ready to execute
    """
    logger.info("Building audit graph")

    has_retrieval = embedding_service is not None and qdrant_store is not None

    # Create nodes
    preflight = PreflightNode(retrieval_available=has_retrieval)
    decomposer = DecomposerNode(provider=provider)
    verifier = VerifierNode(provider=provider, concurrency=verifier_concurrency)
    scorer = ScorerNode()

    # Initialize graph with state schema
    workflow = StateGraph(AuditState)

    # Add nodes to graph
    workflow.add_node("preflight", preflight.run)
    workflow.add_node(
        "decompose", _unless_not_assessed(_failing_as("decomposer_failed", decomposer.run))
    )
    workflow.add_node("verify", _unless_not_assessed(verifier.run))
    workflow.add_node("score", _unless_not_assessed(scorer.run))

    workflow.set_entry_point("preflight")
    workflow.add_edge("preflight", "decompose")

    if embedding_service is not None and qdrant_store is not None:  # has_retrieval
        # Imported here so the engine starts without the knowledge add-on's
        # heavy dependencies (sentence-transformers, torch) unless it uses them.
        from .nodes.retriever import RetrieverNode

        retriever = RetrieverNode(
            embedding_service=embedding_service,
            qdrant_store=qdrant_store,
            hybrid_retriever=hybrid_retriever,
        )
        workflow.add_node("retrieve", KnowledgeRetrieveNode(retriever).run)

        # Flow: preflight -> decompose -> retrieve -> verify -> score -> END
        workflow.add_edge("decompose", "retrieve")
        workflow.add_edge("retrieve", "verify")

        logger.info("Audit graph built WITH retrieval (Qdrant enabled)")
    else:
        # Flow without retrieval: preflight -> decompose -> verify -> score -> END
        workflow.add_edge("decompose", "verify")

        logger.info(
            "Audit graph built without knowledge retrieval: claims are verified against "
            "the evidence documents sent with each audit, and an audit without any is "
            "NOT_ASSESSED."
        )

    workflow.add_edge("verify", "score")
    workflow.add_edge("score", END)

    # Compile the graph
    app = workflow.compile()

    logger.info("Audit graph compiled successfully")

    return app


async def run_audit(
    graph: Any,
    request_id: str,
    user_query: str,
    llm_response: str,
    context_docs: List[str],
    *,
    context_sources: Optional[List[str]] = None,
    knowledge_enabled: bool = True,
    unsupported_policy: str = "",
) -> AuditState:
    """
    Execute the audit workflow.

    Args:
        graph: Compiled audit graph
        request_id: Unique identifier for this audit
        user_query: The user's question
        llm_response: The LLM's answer to audit
        context_docs: Evidence documents (may be empty; with knowledge enabled
                     the retrieve node populates them from Qdrant)
        context_sources: Evidence source of each document (parallel to
                     context_docs; omitted for callers that do not track it)
        knowledge_enabled: Whether knowledge retrieval may supply evidence when
                     the graph has a knowledge base. The default keeps the
                     legacy behaviour: use the knowledge base when there is one.
        unsupported_policy: "", auto, hallucination or unverifiable

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
        "context_sources": list(context_sources or []),
        "knowledge_enabled": knowledge_enabled,
        "unsupported_policy": unsupported_policy,
        "claims": None,
        "claim_verifications": None,
        "faithfulness_score": None,
        "hallucination_detected": None,
        "reasoning_trace": None,
        "verdict": None,
        "not_assessed_reason": None,
        "audit_status": None,
        "degraded_reasons": [],
        "error_kind": None,
        "step_timings": {},  # Initialize for pipeline observability
    }

    # Execute graph
    final_state: AuditState = await graph.ainvoke(initial_state)

    logger.info(
        f"Audit complete for {request_id}: verdict={final_state.get('verdict')}, "
        f"status={final_state.get('audit_status')}, "
        f"score={final_state.get('faithfulness_score') or 0.0:.3f}"
    )

    return final_state
