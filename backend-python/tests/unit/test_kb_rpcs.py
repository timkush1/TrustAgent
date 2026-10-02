"""
Knowledge-base RPCs keep blocking I/O off the event loop (D8): the servicer
uses the async forms of the embedding service and the Qdrant store, never the
blocking calls, so one ingest or listing cannot stall the audits and RPCs the
engine is serving.

IngestDocuments also returns what its judge calls cost (P4 PY-5): every
decompose and Gate-1 call of a claim-level ingest is one `kb_ingest` record,
a failed ingest's included (the money is spent).
"""

import json
from typing import Any, AsyncIterator, Dict, List, Optional

import pytest
from grpc import aio

from test_kb import FakeEmbedding, FakeQdrant, SequenceProvider, verdict
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer
from truthtable.kb.ingestion import ClaimIngestor
from truthtable.runtime.redaction import Redactor

RPC_TIMEOUT_S = 5


def _claim(point_id: str, status: str, ingested_at_ms: int, conflicts: List[str]) -> Dict[str, Any]:
    return {
        "id": point_id,
        "text": f"claim {point_id}",
        "kind": "claim",
        "kb_status": status,
        "ingested_at_ms": ingested_at_ms,
        "conflicts_with": conflicts,
    }


class AsyncOnlyStore:
    """A Qdrant store whose blocking methods fail the test when called."""

    def __init__(self) -> None:
        self.points = [
            _claim("a", "accepted", 1, ["b"]),
            _claim("b", "quarantined", 2, ["a"]),
            _claim("c", "accepted", 3, []),
        ]
        self.upserted: List[str] = []

    def __getattr__(self, name: str) -> Any:
        raise AssertionError(f"blocking QdrantStore.{name} called on the event loop")

    async def aensure_collection(self) -> None:
        return None

    async def aupsert_documents(
        self,
        texts: List[str],
        vectors: List[List[float]],
        metadata: Optional[List[Dict[str, Any]]] = None,
    ) -> int:
        self.upserted.extend(texts)
        return len(texts)

    async def ascroll_points(
        self, must: Optional[Dict[str, Any]] = None, limit: Optional[int] = None
    ) -> List[Dict[str, Any]]:
        assert must == {"kind": "claim"}
        return [dict(point) for point in self.points]


class AsyncOnlyEmbeddings:
    def embed(self, texts: List[str]) -> List[List[float]]:
        raise AssertionError("blocking EmbeddingService.embed called on the event loop")

    async def aembed(self, texts: List[str]) -> List[List[float]]:
        return [[0.0, 1.0] for _ in texts]


@pytest.fixture
async def kb() -> AsyncIterator[Dict[str, Any]]:
    store = AsyncOnlyStore()
    servicer = AuditServicer(
        audit_graph=None, qdrant_store=store, embedding_service=AsyncOnlyEmbeddings()
    )
    server = aio.server()
    pb_grpc.add_AuditServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield {"stub": pb_grpc.AuditServiceStub(channel), "store": store}
    finally:
        await channel.close()
        await server.stop(grace=None)


async def test_list_claims_newest_first(kb: Dict[str, Any]) -> None:
    response = await kb["stub"].ListKBClaims(pb.ListKBClaimsRequest(), timeout=RPC_TIMEOUT_S)

    assert [claim.claim_id for claim in response.claims] == ["c", "b", "a"]
    assert response.total == 3


async def test_list_claims_filters_by_status(kb: Dict[str, Any]) -> None:
    response = await kb["stub"].ListKBClaims(
        pb.ListKBClaimsRequest(status_filter=pb.KB_CLAIM_STATUS_QUARANTINED),
        timeout=RPC_TIMEOUT_S,
    )

    assert [claim.claim_id for claim in response.claims] == ["b"]


async def test_conflicts_and_stats(kb: Dict[str, Any]) -> None:
    conflicts = await kb["stub"].ListConflicts(pb.ListConflictsRequest(), timeout=RPC_TIMEOUT_S)
    stats = await kb["stub"].GetKBStats(pb.KBStatsRequest(), timeout=RPC_TIMEOUT_S)

    assert conflicts.total == 1
    assert {conflicts.conflicts[0].claim_a.claim_id, conflicts.conflicts[0].claim_b.claim_id} == {
        "a",
        "b",
    }
    assert (stats.total_claims, stats.accepted, stats.quarantined, stats.conflict_pairs) == (
        3,
        2,
        1,
        1,
    )


async def test_legacy_chunk_ingest_uses_the_async_calls(kb: Dict[str, Any]) -> None:
    response = await kb["stub"].IngestDocuments(
        pb.IngestRequest(documents=[pb.ContextDocument(id="d1", content="Paris is in France.")]),
        timeout=RPC_TIMEOUT_S,
    )

    assert (response.status, response.documents_ingested) == ("success", 1)
    assert kb["store"].upserted == ["Paris is in France."]


# ---------------------------------------------------------------------------
# IngestDocuments costs (P4 PY-5)
# ---------------------------------------------------------------------------

# Fake; matches no redaction pattern, so only its registration masks it.
UNMATCHED_SECRET = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"


class ClaimStore(FakeQdrant):
    """The in-memory claim store, optionally failing when a claim is stored."""

    def __init__(self, fail_with: Optional[str] = None) -> None:
        super().__init__()
        self.fail_with = fail_with

    async def aensure_collection(self) -> None:
        return None

    async def aupsert_points(self, ids: Any, vectors: Any, payloads: Any) -> None:
        if self.fail_with is not None:
            raise RuntimeError(self.fail_with)
        await super().aupsert_points(ids, vectors, payloads)


async def _ingest_with_claims(store: ClaimStore) -> pb.IngestResponse:
    judge = SequenceProvider(
        [
            json.dumps(["Paris is the capital of France."]),  # decompose
            verdict("SUPPORTED", 0.95),  # Gate-1
        ]
    )
    servicer = AuditServicer(
        audit_graph=None,
        provider=judge,
        qdrant_store=store,
        embedding_service=FakeEmbedding(),
        ingestor=ClaimIngestor(judge, FakeEmbedding(), store),
        redactor=Redactor([UNMATCHED_SECRET]),
    )
    server = aio.server()
    pb_grpc.add_AuditServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        stub = pb_grpc.AuditServiceStub(channel)
        return await stub.IngestDocuments(
            pb.IngestRequest(
                documents=[pb.ContextDocument(id="d1", content="Paris is the capital of France.")]
            ),
            timeout=RPC_TIMEOUT_S,
        )
    finally:
        await channel.close()
        await server.stop(grace=None)


async def test_claim_ingest_returns_one_kb_ingest_cost_per_judge_call() -> None:
    response = await _ingest_with_claims(ClaimStore())

    assert (response.status, response.claims_accepted) == ("success", 1)
    # The decomposer and the verifier book their calls as kb_ingest, not as audit calls.
    assert [(c.component, c.kind, c.status) for c in response.costs] == [
        ("kb_ingest", "llm", "ok"),
        ("kb_ingest", "llm", "ok"),
    ]
    assert all(c.model == "scripted" for c in response.costs)


async def test_a_failed_ingest_still_returns_its_costs_and_a_redacted_status() -> None:
    response = await _ingest_with_claims(
        ClaimStore(fail_with=f"store refused the write (token {UNMATCHED_SECRET})")
    )

    assert response.status.startswith("error: store refused the write")
    assert UNMATCHED_SECRET not in response.status, "a secret leaked into IngestResponse.status"
    assert [c.component for c in response.costs] == ["kb_ingest", "kb_ingest"]


async def test_legacy_chunk_ingest_costs_nothing(kb: Dict[str, Any]) -> None:
    response = await kb["stub"].IngestDocuments(
        pb.IngestRequest(documents=[pb.ContextDocument(id="d1", content="Paris is in France.")]),
        timeout=RPC_TIMEOUT_S,
    )

    assert response.status == "success"
    assert list(response.costs) == []
