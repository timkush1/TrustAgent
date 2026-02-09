"""Unit tests for Claim Decomposer Node."""

import pytest
from unittest.mock import AsyncMock, MagicMock
import json

from truthtable.graphs.nodes.decomposer import (
    DecomposerNode,
    decompose_claims,
    create_decomposer_prompt,
    DECOMPOSER_SYSTEM_PROMPT
)
from truthtable.graphs.state import AuditState
from truthtable.providers.base import CompletionResponse


class TestDecomposerPrompts:
    """Test prompt generation."""
    
    def test_create_decomposer_prompt(self):
        """Test prompt creation."""
        response = "Paris is the capital of France."
        prompt = create_decomposer_prompt(response)
        
        assert "Paris is the capital of France" in prompt
        assert "JSON array" in prompt
        assert "<text>" in prompt


class TestDecomposeClaims:
    """Test claim decomposition function."""
    
    @pytest.mark.asyncio
    async def test_successful_decomposition(self):
        """Test successful claim extraction."""
        # Mock provider
        mock_provider = AsyncMock()
        mock_provider.model = "test-model"
        mock_provider.create_messages = MagicMock(return_value=[])
        
        # Mock LLM response with valid JSON
        claims_json = json.dumps([
            "Paris is the capital of France",
            "The Eiffel Tower is in Paris"
        ])
        mock_provider.complete.return_value = CompletionResponse(
            content=claims_json,
            model="test-model",
            finish_reason="stop"
        )
        
        # Run decomposition
        result = await decompose_claims(
            llm_response="Paris is the capital of France. The Eiffel Tower is in Paris.",
            provider=mock_provider
        )
        
        assert len(result) == 2
        assert "Paris is the capital of France" in result
        assert "The Eiffel Tower is in Paris" in result
    
    @pytest.mark.asyncio
    async def test_invalid_json_fallback(self):
        """Test fallback when LLM returns invalid JSON."""
        mock_provider = AsyncMock()
        mock_provider.model = "test-model"
        mock_provider.create_messages = MagicMock(return_value=[])
        
        # Mock LLM response with invalid JSON
        mock_provider.complete.return_value = CompletionResponse(
            content="This is not valid JSON",
            model="test-model",
            finish_reason="stop"
        )
        
        original_text = "Some response text"
        result = await decompose_claims(
            llm_response=original_text,
            provider=mock_provider
        )
        
        # Should fall back to original text as single claim
        assert len(result) == 1
        assert result[0] == original_text
    
    @pytest.mark.asyncio
    async def test_filters_short_claims(self):
        """Test that very short claims are filtered out."""
        mock_provider = AsyncMock()
        mock_provider.model = "test-model"
        mock_provider.create_messages = MagicMock(return_value=[])
        
        claims_json = json.dumps([
            "This is a valid claim",
            "x",  # Too short
            "",   # Empty
            "Another valid claim"
        ])
        mock_provider.complete.return_value = CompletionResponse(
            content=claims_json,
            model="test-model",
            finish_reason="stop"
        )
        
        result = await decompose_claims(
            llm_response="test",
            provider=mock_provider
        )
        
        assert len(result) == 2
        assert "This is a valid claim" in result
        assert "Another valid claim" in result


class TestDecomposerNode:
    """Test DecomposerNode class."""
    
    @pytest.mark.asyncio
    async def test_node_execution(self):
        """Test node execution updates state correctly."""
        # Mock provider
        mock_provider = AsyncMock()
        mock_provider.model = "test-model"
        mock_provider.create_messages = MagicMock(return_value=[])
        
        claims_json = json.dumps([
            "Claim 1",
            "Claim 2"
        ])
        mock_provider.complete.return_value = CompletionResponse(
            content=claims_json,
            model="test-model",
            finish_reason="stop"
        )
        
        # Create node
        node = DecomposerNode(provider=mock_provider)
        
        # Create initial state
        state: AuditState = {
            "request_id": "test-123",
            "user_query": "Test query?",
            "llm_response": "Test response with claims.",
            "context_docs": ["doc1", "doc2"],
            "claims": None,
            "claim_verifications": None,
            "faithfulness_score": None,
            "hallucination_detected": None,
            "reasoning_trace": None
        }
        
        # Run node
        updated_state = await node.run(state)
        
        # Verify state was updated
        assert updated_state["claims"] is not None
        assert len(updated_state["claims"]) == 2
        assert "Claim 1" in updated_state["claims"]
        assert "Claim 2" in updated_state["claims"]
        
        # Other fields should be unchanged
        assert updated_state["request_id"] == "test-123"
        assert updated_state["user_query"] == "Test query?"
