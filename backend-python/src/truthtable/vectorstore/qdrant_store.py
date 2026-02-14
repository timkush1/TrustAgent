"""
Qdrant Vector Store

Manages storage and retrieval of knowledge documents in Qdrant.

HOW IT WORKS (for learning):
    Qdrant is a "vector database" - it stores vectors and finds similar ones fast.

    Think of it like this:
    - Regular database:  "Find all rows WHERE name = 'Paris'"  (exact match)
    - Vector database:   "Find all rows SIMILAR TO this meaning"  (fuzzy, semantic)

    Architecture:
        Text -> EmbeddingService -> Vector -> Qdrant (store)
        Query -> EmbeddingService -> Vector -> Qdrant (search) -> Matching docs

    Key terms:
    - "Collection" = Like a table in a regular database
    - "Point" = Like a row. Has: ID, vector, payload (the original text + metadata)
    - "Cosine distance" = How we measure similarity between vectors (0=identical, 2=opposite)
"""

import logging
import uuid
from typing import List, Optional, Dict, Any

from qdrant_client import QdrantClient
from qdrant_client.http import models as qdrant_models

logger = logging.getLogger(__name__)

# Name of our collection in Qdrant
DEFAULT_COLLECTION = "truthtable_knowledge"


class QdrantStore:
    """
    Vector store backed by Qdrant for knowledge retrieval.

    This is the 'R' in RAG (Retrieval-Augmented Generation).
    It stores knowledge as vectors and retrieves relevant documents
    when we need to verify claims.

    Args:
        url: Qdrant server URL (e.g., "http://localhost:6333")
        collection_name: Name of the Qdrant collection to use
        vector_dimension: Size of vectors (must match embedding model; 384 for MiniLM)

    Usage:
        store = QdrantStore(url="http://localhost:6333", vector_dimension=384)
        store.ensure_collection()

        # Store knowledge
        store.upsert_documents(
            texts=["Paris is the capital of France"],
            vectors=[[0.12, -0.34, ...]],
            metadata=[{"source": "geography"}]
        )

        # Retrieve relevant knowledge
        results = store.search(query_vector=[0.11, -0.33, ...], top_k=5)
    """

    def __init__(
        self,
        url: str = "http://localhost:6333",
        collection_name: str = DEFAULT_COLLECTION,
        vector_dimension: int = 384,
    ):
        self.url = url
        self.collection_name = collection_name
        self.vector_dimension = vector_dimension

        logger.info(f"Connecting to Qdrant at {url}")
        self._client = QdrantClient(url=url, timeout=10)

    def ensure_collection(self) -> None:
        """
        Create the collection if it doesn't exist.

        A collection is like a database table. We need to create it
        once before storing any data. If it already exists, this is a no-op.

        We use cosine distance because our embeddings are normalized,
        making cosine similarity the standard metric for semantic search.
        """
        try:
            collections = self._client.get_collections().collections
            exists = any(c.name == self.collection_name for c in collections)

            if exists:
                logger.info(f"Collection '{self.collection_name}' already exists")
                return

            self._client.create_collection(
                collection_name=self.collection_name,
                vectors_config=qdrant_models.VectorParams(
                    size=self.vector_dimension,
                    distance=qdrant_models.Distance.COSINE,
                ),
            )
            logger.info(
                f"Created collection '{self.collection_name}' "
                f"(dimension={self.vector_dimension}, distance=cosine)"
            )
        except Exception as e:
            logger.error(f"Failed to ensure collection: {e}")
            raise

    def upsert_documents(
        self,
        texts: List[str],
        vectors: List[List[float]],
        metadata: Optional[List[Dict[str, Any]]] = None,
    ) -> int:
        """
        Store documents (text + vectors) in Qdrant.

        'Upsert' means 'insert or update' -- if a document with the same
        ID exists, it gets updated. Otherwise a new one is created.

        Args:
            texts: The original text content of each document
            vectors: Pre-computed embedding vectors (one per text)
            metadata: Optional metadata for each document (source, category, etc.)

        Returns:
            Number of documents upserted
        """
        if len(texts) != len(vectors):
            raise ValueError(
                f"texts ({len(texts)}) and vectors ({len(vectors)}) must have same length"
            )

        if metadata is None:
            metadata = [{}] * len(texts)

        # Build Qdrant 'points' (their term for records/rows)
        points = []
        for text, vector, meta in zip(texts, vectors, metadata):
            point_id = str(uuid.uuid4())
            payload = {
                "text": text,
                **meta,
            }
            points.append(
                qdrant_models.PointStruct(
                    id=point_id,
                    vector=vector,
                    payload=payload,
                )
            )

        # Upsert in batches of 100
        batch_size = 100
        for i in range(0, len(points), batch_size):
            batch = points[i : i + batch_size]
            self._client.upsert(
                collection_name=self.collection_name,
                points=batch,
            )

        logger.info(f"Upserted {len(points)} documents into '{self.collection_name}'")
        return len(points)

    def search(
        self,
        query_vector: List[float],
        top_k: int = 5,
        score_threshold: float = 0.3,
    ) -> List[Dict[str, Any]]:
        """
        Search for documents similar to the query vector.

        This is the core retrieval operation. Given a query vector,
        Qdrant finds the closest vectors in the collection and returns
        the associated text and metadata.

        Args:
            query_vector: The embedding of the query/claim to search for
            top_k: Maximum number of results to return
            score_threshold: Minimum similarity score (0.0 to 1.0).
                           Documents below this threshold are excluded.
                           0.3 is a reasonable default for MiniLM.

        Returns:
            List of dicts with keys: 'text', 'score', and any metadata
        """
        results = self._client.query_points(
            collection_name=self.collection_name,
            query=query_vector,
            limit=top_k,
            score_threshold=score_threshold,
        )

        docs = []
        for point in results.points:
            doc = {
                "text": point.payload.get("text", ""),
                "score": point.score,
                "id": str(point.id),
            }
            for key, value in point.payload.items():
                if key != "text":
                    doc[key] = value
            docs.append(doc)

        logger.debug(f"Search returned {len(docs)} results (threshold={score_threshold})")
        return docs

    def count(self) -> int:
        """Get the number of documents in the collection."""
        try:
            info = self._client.get_collection(self.collection_name)
            return info.points_count
        except Exception:
            return 0

    def health_check(self) -> bool:
        """Check if Qdrant is reachable."""
        try:
            self._client.get_collections()
            return True
        except Exception as e:
            logger.error(f"Qdrant health check failed: {e}")
            return False
