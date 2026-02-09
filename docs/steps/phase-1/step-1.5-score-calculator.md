# Step 1.5: Score Calculator Node

## 🎯 Goal

Build the **Score Calculator** node that aggregates verification results into meaningful scores. This node takes all claim verifications and produces:

- **Trust Score**: Overall reliability of the response (0-100)
- **Hallucination Rate**: Percentage of unsupported claims
- **Detailed Breakdown**: Per-claim scoring for transparency

---

## 📚 Prerequisites

- Completed Step 1.4 (Fact Verifier)
- Understanding of weighted averages

---

## 🧠 Concepts Explained

### Scoring Methodology

We'll implement a weighted scoring system:

```
┌─────────────────────────────────────────────────────────┐
│                  Trust Score Formula                     │
├─────────────────────────────────────────────────────────┤
│                                                          │
│   For each claim:                                        │
│     SUPPORTED       → +1.0 × confidence                  │
│     PARTIALLY       → +0.5 × confidence                  │
│     UNKNOWN         → +0.0 (neutral)                     │
│     UNSUPPORTED     → -1.0 × confidence                  │
│                                                          │
│   Trust Score = (sum of weighted scores + n) / (2n) × 100│
│                                                          │
│   Where n = number of claims                             │
│                                                          │
└─────────────────────────────────────────────────────────┘
```

This formula:
- Ranges from 0 (all hallucinations) to 100 (all supported)
- Weights by confidence (low confidence = less impact)
- Treats UNKNOWN as neutral (doesn't penalize)

### Score Interpretation

| Score Range | Interpretation | Visual |
|------------|----------------|--------|
| 90-100 | Highly Trustworthy | 🟢 |
| 70-89 | Generally Reliable | 🟡 |
| 50-69 | Needs Verification | 🟠 |
| 0-49 | Significant Issues | 🔴 |

### Why Not Simple Percentages?

Consider these scenarios:
- 5 claims, 4 supported, 1 unknown → Should be ~80%, not 80% × (5/5) = 80%
- 5 claims, 4 supported, 1 unsupported (high confidence) → Should penalize more

Our weighted approach handles these nuances.

---

## 💻 Implementation

### Step 1: Define Score Types

First, add score types to the state. Update `src/truthtable/graphs/state.py`:

```python
"""
State definitions for the audit workflow.

This module defines the TypedDict classes that represent the state
passed between LangGraph nodes during audit execution.
"""

from typing import TypedDict, Annotated
from operator import add


class ClaimVerification(TypedDict):
    """Result of verifying a single claim against context."""
    claim: str
    supported: bool
    confidence: float
    evidence: list[str]
    reasoning: str


class AuditScores(TypedDict):
    """Aggregated scores from the audit."""
    trust_score: float          # 0-100 overall score
    hallucination_rate: float   # 0-100 percentage
    supported_count: int        # Number of supported claims
    unsupported_count: int      # Number of unsupported claims
    unknown_count: int          # Number of unknown claims
    total_claims: int           # Total claims analyzed
    grade: str                  # Letter grade (A, B, C, D, F)
    verdict: str                # Human-readable verdict


class AuditState(TypedDict):
    """
    Full state for the audit workflow.
    
    This is passed through all nodes and accumulates results.
    Using Annotated with add allows list fields to be appended to
    rather than replaced, which is useful for multi-step processing.
    """
    # Input fields
    request_id: str
    user_query: str
    llm_response: str
    context_docs: list[dict]
    
    # Processing fields (accumulated by nodes)
    claims: Annotated[list[str], add]
    verifications: Annotated[list[ClaimVerification], add]
    
    # Output fields
    scores: AuditScores | None
    processing_time_ms: int
```

### Step 2: Create the Calculator Node

Create `src/truthtable/graphs/nodes/calculator.py`:

```python
"""
Score Calculator Node.

This node aggregates verification results into meaningful scores
that represent the overall trustworthiness of the LLM response.
"""

from typing import Any
from dataclasses import dataclass
from enum import Enum

from ..state import ClaimVerification, AuditScores


class TrustGrade(str, Enum):
    """Letter grades for trust scores."""
    A_PLUS = "A+"
    A = "A"
    B = "B"
    C = "C"
    D = "D"
    F = "F"


@dataclass
class ScoreWeights:
    """
    Configurable weights for score calculation.
    
    Adjust these to change how the scoring behaves.
    """
    supported: float = 1.0
    partially_supported: float = 0.5
    unknown: float = 0.0
    unsupported: float = -1.0
    
    # Minimum confidence to count a claim
    min_confidence_threshold: float = 0.3


class CalculatorNode:
    """
    LangGraph node that calculates trust scores from verifications.
    
    Takes verification results and produces aggregate scores.
    """
    
    def __init__(
        self,
        weights: ScoreWeights | None = None,
    ):
        """
        Initialize the calculator.
        
        Args:
            weights: Optional custom scoring weights
        """
        self.weights = weights or ScoreWeights()
    
    async def __call__(self, state: dict[str, Any]) -> dict[str, Any]:
        """
        Execute the calculator node.
        
        Args:
            state: Current workflow state with 'verifications'
            
        Returns:
            Dict with 'scores' containing aggregate results
        """
        verifications = state.get("verifications", [])
        
        if not verifications:
            return {"scores": self._empty_scores()}
        
        scores = self._calculate_scores(verifications)
        return {"scores": scores}
    
    def _calculate_scores(
        self,
        verifications: list[ClaimVerification],
    ) -> AuditScores:
        """
        Calculate aggregate scores from verifications.
        
        Uses weighted scoring that accounts for:
        - Support status of each claim
        - Confidence level of each verification
        """
        # Count by category
        supported_count = 0
        unsupported_count = 0
        unknown_count = 0
        
        # Track weighted scores
        weighted_scores = []
        
        for v in verifications:
            confidence = v.get("confidence", 0.5)
            
            # Skip low-confidence verifications
            if confidence < self.weights.min_confidence_threshold:
                unknown_count += 1
                weighted_scores.append(0.0)
                continue
            
            if v.get("supported", False):
                supported_count += 1
                weighted_scores.append(self.weights.supported * confidence)
            else:
                # Check reasoning for UNKNOWN vs UNSUPPORTED
                reasoning = v.get("reasoning", "").lower()
                if self._is_unknown(reasoning):
                    unknown_count += 1
                    weighted_scores.append(self.weights.unknown)
                else:
                    unsupported_count += 1
                    weighted_scores.append(self.weights.unsupported * confidence)
        
        # Calculate trust score
        total_claims = len(verifications)
        trust_score = self._compute_trust_score(weighted_scores, total_claims)
        
        # Calculate hallucination rate
        if total_claims > 0:
            hallucination_rate = (unsupported_count / total_claims) * 100
        else:
            hallucination_rate = 0.0
        
        # Determine grade and verdict
        grade = self._get_grade(trust_score)
        verdict = self._get_verdict(trust_score, hallucination_rate)
        
        return AuditScores(
            trust_score=round(trust_score, 1),
            hallucination_rate=round(hallucination_rate, 1),
            supported_count=supported_count,
            unsupported_count=unsupported_count,
            unknown_count=unknown_count,
            total_claims=total_claims,
            grade=grade.value,
            verdict=verdict,
        )
    
    def _compute_trust_score(
        self,
        weighted_scores: list[float],
        total_claims: int,
    ) -> float:
        """
        Compute the trust score from weighted claim scores.
        
        Formula: (sum + n) / (2n) * 100
        
        This maps [-n, +n] range to [0, 100]:
        - All -1.0 scores → 0%
        - All +1.0 scores → 100%
        - All 0.0 scores → 50%
        """
        if total_claims == 0:
            return 50.0  # Neutral
        
        score_sum = sum(weighted_scores)
        
        # Normalize to 0-100 range
        trust_score = ((score_sum + total_claims) / (2 * total_claims)) * 100
        
        # Clamp to valid range
        return max(0.0, min(100.0, trust_score))
    
    def _is_unknown(self, reasoning: str) -> bool:
        """Check if reasoning indicates UNKNOWN status."""
        unknown_indicators = [
            "unknown",
            "cannot determine",
            "no information",
            "not mentioned",
            "insufficient",
            "unclear",
            "no relevant",
        ]
        return any(indicator in reasoning for indicator in unknown_indicators)
    
    def _get_grade(self, trust_score: float) -> TrustGrade:
        """Convert trust score to letter grade."""
        if trust_score >= 95:
            return TrustGrade.A_PLUS
        elif trust_score >= 85:
            return TrustGrade.A
        elif trust_score >= 70:
            return TrustGrade.B
        elif trust_score >= 55:
            return TrustGrade.C
        elif trust_score >= 40:
            return TrustGrade.D
        else:
            return TrustGrade.F
    
    def _get_verdict(
        self,
        trust_score: float,
        hallucination_rate: float,
    ) -> str:
        """Generate human-readable verdict."""
        if trust_score >= 90:
            return "Highly trustworthy response with strong factual support."
        elif trust_score >= 70:
            return "Generally reliable response with minor concerns."
        elif trust_score >= 50:
            return "Mixed reliability - some claims need independent verification."
        elif trust_score >= 30:
            return "Low reliability - significant unsupported claims detected."
        else:
            return "Unreliable response - high hallucination rate detected."
    
    def _empty_scores(self) -> AuditScores:
        """Return empty scores when no verifications exist."""
        return AuditScores(
            trust_score=0.0,
            hallucination_rate=0.0,
            supported_count=0,
            unsupported_count=0,
            unknown_count=0,
            total_claims=0,
            grade=TrustGrade.F.value,
            verdict="No claims to verify.",
        )


# ===== Utility Functions =====

def calculate_quick_score(
    supported: int,
    unsupported: int,
    unknown: int = 0,
) -> float:
    """
    Quick utility to calculate trust score from counts.
    
    Useful for testing and simple cases.
    
    Args:
        supported: Number of supported claims
        unsupported: Number of unsupported claims
        unknown: Number of unknown claims
        
    Returns:
        Trust score (0-100)
    """
    total = supported + unsupported + unknown
    if total == 0:
        return 50.0
    
    # Simple weighted calculation
    score_sum = (supported * 1.0) + (unknown * 0.0) + (unsupported * -1.0)
    return ((score_sum + total) / (2 * total)) * 100


def format_score_summary(scores: AuditScores) -> str:
    """
    Format scores as a human-readable summary.
    
    Useful for logging and debugging.
    """
    return f"""
╔══════════════════════════════════════╗
║          AUDIT SUMMARY               ║
╠══════════════════════════════════════╣
║  Trust Score: {scores['trust_score']:>6.1f}%  Grade: {scores['grade']:<4}  ║
║  Hallucination Rate: {scores['hallucination_rate']:>6.1f}%         ║
╠══════════════════════════════════════╣
║  Claims Analyzed: {scores['total_claims']:>3}                ║
║    ✓ Supported:   {scores['supported_count']:>3}                ║
║    ✗ Unsupported: {scores['unsupported_count']:>3}                ║
║    ? Unknown:     {scores['unknown_count']:>3}                ║
╠══════════════════════════════════════╣
║  {scores['verdict'][:38]:<38} ║
╚══════════════════════════════════════╝
"""
```

### Step 3: Update the Audit Graph

Update `src/truthtable/graphs/audit_graph.py`:

```python
"""
Main audit workflow graph.

This defines the LangGraph workflow that processes audit requests:
1. Decompose response into claims
2. Verify each claim against context
3. Calculate overall scores
"""

import time
from langgraph.graph import StateGraph, END
from typing import Callable, Any

from .state import AuditState
from .nodes.decomposer import DecomposerNode
from .nodes.verifier import VerifierNode
from .nodes.calculator import CalculatorNode
from ..providers import LLMProvider


def build_audit_graph(
    llm: LLMProvider,
    custom_nodes: dict[str, Callable] | None = None,
) -> StateGraph:
    """
    Build the audit workflow graph.
    
    Graph Structure:
    ┌──────────────┐    ┌────────────┐    ┌──────────────┐
    │  Decompose   │ → │   Verify   │ → │  Calculate   │ → END
    │  (claims)    │    │  (facts)   │    │  (scores)    │
    └──────────────┘    └────────────┘    └──────────────┘
    
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
    calculator = CalculatorNode()
    
    # Allow custom node overrides
    nodes = {
        "decompose": decomposer,
        "verify": verifier,
        "calculate": calculator,
    }
    
    if custom_nodes:
        nodes.update(custom_nodes)
    
    # ===== Add Nodes to Graph =====
    for name, node in nodes.items():
        workflow.add_node(name, node)
    
    # ===== Define Edges =====
    workflow.set_entry_point("decompose")
    workflow.add_edge("decompose", "verify")
    workflow.add_edge("verify", "calculate")
    workflow.add_edge("calculate", END)
    
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
        Final audit state with claims, verifications, and scores
    """
    start_time = time.time()
    
    graph = build_audit_graph(llm)
    
    initial_state: AuditState = {
        "request_id": request_id,
        "user_query": user_query,
        "llm_response": llm_response,
        "context_docs": context_docs or [],
        "claims": [],
        "verifications": [],
        "scores": None,
        "processing_time_ms": 0,
    }
    
    result = await graph.ainvoke(initial_state)
    
    # Add processing time
    elapsed_ms = int((time.time() - start_time) * 1000)
    result["processing_time_ms"] = elapsed_ms
    
    return result
```

### Step 4: Update Exports

Update `src/truthtable/graphs/nodes/__init__.py`:

```python
"""
LangGraph node implementations.
"""

from .decomposer import DecomposerNode, decompose_claims
from .verifier import VerifierNode, BatchVerifierNode, VerificationStatus
from .calculator import (
    CalculatorNode,
    ScoreWeights,
    TrustGrade,
    calculate_quick_score,
    format_score_summary,
)

__all__ = [
    "DecomposerNode",
    "decompose_claims",
    "VerifierNode",
    "BatchVerifierNode",
    "VerificationStatus",
    "CalculatorNode",
    "ScoreWeights",
    "TrustGrade",
    "calculate_quick_score",
    "format_score_summary",
]
```

---

## ✅ Testing

### Test 1: Full Workflow Test

```bash
cd backend-python
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider
from truthtable.graphs import run_audit
from truthtable.graphs.nodes.calculator import format_score_summary

async def test():
    provider = OllamaProvider()
    
    context = [
        {
            'id': '1',
            'content': '''The Great Wall of China is a series of fortifications 
            built across the historical northern borders of ancient Chinese states.
            The wall spans approximately 21,196 kilometers (13,171 miles).
            Construction began in the 7th century BC and continued until the 
            17th century AD. The most well-known sections were built during
            the Ming Dynasty (1368-1644).''',
            'source': 'Encyclopedia',
        }
    ]
    
    # Response with mixed accuracy
    response = '''The Great Wall of China is approximately 21,196 kilometers long.
    It was primarily built during the Ming Dynasty from 1368 to 1644.
    The wall was completed in a single decade.
    It is visible from space with the naked eye.'''
    
    result = await run_audit(
        llm=provider,
        user_query='Tell me about the Great Wall of China',
        llm_response=response,
        context_docs=context,
    )
    
    # Print score summary
    print(format_score_summary(result['scores']))
    
    print('Claims and Verifications:')
    for v in result['verifications']:
        status = '✓' if v['supported'] else '✗'
        print(f'  {status} {v[\"claim\"][:60]}...')
    
    print(f'\\nProcessing time: {result[\"processing_time_ms\"]}ms')
    
    await provider.close()

asyncio.run(test())
"
```

### Test 2: Unit Tests

Create `tests/unit/test_calculator.py`:

```python
"""Tests for the Score Calculator node."""

import pytest
from truthtable.graphs.nodes.calculator import (
    CalculatorNode,
    ScoreWeights,
    TrustGrade,
    calculate_quick_score,
    format_score_summary,
)
from truthtable.graphs.state import ClaimVerification, AuditScores


@pytest.fixture
def calculator():
    return CalculatorNode()


class TestCalculatorNode:
    
    @pytest.mark.asyncio
    async def test_empty_verifications(self, calculator):
        result = await calculator({"verifications": []})
        
        assert result["scores"]["total_claims"] == 0
        assert result["scores"]["grade"] == "F"
    
    @pytest.mark.asyncio
    async def test_all_supported(self, calculator):
        verifications = [
            ClaimVerification(
                claim="Claim 1",
                supported=True,
                confidence=0.9,
                evidence=["evidence"],
                reasoning="Supported",
            ),
            ClaimVerification(
                claim="Claim 2",
                supported=True,
                confidence=0.95,
                evidence=["evidence"],
                reasoning="Supported",
            ),
        ]
        
        result = await calculator({"verifications": verifications})
        
        assert result["scores"]["trust_score"] >= 90
        assert result["scores"]["supported_count"] == 2
        assert result["scores"]["hallucination_rate"] == 0.0
    
    @pytest.mark.asyncio
    async def test_all_unsupported(self, calculator):
        verifications = [
            ClaimVerification(
                claim="False claim",
                supported=False,
                confidence=0.9,
                evidence=[],
                reasoning="Contradicted",
            ),
        ]
        
        result = await calculator({"verifications": verifications})
        
        assert result["scores"]["trust_score"] <= 20
        assert result["scores"]["unsupported_count"] == 1
        assert result["scores"]["hallucination_rate"] == 100.0
    
    @pytest.mark.asyncio
    async def test_mixed_verifications(self, calculator):
        verifications = [
            ClaimVerification(claim="True", supported=True, confidence=0.9, evidence=[], reasoning="OK"),
            ClaimVerification(claim="False", supported=False, confidence=0.8, evidence=[], reasoning="Wrong"),
        ]
        
        result = await calculator({"verifications": verifications})
        
        # Score should be around 50%
        assert 30 <= result["scores"]["trust_score"] <= 70
        assert result["scores"]["supported_count"] == 1
        assert result["scores"]["unsupported_count"] == 1


class TestGrading:
    
    def test_grade_a_plus(self, calculator):
        assert calculator._get_grade(98) == TrustGrade.A_PLUS
    
    def test_grade_a(self, calculator):
        assert calculator._get_grade(87) == TrustGrade.A
    
    def test_grade_b(self, calculator):
        assert calculator._get_grade(75) == TrustGrade.B
    
    def test_grade_c(self, calculator):
        assert calculator._get_grade(60) == TrustGrade.C
    
    def test_grade_d(self, calculator):
        assert calculator._get_grade(45) == TrustGrade.D
    
    def test_grade_f(self, calculator):
        assert calculator._get_grade(25) == TrustGrade.F


class TestQuickScore:
    
    def test_all_supported(self):
        score = calculate_quick_score(supported=5, unsupported=0)
        assert score == 100.0
    
    def test_all_unsupported(self):
        score = calculate_quick_score(supported=0, unsupported=5)
        assert score == 0.0
    
    def test_balanced(self):
        score = calculate_quick_score(supported=2, unsupported=2)
        assert score == 50.0
    
    def test_with_unknown(self):
        score = calculate_quick_score(supported=3, unsupported=1, unknown=1)
        # (3 - 1 + 0) / (5 * 2) * 100 + 50 = 70%
        assert 60 <= score <= 80


class TestFormatSummary:
    
    def test_format_produces_string(self):
        scores = AuditScores(
            trust_score=85.5,
            hallucination_rate=10.0,
            supported_count=8,
            unsupported_count=1,
            unknown_count=1,
            total_claims=10,
            grade="A",
            verdict="Generally reliable response.",
        )
        
        summary = format_score_summary(scores)
        
        assert "85.5%" in summary
        assert "Grade: A" in summary
        assert "10.0%" in summary


class TestCustomWeights:
    
    @pytest.mark.asyncio
    async def test_strict_weights(self):
        # Stricter scoring - partial support counts less
        weights = ScoreWeights(
            supported=1.0,
            partially_supported=0.3,  # Less credit
            unsupported=-1.5,  # Harsher penalty
        )
        calculator = CalculatorNode(weights)
        
        verifications = [
            ClaimVerification(claim="test", supported=False, confidence=0.9, evidence=[], reasoning="Wrong"),
        ]
        
        result = await calculator({"verifications": verifications})
        
        # With -1.5 penalty, score should be lower
        assert result["scores"]["trust_score"] < 30
```

Run tests:

```bash
poetry run pytest tests/unit/test_calculator.py -v
```

---

## 🎨 Visualization Helper

For debugging, add a visualization:

```python
# src/truthtable/graphs/nodes/calculator.py (add to end)

def visualize_score_bar(trust_score: float, width: int = 40) -> str:
    """
    Create an ASCII progress bar for the trust score.
    
    Args:
        trust_score: Score from 0-100
        width: Width of the bar in characters
        
    Returns:
        ASCII visualization string
    """
    filled = int(width * trust_score / 100)
    empty = width - filled
    
    # Color the bar based on score
    if trust_score >= 70:
        bar_char = "█"
        color_start = "\033[92m"  # Green
    elif trust_score >= 50:
        bar_char = "█"
        color_start = "\033[93m"  # Yellow
    else:
        bar_char = "█"
        color_start = "\033[91m"  # Red
    
    color_end = "\033[0m"
    
    return f"[{color_start}{bar_char * filled}{color_end}{'░' * empty}] {trust_score:.1f}%"
```

---

## 🐛 Common Issues

### Issue: Scores always 50%

**Cause:** All claims returning UNKNOWN
**Solution:** Check verifier prompts; LLM may need clearer instructions

### Issue: Unexpected F grades

**Cause:** Low confidence filtering out valid verifications
**Solution:** Lower `min_confidence_threshold` in `ScoreWeights`

```python
weights = ScoreWeights(min_confidence_threshold=0.2)
calculator = CalculatorNode(weights)
```

---

## ⏭️ Next Step

Continue to [Step 1.6: gRPC Server Setup](step-1.6-grpc-server.md) to expose the audit workflow as a gRPC service.
