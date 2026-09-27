package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// clearEnv unsets every key Load reads, so the host environment cannot leak into a test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TRUTHTABLE_") || k == "UPSTREAM_LLM_URL" || k == "OLLAMA_BASE_URL" ||
			k == "OPENROUTER_API_KEY" || k == "TYPESAFE_API_KEY" || k == "REDIS_URL" {
			t.Setenv(k, "")
		}
	}
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	c := Load()
	if len(c.Warnings) != 0 {
		t.Errorf("warnings %v", c.Warnings)
	}
	if c.IngestAuth != "keys" || c.TestMode || c.DemoMode || c.UpstreamURL != "https://api.openai.com" {
		t.Errorf("gateway defaults %+v", c)
	}
	if c.GateDefaultMode != "observe" || c.GateFailMode != "open" || c.GateMaxConcurrency != 256 || c.UniformAuditsPerDay != 50 {
		t.Errorf("gate defaults %+v", c)
	}
	if c.MaxUpstreamResponseBytes != 8<<20 || c.UpstreamHeaderTimeout != 60*time.Second || c.UpstreamTimeout != 5*time.Minute {
		t.Errorf("upstream defaults %+v", c)
	}
	j := c.Jev
	want := JevConfig{
		Backend: "auto", AttemptTimeout: 3 * time.Second, AsyncDeadline: 10 * time.Second, TokenBudget: 24000,
		CharsPerToken: 4, MaxQuestionsPerCall: 16, MaxResponseTokens: 6000, MockMode: "scenario", MockSeed: 42,
		MockLatency: 40 * time.Millisecond,
	}
	if !reflect.DeepEqual(j, want) {
		t.Errorf("jev defaults %+v, want %+v", j, want)
	}
	if !strings.HasSuffix(c.PolicyFile, filepath.Join("config", "policies.yaml")) {
		t.Errorf("policy file %q", c.PolicyFile)
	}
}

func TestPolicyFileResolvesAgainstRepoRoot(t *testing.T) {
	clearEnv(t)
	// Tests run in backend-go/internal/config, where config/policies.yaml does not exist; the
	// repository root (holding VERSION) has it.
	c := Load()
	if !filepath.IsAbs(c.PolicyFile) {
		t.Fatalf("policy file %q was not resolved against the repository root", c.PolicyFile)
	}
	if _, err := os.Stat(c.PolicyFile); err != nil {
		t.Errorf("resolved policy file: %v", err)
	}
	if got := RepoPath("no/such/file.yaml"); got != "no/such/file.yaml" {
		t.Errorf("an unresolvable path is returned as is, got %q", got)
	}
	if got := RepoPath("/etc/trustagent/policies.yaml"); got != "/etc/trustagent/policies.yaml" {
		t.Errorf("an absolute path is returned as is, got %q", got)
	}
}

func TestKeyList(t *testing.T) {
	clearEnv(t)
	t.Setenv("TRUTHTABLE_API_KEYS", " trafficgen=ta-tg-secret , app=ta=with=equals,legacy-key,=nolabel,nokey=, ,app=dup")
	c := Load()
	want := []APIKey{
		{Label: "trafficgen", Key: "ta-tg-secret"},
		{Label: "app", Key: "ta=with=equals"},
		{Label: "key3", Key: "legacy-key"},
		{Label: "app", Key: "dup"},
	}
	if !reflect.DeepEqual(c.APIKeys, want) {
		t.Errorf("keys %+v, want %+v", c.APIKeys, want)
	}
	if len(c.Warnings) != 4 {
		t.Errorf("warnings %v: want no-label, two incomplete entries and a duplicate label", c.Warnings)
	}
	if !reflect.DeepEqual(c.APIKeySecrets(), []string{"ta-tg-secret", "ta=with=equals", "legacy-key", "dup"}) {
		t.Errorf("secrets %v", c.APIKeySecrets())
	}
}

func TestGatewayKeysAndInvalidValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("TRUTHTABLE_TEST_MODE", "true")
	t.Setenv("TRUTHTABLE_DEMO_MODE", "1")
	t.Setenv("TRUTHTABLE_INGEST_AUTH", "open")
	t.Setenv("TRUTHTABLE_JEV_BACKEND", "mock")
	t.Setenv("TRUTHTABLE_JEV_MOCK_STRICT", "yes-please")
	t.Setenv("TRUTHTABLE_JEV_MOCK_LATENCY", "0s")
	t.Setenv("TRUTHTABLE_GATE_DEFAULT_MODE", "strict")
	t.Setenv("TRUTHTABLE_GATE_MAX_CONCURRENCY", "lots")
	t.Setenv("TRUTHTABLE_JEV_CHARS_PER_TOKEN", "3.5")
	c := Load()
	if !c.TestMode || !c.DemoMode || c.IngestAuth != "open" || c.Jev.Backend != "mock" || c.Jev.MockLatency != 0 || c.Jev.CharsPerToken != 3.5 {
		t.Errorf("parsed %+v jev %+v", c, c.Jev)
	}
	if c.Jev.MockStrict || c.GateDefaultMode != "observe" || c.GateMaxConcurrency != 256 {
		t.Errorf("invalid values must fall back: strict %v mode %s concurrency %d", c.Jev.MockStrict, c.GateDefaultMode, c.GateMaxConcurrency)
	}
	if len(c.Warnings) != 3 {
		t.Errorf("warnings %v", c.Warnings)
	}
}

func TestUpstreamURL(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"TRUTHTABLE_UPSTREAM_URL", "https://gw.example/v1base", "https://gw.example/v1base"},
		{"UPSTREAM_LLM_URL", "http://legacy:9000", "http://legacy:9000"},
		{"TRUTHTABLE_UPSTREAM_URL", "openrouter", "https://openrouter.ai/api"},
		{"TRUTHTABLE_UPSTREAM_URL", "ollama", "http://localhost:11434"},
	} {
		clearEnv(t)
		t.Setenv(c.key, c.value)
		if got := Load().UpstreamURL; got != c.want {
			t.Errorf("%s=%s: %s, want %s", c.key, c.value, got, c.want)
		}
	}
	clearEnv(t)
	t.Setenv("TRUTHTABLE_UPSTREAM_URL", "ollama")
	t.Setenv("OLLAMA_BASE_URL", "http://ollama:11434")
	if got := Load().UpstreamURL; got != "http://ollama:11434" {
		t.Errorf("ollama shorthand uses OLLAMA_BASE_URL: %s", got)
	}
}
