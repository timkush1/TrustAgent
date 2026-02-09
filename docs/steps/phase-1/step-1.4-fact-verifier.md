# Step 1.4: Fact Verifier Node

## 🎯 Goal

Build the **Fact Verifier** node that checks each claim against the provided context. This is the heart of hallucination detection - determining whether each claim is supported by the source material.

**Example:**
- **Claim:** "The Eiffel Tower was built in 1920"
- **Context:** "The Eiffel Tower, constructed in 1889 for the World's Fair..."
- **Result:** ❌ UNSUPPORTED (Context says 1889, not 1920)

---

## 📚 Prerequisites

- Completed Step 1.3 (Claim Decomposer)
- Understanding of Natural Language Inference (NLI)

---

## 🧠 Concepts Explained

### What is Natural Language Inference (NLI)?

NLI is the task of determining whether a "hypothesis" follows from a "premise":

| Premise (Context) | Hypothesis (Claim) | Relation |
|-------------------|-------------------|----------|
| "Paris is in France" | "Paris is a French city" | **ENTAILMENT** ✓ |
| "Paris is in France" | "Paris is in Germany" | **CONTRADICTION** ✗ |
| "Paris is in France" | "Paris has good food" | **NEUTRAL** ? |

For TruthTable:
- **ENTAILMENT** = Claim is SUPPORTED
- **CONTRADICTION** = Claim is UNSUPPORTED (hallucination!)
- **NEUTRAL** = Cannot determine (not enough info)

### Verification Approaches

We have two options:

1. **LLM-based verification** (what we'll implement)
   - Use the same LLM to reason about support
   - More flexible, works with any model
   - Can provide detailed reasoning

2. **Specialized NLI model** (alternative)
   - Use a model trained specifically for NLI (like BART-MNLI)
   - Faster, more consistent
   - Less flexible with reasoning

We'll use the LLM approach since we already have Ollama set up.

### The Verification Prompt

We ask the LLM to be a judge:
```
Given this context: [context documents]

Determine if this claim is supported: [claim]

Respond with:
- SUPPORTED: If the context directly supports this claim
- UNSUPPORTED: If the context contradicts this claim
- UNKNOWN: If the context doesn't address this claim
```

---

## 💻 Implementation

### Step 1: Create the Verifier Node

Create `src/truthtable/graphs/nodes/verifier.py`:

```python
"""
Fact Verifier Node.

This node checks each claim against the provided context to determine
if it is supported, unsupported (hallucination), or unknown.
"""

import re
from typing import Any
from enum import Enum

from ...providers import LLMProvider, CompletionRequest, Message
from ..state import ClaimVerification


class VerificationStatus(str, Enum):
    """Possible verification outcomes."""
    SUPPORTED = "supported"
    UNSUPPORTED = "unsupported"
    PARTIALLY_SUPPORTED = "partially_supported"
    UNKNOWN = "unknown"


# ===== Prompt Templates =====

VERIFICATION_SYSTEM_PROMPT = """You are a precise fact-checking assistant. Your job is to determine whether claims are supported by the provided context.

You must be:
- STRICT: Only mark as SUPPORTED if the context clearly confirms the claim
- PRECISE: Pay attention to numbers, dates, names, and specific details
- HONEST: Use UNKNOWN if the context simply doesn't address the claim

Verification Labels:
- SUPPORTED: The context directly and clearly supports this claim
- UNSUPPORTED: The context contradicts this claim or provides different information
- PARTIALLY_SUPPORTED: Some aspects are supported, but key details differ
- UNKNOWN: The context doesn't provide enough information to verify this claim"""

VERIFICATION_USER_PROMPT = """Context:
{context}

---

Claim to verify:
"{claim}"

---

Analyze whether the context supports this claim. Respond in this exact format:

VERDICT: [SUPPORTED/UNSUPPORTED/PARTIALLY_SUPPORTED/UNKNOWN]
CONFIDENCE: [0.0-1.0]
EVIDENCE: [Quote the specific part of the context that supports or refutes this claim, or "No relevant evidence found"]
REASONING: [Brief explanation of your verdict]"""


class VerifierNode:
    """
    LangGraph node that verifies claims against context.
    
    For each claim in the state, checks if it's supported by the context
    and adds a ClaimVerification result.
    """
    
    def __init__(
        self,
        llm: LLMProvider,
        confidence_threshold: float = 0.7,
    ):
        """
        Initialize the verifier.
        
        Args:
            llm: LLM provider for verification
            confidence_threshold: Minimum confidence to trust verdict
        """
        self.llm = llm
        self.confidence_threshold = confidence_threshold
    
    async def __call__(self, state: dict[str, Any]) -> dict[str, Any]:
        """
        Execute the verifier node.
        
        Args:
            state: Current workflow state with 'claims' and 'context_docs'
            
        Returns:
            Dict with 'verifications' containing verification results
        """
        claims = state.get("claims", [])
        context_docs = state.get("context_docs", [])
        
        if not claims:
            return {"verifications": []}
        
        # Combine context documents into one text
        context_text = self._build_context_text(context_docs)
        
        # Verify each claim
        verifications = []
        for claim in claims:
            verification = await self._verify_claim(claim, context_text)
            verifications.append(verification)
        
        return {"verifications": verifications}
    
    async def _verify_claim(
        self,
        claim: str,
        context: str,
    ) -> ClaimVerification:
        """
        Verify a single claim against the context.
        
        Args:
            claim: The claim to verify
            context: The combined context text
            
        Returns:
            ClaimVerification with the result
        """
        # Handle empty context
        if not context.strip():
            return ClaimVerification(
                claim=claim,
                supported=False,
                confidence=0.0,
                evidence=[],
                reasoning="No context provided to verify against",
            )
        
        # Build the prompt
        user_prompt = VERIFICATION_USER_PROMPT.format(
            context=context,
            claim=claim,
        )
        
        # Call the LLM
        request = CompletionRequest(
            messages=[
                Message(role="system", content=VERIFICATION_SYSTEM_PROMPT),
                Message(role="user", content=user_prompt),
            ],
            temperature=0.0,  # Deterministic for consistency
            max_tokens=500,
        )
        
        try:
            response = await self.llm.complete(request)
            return self._parse_verification(claim, response.content)
        except Exception as e:
            # Return unknown on error
            return ClaimVerification(
                claim=claim,
                supported=False,
                confidence=0.0,
                evidence=[],
                reasoning=f"Verification failed: {str(e)}",
            )
    
    def _parse_verification(
        self,
        claim: str,
        response: str,
    ) -> ClaimVerification:
        """
        Parse the LLM's verification response.
        
        Extracts verdict, confidence, evidence, and reasoning from
        the structured response format.
        """
        # Default values
        verdict = VerificationStatus.UNKNOWN
        confidence = 0.5
        evidence = []
        reasoning = ""
        
        lines = response.strip().split("\n")
        
        for line in lines:
            line = line.strip()
            
            # Parse VERDICT
            if line.upper().startswith("VERDICT:"):
                verdict_text = line.split(":", 1)[1].strip().upper()
                verdict = self._parse_verdict(verdict_text)
            
            # Parse CONFIDENCE
            elif line.upper().startswith("CONFIDENCE:"):
                try:
                    conf_text = line.split(":", 1)[1].strip()
                    # Handle formats like "0.8" or "80%" or "0.8/1.0"
                    conf_text = re.sub(r"[%/].*", "", conf_text)
                    confidence = float(conf_text)
                    confidence = max(0.0, min(1.0, confidence))  # Clamp
                except ValueError:
                    confidence = 0.5
            
            # Parse EVIDENCE
            elif line.upper().startswith("EVIDENCE:"):
                evidence_text = line.split(":", 1)[1].strip()
                if evidence_text and evidence_text.lower() != "no relevant evidence found":
                    # Remove surrounding quotes
                    evidence_text = evidence_text.strip('"\'')
                    evidence = [evidence_text]
            
            # Parse REASONING
            elif line.upper().startswith("REASONING:"):
                reasoning = line.split(":", 1)[1].strip()
        
        # Determine if supported
        supported = verdict == VerificationStatus.SUPPORTED
        
        return ClaimVerification(
            claim=claim,
            supported=supported,
            confidence=confidence,
            evidence=evidence,
            reasoning=reasoning,
        )
    
    def _parse_verdict(self, verdict_text: str) -> VerificationStatus:
        """Parse verdict text to enum."""
        verdict_text = verdict_text.strip().upper()
        
        # Handle variations
        if "SUPPORT" in verdict_text and "UNSUPPORT" not in verdict_text:
            if "PARTIAL" in verdict_text:
                return VerificationStatus.PARTIALLY_SUPPORTED
            return VerificationStatus.SUPPORTED
        elif "UNSUPPORT" in verdict_text or "CONTRADICT" in verdict_text:
            return VerificationStatus.UNSUPPORTED
        else:
            return VerificationStatus.UNKNOWN
    
    def _build_context_text(self, context_docs: list[dict]) -> str:
        """
        Combine context documents into a single text.
        
        Formats each document with source information.
        """
        if not context_docs:
            return ""
        
        parts = []
        for i, doc in enumerate(context_docs, 1):
            content = doc.get("content", "")
            source = doc.get("source", f"Document {i}")
            
            if content:
                parts.append(f"[Source: {source}]\n{content}")
        
        return "\n\n---\n\n".join(parts)


# ===== Batch Verification (Optimization) =====

class BatchVerifierNode(VerifierNode):
    """
    Optimized verifier that verifies multiple claims in one LLM call.
    
    More efficient for many claims, but may be less accurate.
    """
    
    async def __call__(self, state: dict[str, Any]) -> dict[str, Any]:
        claims = state.get("claims", [])
        context_docs = state.get("context_docs", [])
        
        if not claims:
            return {"verifications": []}
        
        context_text = self._build_context_text(context_docs)
        
        # For small number of claims, verify individually
        if len(claims) <= 3:
            return await super().__call__(state)
        
        # For many claims, batch verify
        verifications = await self._batch_verify(claims, context_text)
        return {"verifications": verifications}
    
    async def _batch_verify(
        self,
        claims: list[str],
        context: str,
    ) -> list[ClaimVerification]:
        """Verify all claims in a single LLM call."""
        
        # Build numbered claims list
        claims_text = "\n".join(
            f"{i}. \"{claim}\"" 
            for i, claim in enumerate(claims, 1)
        )
        
        prompt = f"""Context:
{context}

---

Claims to verify:
{claims_text}

---

For each claim, provide verification in this format:

CLAIM 1:
VERDICT: [SUPPORTED/UNSUPPORTED/UNKNOWN]
CONFIDENCE: [0.0-1.0]
REASONING: [Brief explanation]

CLAIM 2:
...and so on for all claims."""

        request = CompletionRequest(
            messages=[
                Message(role="system", content=VERIFICATION_SYSTEM_PROMPT),
                Message(role="user", content=prompt),
            ],
            temperature=0.0,
            max_tokens=2000,
        )
        
        response = await self.llm.complete(request)
        return self._parse_batch_response(claims, response.content)
    
    def _parse_batch_response(
        self,
        claims: list[str],
        response: str,
    ) -> list[ClaimVerification]:
        """Parse batch verification response."""
        verifications = []
        
        # Split by claim markers
        claim_sections = re.split(r"CLAIM\s*\d+\s*:", response, flags=re.IGNORECASE)
        
        for i, claim in enumerate(claims):
            if i + 1 < len(claim_sections):
                section = claim_sections[i + 1]
                verification = self._parse_verification(claim, section)
            else:
                # Fallback for missing sections
                verification = ClaimVerification(
                    claim=claim,
                    supported=False,
                    confidence=0.0,
                    evidence=[],
                    reasoning="Failed to parse batch response",
                )
            verifications.append(verification)
        
        return verifications
```

### Step 2: Update the Graph

Update `src/truthtable/graphs/audit_graph.py`:

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
from .nodes.verifier import VerifierNode
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
    verifier = VerifierNode(llm)
    
    # Allow custom node overrides
    nodes = {
        "decompose": decomposer,
        "verify": verifier,
    }
    
    if custom_nodes:
        nodes.update(custom_nodes)
    
    # ===== Add Nodes to Graph =====
    for name, node in nodes.items():
        workflow.add_node(name, node)
    
    # ===== Define Edges =====
    workflow.set_entry_point("decompose")
    workflow.add_edge("decompose", "verify")
    workflow.add_edge("verify", END)
    
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
        Final audit state with claims and verifications
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

### Step 3: Update Exports

Update `src/truthtable/graphs/nodes/__init__.py`:

```python
"""
LangGraph node implementations.
"""

from .decomposer import DecomposerNode, decompose_claims
from .verifier import VerifierNode, BatchVerifierNode, VerificationStatus

__all__ = [
    "DecomposerNode",
    "decompose_claims",
    "VerifierNode",
    "BatchVerifierNode",
    "VerificationStatus",
]
```

---

## ✅ Testing

### Test 1: Smoke Test with Real Data

```bash
cd backend-python
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider
from truthtable.graphs import run_audit

async def test():
    provider = OllamaProvider()
    
    # Context that we'll verify against
    context = [
        {
            'id': '1',
            'content': 'The Eiffel Tower is a wrought-iron lattice tower in Paris, France. It was constructed from 1887 to 1889 as the entrance arch for the 1889 World Fair. The tower is 330 metres (1,083 ft) tall.',
            'source': 'Wikipedia',
        }
    ]
    
    # Response with one correct fact and one hallucination
    response = 'The Eiffel Tower was built in 1889 and is located in Paris. It stands at 400 meters tall.'
    
    result = await run_audit(
        llm=provider,
        user_query='Tell me about the Eiffel Tower',
        llm_response=response,
        context_docs=context,
    )
    
    print('Claims:')
    for claim in result['claims']:
        print(f'  • {claim}')
    
    print('\\nVerifications:')
    for v in result['verifications']:
        status = '✓' if v['supported'] else '✗'
        print(f'  {status} {v[\"claim\"]}')
        print(f'    Confidence: {v[\"confidence\"]:.0%}')
        print(f'    Reasoning: {v[\"reasoning\"]}')
    
    await provider.close()

asyncio.run(test())
"
```

Expected output (may vary based on LLM):
```
Claims:
  • The Eiffel Tower was built in 1889.
  • The Eiffel Tower is located in Paris.
  • The Eiffel Tower stands at 400 meters tall.

Verifications:
  ✓ The Eiffel Tower was built in 1889.
    Confidence: 95%
    Reasoning: Context confirms construction from 1887 to 1889.
  ✓ The Eiffel Tower is located in Paris.
    Confidence: 95%
    Reasoning: Context states it is in Paris, France.
  ✗ The Eiffel Tower stands at 400 meters tall.
    Confidence: 90%
    Reasoning: Context says 330 meters, not 400 meters.
```

### Test 2: Edge Cases

```bash
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider
from truthtable.graphs.nodes.verifier import VerifierNode

async def test_edge_cases():
    provider = OllamaProvider()
    verifier = VerifierNode(provider)
    
    # Test with no context
    result = await verifier({
        'claims': ['The sky is blue.'],
        'context_docs': [],
    })
    print('No context:')
    print(f'  Supported: {result[\"verifications\"][0][\"supported\"]}')
    print(f'  Reasoning: {result[\"verifications\"][0][\"reasoning\"]}')
    
    # Test with no claims
    result = await verifier({
        'claims': [],
        'context_docs': [{'content': 'Some context', 'source': 'test'}],
    })
    print(f'\\nNo claims: {result[\"verifications\"]}')
    
    await provider.close()
    print('\\n✓ All edge cases handled!')

asyncio.run(test_edge_cases())
"
```

### Test 3: Unit Tests

Create `tests/unit/test_verifier.py`:

```python
"""Tests for the Fact Verifier node."""

import pytest
from unittest.mock import AsyncMock, MagicMock

from truthtable.graphs.nodes.verifier import (
    VerifierNode,
    VerificationStatus,
)
from truthtable.providers import CompletionResponse


@pytest.fixture
def mock_llm():
    llm = MagicMock()
    llm.complete = AsyncMock()
    return llm


@pytest.fixture
def verifier(mock_llm):
    return VerifierNode(mock_llm)


class TestVerifierNode:
    
    @pytest.mark.asyncio
    async def test_empty_claims(self, verifier):
        result = await verifier({
            "claims": [],
            "context_docs": [{"content": "test", "source": "test"}],
        })
        assert result["verifications"] == []
    
    @pytest.mark.asyncio
    async def test_empty_context(self, verifier):
        result = await verifier({
            "claims": ["The sky is blue."],
            "context_docs": [],
        })
        
        assert len(result["verifications"]) == 1
        assert result["verifications"][0]["supported"] is False
        assert "No context" in result["verifications"][0]["reasoning"]
    
    @pytest.mark.asyncio
    async def test_supported_claim(self, verifier, mock_llm):
        mock_llm.complete.return_value = CompletionResponse(
            content="""
            VERDICT: SUPPORTED
            CONFIDENCE: 0.95
            EVIDENCE: "Paris is the capital of France"
            REASONING: Context directly confirms this claim.
            """,
            model="test",
        )
        
        result = await verifier({
            "claims": ["Paris is the capital of France."],
            "context_docs": [{"content": "Paris is the capital of France.", "source": "wiki"}],
        })
        
        v = result["verifications"][0]
        assert v["supported"] is True
        assert v["confidence"] == 0.95
    
    @pytest.mark.asyncio
    async def test_unsupported_claim(self, verifier, mock_llm):
        mock_llm.complete.return_value = CompletionResponse(
            content="""
            VERDICT: UNSUPPORTED
            CONFIDENCE: 0.9
            EVIDENCE: "The tower is 330 metres tall"
            REASONING: Context says 330m, not 400m.
            """,
            model="test",
        )
        
        result = await verifier({
            "claims": ["The Eiffel Tower is 400 meters tall."],
            "context_docs": [{"content": "The tower is 330 metres tall.", "source": "wiki"}],
        })
        
        v = result["verifications"][0]
        assert v["supported"] is False


class TestVerdictParsing:
    
    def test_parse_supported(self, verifier):
        status = verifier._parse_verdict("SUPPORTED")
        assert status == VerificationStatus.SUPPORTED
    
    def test_parse_unsupported(self, verifier):
        status = verifier._parse_verdict("UNSUPPORTED")
        assert status == VerificationStatus.UNSUPPORTED
    
    def test_parse_partial(self, verifier):
        status = verifier._parse_verdict("PARTIALLY_SUPPORTED")
        assert status == VerificationStatus.PARTIALLY_SUPPORTED
    
    def test_parse_unknown(self, verifier):
        status = verifier._parse_verdict("UNKNOWN")
        assert status == VerificationStatus.UNKNOWN
    
    def test_parse_variations(self, verifier):
        # Handle slight variations
        assert verifier._parse_verdict("Supported") == VerificationStatus.SUPPORTED
        assert verifier._parse_verdict("CONTRADICTION") == VerificationStatus.UNSUPPORTED


class TestResponseParsing:
    
    def test_parse_full_response(self, verifier):
        response = """
        VERDICT: SUPPORTED
        CONFIDENCE: 0.85
        EVIDENCE: "The relevant text here"
        REASONING: This matches the claim.
        """
        
        v = verifier._parse_verification("test claim", response)
        
        assert v["supported"] is True
        assert v["confidence"] == 0.85
        assert "relevant text" in v["evidence"][0]
        assert v["reasoning"] == "This matches the claim."
    
    def test_parse_missing_fields(self, verifier):
        response = "VERDICT: UNKNOWN"
        
        v = verifier._parse_verification("test claim", response)
        
        assert v["supported"] is False
        assert v["confidence"] == 0.5  # Default
        assert v["evidence"] == []


class TestContextBuilding:
    
    def test_build_single_doc(self, verifier):
        docs = [{"content": "Test content", "source": "doc1"}]
        text = verifier._build_context_text(docs)
        
        assert "Test content" in text
        assert "doc1" in text
    
    def test_build_multiple_docs(self, verifier):
        docs = [
            {"content": "First doc", "source": "source1"},
            {"content": "Second doc", "source": "source2"},
        ]
        text = verifier._build_context_text(docs)
        
        assert "First doc" in text
        assert "Second doc" in text
        assert "---" in text  # Separator
    
    def test_empty_docs(self, verifier):
        text = verifier._build_context_text([])
        assert text == ""
```

Run tests:

```bash
poetry run pytest tests/unit/test_verifier.py -v
```

---

## 🐛 Common Issues

### Issue: All claims marked as UNKNOWN

**Solution:** The LLM might not be following the format. Try:
1. Using a larger model
2. Simplifying the context (less text)
3. Testing with more explicit context

### Issue: Inconsistent results

**Solution:** Set `temperature=0.0` for deterministic outputs (already done in our implementation).

### Issue: Slow verification

**Solution:** Use `BatchVerifierNode` for many claims, or parallelize:
```python
import asyncio

verifications = await asyncio.gather(*[
    self._verify_claim(claim, context)
    for claim in claims
])
```

---

## 📖 Further Reading

- [Natural Language Inference Explained](https://nlp.stanford.edu/projects/snli/)
- [Prompt Engineering for Fact-Checking](https://arxiv.org/abs/2307.11558)
- [RAGAS: Evaluation Framework](https://docs.ragas.io/)

---

## ⏭️ Next Step

Continue to [Step 1.5: Score Calculator Node](step-1.5-score-calculator.md) to aggregate verification results into scores.
