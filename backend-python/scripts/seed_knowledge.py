"""
Knowledge Base Seed Script

Loads curated facts into Qdrant for use as the verification knowledge base.

This script:
1. Reads seed data from data/seed_knowledge.json
2. Generates embeddings for each fact using sentence-transformers
3. Stores the embeddings + text in Qdrant

Usage (from the backend-python directory):
    .venv/Scripts/python scripts/seed_knowledge.py

Prerequisites:
    - Qdrant must be running: docker start truthtable-qdrant
    - The seed data file must exist at data/seed_knowledge.json
"""

import json
import logging
import sys
import time
from pathlib import Path

# Setup path so we can import from our project
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))

from truthtable.vectorstore.embeddings import EmbeddingService
from truthtable.vectorstore.qdrant_store import QdrantStore

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger(__name__)


def load_seed_data() -> list:
    """Load seed knowledge from JSON file."""
    data_path = Path(__file__).parent.parent / "data" / "seed_knowledge.json"

    if not data_path.exists():
        logger.error(f"Seed data file not found: {data_path}")
        sys.exit(1)

    with open(data_path, "r", encoding="utf-8") as f:
        data = json.load(f)

    logger.info(f"Loaded {len(data)} seed documents from {data_path}")
    return data


def main():
    print("=" * 60)
    print("TruthTable Knowledge Base Seeder")
    print("=" * 60)
    print()

    # 1. Load seed data
    print("[1/4] Loading seed data...")
    seed_data = load_seed_data()
    texts = [item["text"] for item in seed_data]
    metadata = [
        {"category": item.get("category", "general"), "source": item.get("source", "unknown")}
        for item in seed_data
    ]
    print(f"       Loaded {len(texts)} documents")
    print()

    # 2. Initialize embedding service
    print("[2/4] Loading embedding model (this may take a moment on first run)...")
    start = time.time()
    embed_service = EmbeddingService()
    print(f"       Model loaded in {time.time() - start:.1f}s")
    print(f"       Vector dimension: {embed_service.dimension}")
    print()

    # 3. Generate embeddings
    print("[3/4] Generating embeddings...")
    start = time.time()
    vectors = embed_service.embed(texts)
    print(f"       Generated {len(vectors)} embeddings in {time.time() - start:.1f}s")
    print()

    # 4. Store in Qdrant
    print("[4/4] Storing in Qdrant...")
    qdrant_url = "http://localhost:6333"
    store = QdrantStore(url=qdrant_url, vector_dimension=embed_service.dimension)

    # Create collection (idempotent - safe to call multiple times)
    store.ensure_collection()

    # Upsert documents
    count = store.upsert_documents(texts=texts, vectors=vectors, metadata=metadata)
    total = store.count()
    print(f"       Stored {count} documents (total in collection: {total})")
    print()

    # Verification: test a search
    print("=" * 60)
    print("Verification: Testing search...")
    test_query = "What is the capital of France?"
    query_vector = embed_service.embed_single(test_query)
    results = store.search(query_vector=query_vector, top_k=3)

    print(f"  Query: '{test_query}'")
    print(f"  Top {len(results)} results:")
    for i, r in enumerate(results, 1):
        print(f"    {i}. (score={r['score']:.3f}) {r['text'][:80]}...")
    print()
    print("Seeding complete!")
    print("=" * 60)


if __name__ == "__main__":
    main()
