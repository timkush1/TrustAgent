"""
Knowledge-base RPCs keep blocking I/O off the event loop (D8): the servicer
uses the async forms of the embedding service and the Qdrant store, never the
blocking calls, so one ingest or listing cannot stall the audits and RPCs the
engine is serving.
"""

from typing import Any, AsyncIterator, Dict, List, Optional

import pytest
from grpc import aio

from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer

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
