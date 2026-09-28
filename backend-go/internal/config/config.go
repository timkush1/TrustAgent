// Package config reads the proxy's environment. Key names, defaults and enums follow the registry
// contracts/config/keys.v1.json (SPEC §2.7). Invalid values fall back to the default and are
// reported in Config.Warnings.
//
// The registry-driven loader (dotenv, strict mode, typo hints, masking) replaces this in P2.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultInternalTokenFile is the TRUTHTABLE_INTERNAL_TOKEN_FILE default: the volume the proxy and
// the engine share in compose.
const DefaultInternalTokenFile = "/var/lib/trustagent-internal/internal.token" // #nosec G101 -- a path, not a credential

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
	PublicURL              string        // TRUTHTABLE_PUBLIC_URL: the dashboard's public origin
	Readonly               bool          // TRUTHTABLE_READONLY
	InternalToken          string        // TRUTHTABLE_INTERNAL_TOKEN (metadata x-trustagent-internal)
	InternalTokenFile      string        // TRUTHTABLE_INTERNAL_TOKEN_FILE: the proxy writes it, the engine reads it
	TrustedProxies         []string      // TRUTHTABLE_TRUSTED_PROXIES: CIDRs or IPs whose X-Forwarded-For is trusted
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
	APIKey              string // TRUTHTABLE_JEV_API_KEY, else the contents of TRUTHTABLE_JEV_API_KEY_FILE
	BaseURL             string // TRUTHTABLE_JEV_BASE_URL
	Path                string // TRUTHTABLE_JEV_PATH ("" = the backend's path)
	OpenRouterBaseURL   string // TRUTHTABLE_OPENROUTER_BASE_URL
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

	// Resilience and limits of the HTTP backends (jev.HTTPConfig).
	MaxRetries      int           // TRUTHTABLE_JEV_MAX_RETRIES
	MaxRetriesAsync int           // TRUTHTABLE_JEV_MAX_RETRIES_ASYNC
	RetryAfterCap   time.Duration // TRUTHTABLE_JEV_RETRY_AFTER_CAP
	RPM             int           // TRUTHTABLE_GATE_JEV_RPM
	SyncReserve     float64       // TRUTHTABLE_GATE_JEV_SYNC_RESERVE
	MaxInFlight     int           // TRUTHTABLE_GATE_JEV_MAX_INFLIGHT
	BreakerFailures int           // TRUTHTABLE_JEV_BREAKER_FAILURES
	BreakerCooldown time.Duration // TRUTHTABLE_JEV_BREAKER_COOLDOWN
	AuthCooldown    time.Duration // TRUTHTABLE_JEV_AUTH_COOLDOWN
	PricePerMTok    float64       // TRUTHTABLE_JEV_PRICE_PER_MTOK (0 = the price table)
	AppURL          string        // TRUTHTABLE_JEV_APP_URL
	AppTitle        string        // TRUTHTABLE_JEV_APP_TITLE
	ZDR             bool          // TRUTHTABLE_JEV_ZDR
	SendSessionID   bool          // TRUTHTABLE_JEV_SEND_SESSION_ID
	StartupProbe    bool          // TRUTHTABLE_JEV_STARTUP_PROBE
	KeepAlive       time.Duration // TRUTHTABLE_JEV_KEEPALIVE (0 = no keep-warm)
	LogPayloads     bool          // TRUTHTABLE_JEV_LOG_PAYLOADS
	// SpendEnforcingReserve is TRUTHTABLE_SPEND_ENFORCING_RESERVE and KeyUSDDay
	// TRUTHTABLE_KEY_DECISION_USD_DAY; the daily cap is Config.SpendCapDecisionUSDDay.
	SpendEnforcingReserve float64
	KeyUSDDay             float64
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

			Path:                  strings.TrimSpace(os.Getenv("TRUTHTABLE_JEV_PATH")),
			OpenRouterBaseURL:     getEnv("TRUTHTABLE_OPENROUTER_BASE_URL", "https://openrouter.ai"),
			MaxRetries:            l.intAtLeast("TRUTHTABLE_JEV_MAX_RETRIES", 1, 0),
			MaxRetriesAsync:       l.intAtLeast("TRUTHTABLE_JEV_MAX_RETRIES_ASYNC", 2, 0),
			RetryAfterCap:         l.duration("TRUTHTABLE_JEV_RETRY_AFTER_CAP", 10*time.Second),
			RPM:                   l.intAtLeast("TRUTHTABLE_GATE_JEV_RPM", 1000, 1),
			SyncReserve:           l.fraction("TRUTHTABLE_GATE_JEV_SYNC_RESERVE", 0.3),
			MaxInFlight:           l.intAtLeast("TRUTHTABLE_GATE_JEV_MAX_INFLIGHT", 64, 1),
			BreakerFailures:       l.intAtLeast("TRUTHTABLE_JEV_BREAKER_FAILURES", 5, 1),
			BreakerCooldown:       l.duration("TRUTHTABLE_JEV_BREAKER_COOLDOWN", 30*time.Second),
			AuthCooldown:          l.duration("TRUTHTABLE_JEV_AUTH_COOLDOWN", 5*time.Minute),
			PricePerMTok:          l.nonNegative("TRUTHTABLE_JEV_PRICE_PER_MTOK", 0),
			AppURL:                getEnv("TRUTHTABLE_JEV_APP_URL", "https://github.com/timkush1/TrustAgent"),
			AppTitle:              getEnv("TRUTHTABLE_JEV_APP_TITLE", "TrustAgent"),
			ZDR:                   l.bool("TRUTHTABLE_JEV_ZDR", false),
			SendSessionID:         l.bool("TRUTHTABLE_JEV_SEND_SESSION_ID", true),
			StartupProbe:          l.bool("TRUTHTABLE_JEV_STARTUP_PROBE", true),
			KeepAlive:             l.duration("TRUTHTABLE_JEV_KEEPALIVE", 60*time.Second),
			LogPayloads:           l.bool("TRUTHTABLE_JEV_LOG_PAYLOADS", false),
			SpendEnforcingReserve: l.fraction("TRUTHTABLE_SPEND_ENFORCING_RESERVE", 0.25),
			KeyUSDDay:             l.nonNegative("TRUTHTABLE_KEY_DECISION_USD_DAY", 0),
		},

		ReadHeaderTimeout:      l.duration("TRUTHTABLE_READ_HEADER_TIMEOUT", 10*time.Second),
		MetricsPort:            l.int("TRUTHTABLE_METRICS_PORT", 8002),
		ShutdownDrainTimeout:   l.duration("TRUTHTABLE_SHUTDOWN_DRAIN_TIMEOUT", 20*time.Second),
		PublicRead:             l.bool("TRUTHTABLE_PUBLIC_READ", false),
		PublicURL:              getEnv("TRUTHTABLE_PUBLIC_URL", "http://localhost:3000"),
		Readonly:               l.bool("TRUTHTABLE_READONLY", false),
		InternalToken:          strings.TrimSpace(os.Getenv("TRUTHTABLE_INTERNAL_TOKEN")),
		InternalTokenFile:      strings.TrimSpace(getEnv("TRUTHTABLE_INTERNAL_TOKEN_FILE", DefaultInternalTokenFile)),
		TrustedProxies:         l.networks("TRUTHTABLE_TRUSTED_PROXIES"),
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
	if c.Jev.APIKey == "" {
		c.Jev.APIKey = l.secretFile("TRUTHTABLE_JEV_API_KEY_FILE")
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

// intAtLeast is an integer no smaller than lo (the registry min).
func (l *loader) intAtLeast(key string, def, lo int) int {
	return parse(l, key, def, fmt.Sprintf("an integer of at least %d", lo), func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err == nil && n < lo {
			err = fmt.Errorf("below %d", lo)
		}
		return n, err
	})
}

// fraction is a number from 0 to 1.
func (l *loader) fraction(key string, def float64) float64 {
	return parse(l, key, def, "a number from 0 to 1", func(s string) (float64, error) {
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && (f < 0 || f > 1) {
			err = fmt.Errorf("out of range")
		}
		return f, err
	})
}

// nonNegative is a number of at least 0.
func (l *loader) nonNegative(key string, def float64) float64 {
	return parse(l, key, def, "a number of at least 0", func(s string) (float64, error) {
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && f < 0 {
			err = fmt.Errorf("negative")
		}
		return f, err
	})
}

// secretFile reads a credential from the file named by key (a *_FILE key, for Docker secrets). A
// file that cannot be read is reported without its contents and yields no credential.
func (l *loader) secretFile(key string) string {
	path := strings.TrimSpace(os.Getenv(key))
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the credential file (a registry *_FILE key)
	if err != nil {
		l.warnings = append(l.warnings, fmt.Sprintf("%s: the file %q cannot be read; no credential loaded", key, path))
		return ""
	}
	return strings.TrimSpace(string(b))
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

// networks parses a comma-separated list of CIDRs and IP addresses (TRUTHTABLE_TRUSTED_PROXIES);
// an entry that is neither is ignored with a warning. Empty means none.
func (l *loader) networks(key string) []string {
	var out []string
	for _, item := range getEnvList(key, nil) {
		if _, _, err := net.ParseCIDR(item); err == nil || net.ParseIP(item) != nil {
			out = append(out, item)
			continue
		}
		l.warnings = append(l.warnings, fmt.Sprintf("%s entry %q is not a CIDR or an IP address; ignored", key, item))
	}
	return out
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
