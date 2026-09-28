// Package config reads the proxy's environment. Key names, defaults and enums follow the registry
// contracts/config/keys.v1.json (SPEC §2.7). Invalid values fall back to the default and are
// reported in Config.Warnings.
//
// The registry-driven loader (dotenv, strict mode, typo hints, masking) replaces this in P2.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ServerPort      int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	UpstreamURL     string
	GRPCAddress     string
	GRPCTimeout     time.Duration
	WorkerCount     int
	QueueSize       int
	LogLevel        string

	// Security
	APIKeys            []APIKey // TRUTHTABLE_API_KEYS: ingest + read + content keys
	AllowedOrigins     []string // CORS + WebSocket origin allowlist
	RedisURL           string   // rate-limiter backend; empty = in-memory
	DatabaseURL        string   // audit persistence; empty = disabled
	RateLimitPerMinute int      // per client (IP or API key)
	UploadLimitPerMin  int      // stricter limit for /api/upload
	MaxBodyBytes       int64    // request body cap (non-upload routes)
	MaxUploadBytes     int64    // request body cap for /api/upload
	MaxTextChars       int      // max chars for query/response fields

	// Gateway
	IngestAuth               string        // TRUTHTABLE_INGEST_AUTH: keys | open
	TestMode                 bool          // TRUTHTABLE_TEST_MODE (M7)
	DemoMode                 bool          // TRUTHTABLE_DEMO_MODE
	UpstreamHeaderTimeout    time.Duration // TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT
	UpstreamTimeout          time.Duration // TRUTHTABLE_UPSTREAM_TIMEOUT
	MaxUpstreamResponseBytes int64         // TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES
	PolicyFile               string        // TRUTHTABLE_POLICY_FILE
	GateDefaultMode          string        // TRUTHTABLE_GATE_DEFAULT_MODE (seed)
	GateFailMode             string        // TRUTHTABLE_GATE_FAIL_MODE (seed)
	GateMaxConcurrency       int           // TRUTHTABLE_GATE_MAX_CONCURRENCY
	UniformAuditsPerDay      int           // TRUTHTABLE_UNIFORM_AUDITS_PER_DAY
	Jev                      JevConfig

	// Server, persistence, engine link and WebSocket
	ReadHeaderTimeout      time.Duration // TRUTHTABLE_READ_HEADER_TIMEOUT
	MetricsPort            int           // TRUTHTABLE_METRICS_PORT
	ShutdownDrainTimeout   time.Duration // TRUTHTABLE_SHUTDOWN_DRAIN_TIMEOUT (M6)
	PublicRead             bool          // TRUTHTABLE_PUBLIC_READ: /api reads and /ws without a key
	Readonly               bool          // TRUTHTABLE_READONLY
	InternalToken          string        // TRUTHTABLE_INTERNAL_TOKEN (metadata x-trustagent-internal)
	TraceFlush             string        // TRUTHTABLE_TRACE_FLUSH: async | sync
	BodyRetention          time.Duration // TRUTHTABLE_BODY_RETENTION
	BodyMaxBytes           int           // TRUTHTABLE_BODY_MAX_BYTES
	AuditQueueHigh         int           // TRUTHTABLE_AUDIT_QUEUE_HIGH (QueueSize is the low lane)
	AuditHighPerMin        int           // TRUTHTABLE_AUDIT_HIGH_PER_MIN
	EngineHealthInterval   time.Duration // TRUTHTABLE_ENGINE_HEALTH_INTERVAL (M4)
	SpendCapDecisionUSDDay float64       // TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY (seed)
	SpendCapLLMUSDDay      float64       // TRUTHTABLE_SPEND_CAP_LLM_USD_DAY (seed)
	WS                     WSConfig

	// Warnings lists invalid values that fell back to their defaults.
	Warnings []string
}

// WSConfig is the WebSocket hub configuration (group websocket).
type WSConfig struct {
	MaxClients     int           // TRUTHTABLE_WS_MAX_CLIENTS
	MaxPerIP       int           // TRUTHTABLE_WS_MAX_PER_IP
	ReplayBuffer   int           // TRUTHTABLE_WS_REPLAY_BUFFER
	ReplayMaxBytes int64         // TRUTHTABLE_WS_REPLAY_MAX_BYTES
	Heartbeat      time.Duration // TRUTHTABLE_WS_HEARTBEAT
}

// APIKey is one TRUTHTABLE_API_KEYS entry.
type APIKey struct {
	Label string
	Key   string
}

// JevConfig is the decision-model configuration (group decision_model).
type JevConfig struct {
	Backend             string // TRUTHTABLE_JEV_BACKEND
	Model               string // TRUTHTABLE_JEV_MODEL
	OpenRouterKey       string // OPENROUTER_API_KEY
	TypeSafeKey         string // TYPESAFE_API_KEY
	APIKey              string // TRUTHTABLE_JEV_API_KEY
	BaseURL             string // TRUTHTABLE_JEV_BASE_URL
	AttemptTimeout      time.Duration
	AsyncDeadline       time.Duration
	TokenBudget         int
	CharsPerToken       float64
	MaxQuestionsPerCall int
	MaxResponseTokens   int
	MockMode            string
	MockStrict          bool
	MockFixtures        string
	MockSeed            int64
	MockLatency         time.Duration
}

// APIKeySecrets returns the secrets of the API keys (for the legacy /api key check).
func (c *Config) APIKeySecrets() []string {
	out := make([]string, 0, len(c.APIKeys))
	for _, k := range c.APIKeys {
		out = append(out, k.Key)
	}
	return out
}

func Load() *Config {
	l := &loader{}
	c := &Config{
		ServerPort:  l.int("TRUTHTABLE_PORT", 8080),
		ReadTimeout: l.duration("TRUTHTABLE_READ_TIMEOUT", 30*time.Second),
		// The write deadline must cover the slowest responses the proxy serves:
		// streamed LLM completions and /api/upload (Gate-1 ingestion runs LLM
		// decomposition + entailment per claim — minutes on a slow local judge;
		// the ingest gRPC call itself is allowed 4x GRPCTimeout). 120s cut those
		// off mid-flight even when the backend succeeded.
		WriteTimeout:    l.duration("TRUTHTABLE_WRITE_TIMEOUT", 8*time.Minute),
		ShutdownTimeout: l.duration("TRUTHTABLE_SHUTDOWN_TIMEOUT", 10*time.Second),
		UpstreamURL:     upstreamURL(),
		GRPCAddress:     getEnv("TRUTHTABLE_GRPC_ADDRESS", "localhost:50051"),
		// Audits run the full LangGraph pipeline (decompose + per-claim verify +
		// score). A slow local judge with several claims can exceed 30s, so the
		// default is generous; a fast cloud judge (Claude Haiku) finishes in ~1-3s.
		GRPCTimeout: l.duration("TRUTHTABLE_GRPC_TIMEOUT", 120*time.Second),
		WorkerCount: l.int("TRUTHTABLE_WORKER_COUNT", 10),
		QueueSize:   l.int("TRUTHTABLE_QUEUE_SIZE", 1000),
		LogLevel:    getEnv("TRUTHTABLE_LOG_LEVEL", "info"),

		APIKeys: l.keyList("TRUTHTABLE_API_KEYS"),
		AllowedOrigins: getEnvList("TRUTHTABLE_ALLOWED_ORIGINS", []string{
			"http://localhost:5173",
			"http://localhost:3000",
		}),
		RedisURL:           getEnv("REDIS_URL", ""),
		DatabaseURL:        getEnv("TRUTHTABLE_DATABASE_URL", ""),
		RateLimitPerMinute: l.int("TRUTHTABLE_RATE_LIMIT_PER_MINUTE", 120),
		UploadLimitPerMin:  l.int("TRUTHTABLE_UPLOAD_LIMIT_PER_MINUTE", 10),
		MaxBodyBytes:       l.int64("TRUTHTABLE_MAX_BODY_BYTES", 1<<20),    // 1 MiB
		MaxUploadBytes:     l.int64("TRUTHTABLE_MAX_UPLOAD_BYTES", 10<<20), // 10 MiB
		MaxTextChars:       l.int("TRUTHTABLE_MAX_TEXT_CHARS", 20000),

		IngestAuth:               l.enum("TRUTHTABLE_INGEST_AUTH", "keys", "keys", "open"),
		TestMode:                 l.bool("TRUTHTABLE_TEST_MODE", false),
		DemoMode:                 l.bool("TRUTHTABLE_DEMO_MODE", false),
		UpstreamHeaderTimeout:    l.duration("TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT", 60*time.Second),
		UpstreamTimeout:          l.duration("TRUTHTABLE_UPSTREAM_TIMEOUT", 5*time.Minute),
		MaxUpstreamResponseBytes: l.int64("TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES", 8<<20), // 8 MiB
		PolicyFile:               RepoPath(getEnv("TRUTHTABLE_POLICY_FILE", "config/policies.yaml")),
		GateDefaultMode:          l.enum("TRUTHTABLE_GATE_DEFAULT_MODE", "observe", "observe", "flag", "block", "fallback"),
		GateFailMode:             l.enum("TRUTHTABLE_GATE_FAIL_MODE", "open", "open", "closed"),
		GateMaxConcurrency:       l.int("TRUTHTABLE_GATE_MAX_CONCURRENCY", 256),
		UniformAuditsPerDay:      l.int("TRUTHTABLE_UNIFORM_AUDITS_PER_DAY", 50),
		Jev: JevConfig{
			Backend: l.enum("TRUTHTABLE_JEV_BACKEND", "auto",
				"auto", "openrouter-decisions", "openrouter-systemone", "typesafe", "compatible", "mock", "off"),
			Model:               getEnv("TRUTHTABLE_JEV_MODEL", ""),
			OpenRouterKey:       getEnv("OPENROUTER_API_KEY", ""),
			TypeSafeKey:         getEnv("TYPESAFE_API_KEY", ""),
			APIKey:              getEnv("TRUTHTABLE_JEV_API_KEY", ""),
			BaseURL:             getEnv("TRUTHTABLE_JEV_BASE_URL", ""),
			AttemptTimeout:      l.duration("TRUTHTABLE_JEV_ATTEMPT_TIMEOUT", 3*time.Second),
			AsyncDeadline:       l.duration("TRUTHTABLE_JEV_ASYNC_DEADLINE", 10*time.Second),
			TokenBudget:         l.int("TRUTHTABLE_JEV_TOKEN_BUDGET", 24000),
			CharsPerToken:       l.float("TRUTHTABLE_JEV_CHARS_PER_TOKEN", 4.0),
			MaxQuestionsPerCall: l.int("TRUTHTABLE_JEV_MAX_QUESTIONS_PER_CALL", 16),
			MaxResponseTokens:   l.int("TRUTHTABLE_JEV_MAX_RESPONSE_TOKENS", 6000),
			MockMode:            l.enum("TRUTHTABLE_JEV_MOCK_MODE", "scenario", "scenario", "hash", "heuristic"),
			MockStrict:          l.bool("TRUTHTABLE_JEV_MOCK_STRICT", false),
			MockFixtures:        RepoPath(getEnv("TRUTHTABLE_JEV_MOCK_FIXTURES", "")),
			MockSeed:            l.int64("TRUTHTABLE_JEV_MOCK_SEED", 42),
			MockLatency:         l.duration("TRUTHTABLE_JEV_MOCK_LATENCY", 40*time.Millisecond),
		},

		ReadHeaderTimeout:      l.duration("TRUTHTABLE_READ_HEADER_TIMEOUT", 10*time.Second),
		MetricsPort:            l.int("TRUTHTABLE_METRICS_PORT", 8002),
		ShutdownDrainTimeout:   l.duration("TRUTHTABLE_SHUTDOWN_DRAIN_TIMEOUT", 20*time.Second),
		PublicRead:             l.bool("TRUTHTABLE_PUBLIC_READ", false),
		Readonly:               l.bool("TRUTHTABLE_READONLY", false),
		InternalToken:          strings.TrimSpace(os.Getenv("TRUTHTABLE_INTERNAL_TOKEN")),
		TraceFlush:             l.enum("TRUTHTABLE_TRACE_FLUSH", "async", "async", "sync"),
		BodyRetention:          l.duration("TRUTHTABLE_BODY_RETENTION", 168*time.Hour),
		BodyMaxBytes:           l.int("TRUTHTABLE_BODY_MAX_BYTES", 262144),
		AuditQueueHigh:         l.int("TRUTHTABLE_AUDIT_QUEUE_HIGH", 200),
		AuditHighPerMin:        l.int("TRUTHTABLE_AUDIT_HIGH_PER_MIN", 30),
		EngineHealthInterval:   l.duration("TRUTHTABLE_ENGINE_HEALTH_INTERVAL", 10*time.Second),
		SpendCapDecisionUSDDay: l.float("TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY", 2.00),
		SpendCapLLMUSDDay:      l.float("TRUTHTABLE_SPEND_CAP_LLM_USD_DAY", 5.00),
		WS: WSConfig{
			MaxClients:     l.int("TRUTHTABLE_WS_MAX_CLIENTS", 200),
			MaxPerIP:       l.int("TRUTHTABLE_WS_MAX_PER_IP", 10),
			ReplayBuffer:   l.int("TRUTHTABLE_WS_REPLAY_BUFFER", 2000),
			ReplayMaxBytes: l.int64("TRUTHTABLE_WS_REPLAY_MAX_BYTES", 64<<20),
			Heartbeat:      l.duration("TRUTHTABLE_WS_HEARTBEAT", 20*time.Second),
		},
	}
	c.Warnings = l.warnings
	return c
}

// loader parses typed values and collects warnings for invalid ones.
type loader struct {
	warnings []string
}

func (l *loader) warn(key, value, want string) {
	l.warnings = append(l.warnings, fmt.Sprintf("%s=%q is not %s; using the default", key, value, want))
}

// parse reads key and converts it with conv; an empty value is the default and a value conv
// rejects is the default plus a warning.
func parse[T any](l *loader, key string, def T, want string, conv func(string) (T, error)) T {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	out, err := conv(v)
	if err != nil {
		l.warn(key, v, want)
		return def
	}
	return out
}

func (l *loader) int(key string, def int) int {
	return parse(l, key, def, "an integer", strconv.Atoi)
}

func (l *loader) int64(key string, def int64) int64 {
	return parse(l, key, def, "an integer", func(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) })
}

func (l *loader) float(key string, def float64) float64 {
	return parse(l, key, def, "a number", func(s string) (float64, error) { return strconv.ParseFloat(s, 64) })
}

func (l *loader) bool(key string, def bool) bool {
	return parse(l, key, def, "true or false", strconv.ParseBool)
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	return parse(l, key, def, "a Go duration such as 30s", time.ParseDuration)
}

func (l *loader) enum(key, def string, allowed ...string) string {
	return parse(l, key, def, "one of "+strings.Join(allowed, ", "), func(s string) (string, error) {
		if !slices.Contains(allowed, s) {
			return "", fmt.Errorf("not allowed")
		}
		return s, nil
	})
}

// keyList parses "label=key,..." (registry kind key_list). An entry without a label is kept as
// key<n> with a warning; an entry missing its label or key is ignored with a warning.
func (l *loader) keyList(key string) []APIKey {
	var out []APIKey
	seen := map[string]bool{}
	for i, item := range strings.Split(os.Getenv(key), ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		label, secret, ok := strings.Cut(item, "=")
		label, secret = strings.TrimSpace(label), strings.TrimSpace(secret)
		if !ok {
			label, secret = fmt.Sprintf("key%d", i+1), item
			l.warnings = append(l.warnings, fmt.Sprintf("%s entry %d has no label; write it as label=key", key, i+1))
		}
		if label == "" || secret == "" {
			l.warnings = append(l.warnings, fmt.Sprintf("%s entry %d needs both a label and a key; ignored", key, i+1))
			continue
		}
		if seen[label] {
			l.warnings = append(l.warnings, fmt.Sprintf("%s: label %q is used twice", key, label))
		}
		seen[label] = true
		out = append(out, APIKey{Label: label, Key: secret})
	}
	return out
}

// upstreamURL reads TRUTHTABLE_UPSTREAM_URL (alias UPSTREAM_LLM_URL) with its shorthands; empty
// means https://api.openai.com.
func upstreamURL() string {
	v := strings.TrimSpace(getEnv("TRUTHTABLE_UPSTREAM_URL", getEnv("UPSTREAM_LLM_URL", "")))
	switch strings.ToLower(v) {
	case "":
		return "https://api.openai.com"
	case "ollama":
		return getEnv("OLLAMA_BASE_URL", "http://localhost:11434")
	case "openrouter":
		return "https://openrouter.ai/api"
	}
	return v
}

// RepoPath resolves a relative path that does not exist from the working directory against the
// repository root (the nearest parent directory holding VERSION), so `make dev-go` run from
// backend-go finds config/policies.yaml. Empty, absolute, existing and unresolvable paths are
// returned unchanged.
func RepoPath(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	dir, err := os.Getwd()
	if err != nil {
		return p
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			if candidate := filepath.Join(dir, p); fileExists(candidate) {
				return candidate
			}
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		dir = parent
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func getEnvList(key string, defaultValue []string) []string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	var items []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
