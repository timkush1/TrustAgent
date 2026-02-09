package websocket

import (
	"testing"
	"time"
)

func TestNewHub(t *testing.T) {
	hub := NewHub()

	if hub == nil {
		t.Fatal("NewHub returned nil")
	}
	if hub.clients == nil {
		t.Error("clients map not initialized")
	}
	if hub.broadcast == nil {
		t.Error("broadcast channel not initialized")
	}
	if hub.register == nil {
		t.Error("register channel not initialized")
	}
	if hub.unregister == nil {
		t.Error("unregister channel not initialized")
	}
}

func TestHubClientCount(t *testing.T) {
	hub := NewHub()

	if hub.ClientCount() != 0 {
		t.Errorf("Expected 0 clients, got %d", hub.ClientCount())
	}
}

func TestHubStats(t *testing.T) {
	hub := NewHub()

	connections, broadcasts := hub.Stats()
	if connections != 0 {
		t.Errorf("Expected 0 connections, got %d", connections)
	}
	if broadcasts != 0 {
		t.Errorf("Expected 0 broadcasts, got %d", broadcasts)
	}
}

func TestAuditEventJSON(t *testing.T) {
	event := &AuditEvent{
		Type:       "audit_complete",
		RequestID:  "req-123",
		Timestamp:  time.Now(),
		Model:      "gpt-4",
		TrustScore: 0.85,
		Claims: []ClaimInfo{
			{Text: "Paris is the capital of France", Verdict: "verified", Confidence: 0.95},
		},
		Duration: 150,
	}

	if event.Type != "audit_complete" {
		t.Errorf("Expected type 'audit_complete', got '%s'", event.Type)
	}
	if event.TrustScore != 0.85 {
		t.Errorf("Expected trust score 0.85, got %f", event.TrustScore)
	}
	if len(event.Claims) != 1 {
		t.Errorf("Expected 1 claim, got %d", len(event.Claims))
	}
}

func TestGenerateClientID(t *testing.T) {
	id1 := generateClientID()
	time.Sleep(2 * time.Millisecond)
	id2 := generateClientID()

	if id1 == "" {
		t.Error("generateClientID returned empty string")
	}
	if id1 == id2 {
		t.Error("generateClientID should return unique IDs")
	}
}
