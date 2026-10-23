"""
The knowledge stack's seed with the mock judge (LLM_PROVIDER=mock; D131).

make knowledge-seed uploads the knowledge documents of the scenario packs
(trafficgen --seed-knowledge -> POST /api/kb/documents -> IngestDocuments), and
the engine's claim-level ingestion asks its judge to decompose each document,
to verify each claim against its own source (Gate-1) and to check each accepted
claim against the nearest accepted ones. Here the engine's own mock judge
(judge.build_mock_judge over the contracts packs) answers all of it through the
real IngestDocuments RPC: every document becomes one accepted claim, its own
text, which RetrieveContext then returns as the knowledge evidence the
scenario expects (scenarios.v1 knowledge: "RetrieveContext returns them").
Seeding twice stays consistent, and a document the packs do not list fails its
ingest loudly instead of being invented.
"""

from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Dict, List, Tuple

import pytest
from grpc import aio

from test_kb import FakeEmbedding, FakeQdrant
from truthtable.config import Settings
from truthtable.contracts import DEMO_FAULTS, DEMO_SCENARIOS, load_contract
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer
from truthtable.judge import MOCK_JUDGE_MODEL, build_mock_judge
from truthtable.kb import ClaimIngestor, ContradictionDetector
from truthtable.kb.contradiction import CONTRADICTION_SYSTEM_PROMPT
from truthtable.providers.base import CompletionRequest, CompletionResponse, MessageRole
from truthtable.providers.mock import FixtureNotFoundError, MockLLMProvider

RPC_TIMEOUT_S = 10.0

PACKS = [load_contract(DEMO_SCENARIOS), load_contract(DEMO_FAULTS)]

# The scenarios trafficgen --seed-knowledge seeds for: every scenario with knowledge documents.
KNOWLEDGE_SCENARIOS: List[Dict[str, Any]] = [
    scenario for pack in PACKS for scenario in pack["scenarios"] if scenario.get("knowledge")
]


def _documents() -> List[Dict[str, str]]:
    """The knowledge documents, once per id in first-seen order (as trafficgen uploads them)."""
    seen: Dict[str, Dict[str, str]] = {}
    for scenario in KNOWLEDGE_SCENARIOS:
        for document in scenario["knowledge"]:
            seen.setdefault(document["id"], document)
    return list(seen.values())


KNOWLEDGE: List[Dict[str, str]] = _documents()


class KnowledgeStore(FakeQdrant):
    """The in-memory claim store, with what RetrieveContext and IngestDocuments also call."""

    def count(self) -> int:
        return len(self.points)

    async def aensure_collection(self) -> None:
        return None


class RecordingMockJudge(MockLLMProvider):
    """The engine's mock judge, recording the system prompt of every call and every miss."""

    def __init__(self, inner: MockLLMProvider):
        super().__init__(model=inner.model, fixtures=inner.fixtures, scenarios=inner.scenarios)
        self.systems: List[str] = []
        self.misses: List[str] = []

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.systems.append(
            next((m.content for m in request.messages if m.role == MessageRole.SYSTEM), "")
        )
        try:
            return await super().complete(request)
        except FixtureNotFoundError as exc:
            self.misses.append(str(exc))
            raise


def _engine_mock_judge() -> RecordingMockJudge:
    """What the knowledge stack's engine builds for LLM_PROVIDER=mock (the contracts packs)."""
    settings = Settings(_env_file=None, llm_provider="mock", mock_fixtures_path="")
    return RecordingMockJudge(build_mock_judge(settings, MOCK_JUDGE_MODEL))


@asynccontextmanager
async def knowledge_engine() -> AsyncIterator[Tuple[pb_grpc.AuditServiceStub, Any, Any]]:
    """The knowledge add-on as main.py wires it, served on 127.0.0.1:<ephemeral>."""
    judge, store, embeddings = _engine_mock_judge(), KnowledgeStore(), FakeEmbedding()
    servicer = AuditServicer(
        audit_graph=None,
        provider=judge,
        qdrant_store=store,
        embedding_service=embeddings,
        ingestor=ClaimIngestor(
            provider=judge,
            embedding_service=embeddings,
            qdrant_store=store,
            detector=ContradictionDetector(provider=judge),
        ),
    )
    server = aio.server()
    pb_grpc.add_AuditServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield pb_grpc.AuditServiceStub(channel), judge, store
    finally:
        await channel.close()
        await server.stop(grace=None)
        if servicer.knowledge_retriever is not None:
            servicer.knowledge_retriever.close()


async def _seed(stub: pb_grpc.AuditServiceStub, documents: List[Dict[str, str]]) -> Any:
    """One upload, as the gateway forwards trafficgen's: doc_id, title and source metadata."""
    return await stub.IngestDocuments(
        pb.IngestRequest(
            documents=[
                pb.ContextDocument(
                    id=d["id"],
                    content=d["text"],
                    metadata={
                        "doc_id": d["id"],
                        "title": d["id"],
                        "source": "contracts/demo/scenarios.v1.json",
                    },
                )
                for d in documents
            ]
        ),
        timeout=RPC_TIMEOUT_S,
    )


def test_the_packs_have_knowledge_documents() -> None:
    assert KNOWLEDGE, "no scenario pack lists knowledge documents: make knowledge-seed has none"


@pytest.mark.parametrize("document", KNOWLEDGE, ids=[d["id"] for d in KNOWLEDGE])
async def test_each_knowledge_document_is_accepted_as_one_claim(document: Dict[str, str]) -> None:
    async with knowledge_engine() as (stub, judge, store):
        response = await _seed(stub, [document])

    assert response.status == "success", response.status
    assert (response.documents_ingested, response.claims_accepted) == (1, 1)
    assert (response.claims_quarantined, response.conflicts_detected) == (0, 0)
    [result] = response.claim_results
    assert result.claim == document["text"].strip()
    assert result.source_doc_id == document["id"]
    assert result.status == pb.KB_CLAIM_STATUS_ACCEPTED
    assert result.entailment_score == pytest.approx(1.0)
    # One decomposition and one Gate-1 verification, each booked as kb_ingest.
    assert [c.component for c in response.costs] == ["kb_ingest", "kb_ingest"]
    assert judge.misses == []
    [point] = store.points.values()
    assert point["payload"]["kb_status"] == "accepted"


async def test_the_seed_is_retrieved_and_reseeding_stays_consistent() -> None:
    async with knowledge_engine() as (stub, judge, store):
        first = await _seed(stub, KNOWLEDGE)
        # make knowledge-seed run again: every claim meets its twin from the first seed.
        second = await _seed(stub, KNOWLEDGE)

        retrieved = {}
        for scenario in KNOWLEDGE_SCENARIOS:
            question = next(
                m["content"] for m in scenario["request"]["messages"] if m["role"] == "user"
            )
            answer = await stub.RetrieveContext(
                pb.RetrieveContextRequest(query=question, top_k=5, min_score=0.3),
                timeout=RPC_TIMEOUT_S,
            )
            retrieved[scenario["id"]] = answer

    for response in (first, second):
        assert response.status == "success", response.status
        assert response.documents_ingested == len(KNOWLEDGE)
        assert response.claims_accepted == len(KNOWLEDGE)
        assert (response.claims_quarantined, response.conflicts_detected) == (0, 0)
    # The second seed ran the contradiction check, and the mock judge answered it.
    assert CONTRADICTION_SYSTEM_PROMPT in judge.systems
    assert judge.misses == []

    # RetrieveContext returns each scenario's documents as they are (one claim each). The fake
    # store returns every accepted claim, so this checks the texts, not the ranking.
    for scenario in KNOWLEDGE_SCENARIOS:
        answer = retrieved[scenario["id"]]
        contents = [document.content for document in answer.documents]
        assert answer.index_state == "ready"
        assert all(document.source == "knowledge" for document in answer.documents)
        for document in scenario["knowledge"]:
            assert contents.count(document["text"].strip()) == 1, contents
    assert len(store.points) == 2 * len(KNOWLEDGE)


async def test_a_document_the_packs_do_not_list_fails_its_ingest() -> None:
    unknown = {"id": "not-in-the-pack", "text": "The office closes at 5 pm on Fridays."}

    async with knowledge_engine() as (stub, judge, store):
        response = await _seed(stub, [unknown])

    assert response.documents_ingested == 0
    assert response.status.startswith("error: Failed to decompose claims: No fixture matched")
    assert len(judge.misses) == 1
    assert store.points == {}


async def test_the_ingestor_raises_on_a_missing_fixture() -> None:
    judge, store = _engine_mock_judge(), KnowledgeStore()
    ingestor = ClaimIngestor(provider=judge, embedding_service=FakeEmbedding(), qdrant_store=store)

    with pytest.raises(RuntimeError, match="Failed to decompose claims") as failure:
        await ingestor.ingest_documents([{"content": "Lunch is served at noon.", "metadata": {}}])

    assert isinstance(failure.value.__cause__, FixtureNotFoundError)
    assert store.points == {}
