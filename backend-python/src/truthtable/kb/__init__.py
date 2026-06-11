"""
VERITAS-lite knowledge base: claim-level, entailment-gated storage.

Most RAG stores index unverified text chunks. This package upgrades ingestion
so the knowledge base only admits atomic claims that pass a Gate-1 entailment
check against their own source, and detects contradictions between sources at
ingest time. Design and research lineage: docs/KB-DESIGN.md.
"""

from .contradiction import ContradictionDetector
from .hybrid import BM25Index, rrf_fuse
from .ingestion import ClaimIngestor, IngestReport

__all__ = [
    "ClaimIngestor",
    "IngestReport",
    "ContradictionDetector",
    "BM25Index",
    "rrf_fuse",
]
