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
knowledge off; blank documents do not count) to NOT_ASSESSED(no_evidence) with
zero LLM calls, instead of verifying every claim against empty context and
reporting it UNSUPPORTED.
Skipping happens inside the nodes rather than through conditional edges, so
routing stays uniform: once an audit is NOT_ASSESSED every later node returns
the state unchanged.

Retrieval runs only when the caller enables knowledge and the engine has a
knowledge base; when it finds nothing the audit is NOT_ASSESSED
(knowledge_no_hits) and its claims come back NOT_CHECKED.

With `extract_claims_without_evidence` (AuditOptions field 7, or the engine's
TRUTHTABLE_AUDIT_EXTRACT_CLAIMS_WITHOUT_EVIDENCE) an audit without evidence is
still decomposed; the verifier never asks the judge about a claim it has
nothing to check against, so the claims come back NOT_CHECKED and the audit is
NOT_ASSESSED (no_evidence) with the claims located in the answer.

`build_audit_graph` takes the engine settings that shape an audit: the scoring
thresholds (TRUTHTABLE_SCORING_*), TRUTHTABLE_MAX_CLAIMS, the engine's default
TRUTHTABLE_UNSUPPORTED_POLICY (used when the request does not name one) and the
knowledge retriever's top-k and score threshold.
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
from .nodes.decomposer import DEFAULT_MAX_CLAIMS, claim_records
from ..providers.base import LLMProvider
from ..scoring.aggregate import DEFAULT_SCORING, ScoringConfig
from ..scoring.aggregate import resolve_unsupported_policy as resolve_policy

if TYPE_CHECKING:  # knowledge add-on: imported only when retrieval is configured
    from ..vectorstore.embeddings import EmbeddingService
    from ..vectorstore.qdrant_store import QdrantStore

logger = logging.getLogger(__name__)

GraphStep = Callable[[AuditState], Awaitable[AuditState]]

# ContextDocument.source of the documents the knowledge retriever supplies.
KNOWLEDGE_SOURCE = "knowledge"

# decided_by of a claim nobody judged (proto ClaimVerification.decided_by).
DECIDED_BY_NONE = "none"

_POLICY_VALUES = frozenset({"", "auto"} | {policy.value for policy in UnsupportedPolicy})

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
    Resolve AuditOptions.unsupported_policy (scoring.resolve_unsupported_policy).

    "hallucination" and "unverifiable" are taken as given. "", "auto" (and any
    unrecognised value) mean hallucination when at least one of the caller's
    documents is request evidence (any source but knowledge or upload),
    otherwise unverifiable: an answer checked only against the knowledge base
    is not wrong just because the knowledge base does not mention it.
    """
    value = (requested or "").strip().lower()
    if value not in _POLICY_VALUES:
        logger.warning(f"Unknown unsupported_policy {requested!r}; using auto")
    sources = [context_sources[i] if i < len(context_sources) else "" for i in range(documents)]
    return resolve_policy(value, sources)


def is_not_assessed(state: AuditState) -> bool:
    return state.get("verdict") == AuditVerdict.NOT_ASSESSED.value


def mark_not_assessed(state: AuditState, reason: NotAssessedReason) -> AuditState:
    """
    End the audit as NOT_ASSESSED. Claims already extracted come back
    NOT_CHECKED, each still located in the answer (the decomposer's quote and
    span).

    The v1 fields keep their types for legacy callers: score 0.0, no hallucination.
    """
    records = claim_records(state.get("claims") or [], state.get("extracted_claims"))
    unchecked: List[ClaimVerification] = [
        {
            "claim": record["claim"],
            "status": VerificationStatus.NOT_CHECKED,
            "confidence": 0.0,
            "evidence": [],
            "decided_by": DECIDED_BY_NONE,
            "index": index,
            "quote": record["quote"],
            "span_start": record["span_start"],
            "span_end": record["span_end"],
            "span_method": record["span_method"],
        }
        for index, record in enumerate(records)
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

    def __init__(
        self,
        retrieval_available: bool,
        default_policy: str = "auto",
        extract_claims_without_evidence: bool = False,
    ):
        """
        Args:
            retrieval_available: Whether the graph has a knowledge retrieve step.
            default_policy: The policy when the request names none
                (TRUTHTABLE_UNSUPPORTED_POLICY: auto, hallucination or unverifiable).
            extract_claims_without_evidence: Decompose audits without evidence
                (TRUTHTABLE_AUDIT_EXTRACT_CLAIMS_WITHOUT_EVIDENCE); a request can
                also ask for it (AuditOptions.extract_claims_without_evidence).
        """
        self.retrieval_available = retrieval_available
        self.default_policy = default_policy
        self.extract_claims_without_evidence = extract_claims_without_evidence

    async def run(self, state: AuditState) -> AuditState:
        documents = list(state.get("context_docs") or [])
        evidence = [i for i, doc in enumerate(documents) if doc and doc.strip()]
        if len(evidence) < len(documents):
            # A blank document is not evidence: verifying against it would call
            # the judge with empty context and report every claim UNSUPPORTED.
            logger.info(
                f"Audit {state['request_id']}: ignoring "
                f"{len(documents) - len(evidence)} blank evidence document(s)"
            )
            sources = state.get("context_sources") or []
            documents = [documents[i] for i in evidence]
            state["context_docs"] = documents
            if sources:
                state["context_sources"] = [
                    sources[i] if i < len(sources) else "" for i in evidence
                ]

        state["unsupported_policy"] = resolve_unsupported_policy(
            (state.get("unsupported_policy") or "").strip() or self.default_policy,
            state.get("context_sources") or [],
            len(documents),
        ).value

        if documents:
            return state
        if state.get("knowledge_enabled") and self.retrieval_available:
            return state
        if state.get("extract_claims_without_evidence") or self.extract_claims_without_evidence:
            logger.info(
                f"Audit {state['request_id']} has no evidence: extracting its claims anyway "
                "(they come back NOT_CHECKED; no verification call)"
            )
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
    An answer without claims skips retrieval: there is nothing to check, and the
    scorer reports NO_CLAIMS whatever the knowledge base holds.
    """

    def __init__(self, retriever: Any):
        self.retriever = retriever

    async def run(self, state: AuditState) -> AuditState:
        if is_not_assessed(state) or not state.get("knowledge_enabled"):
            return state
        if not state.get("claims"):
            return state

        supplied = len(state.get("context_docs") or [])
        state = await self.retriever.run(state)
        documents = state.get("context_docs") or []
        if not documents:
            logger.info(f"Knowledge retrieval found nothing for audit {state['request_id']}")
            return mark_not_assessed(state, NotAssessedReason.KNOWLEDGE_NO_HITS)
        if not supplied:
            # Every document came from the knowledge base.
            state["context_sources"] = [KNOWLEDGE_SOURCE] * len(documents)
        return state


def build_audit_graph(
    provider: LLMProvider,
    embedding_service: Optional["EmbeddingService"] = None,
    qdrant_store: Optional["QdrantStore"] = None,
    hybrid_retriever: Any = None,
    verifier_concurrency: int = 5,
    *,
    scoring: ScoringConfig = DEFAULT_SCORING,
    max_claims: int = DEFAULT_MAX_CLAIMS,
    unsupported_policy: str = "auto",
    extract_claims_without_evidence: bool = False,
    retriever_top_k: int = 3,
    retriever_score_threshold: float = 0.3,
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
        scoring: Scoring thresholds (ScoringConfig.from_settings)
        max_claims: Most claims per answer (TRUTHTABLE_MAX_CLAIMS)
        unsupported_policy: Policy when a request names none
            (TRUTHTABLE_UNSUPPORTED_POLICY)
        extract_claims_without_evidence: Decompose audits without evidence
            (TRUTHTABLE_AUDIT_EXTRACT_CLAIMS_WITHOUT_EVIDENCE)
        retriever_top_k: Knowledge documents per search (TRUTHTABLE_RETRIEVER_TOP_K)
        retriever_score_threshold: Lowest dense score kept
            (TRUTHTABLE_RETRIEVER_SCORE_THRESHOLD)

    Returns:
        Compiled StateGraph ready to execute
    """
    logger.info("Building audit graph")

    has_retrieval = embedding_service is not None and qdrant_store is not None

    # Create nodes
    preflight = PreflightNode(
        retrieval_available=has_retrieval,
        default_policy=unsupported_policy,
        extract_claims_without_evidence=extract_claims_without_evidence,
    )
    decomposer = DecomposerNode(provider=provider, max_claims=max_claims)
    verifier = VerifierNode(provider=provider, concurrency=verifier_concurrency)
    scorer = ScorerNode(config=scoring)

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
            top_k_per_claim=retriever_top_k,
            score_threshold=retriever_score_threshold,
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
    context_truncated: bool = False,
    extract_claims_without_evidence: bool = False,
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
        unsupported_policy: "", auto, hallucination or unverifiable ("" uses the
                     graph's default, TRUTHTABLE_UNSUPPORTED_POLICY)
        context_truncated: The caller already cut the evidence (a document's
                     ContextDocument.truncated or EvidenceSummary.truncated): the
                     audit records context_trimmed
        extract_claims_without_evidence: Decompose even without evidence
                     (AuditOptions.extract_claims_without_evidence)

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
        "context_truncated": context_truncated,
        "extract_claims_without_evidence": extract_claims_without_evidence,
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
