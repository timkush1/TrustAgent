"""
Integration test for the RAG pipeline.

Tests the full flow: embed -> store -> search -> retrieve.

Prerequisites:
    - Qdrant must be running on localhost:6333
    - Run: docker start truthtable-qdrant

Usage:
    cd backend-python
    .venv/Scripts/python -m pytest tests/integration/test_rag_pipeline.py -v
"""

import pytest
import numpy as np

from truthtable.vectorstore.embeddings import EmbeddingService
from truthtable.vectorstore.qdrant_store import QdrantStore

# Use a separate test collection so we don't pollute production data
TEST_COLLECTION = "truthtable_test"


@pytest.fixture(scope="module")
def embedding_service():
    """Create embedding service once for all tests in this module."""
    return EmbeddingService()


@pytest.fixture(scope="module")
def qdrant_store(embedding_service):
    """Create and populate a test Qdrant collection."""
    store = QdrantStore(
        url="http://localhost:6333",
        collection_name=TEST_COLLECTION,
        vector_dimension=embedding_service.dimension,
    )

    # Clean up any existing test collection
    try:
        store._client.delete_collection(TEST_COLLECTION)
    except Exception:
        pass

    store.ensure_collection()

    # Seed test data
    texts = [
        "Paris is the capital of France.",
        "The Earth orbits the Sun.",
        "Water boils at 100 degrees Celsius.",
        "Python was created by Guido van Rossum in 1991.",
    ]
    vectors = embedding_service.embed(texts)
    store.upsert_documents(texts=texts, vectors=vectors)

    yield store

    # Cleanup
    try:
        store._client.delete_collection(TEST_COLLECTION)
    except Exception:
        pass


class TestEmbeddingService:
    def test_embed_returns_correct_dimension(self, embedding_service):
        vectors = embedding_service.embed(["Hello world"])
        assert len(vectors) == 1
        assert len(vectors[0]) == 384  # MiniLM dimension

    def test_embed_empty_list(self, embedding_service):
        vectors = embedding_service.embed([])
        assert vectors == []

    def test_similar_texts_have_similar_embeddings(self, embedding_service):
        vecs = embedding_service.embed(
            [
                "Paris is the capital of France",
                "France's capital city is Paris",
                "Python is a programming language",
            ]
        )
        # Cosine similarity between similar texts should be high
        sim_12 = np.dot(vecs[0], vecs[1])
        sim_13 = np.dot(vecs[0], vecs[2])
        assert sim_12 > sim_13, "Similar texts should have higher similarity"
        assert sim_12 > 0.8, f"Expected high similarity, got {sim_12}"


class TestQdrantStore:
    def test_search_returns_relevant_results(self, qdrant_store, embedding_service):
        query_vec = embedding_service.embed_single("What is the capital of France?")
        results = qdrant_store.search(query_vector=query_vec, top_k=2)

        assert len(results) > 0
        # The top result should be about Paris/France
        assert "Paris" in results[0]["text"] or "France" in results[0]["text"]

    def test_search_returns_score(self, qdrant_store, embedding_service):
        query_vec = embedding_service.embed_single("Paris France capital")
        results = qdrant_store.search(query_vector=query_vec, top_k=1)

        assert len(results) == 1
        assert results[0]["score"] > 0.3  # Above threshold

    def test_count(self, qdrant_store):
        assert qdrant_store.count() == 4

    def test_health_check(self, qdrant_store):
        assert qdrant_store.health_check() is True
