"""
Embedding Service

Converts text into vector representations using sentence-transformers.

HOW IT WORKS (for learning):
    1. The model (all-MiniLM-L6-v2) reads text and outputs a 384-dimensional vector
    2. Similar texts produce vectors that are close together in "vector space"
    3. We use this to find relevant knowledge for fact-checking

    Example:
        "Paris is the capital of France" -> [0.12, -0.34, 0.56, ...]  (384 numbers)
        "France's capital city is Paris" -> [0.11, -0.33, 0.55, ...]  (very similar!)
        "Python is a programming language" -> [0.87, 0.23, -0.45, ...]  (very different!)

    The "distance" between the first two vectors is small (similar meaning),
    while the distance to the third is large (different meaning).
    This is called "semantic similarity".
"""

import logging
from typing import List

from sentence_transformers import SentenceTransformer

logger = logging.getLogger(__name__)

# The model name - a small, fast model good for semantic search.
# It produces 384-dimensional vectors and runs well on CPU.
# You can swap this for a larger model (e.g., all-mpnet-base-v2 with 768 dims)
# for better accuracy at the cost of speed.
DEFAULT_MODEL = "all-MiniLM-L6-v2"


class EmbeddingService:
    """
    Service for generating text embeddings.

    Uses sentence-transformers to convert text into dense vectors.
    The model is loaded once at initialization and reused for all requests.

    Args:
        model_name: Name of the sentence-transformers model to use.
                   Defaults to all-MiniLM-L6-v2 (384 dimensions, fast, good quality).

    Usage:
        service = EmbeddingService()
        vectors = service.embed(["Hello world", "How are you?"])
        # vectors is a list of 2 vectors, each with 384 floats
    """

    def __init__(self, model_name: str = DEFAULT_MODEL):
        logger.info(f"Loading embedding model: {model_name}")
        self._model = SentenceTransformer(model_name)
        self._dimension = self._model.get_sentence_embedding_dimension()
        logger.info(
            f"Embedding model loaded: {model_name} "
            f"(dimension={self._dimension})"
        )

    @property
    def dimension(self) -> int:
        """The size of vectors this model produces (384 for MiniLM)."""
        return self._dimension

    def embed(self, texts: List[str]) -> List[List[float]]:
        """
        Convert a list of texts into vectors.

        Args:
            texts: List of strings to embed.

        Returns:
            List of vectors. Each vector is a list of floats.
            Length matches input; each vector has `self.dimension` elements.
        """
        if not texts:
            return []

        # encode() does the heavy lifting:
        # 1. Tokenizes text (splits into subwords the model understands)
        # 2. Passes tokens through the neural network
        # 3. Pools the output into a single vector per text
        # normalize_embeddings=True makes cosine similarity easier to compute
        embeddings = self._model.encode(
            texts,
            normalize_embeddings=True,
            show_progress_bar=False,
        )

        # Convert numpy arrays to plain Python lists (for JSON/gRPC compatibility)
        return [vec.tolist() for vec in embeddings]

    def embed_single(self, text: str) -> List[float]:
        """Convenience method to embed a single text."""
        return self.embed([text])[0]
