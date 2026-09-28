"""
Knowledge add-on I/O: the async forms run off the event loop (D8) and
scroll_points reads every page (D9).

No Qdrant server and no embedding model: the Qdrant client and the
sentence-transformers model are replaced with in-memory fakes, and the
vectorstore package is imported over a fake sentence-transformers module so
the test never loads torch.
"""

import importlib
import sys
import threading
import types
from dataclasses import dataclass, field
from typing import Any, Dict, Iterator, List, Optional, Tuple

import pytest

pytest.importorskip("qdrant_client")

_VECTORSTORE_MODULES = (
    "truthtable.vectorstore",
    "truthtable.vectorstore.embeddings",
    "truthtable.vectorstore.qdrant_store",
)


class FakeVector(list):
    def tolist(self) -> List[float]:
        return list(self)


class FakeSentenceTransformer:
    def __init__(self, model_name: str):
        self.model_name = model_name
        self.threads: List[str] = []

    def get_sentence_embedding_dimension(self) -> int:
        return 2

    def encode(self, texts: List[str], **kwargs: Any) -> List[FakeVector]:
        self.threads.append(threading.current_thread().name)
        return [FakeVector([float(len(text)), 1.0]) for text in texts]


@pytest.fixture(autouse=True)
def vectorstore(monkeypatch: pytest.MonkeyPatch) -> Iterator[types.ModuleType]:
    """The vectorstore package, imported fresh over a fake sentence-transformers."""
    fake = types.ModuleType("sentence_transformers")
    fake.SentenceTransformer = FakeSentenceTransformer  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "sentence_transformers", fake)
    for name in _VECTORSTORE_MODULES:
        monkeypatch.delitem(sys.modules, name, raising=False)
    package = importlib.import_module("truthtable.vectorstore")
    yield package
    for name in _VECTORSTORE_MODULES:
        sys.modules.pop(name, None)


@dataclass
class Record:
    id: str
    payload: Dict[str, Any]


@dataclass
class FakeClient:
    """The QdrantClient calls QdrantStore makes, over an in-memory point list."""

    points: List[Record]
    scroll_calls: List[Tuple[int, Optional[int]]] = field(default_factory=list)
    threads: List[str] = field(default_factory=list)

    def scroll(
        self,
        collection_name: str,
        scroll_filter: Any,
        limit: int,
        offset: Optional[int] = None,
        with_payload: bool = True,
        with_vectors: bool = False,
    ) -> Tuple[List[Record], Optional[int]]:
        self.threads.append(threading.current_thread().name)
        self.scroll_calls.append((limit, offset))
        start = offset or 0
        page = self.points[start : start + limit]
        next_offset = start + limit if start + limit < len(self.points) else None
        return page, next_offset

    def get_collections(self) -> Any:
        self.threads.append(threading.current_thread().name)
        return types.SimpleNamespace(collections=[])

    def set_payload(self, collection_name: str, payload: Dict[str, Any], points: List[str]) -> None:
        self.threads.append(threading.current_thread().name)
        for record in self.points:
            if record.id in points:
                record.payload.update(payload)


def make_store(count: int) -> Tuple[Any, FakeClient]:
    client = FakeClient([Record(f"p{i}", {"text": f"claim {i}"}) for i in range(count)])
    store_class = sys.modules["truthtable.vectorstore.qdrant_store"].QdrantStore
    store = store_class.__new__(store_class)
    store.url = "http://qdrant.invalid"
    store.collection_name = "test"
    store.vector_dimension = 3
    store._client = client
    return store, client


PAGE = 1000  # qdrant_store.SCROLL_PAGE_SIZE


def test_scroll_points_reads_every_page():
    assert sys.modules["truthtable.vectorstore.qdrant_store"].SCROLL_PAGE_SIZE == PAGE
    store, client = make_store(2 * PAGE + 500)

    points = store.scroll_points(must={"kind": "claim"})

    assert len(points) == 2 * PAGE + 500
    assert points[0] == {"id": "p0", "text": "claim 0"}
    assert points[-1]["id"] == f"p{2 * PAGE + 499}"
    assert client.scroll_calls == [
        (PAGE, None),
        (PAGE, PAGE),
        (PAGE, 2 * PAGE),
    ]


def test_scroll_points_stops_at_the_limit():
    store, client = make_store(5000)

    points = store.scroll_points(limit=1500)

    assert len(points) == 1500
    assert client.scroll_calls == [(1000, None), (500, 1000)]


def test_scroll_points_small_pages_and_an_empty_collection():
    store, client = make_store(5)
    assert [p["id"] for p in store.scroll_points(page_size=2)] == [f"p{i}" for i in range(5)]
    assert client.scroll_calls == [(2, None), (2, 2), (2, 4)]

    empty, _ = make_store(0)
    assert empty.scroll_points() == []

    with pytest.raises(ValueError):
        store.scroll_points(page_size=0)


async def test_async_forms_run_in_a_worker_thread():
    store, client = make_store(3)
    main = threading.current_thread().name

    points = await store.ascroll_points()
    healthy = await store.ahealth_check()
    await store.aset_payload("p1", {"kb_status": "quarantined"})

    assert [p["id"] for p in points] == ["p0", "p1", "p2"]
    assert healthy is True
    assert client.points[1].payload["kb_status"] == "quarantined"
    assert client.threads and main not in client.threads


async def test_aembed_encodes_on_the_dedicated_embedding_thread(vectorstore):
    service = vectorstore.EmbeddingService("fake-model")
    try:
        vectors = await service.aembed(["abc", "de"])
        single = await service.aembed_single("xyz1")
        empty = await service.aembed([])
    finally:
        service.close()

    assert vectors == [[3.0, 1.0], [2.0, 1.0]]
    assert single == [4.0, 1.0]
    assert empty == []
    threads = service._model.threads
    assert len(threads) == 2 and all(name.startswith("embed") for name in threads)
