"""Node implementations for the audit graph."""

from typing import Any

from .decomposer import DecomposerNode
from .verifier import VerifierNode
from .scorer import ScorerNode

__all__ = [
    "DecomposerNode",
    "RetrieverNode",
    "VerifierNode",
    "ScorerNode",
]


def __getattr__(name: str) -> Any:
    # RetrieverNode belongs to the knowledge add-on; importing it lazily keeps
    # `import truthtable.graphs` free of sentence-transformers and torch.
    if name == "RetrieverNode":
        from .retriever import RetrieverNode

        return RetrieverNode
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
