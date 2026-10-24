package main

import (
	"testing"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/recording"
)

// TestRecordingHosts: the admin export names the hosts this gateway calls (D203), so a stack on
// mock-upstream (the e2e and onboarding stacks) cannot export a recorded manifest.
func TestRecordingHosts(t *testing.T) {
	vendor := &server{cfg: &config.Config{UpstreamURL: "https://openrouter.ai/api", Jev: config.JevConfig{OpenRouterBaseURL: "https://openrouter.ai"}}}
	h := vendor.recordingHosts(&recording.Judge{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5"})
	if h.Judge != "openrouter.ai" || h.Generated != "openrouter.ai" || h.Decision != "" {
		t.Errorf("vendor stack: %+v", h)
	}
	mock := &server{cfg: &config.Config{UpstreamURL: "http://mock-upstream:8090", Jev: config.JevConfig{OpenRouterBaseURL: "http://mock-upstream:8090"}}}
	h = mock.recordingHosts(&recording.Judge{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5"})
	if h.Judge != "mock-upstream" || h.Generated != "mock-upstream" || recording.VendorHost(h.Judge) {
		t.Errorf("mock-upstream stack: %+v", h)
	}
	if h := mock.recordingHosts(nil); h.Judge != "" {
		t.Errorf("no judge: %+v", h)
	}
}
