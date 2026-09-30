package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPolicyWiringKnowsTheFallbackUpstream: the policy services accept fallback.upstream: fallback
// only when the gateway has a fallback upstream (TRUTHTABLE_FALLBACK_UPSTREAM_URL, GH-7): without
// one, a policy file that names it does not load.
func TestPolicyWiringKnowsTheFallbackUpstream(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(file, []byte("version: 1\ndefaults: {fallback: {upstream: fallback}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, envOpts{})
	cfg.PolicyFile = file
	if _, err := newPolicyServices(context.Background(), policyWiring{cfg: cfg, log: quietLog}); err == nil ||
		!strings.Contains(err.Error(), "fallback.upstream") {
		t.Errorf("without a fallback upstream the file must not load: %v", err)
	}

	cfg.FallbackUpstreamURL = "http://127.0.0.1:9"
	ps, err := newPolicyServices(context.Background(), policyWiring{cfg: cfg, log: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	if d := ps.Engine.Current().Default; d.Fallback.Upstream != "fallback" {
		t.Errorf("default route fallback %+v", d.Fallback)
	}
}
