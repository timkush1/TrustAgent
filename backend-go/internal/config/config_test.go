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
			k == "OPENROUTER_API_KEY" || k == "TYPESAFE_API_KEY" || k == "REDIS_URL" || k == "KNOWLEDGE_ENABLED" ||
			k == "RAG_ENABLED" {
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
		// The registry defaults of the HTTP backends (SPEC §2.7), which jev.DefaultHTTPConfig mirrors.
		OpenRouterBaseURL: "https://openrouter.ai", MaxRetries: 1, MaxRetriesAsync: 2, RetryAfterCap: 10 * time.Second,
		RPM: 1000, SyncReserve: 0.3, MaxInFlight: 64, BreakerFailures: 5, BreakerCooldown: 30 * time.Second,
		AuthCooldown: 5 * time.Minute, AppURL: "https://github.com/timkush1/TrustAgent", AppTitle: "TrustAgent",
		SendSessionID: true, StartupProbe: true, KeepAlive: 60 * time.Second, SpendEnforcingReserve: 0.25,
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

func TestPersistenceWorkerAndWebSocketKeys(t *testing.T) {
	clearEnv(t)
	c := Load()
	want := WSConfig{MaxClients: 200, MaxPerIP: 10, ReplayBuffer: 2000, ReplayMaxBytes: 64 << 20, Heartbeat: 20 * time.Second}
	if c.WS != want {
		t.Errorf("ws defaults %+v, want %+v", c.WS, want)
	}
	if c.TraceFlush != "async" || c.BodyRetention != 168*time.Hour || c.BodyMaxBytes != 262144 || c.PublicRead || c.Readonly {
		t.Errorf("persistence defaults %+v", c)
	}
	if c.AuditQueueHigh != 200 || c.AuditHighPerMin != 30 || c.EngineHealthInterval != 10*time.Second ||
		c.ShutdownDrainTimeout != 20*time.Second || c.MetricsPort != 8002 || c.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("worker and server defaults %+v", c)
	}
	if c.SpendCapDecisionUSDDay != 2 || c.SpendCapLLMUSDDay != 5 || c.InternalToken != "" {
		t.Errorf("budget and token defaults %+v", c)
	}

	t.Setenv("TRUTHTABLE_TRACE_FLUSH", "sync")
	t.Setenv("TRUTHTABLE_PUBLIC_READ", "true")
	t.Setenv("TRUTHTABLE_WS_MAX_PER_IP", "3")
	t.Setenv("TRUTHTABLE_WS_HEARTBEAT", "5s")
	t.Setenv("TRUTHTABLE_INTERNAL_TOKEN", " tok ")
	t.Setenv("TRUTHTABLE_AUDIT_QUEUE_HIGH", "many")
	c = Load()
	if c.TraceFlush != "sync" || !c.PublicRead || c.WS.MaxPerIP != 3 || c.WS.Heartbeat != 5*time.Second || c.InternalToken != "tok" {
		t.Errorf("parsed %+v", c)
	}
	if c.AuditQueueHigh != 200 || len(c.Warnings) != 1 {
		t.Errorf("an invalid queue size falls back with a warning: %d %v", c.AuditQueueHigh, c.Warnings)
	}
}

func TestTrustedProxiesAndInternalTokenFile(t *testing.T) {
	clearEnv(t)
	c := Load()
	if c.TrustedProxies != nil || c.InternalTokenFile != DefaultInternalTokenFile {
		t.Errorf("defaults: trusted proxies %v, token file %q", c.TrustedProxies, c.InternalTokenFile)
	}

	t.Setenv("TRUTHTABLE_TRUSTED_PROXIES", "172.16.0.0/12, 10.0.0.7 ,nginx, ::1")
	t.Setenv("TRUTHTABLE_INTERNAL_TOKEN_FILE", "/run/trustagent/token")
	c = Load()
	if want := []string{"172.16.0.0/12", "10.0.0.7", "::1"}; !reflect.DeepEqual(c.TrustedProxies, want) {
		t.Errorf("trusted proxies %v, want %v", c.TrustedProxies, want)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], `"nginx"`) {
		t.Errorf("an entry that is not a network is ignored with a warning: %v", c.Warnings)
	}
	if c.InternalTokenFile != "/run/trustagent/token" {
		t.Errorf("token file %q", c.InternalTokenFile)
	}
}

func TestDecisionModelHTTPKeys(t *testing.T) {
	clearEnv(t)
	keyFile := filepath.Join(t.TempDir(), "jev.key")
	if err := os.WriteFile(keyFile, []byte("  gw-key-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"TRUTHTABLE_JEV_PATH": "/typesafe/v1/systemone", "TRUTHTABLE_OPENROUTER_BASE_URL": "http://mock-upstream:8090",
		"TRUTHTABLE_JEV_MAX_RETRIES": "0", "TRUTHTABLE_JEV_MAX_RETRIES_ASYNC": "3", "TRUTHTABLE_JEV_RETRY_AFTER_CAP": "4s",
		"TRUTHTABLE_GATE_JEV_RPM": "120", "TRUTHTABLE_GATE_JEV_SYNC_RESERVE": "0.5", "TRUTHTABLE_GATE_JEV_MAX_INFLIGHT": "8",
		"TRUTHTABLE_JEV_BREAKER_FAILURES": "2", "TRUTHTABLE_JEV_BREAKER_COOLDOWN": "5s", "TRUTHTABLE_JEV_AUTH_COOLDOWN": "1m",
		"TRUTHTABLE_JEV_PRICE_PER_MTOK": "0.05", "TRUTHTABLE_JEV_ZDR": "true", "TRUTHTABLE_JEV_SEND_SESSION_ID": "false",
		"TRUTHTABLE_JEV_STARTUP_PROBE": "false", "TRUTHTABLE_JEV_KEEPALIVE": "0s", "TRUTHTABLE_JEV_LOG_PAYLOADS": "true",
		"TRUTHTABLE_SPEND_ENFORCING_RESERVE": "0.1", "TRUTHTABLE_KEY_DECISION_USD_DAY": "0.5",
		"TRUTHTABLE_JEV_API_KEY_FILE": keyFile, "TRUTHTABLE_PUBLIC_URL": "https://trust.example.com",
	} {
		t.Setenv(k, v)
	}
	c := Load()
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings %v", c.Warnings)
	}
	j := c.Jev
	if j.Path != "/typesafe/v1/systemone" || j.OpenRouterBaseURL != "http://mock-upstream:8090" || j.APIKey != "gw-key-from-file" {
		t.Errorf("endpoint keys %+v", j)
	}
	if red := c.Redactor().Redact("key gw-key-from-file"); strings.Contains(red, "gw-key-from-file") {
		t.Errorf("a credential read from a file is masked in logs: %q", red)
	}
	if j.MaxRetries != 0 || j.MaxRetriesAsync != 3 || j.RetryAfterCap != 4*time.Second || j.RPM != 120 ||
		j.SyncReserve != 0.5 || j.MaxInFlight != 8 || j.BreakerFailures != 2 || j.BreakerCooldown != 5*time.Second ||
		j.AuthCooldown != time.Minute || j.PricePerMTok != 0.05 {
		t.Errorf("resilience keys %+v", j)
	}
	if !j.ZDR || j.SendSessionID || j.StartupProbe || j.KeepAlive != 0 || !j.LogPayloads ||
		j.SpendEnforcingReserve != 0.1 || j.KeyUSDDay != 0.5 || c.PublicURL != "https://trust.example.com" {
		t.Errorf("flags %+v public %q", j, c.PublicURL)
	}
}

func TestDecisionModelKeysOutOfRangeFallBack(t *testing.T) {
	clearEnv(t)
	t.Setenv("TRUTHTABLE_GATE_JEV_RPM", "0")
	t.Setenv("TRUTHTABLE_GATE_JEV_SYNC_RESERVE", "1.5")
	t.Setenv("TRUTHTABLE_JEV_MAX_RETRIES", "-1")
	t.Setenv("TRUTHTABLE_JEV_PRICE_PER_MTOK", "-0.1")
	t.Setenv("TRUTHTABLE_JEV_API_KEY_FILE", filepath.Join(t.TempDir(), "missing.key"))
	t.Setenv("TRUTHTABLE_JEV_API_KEY", "")
	c := Load()
	if c.Jev.RPM != 1000 || c.Jev.SyncReserve != 0.3 || c.Jev.MaxRetries != 1 || c.Jev.PricePerMTok != 0 || c.Jev.APIKey != "" {
		t.Errorf("fallbacks %+v", c.Jev)
	}
	if len(c.Warnings) != 5 {
		t.Errorf("want 5 warnings, got %v", c.Warnings)
	}
	for _, w := range c.Warnings {
		if strings.Contains(w, "gw-key") {
			t.Errorf("a warning leaks a credential: %q", w)
		}
	}
}

func TestPolicyEngineKeys(t *testing.T) {
	clearEnv(t)
	c := Load()
	if c.PolicyReloadInterval != 5*time.Second || c.ConfigLock {
		t.Errorf("policy defaults: reload %s lock %v", c.PolicyReloadInterval, c.ConfigLock)
	}
	if !strings.HasSuffix(c.EvaluatorsDir, filepath.Join("config", "evaluators")) {
		t.Errorf("evaluators dir %q", c.EvaluatorsDir)
	}

	t.Setenv("TRUTHTABLE_POLICY_RELOAD_INTERVAL", "0")
	t.Setenv("TRUTHTABLE_CONFIG_LOCK", "true")
	t.Setenv("TRUTHTABLE_EVALUATORS_DIR", "/etc/trustagent/evaluators")
	c = Load()
	if c.PolicyReloadInterval != 0 || !c.ConfigLock || c.EvaluatorsDir != "/etc/trustagent/evaluators" {
		t.Errorf("policy keys: reload %s lock %v dir %q", c.PolicyReloadInterval, c.ConfigLock, c.EvaluatorsDir)
	}

	t.Setenv("TRUTHTABLE_POLICY_RELOAD_INTERVAL", "soon")
	c = Load()
	if c.PolicyReloadInterval != 5*time.Second || len(c.Warnings) != 1 {
		t.Errorf("an invalid interval falls back with a warning: %s %v", c.PolicyReloadInterval, c.Warnings)
	}
}

// TestUpstreamStreamCalibrationAndKnowledgeKeys: the keys P3 binds load with their registry
// defaults, and every one reads its variable (SPEC §2.7 upstream and policy groups).
func TestUpstreamStreamCalibrationAndKnowledgeKeys(t *testing.T) {
	clearEnv(t)
	c := Load()
	if len(c.Warnings) != 0 {
		t.Errorf("warnings %v", c.Warnings)
	}
	if c.UpstreamAuthHeader != "authorization" || c.AnthropicUpstreamURL != "https://api.anthropic.com" ||
		c.AnthropicUpstreamAPIKey != "" || c.FallbackUpstreamURL != "" || c.FallbackUpstreamAPIKey != "" ||
		c.FallbackUpstreamFormat != "openai" || c.StreamIdleTimeout != 120*time.Second {
		t.Errorf("upstream defaults: header %q anthropic %q/%q fallback %q/%q/%q idle %s", c.UpstreamAuthHeader,
			c.AnthropicUpstreamURL, c.AnthropicUpstreamAPIKey, c.FallbackUpstreamURL, c.FallbackUpstreamAPIKey,
			c.FallbackUpstreamFormat, c.StreamIdleTimeout)
	}
	if !strings.HasSuffix(c.CalibrationDir, filepath.Join("config", "calibration")) || c.CalibrationFamilyFallback {
		t.Errorf("calibration defaults: %q family %v", c.CalibrationDir, c.CalibrationFamilyFallback)
	}
	if c.KnowledgeEnabled || c.KnowledgeDeadline != 150*time.Millisecond {
		t.Errorf("knowledge defaults: %v %s", c.KnowledgeEnabled, c.KnowledgeDeadline)
	}
	if c.RetentionDays != 30 || c.CostRetentionDays != 400 || c.LabelSampleRetention != 2160*time.Hour {
		t.Errorf("retention defaults: %d %d %s", c.RetentionDays, c.CostRetentionDays, c.LabelSampleRetention)
	}
	if c.Injecting() {
		t.Error("no upstream key: passthrough")
	}

	for k, v := range map[string]string{
		"TRUTHTABLE_UPSTREAM_AUTH_HEADER":        "X-Api-Key",
		"TRUTHTABLE_ANTHROPIC_UPSTREAM_URL":      "https://anthropic.example/base",
		"TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY":  "sk-ant-anthropic-0123456789",
		"TRUTHTABLE_FALLBACK_UPSTREAM_URL":       "http://fallback:9000/v1base?tier=cheap",
		"TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY":   "sk-fallback-0123456789",
		"TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT":    "Anthropic",
		"TRUTHTABLE_STREAM_IDLE_TIMEOUT":         "45s",
		"TRUTHTABLE_CALIBRATION_DIR":             "/etc/trustagent/calibration",
		"TRUTHTABLE_CALIBRATION_FAMILY_FALLBACK": "true",
		"RAG_ENABLED":                            "true", // a legacy alias of TRUTHTABLE_KNOWLEDGE_ENABLED
		"TRUTHTABLE_KNOWLEDGE_DEADLINE":          "250ms",
		"TRUTHTABLE_RETENTION_DAYS":              "7",
		"TRUTHTABLE_COST_RETENTION_DAYS":         "90",
		"TRUTHTABLE_LABEL_SAMPLE_RETENTION":      "720h",
	} {
		t.Setenv(k, v)
	}
	c = Load()
	if len(c.Warnings) != 0 {
		t.Errorf("warnings %v", c.Warnings)
	}
	if c.UpstreamAuthHeader != "x-api-key" || c.AnthropicUpstreamURL != "https://anthropic.example/base" ||
		c.AnthropicUpstreamAPIKey != "sk-ant-anthropic-0123456789" || c.FallbackUpstreamURL != "http://fallback:9000/v1base?tier=cheap" ||
		c.FallbackUpstreamAPIKey != "sk-fallback-0123456789" || c.FallbackUpstreamFormat != "anthropic" ||
		c.StreamIdleTimeout != 45*time.Second {
		t.Errorf("upstream keys: header %q anthropic %q fallback %q/%q idle %s", c.UpstreamAuthHeader, c.AnthropicUpstreamURL,
			c.FallbackUpstreamURL, c.FallbackUpstreamFormat, c.StreamIdleTimeout)
	}
	if c.CalibrationDir != "/etc/trustagent/calibration" || !c.CalibrationFamilyFallback {
		t.Errorf("calibration keys: %q %v", c.CalibrationDir, c.CalibrationFamilyFallback)
	}
	if !c.KnowledgeEnabled || c.KnowledgeDeadline != 250*time.Millisecond {
		t.Errorf("knowledge keys: %v %s", c.KnowledgeEnabled, c.KnowledgeDeadline)
	}
	if c.RetentionDays != 7 || c.CostRetentionDays != 90 || c.LabelSampleRetention != 720*time.Hour {
		t.Errorf("retention keys: %d %d %s", c.RetentionDays, c.CostRetentionDays, c.LabelSampleRetention)
	}
	if !c.Injecting() || !reflect.DeepEqual(c.InjectedKeys(), []string{"TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY", "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY"}) {
		t.Errorf("injected keys %v", c.InjectedKeys())
	}
}

// TestUpstreamKeysValidate: the enums and durations refuse bad values with a warning and keep
// their defaults; a URL that is not one keeps its default too.
func TestUpstreamKeysValidate(t *testing.T) {
	clearEnv(t)
	for k, v := range map[string]string{
		"TRUTHTABLE_UPSTREAM_AUTH_HEADER":     "bearer",
		"TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT": "gemini",
		"TRUTHTABLE_STREAM_IDLE_TIMEOUT":      "-5s",
		"TRUTHTABLE_KNOWLEDGE_DEADLINE":       "soon",
		"TRUTHTABLE_ANTHROPIC_UPSTREAM_URL":   "api.anthropic.com",
		"TRUTHTABLE_RETENTION_DAYS":           "0",
	} {
		t.Setenv(k, v)
	}
	c := Load()
	if c.UpstreamAuthHeader != "authorization" || c.FallbackUpstreamFormat != "openai" || c.StreamIdleTimeout != 120*time.Second ||
		c.KnowledgeDeadline != 150*time.Millisecond || c.AnthropicUpstreamURL != "https://api.anthropic.com" || c.RetentionDays != 30 {
		t.Errorf("invalid values keep their defaults: %q %q %s %s %q %d", c.UpstreamAuthHeader, c.FallbackUpstreamFormat,
			c.StreamIdleTimeout, c.KnowledgeDeadline, c.AnthropicUpstreamURL, c.RetentionDays)
	}
	for _, key := range []string{
		"TRUTHTABLE_UPSTREAM_AUTH_HEADER", "TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT", "TRUTHTABLE_STREAM_IDLE_TIMEOUT",
		"TRUTHTABLE_KNOWLEDGE_DEADLINE", "TRUTHTABLE_ANTHROPIC_UPSTREAM_URL", "TRUTHTABLE_RETENTION_DAYS",
	} {
		if !hasWarning(c, key, "is not") {
			t.Errorf("no warning for %s: %v", key, c.Warnings)
		}
	}
	if len(c.Diagnostics) != 6 {
		t.Errorf("warnings %v", c.Warnings)
	}
	if !hasWarning(c, "TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT", "one of openai, anthropic") {
		t.Errorf("the format enum is named: %v", c.Warnings)
	}
}

// TestInjectModePerUpstream (D15): every upstream key alone is inject mode and refuses open
// ingest, naming the key; a fallback key without a fallback upstream is reported as unused.
func TestInjectModePerUpstream(t *testing.T) {
	for _, key := range []string{
		"TRUTHTABLE_UPSTREAM_API_KEY", "TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY", "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY",
	} {
		c := LoadWith(LoadOptions{Environ: []string{
			"TRUTHTABLE_INGEST_AUTH=open", key + "=sk-injected-0123456789", "TRUTHTABLE_FALLBACK_UPSTREAM_URL=http://fb:1",
		}})
		if !c.Injecting() || c.IngestAuth != "keys" || !hasWarning(c, "TRUTHTABLE_INGEST_AUTH", "open is refused") ||
			!hasWarning(c, "TRUTHTABLE_INGEST_AUTH", key) {
			t.Errorf("%s: injecting %v ingest %s warnings %v", key, c.Injecting(), c.IngestAuth, c.Warnings)
		}
		for _, w := range c.Warnings {
			if strings.Contains(w, "sk-injected") {
				t.Errorf("%s: a credential leaked into a warning: %s", key, w)
			}
		}
	}

	c := LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY=sk-fallback-0123456789"}})
	if !hasWarning(c, "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY", "never sent") || len(c.Diagnostics) != 1 {
		t.Errorf("an unused fallback key is reported: %v", c.Warnings)
	}
	if strings.Contains(strings.Join(c.Warnings, "\n"), "sk-fallback") {
		t.Errorf("the fallback key leaked: %v", c.Warnings)
	}
	c = LoadWith(LoadOptions{Environ: []string{"TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY=sk-fallback-0123456789", "TRUTHTABLE_FALLBACK_UPSTREAM_URL=https://fb.example"}})
	if len(c.Diagnostics) != 0 {
		t.Errorf("a fallback key with its upstream: %v", c.Warnings)
	}
}

// TestFallbackUpstreamOnAnotherHostNeedsItsOwnKey (D23): a fallback upstream without its own key
// gets the client's credentials, so it must be on the host of the primary upstream of its format
// (scheme, host name and port; case and a default port do not matter). On another host it is a
// warning, which TRUTHTABLE_STRICT_CONFIG refuses to start with; the warning names keys, never a
// value. A key of its own settles it.
func TestFallbackUpstreamOnAnotherHostNeedsItsOwnKey(t *testing.T) {
	for _, c := range []struct {
		name    string
		environ []string
		// primary is the key the warning names; "" means no warning.
		primary string
	}{
		{"another host", []string{"TRUTHTABLE_FALLBACK_UPSTREAM_URL=https://cheap-llm.example/v1"}, "TRUTHTABLE_UPSTREAM_URL"},
		{"another port", []string{"TRUTHTABLE_UPSTREAM_URL=http://llm.internal:8080", "TRUTHTABLE_FALLBACK_UPSTREAM_URL=http://llm.internal:8081"},
			"TRUTHTABLE_UPSTREAM_URL"},
		{"another scheme", []string{"TRUTHTABLE_FALLBACK_UPSTREAM_URL=http://api.openai.com"}, "TRUTHTABLE_UPSTREAM_URL"},
		{"the anthropic primary", []string{"TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT=anthropic", "TRUTHTABLE_FALLBACK_UPSTREAM_URL=https://api.openai.com"},
			"TRUTHTABLE_ANTHROPIC_UPSTREAM_URL"},
		{"the same host", []string{"TRUTHTABLE_UPSTREAM_URL=http://mock-upstream:8090", "TRUTHTABLE_FALLBACK_UPSTREAM_URL=http://mock-upstream:8090/fallback?tier=cheap"}, ""},
		{"the same host by default", []string{"TRUTHTABLE_FALLBACK_UPSTREAM_URL=https://API.OpenAI.com:443/v1"}, ""},
		{"the anthropic host", []string{"TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT=anthropic", "TRUTHTABLE_FALLBACK_UPSTREAM_URL=https://api.anthropic.com/"}, ""},
		{"its own key", []string{"TRUTHTABLE_FALLBACK_UPSTREAM_URL=https://cheap-llm.example", "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY=sk-fallback-0123456789"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := LoadWith(LoadOptions{Environ: append([]string{"TRUTHTABLE_STRICT_CONFIG=true"}, c.environ...)})
			if c.primary == "" {
				if len(cfg.Diagnostics) != 0 {
					t.Errorf("warnings %v", cfg.Warnings)
				}
				return
			}
			if !hasWarning(cfg, "TRUTHTABLE_FALLBACK_UPSTREAM_URL", "another host than "+c.primary) ||
				!hasWarning(cfg, "TRUTHTABLE_FALLBACK_UPSTREAM_URL", "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY") || len(cfg.Diagnostics) != 1 {
				t.Fatalf("warnings %v", cfg.Warnings)
			}
			if !cfg.StrictFailure() {
				t.Error("strict config refuses to start with it")
			}
			for _, host := range []string{"cheap-llm", "llm.internal", "openai.com", "http:"} {
				if strings.Contains(cfg.Warnings[0], host) {
					t.Errorf("the warning names a value (%s): %s", host, cfg.Warnings[0])
				}
			}
		})
	}
}
