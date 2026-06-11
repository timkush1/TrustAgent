"""
Claim-level ingestion with the Gate-1 entailment check.

Instead of indexing raw document chunks, uploaded documents are decomposed
into atomic claims (reusing the audit decomposer), and each claim must be
verified as ENTAILED BY ITS OWN SOURCE (reusing the audit verifier with the
source document as context) before it becomes retrievable:

    document --decompose--> claims --Gate-1 entailment--> accepted | quarantined

Accepted claims are then checked for contradictions against the nearest
existing accepted claims. Quarantined claims are stored (auditable, visible in
the dashboard) but excluded from retrieval, so a decomposition artifact or an
unsupported assertion can never silently become "knowledge".
"""

import logging
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

from ..graphs.nodes.decomposer import decompose_claims
from ..graphs.nodes.verifier import verify_claim
from ..graphs.state import VerificationStatus
from ..providers.base import LLMProvider
from .contradiction import ContradictionDetector, merge_conflicts

logger = logging.getLogger(__name__)

SOURCE_EXCERPT_CHARS = 300


@dataclass
class ClaimResult:
    """Per-claim outcome of ingestion (mirrors proto ClaimIngestResult)."""

    claim_id: str
    claim: str
    source_doc_id: str
    status: str  # "accepted" | "quarantined"
    entailment_score: float
    conflicts_with: List[str] = field(default_factory=list)


@dataclass
class IngestReport:
    """Aggregate outcome of one ingest call (mirrors proto IngestResponse)."""

    documents: int = 0
    claim_results: List[ClaimResult] = field(default_factory=list)

    @property
    def accepted(self) -> int:
        return sum(1 for r in self.claim_results if r.status == "accepted")

    @property
    def quarantined(self) -> int:
        return sum(1 for r in self.claim_results if r.status == "quarantined")

    @property
    def conflicts_detected(self) -> int:
        return sum(len(r.conflicts_with) for r in self.claim_results)


class ClaimIngestor:
    """
    Decompose -> Gate-1 verify -> store -> contradiction-check pipeline.

    Args:
        provider: LLM used for decomposition, entailment, and NLI checks.
        embedding_service: For claim embeddings.
        qdrant_store: Vector store (needs the KB operations: upsert_points,
                      search_filtered, set_payload).
        entailment_threshold: Minimum SUPPORTED-confidence for acceptance.
        detector: Contradiction detector (None disables conflict checks).
        on_change: Optional callback invoked after a successful ingest
                   (used to invalidate the BM25 index).
    """

    def __init__(
        self,
        provider: LLMProvider,
        embedding_service: Any,
        qdrant_store: Any,
        entailment_threshold: float = 0.7,
        detector: Optional[ContradictionDetector] = None,
        on_change: Optional[Any] = None,
    ):
        self.provider = provider
        self.embedding_service = embedding_service
        self.qdrant_store = qdrant_store
        self.entailment_threshold = entailment_threshold
        self.detector = detector
        self.on_change = on_change

    async def ingest_documents(self, documents: List[Dict[str, Any]]) -> IngestReport:
        """
        Args:
            documents: [{"content": str, "metadata": {...}}, ...]

        Returns:
            IngestReport with per-claim acceptance/quarantine + conflicts.
        """
        report = IngestReport(documents=len(documents))
        now_ms = int(time.time() * 1000)

        for document in documents:
            content = document.get("content", "")
            if not content.strip():
                continue
            doc_id = str(document.get("metadata", {}).get("doc_id") or uuid.uuid4())

            claims = await decompose_claims(content, self.provider)
            logger.info(f"Document {doc_id}: decomposed into {len(claims)} claims")

            doc_claim_ids: set[str] = set()
            for claim in claims:
                result = await self._ingest_claim(
                    claim=claim,
                    doc_id=doc_id,
                    source_content=content,
                    metadata=document.get("metadata", {}),
                    sibling_ids=doc_claim_ids,
                    now_ms=now_ms,
                )
                doc_claim_ids.add(result.claim_id)
                report.claim_results.append(result)

        if self.on_change is not None and report.claim_results:
            self.on_change()

        logger.info(
            f"Ingest complete: {report.accepted} accepted, "
            f"{report.quarantined} quarantined, {report.conflicts_detected} conflicts"
        )
        return report

    async def _ingest_claim(
        self,
        claim: str,
        doc_id: str,
        source_content: str,
        metadata: Dict[str, Any],
        sibling_ids: set[str],
        now_ms: int,
    ) -> ClaimResult:
        # Gate-1: is the claim entailed by its own source document?
        verification = await verify_claim(claim, [source_content], self.provider)
        status = verification["status"]
        confidence = verification["confidence"]

        if status == VerificationStatus.SUPPORTED:
            entailment_score = confidence
        elif status == VerificationStatus.PARTIALLY_SUPPORTED:
            entailment_score = confidence * 0.5
        else:
            entailment_score = 0.0

        accepted = entailment_score >= self.entailment_threshold
        kb_status = "accepted" if accepted else "quarantined"
        claim_id = str(uuid.uuid4())

        vector = self.embedding_service.embed([claim])[0]

        # Contradiction check before persisting, so the new claim's payload
        # carries its conflicts from the start. Accepted claims only:
        # quarantined ones can't conflict with anything (not retrievable).
        conflicts: List[Dict[str, Any]] = []
        if accepted and self.detector is not None:
            conflicts = await self.detector.find_conflicts(
                claim_text=claim,
                claim_vector=list(vector),
                qdrant_store=self.qdrant_store,
                exclude_ids=sibling_ids,
            )

        conflict_ids = [c["claim_id"] for c in conflicts]
        payload = {
            "text": claim,
            "kind": "claim",
            "kb_status": kb_status,
            "source_doc_id": doc_id,
            "source_excerpt": source_content[:SOURCE_EXCERPT_CHARS],
            "entailment_score": round(entailment_score, 4),
            "conflicts_with": conflict_ids,
            "ingested_at_ms": now_ms,
            **{k: v for k, v in metadata.items() if k not in ("doc_id",)},
        }
        self.qdrant_store.upsert_points(ids=[claim_id], vectors=[list(vector)], payloads=[payload])

        # Record the conflict on the other side of each pair as well.
        for conflict in conflicts:
            self.qdrant_store.set_payload(
                conflict["claim_id"],
                {"conflicts_with": merge_conflicts(conflict.get("existing_conflicts"), [claim_id])},
            )

        if not accepted:
            logger.info(
                f"Quarantined claim (entailment {entailment_score:.2f} < "
                f"{self.entailment_threshold}): {claim[:80]!r}"
            )

        return ClaimResult(
            claim_id=claim_id,
            claim=claim,
            source_doc_id=doc_id,
            status=kb_status,
            entailment_score=entailment_score,
            conflicts_with=conflict_ids,
        )
