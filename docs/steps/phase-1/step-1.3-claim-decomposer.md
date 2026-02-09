# Step 1.3: Claim Decomposer Node

## 🎯 Goal

Build the first node in our LangGraph workflow: the **Claim Decomposer**. This node takes an LLM response and breaks it down into individual, atomic factual claims that can be independently verified.

**Example:**
- **Input:** "Paris is the capital of France, and it has a population of about 2 million people."
- **Output:** 
  1. "Paris is the capital of France"
  2. "Paris has a population of about 2 million people"

---

## 📚 Prerequisites

- Completed Step 1.2 (Ollama Provider working)
- Understanding of async Python

---

## 🧠 Concepts Explained

### What is LangGraph?

LangGraph is a library for building AI workflows as **state machines**. Think of it like a flowchart where:
- **Nodes** = Individual steps/functions
- **Edges** = Connections between steps
- **State** = Data that flows through the workflow

```
┌──────────────┐      ┌──────────────┐      ┌──────────────┐
│  Decomposer  │ ───► │   Verifier   │ ───► │    Scorer    │
│   (Node 1)   │      │   (Node 2)   │      │   (Node 3)   │
└──────────────┘      └──────────────┘      └──────────────┘
       │                     │                     │
       ▼                     ▼                     ▼
   claims[]           verifications[]        overall_score
```

### State in LangGraph

State is a `TypedDict` that flows through all nodes. Each node can read and modify it:

```python
class AuditState(TypedDict):
    # Initial input
    llm_response: str
    
    # Added by decomposer
    claims: list[str]
    
    # Added by verifier
    verifications: list[dict]
    
    # Added by scorer
    score: float
```

### Node Functions

A node is just a function that takes state and returns updates:

```python
async def decompose(state: AuditState) -> dict:
    # Read from state
    response = state["llm_response"]
    
    # Do work
    claims = extract_claims(response)
    
    # Return updates (merged into state)
    return {"claims": claims}
```

### Why Decompose Claims?

LLM responses often contain multiple facts bundled together. To verify accuracy, we need to check each fact individually:

| Without Decomposition | With Decomposition |
|----------------------|-------------------|
| "The Eiffel Tower in Paris is 330m tall and was built in 1920" | Claim 1: "The Eiffel Tower is in Paris" ✓ |
| Hard to give partial score | Claim 2: "The Eiffel Tower is 330m tall" ✓ |
| | Claim 3: "The Eiffel Tower was built in 1920" ✗ (1889) |
| | Score: 66% (2/3 correct) |

---

## 💻 Implementation

### Step 1: Define the State Schema

Create `src/truthtable/graphs/state.py`:

```python
"""
State definitions for the audit workflow.

This defines the data structure that flows through all nodes.
Using TypedDict provides type hints while allowing partial updates.
"""

from typing import TypedDict, Annotated
from operator import add


class ClaimVerification(TypedDict):
    """Result of verifying a single claim."""
    claim: str                    # The claim being verified
    supported: bool               # Whether context supports it
    confidence: float             # Model's confidence (0.0-1.0)
    evidence: list[str]           # Supporting/refuting evidence
    reasoning: str                # Explanation of verdict


class ContextDocument(TypedDict):
    """A document from the RAG context."""
    id: str
    content: str
    source: str
    relevance_score: float


class AuditState(TypedDict, total=False):
    """
    State that flows through the audit workflow.
    
    Using total=False makes all fields optional,
    allowing nodes to only return the fields they update.
    
    The Annotated[list, add] syntax tells LangGraph to
    append new items to lists rather than replacing them.
    """
    # ===== Input Fields (provided at start) =====
    request_id: str
    user_query: str
    llm_response: str
    context_docs: list[ContextDocument]
    
    # ===== Decomposer Output =====
    claims: Annotated[list[str], add]
    
    # ===== Verifier Output =====
    verifications: Annotated[list[ClaimVerification], add]
    
    # ===== Scorer Output =====
    overall_score: float
    faithfulness_score: float
    relevancy_score: float
    hallucination_detected: bool
    
    # ===== Metadata =====
    reasoning_trace: str
    processing_time_ms: int
```

### Step 2: Create the Decomposer Node

Create `src/truthtable/graphs/nodes/decomposer.py`:

```python
"""
Claim Decomposer Node.

This node breaks down an LLM response into atomic factual claims
that can be individually verified against the source context.
"""

import re
from typing import Any

from ...providers import LLMProvider, CompletionRequest, Message


# ===== Prompt Template =====
# This is the instruction we give to the LLM to extract claims

DECOMPOSE_SYSTEM_PROMPT = """You are a fact extraction assistant. Your job is to break down text into individual atomic factual claims.

Rules:
1. Each claim should be a single, verifiable statement
2. Each claim should be self-contained (understandable without context)
3. Remove opinions, hedging language, and subjective statements
4. Keep claims as close to the original wording as possible
5. Do not add information that wasn't in the original text

Example Input:
"The Eiffel Tower, located in Paris, was built in 1889 and stands at 330 meters tall."

Example Output:
1. The Eiffel Tower is located in Paris.
2. The Eiffel Tower was built in 1889.
3. The Eiffel Tower stands at 330 meters tall."""

DECOMPOSE_USER_PROMPT = """Break down the following text into individual atomic factual claims.

Text to analyze:
{text}

Output each claim on a new line, numbered:
1. [first claim]
2. [second claim]
..."""


class DecomposerNode:
    """
    LangGraph node that decomposes text into atomic claims.
    
    Usage in graph:
        decomposer = DecomposerNode(llm_provider)
        workflow.add_node("decompose", decomposer)
    """
    
    def __init__(
        self,
        llm: LLMProvider,
        max_claims: int = 20,
        min_claim_length: int = 10,
    ):
        """
        Initialize the decomposer.
        
        Args:
            llm: The LLM provider to use for extraction
            max_claims: Maximum number of claims to extract
            min_claim_length: Minimum characters for a valid claim
        """
        self.llm = llm
        self.max_claims = max_claims
        self.min_claim_length = min_claim_length
    
    async def __call__(self, state: dict[str, Any]) -> dict[str, Any]:
        """
        Execute the decomposer node.
        
        Args:
            state: Current workflow state containing llm_response
            
        Returns:
            Dict with 'claims' key containing list of extracted claims
        """
        # Get the response to decompose
        llm_response = state.get("llm_response", "")
        
        if not llm_response.strip():
            return {"claims": []}
        
        # Build the prompt
        user_prompt = DECOMPOSE_USER_PROMPT.format(text=llm_response)
        
        # Call the LLM
        request = CompletionRequest(
            messages=[
                Message(role="system", content=DECOMPOSE_SYSTEM_PROMPT),
                Message(role="user", content=user_prompt),
            ],
            temperature=0.0,  # Deterministic for consistency
            max_tokens=2000,
        )
        
        response = await self.llm.complete(request)
        
        # Parse the response into claims
        claims = self._parse_claims(response.content)
        
        return {"claims": claims}
    
    def _parse_claims(self, response: str) -> list[str]:
        """
        Parse the LLM response to extract claims.
        
        Handles various formats:
        - Numbered lists: "1. Claim text"
        - Bullet points: "- Claim text" or "• Claim text"
        - Plain lines
        """
        claims = []
        
        for line in response.strip().split("\n"):
            line = line.strip()
            
            if not line:
                continue
            
            # Remove numbering (1., 2., etc.)
            line = re.sub(r"^\d+[\.\)]\s*", "", line)
            
            # Remove bullet points
            line = re.sub(r"^[-•*]\s*", "", line)
            
            # Remove surrounding quotes
            line = line.strip('"\'')
            
            # Skip if too short or looks like a header
            if len(line) < self.min_claim_length:
                continue
            
            if line.endswith(":"):
                continue
            
            claims.append(line)
            
            # Limit number of claims
            if len(claims) >= self.max_claims:
                break
        
        return claims


# ===== Standalone Function (alternative to class) =====
# Some prefer functional style for simpler nodes

async def decompose_claims(
    state: dict[str, Any],
    llm: LLMProvider,
) -> dict[str, Any]:
    """
    Functional version of the decomposer.
    
    Usage:
        workflow.add_node("decompose", lambda s: decompose_claims(s, llm))
    """
    node = DecomposerNode(llm)
    return await node(state)
```

### Step 3: Create the Graph Builder

Create `src/truthtable/graphs/audit_graph.py`:

```python
"""
Main audit workflow graph.

This defines the LangGraph workflow that processes audit requests:
1. Decompose response into claims
2. Verify each claim against context
3. Calculate overall scores
"""

from langgraph.graph import StateGraph, END
from typing import Callable, Any

from .state import AuditState
from .nodes.decomposer import DecomposerNode
from ..providers import LLMProvider


def build_audit_graph(
    llm: LLMProvider,
    custom_nodes: dict[str, Callable] | None = None,
) -> StateGraph:
    """
    Build the audit workflow graph.
    
    Args:
        llm: LLM provider for all nodes
        custom_nodes: Optional dict of custom node implementations
        
    Returns:
        Compiled LangGraph workflow
    """
    # Create the state graph
    workflow = StateGraph(AuditState)
    
    # ===== Create Nodes =====
    decomposer = DecomposerNode(llm)
    
    # Allow custom node overrides
    nodes = {
        "decompose": decomposer,
        # We'll add verifier and scorer in later steps
    }
    
    if custom_nodes:
        nodes.update(custom_nodes)
    
    # ===== Add Nodes to Graph =====
    for name, node in nodes.items():
        workflow.add_node(name, node)
    
    # ===== Define Edges =====
    # For now, just decompose and end
    # We'll add more edges as we build more nodes
    workflow.set_entry_point("decompose")
    workflow.add_edge("decompose", END)
    
    # ===== Compile and Return =====
    return workflow.compile()


async def run_audit(
    llm: LLMProvider,
    user_query: str,
    llm_response: str,
    context_docs: list[dict] | None = None,
    request_id: str = "",
) -> dict[str, Any]:
    """
    Convenience function to run a full audit.
    
    Args:
        llm: LLM provider
        user_query: The original user question
        llm_response: The LLM's response to verify
        context_docs: RAG context documents
        request_id: Optional request ID for tracking
        
    Returns:
        Final audit state
    """
    graph = build_audit_graph(llm)
    
    initial_state: AuditState = {
        "request_id": request_id,
        "user_query": user_query,
        "llm_response": llm_response,
        "context_docs": context_docs or [],
        "claims": [],
        "verifications": [],
    }
    
    result = await graph.ainvoke(initial_state)
    return result
```

### Step 4: Create Package Init Files

Create `src/truthtable/graphs/__init__.py`:

```python
"""
LangGraph workflow definitions.
"""

from .state import AuditState, ClaimVerification, ContextDocument
from .audit_graph import build_audit_graph, run_audit

__all__ = [
    "AuditState",
    "ClaimVerification",
    "ContextDocument",
    "build_audit_graph",
    "run_audit",
]
```

Create `src/truthtable/graphs/nodes/__init__.py`:

```python
"""
LangGraph node implementations.
"""

from .decomposer import DecomposerNode, decompose_claims

__all__ = [
    "DecomposerNode",
    "decompose_claims",
]
```

---

## ✅ Testing

### Test 1: Quick Smoke Test

```bash
cd backend-python
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider
from truthtable.graphs import run_audit

async def test():
    provider = OllamaProvider()
    
    result = await run_audit(
        llm=provider,
        user_query='Tell me about the Eiffel Tower',
        llm_response='The Eiffel Tower is located in Paris, France. It was built in 1889 and stands at 330 meters tall. It is named after Gustave Eiffel.',
    )
    
    print('Extracted claims:')
    for i, claim in enumerate(result['claims'], 1):
        print(f'  {i}. {claim}')
    
    await provider.close()

asyncio.run(test())
"
```

Expected output:
```
Extracted claims:
  1. The Eiffel Tower is located in Paris, France.
  2. The Eiffel Tower was built in 1889.
  3. The Eiffel Tower stands at 330 meters tall.
  4. The Eiffel Tower is named after Gustave Eiffel.
```

### Test 2: Test Edge Cases

```bash
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider
from truthtable.graphs.nodes.decomposer import DecomposerNode

async def test_edge_cases():
    provider = OllamaProvider()
    decomposer = DecomposerNode(provider)
    
    # Test empty input
    result = await decomposer({'llm_response': ''})
    print(f'Empty input: {result[\"claims\"]}')
    assert result['claims'] == []
    
    # Test single claim
    result = await decomposer({
        'llm_response': 'The sky is blue.'
    })
    print(f'Single claim: {result[\"claims\"]}')
    assert len(result['claims']) >= 1
    
    # Test complex response
    result = await decomposer({
        'llm_response': '''
        Python is a programming language created by Guido van Rossum.
        It was first released in 1991. Python emphasizes code readability
        and supports multiple programming paradigms.
        '''
    })
    print(f'Complex response: {len(result[\"claims\"])} claims')
    for claim in result['claims']:
        print(f'  - {claim}')
    
    await provider.close()
    print('\\n✓ All edge cases passed!')

asyncio.run(test_edge_cases())
"
```

### Test 3: Unit Tests

Create `tests/unit/test_decomposer.py`:

```python
"""Tests for the Claim Decomposer node."""

import pytest
from unittest.mock import AsyncMock, MagicMock

from truthtable.graphs.nodes.decomposer import DecomposerNode
from truthtable.providers import CompletionResponse


@pytest.fixture
def mock_llm():
    """Create a mock LLM provider."""
    llm = MagicMock()
    llm.complete = AsyncMock()
    return llm


@pytest.fixture
def decomposer(mock_llm):
    """Create a decomposer with mock LLM."""
    return DecomposerNode(mock_llm)


class TestDecomposerNode:
    """Tests for DecomposerNode."""
    
    @pytest.mark.asyncio
    async def test_empty_response(self, decomposer):
        result = await decomposer({"llm_response": ""})
        assert result["claims"] == []
    
    @pytest.mark.asyncio
    async def test_whitespace_response(self, decomposer):
        result = await decomposer({"llm_response": "   \n\t  "})
        assert result["claims"] == []
    
    @pytest.mark.asyncio
    async def test_extracts_claims(self, decomposer, mock_llm):
        # Mock LLM response
        mock_llm.complete.return_value = CompletionResponse(
            content="""
            1. Paris is the capital of France.
            2. The Eiffel Tower is 330 meters tall.
            3. France is in Europe.
            """,
            model="test",
        )
        
        result = await decomposer({
            "llm_response": "Paris, the capital of France, has the 330m Eiffel Tower."
        })
        
        assert len(result["claims"]) == 3
        assert "Paris is the capital of France" in result["claims"][0]
    
    @pytest.mark.asyncio
    async def test_respects_max_claims(self, mock_llm):
        decomposer = DecomposerNode(mock_llm, max_claims=2)
        
        mock_llm.complete.return_value = CompletionResponse(
            content="""
            1. Claim one
            2. Claim two
            3. Claim three
            4. Claim four
            """,
            model="test",
        )
        
        result = await decomposer({"llm_response": "test"})
        
        assert len(result["claims"]) <= 2


class TestClaimParsing:
    """Tests for claim parsing logic."""
    
    def test_parse_numbered_list(self, decomposer):
        response = """
        1. First claim here.
        2. Second claim here.
        3. Third claim here.
        """
        claims = decomposer._parse_claims(response)
        
        assert len(claims) == 3
        assert claims[0] == "First claim here."
    
    def test_parse_bullet_list(self, decomposer):
        response = """
        - First claim here.
        • Second claim here.
        * Third claim here.
        """
        claims = decomposer._parse_claims(response)
        
        assert len(claims) == 3
    
    def test_filters_short_claims(self, decomposer):
        response = """
        1. Too short
        2. This claim is long enough to be valid.
        3. Also ok
        """
        # min_claim_length is 10 by default
        claims = decomposer._parse_claims(response)
        
        # "Too short" and "Also ok" are filtered
        assert len(claims) == 1
    
    def test_filters_headers(self, decomposer):
        response = """
        Claims about Paris:
        1. Paris is the capital of France.
        
        Additional info:
        2. The city has many museums.
        """
        claims = decomposer._parse_claims(response)
        
        # Headers ending with : should be filtered
        assert not any(claim.endswith(":") for claim in claims)
    
    def test_handles_quotes(self, decomposer):
        response = """
        1. "Paris is beautiful."
        2. 'London is great.'
        """
        claims = decomposer._parse_claims(response)
        
        assert claims[0] == "Paris is beautiful."
        assert claims[1] == "London is great."
```

Run the tests:

```bash
poetry run pytest tests/unit/test_decomposer.py -v
```

### Test 4: Integration Test

Create `tests/integration/test_decomposer_integration.py`:

```python
"""Integration tests for decomposer with real LLM."""

import pytest
import httpx

from truthtable.providers import OllamaProvider
from truthtable.graphs.nodes.decomposer import DecomposerNode


def is_ollama_running() -> bool:
    try:
        return httpx.get("http://localhost:11434/", timeout=2).status_code == 200
    except Exception:
        return False


pytestmark = pytest.mark.skipif(
    not is_ollama_running(),
    reason="Ollama is not running"
)


@pytest.fixture
async def decomposer():
    provider = OllamaProvider()
    node = DecomposerNode(provider)
    yield node
    await provider.close()


class TestDecomposerIntegration:
    
    @pytest.mark.asyncio
    async def test_simple_decomposition(self, decomposer):
        result = await decomposer({
            "llm_response": "The Earth orbits the Sun. Water boils at 100 degrees Celsius."
        })
        
        assert len(result["claims"]) >= 2
        
        # Check claims are sensible
        claims_text = " ".join(result["claims"]).lower()
        assert "earth" in claims_text or "sun" in claims_text
        assert "water" in claims_text or "boil" in claims_text
    
    @pytest.mark.asyncio
    async def test_complex_response(self, decomposer):
        result = await decomposer({
            "llm_response": """
            Machine learning is a subset of artificial intelligence that enables 
            computers to learn from data without being explicitly programmed. 
            It was coined as a term by Arthur Samuel in 1959. Modern ML systems 
            use neural networks, which are inspired by the human brain.
            """
        })
        
        assert len(result["claims"]) >= 3
        
        # Each claim should be standalone
        for claim in result["claims"]:
            # Should not start with pronouns (proper decomposition)
            assert not claim.lower().startswith("it ")
            assert not claim.lower().startswith("they ")
```

---

## 🐛 Common Issues

### Issue: LangGraph import error

**Solution:** Install LangGraph:
```bash
poetry add langgraph
```

### Issue: Claims not being extracted properly

**Solution:** The decomposition depends on LLM quality. Try:
1. Using a larger model: `llama3.2:8b` instead of `llama3.2:1b`
2. Adjusting the prompt
3. Lowering temperature to 0 for consistency

### Issue: `TypeError: 'coroutine' object is not subscriptable`

**Solution:** You forgot to `await` the async function:
```python
# Wrong
result = decomposer(state)["claims"]

# Right
result = await decomposer(state)
claims = result["claims"]
```

---

## 📖 Further Reading

- [LangGraph Documentation](https://langchain-ai.github.io/langgraph/)
- [TypedDict in Python](https://docs.python.org/3/library/typing.html#typing.TypedDict)
- [Prompt Engineering for Extraction](https://platform.openai.com/docs/guides/text-generation/how-should-i-set-the-temperature-parameter)

---

## ⏭️ Next Step

Continue to [Step 1.4: Fact Verifier Node](step-1.4-fact-verifier.md) to build the verification logic.
