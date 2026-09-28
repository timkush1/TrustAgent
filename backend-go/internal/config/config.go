// Package config reads the proxy's configuration (SPEC §2.7).
//
// contracts/config/keys.v1.json is the single registry: every Config field is bound to a registry
// key by its `env` struct tag, and the key's kind, default, enum, bounds, aliases and shorthands
// come from the registry, never from this package. Loading:
//
//   - Boot precedence: default < .env < process env. The dotenv file is $TRUTHTABLE_ENV_FILE, or
//     (unset or "auto") the .env in the nearest parent directory holding VERSION; set but empty
//     disables it. A dotenv assignment never overrides a variable that is already set.
//   - A canonical name wins over its aliases.
//   - An invalid value falls back to the default and produces a Warning{key, message, hint}; so
//     does an unknown TRUTHTABLE_* key (with a "did you mean …?" hint within Levenshtein distance
//     2) and a misspelled unprefixed proxy key. TRUTHTABLE_STRICT_CONFIG=true turns any warning
//     into exit code 2 (the proxy's boot does that; see StrictFailure).
//   - Secret values (registry secret=true) are never echoed in warnings; Redactor masks them in
//     logs.
package config

import (
	"os"
	"path/filepath"
	"time"
)

// DefaultInternalTokenFile is the TRUTHTABLE_INTERNAL_TOKEN_FILE default: the volume the proxy and
// the engine share in compose.
const DefaultInternalTokenFile = "/var/lib/trustagent-internal/internal.token" // #nosec G101 -- a path, not a credential

// DefaultUpstreamURL is what an empty TRUTHTABLE_UPSTREAM_URL resolves to (registry default_note).
const DefaultUpstreamURL = "https://api.openai.com"

// Config is the proxy configuration. Each tagged field is bound to the registry key its `env` tag
// names; tag options: repopath (a relative path that does not exist from the working directory is
// resolved against the repository root, see RepoPath) and repodir (a relative directory is placed
// under the repository root when there is one).
type Config struct {
	ServerPort      int           `env:"TRUTHTABLE_PORT"`
	ReadTimeout     time.Duration `env:"TRUTHTABLE_READ_TIMEOUT"`
	WriteTimeout    time.Duration `env:"TRUTHTABLE_WRITE_TIMEOUT"` // 0: per-route deadlines (M12)
	ShutdownTimeout time.Duration `env:"TRUTHTABLE_SHUTDOWN_TIMEOUT"`
	UpstreamURL     string        `env:"TRUTHTABLE_UPSTREAM_URL"` // shorthands expanded; "" is DefaultUpstreamURL
	GRPCAddress     string        `env:"TRUTHTABLE_GRPC_ADDRESS"`
	GRPCTimeout     time.Duration `env:"TRUTHTABLE_GRPC_TIMEOUT"`
	WorkerCount     int           `env:"TRUTHTABLE_WORKER_COUNT"`
	QueueSize       int           `env:"TRUTHTABLE_QUEUE_SIZE"`
	LogLevel        string        `env:"TRUTHTABLE_LOG_LEVEL"`

	// Security
	APIKeys            []APIKey `env:"TRUTHTABLE_API_KEYS"`        // ingest + read + content keys
	AllowedOrigins     []string `env:"TRUTHTABLE_ALLOWED_ORIGINS"` // CORS + WebSocket + CSRF origin allowlist
	RedisURL           string   `env:"REDIS_URL"`                  // rate-limiter backend; empty = in-memory
	DatabaseURL        string   `env:"TRUTHTABLE_DATABASE_URL"`    // persistence; empty = disabled
	RateLimitPerMinute int      `env:"TRUTHTABLE_RATE_LIMIT_PER_MINUTE"`
	UploadLimitPerMin  int      `env:"TRUTHTABLE_UPLOAD_LIMIT_PER_MINUTE"`
	MaxBodyBytes       int64    `env:"TRUTHTABLE_MAX_BODY_BYTES"`   // request body cap (non-upload routes)
	MaxUploadBytes     int64    `env:"TRUTHTABLE_MAX_UPLOAD_BYTES"` // request body cap for /api/upload
	MaxTextChars       int      `env:"TRUTHTABLE_MAX_TEXT_CHARS"`

	// Gateway
	IngestAuth               string        `env:"TRUTHTABLE_INGEST_AUTH"` // keys | open (open is refused in inject mode)
	TestMode                 bool          `env:"TRUTHTABLE_TEST_MODE"`   // M7
	DemoMode                 bool          `env:"TRUTHTABLE_DEMO_MODE"`
	UpstreamAPIKey           string        `env:"TRUTHTABLE_UPSTREAM_API_KEY"` // non-empty enables inject mode
	UpstreamHeaderTimeout    time.Duration `env:"TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT"`
	UpstreamTimeout          time.Duration `env:"TRUTHTABLE_UPSTREAM_TIMEOUT"`
	MaxUpstreamResponseBytes int64         `env:"TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES"`
	PolicyFile               string        `env:"TRUTHTABLE_POLICY_FILE,repopath"`
	GateDefaultMode          string        `env:"TRUTHTABLE_GATE_DEFAULT_MODE"` // seed
	GateFailMode             string        `env:"TRUTHTABLE_GATE_FAIL_MODE"`    // seed
	GateMaxConcurrency       int           `env:"TRUTHTABLE_GATE_MAX_CONCURRENCY"`
	UniformAuditsPerDay      int           `env:"TRUTHTABLE_UNIFORM_AUDITS_PER_DAY"`
	Jev                      JevConfig

	// Policy engine: hot reload, the file lock and the custom evaluator files (SPEC §2.7, §7.2).
	PolicyReloadInterval time.Duration `env:"TRUTHTABLE_POLICY_RELOAD_INTERVAL"` // 0 disables polling
	ConfigLock           bool          `env:"TRUTHTABLE_CONFIG_LOCK"`            // the file is the only authority
	EvaluatorsDir        string        `env:"TRUTHTABLE_EVALUATORS_DIR,repopath"`

	// Server, persistence, engine link and WebSocket
	ReadHeaderTimeout      time.Duration `env:"TRUTHTABLE_READ_HEADER_TIMEOUT"`
	MetricsPort            int           `env:"TRUTHTABLE_METRICS_PORT"`
	ShutdownDrainTimeout   time.Duration `env:"TRUTHTABLE_SHUTDOWN_DRAIN_TIMEOUT"` // M6
	PublicRead             bool          `env:"TRUTHTABLE_PUBLIC_READ"`            // /api reads and /ws without a key
	Readonly               bool          `env:"TRUTHTABLE_READONLY"`
	InternalToken          string        `env:"TRUTHTABLE_INTERNAL_TOKEN"`      // metadata x-trustagent-internal
	InternalTokenFile      string        `env:"TRUTHTABLE_INTERNAL_TOKEN_FILE"` // the proxy writes it, the engine reads it
	TrustedProxies         []string      `env:"TRUTHTABLE_TRUSTED_PROXIES"`     // CIDRs or IPs whose X-Forwarded-For is trusted
	TraceFlush             string        `env:"TRUTHTABLE_TRACE_FLUSH"`         // async | sync
	BodyRetention          time.Duration `env:"TRUTHTABLE_BODY_RETENTION"`
	BodyMaxBytes           int           `env:"TRUTHTABLE_BODY_MAX_BYTES"`
	AuditQueueHigh         int           `env:"TRUTHTABLE_AUDIT_QUEUE_HIGH"` // QueueSize is the low lane
	AuditHighPerMin        int           `env:"TRUTHTABLE_AUDIT_HIGH_PER_MIN"`
	EngineHealthInterval   time.Duration `env:"TRUTHTABLE_ENGINE_HEALTH_INTERVAL"` // M4
	SpendCapDecisionUSDDay float64       `env:"TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY"`
	SpendCapLLMUSDDay      float64       `env:"TRUTHTABLE_SPEND_CAP_LLM_USD_DAY"`
	WS                     WSConfig

	// Instance identity and boot behaviour
	PublicURL    string `env:"TRUTHTABLE_PUBLIC_URL"` // PKCE callback, cookie Secure, CSRF origin, setup URL
	DataDir      string `env:"TRUTHTABLE_DATA_DIR,repodir"`
	StrictConfig bool   `env:"TRUTHTABLE_STRICT_CONFIG"`
	Auth         AuthConfig

	// EnvFile is the dotenv file that was read, or "" when none was.
	EnvFile string
	// Diagnostics are the configuration warnings: invalid values that fell back to their
	// defaults, unknown or misspelled keys, and refused combinations.
	Diagnostics []Warning
	// Warnings are Diagnostics rendered as "KEY: message (hint)".
	Warnings []string

	entries  []Entry
	registry *Registry
}

// AuthConfig is the dashboard and API authentication configuration (group auth).
type AuthConfig struct {
	ReadKeys      []APIKey      `env:"TRUTHTABLE_READ_KEYS"`  // read + content (SPEC §2.4)
	AdminKeys     []APIKey      `env:"TRUTHTABLE_ADMIN_KEYS"` // every scope
	SecretKey     string        `env:"TRUTHTABLE_SECRET_KEY"` // base64, 32 bytes
	SecretKeyFile string        `env:"TRUTHTABLE_SECRET_KEY_FILE,repodir"`
	SessionTTL    time.Duration `env:"TRUTHTABLE_SESSION_TTL"`
	SetupTokenTTL time.Duration `env:"TRUTHTABLE_SETUP_TOKEN_TTL"`
	SetupToken    string        `env:"TRUTHTABLE_SETUP_TOKEN"`   // seeds the setup token
	CookieSecure  string        `env:"TRUTHTABLE_COOKIE_SECURE"` // auto | true | false
}

// WSConfig is the WebSocket hub configuration (group websocket).
type WSConfig struct {
	MaxClients     int           `env:"TRUTHTABLE_WS_MAX_CLIENTS"`
	MaxPerIP       int           `env:"TRUTHTABLE_WS_MAX_PER_IP"`
	ReplayBuffer   int           `env:"TRUTHTABLE_WS_REPLAY_BUFFER"`
	ReplayMaxBytes int64         `env:"TRUTHTABLE_WS_REPLAY_MAX_BYTES"`
	Heartbeat      time.Duration `env:"TRUTHTABLE_WS_HEARTBEAT"`
}

// APIKey is one key_list entry (label=key).
type APIKey struct {
	Label string
	Key   string
}

// JevConfig is the decision-model configuration (group decision_model).
type JevConfig struct {
	Backend             string        `env:"TRUTHTABLE_JEV_BACKEND"`
	Model               string        `env:"TRUTHTABLE_JEV_MODEL"`
	OpenRouterKey       string        `env:"OPENROUTER_API_KEY"`
	TypeSafeKey         string        `env:"TYPESAFE_API_KEY"`
	APIKey              string        `env:"TRUTHTABLE_JEV_API_KEY"` // else the contents of TRUTHTABLE_JEV_API_KEY_FILE
	BaseURL             string        `env:"TRUTHTABLE_JEV_BASE_URL"`
	Path                string        `env:"TRUTHTABLE_JEV_PATH"` // "" = the backend's path
	OpenRouterBaseURL   string        `env:"TRUTHTABLE_OPENROUTER_BASE_URL"`
	AttemptTimeout      time.Duration `env:"TRUTHTABLE_JEV_ATTEMPT_TIMEOUT"`
	AsyncDeadline       time.Duration `env:"TRUTHTABLE_JEV_ASYNC_DEADLINE"`
	TokenBudget         int           `env:"TRUTHTABLE_JEV_TOKEN_BUDGET"`
	CharsPerToken       float64       `env:"TRUTHTABLE_JEV_CHARS_PER_TOKEN"`
	MaxQuestionsPerCall int           `env:"TRUTHTABLE_JEV_MAX_QUESTIONS_PER_CALL"`
	MaxResponseTokens   int           `env:"TRUTHTABLE_JEV_MAX_RESPONSE_TOKENS"`
	MockMode            string        `env:"TRUTHTABLE_JEV_MOCK_MODE"`
	MockStrict          bool          `env:"TRUTHTABLE_JEV_MOCK_STRICT"`
	MockFixtures        string        `env:"TRUTHTABLE_JEV_MOCK_FIXTURES,repopath"`
	MockSeed            int64         `env:"TRUTHTABLE_JEV_MOCK_SEED"`
	MockLatency         time.Duration `env:"TRUTHTABLE_JEV_MOCK_LATENCY"`

	// Resilience and limits of the HTTP backends (jev.HTTPConfig).
	MaxRetries      int           `env:"TRUTHTABLE_JEV_MAX_RETRIES"`
	MaxRetriesAsync int           `env:"TRUTHTABLE_JEV_MAX_RETRIES_ASYNC"`
	RetryAfterCap   time.Duration `env:"TRUTHTABLE_JEV_RETRY_AFTER_CAP"`
	RPM             int           `env:"TRUTHTABLE_GATE_JEV_RPM"`
	SyncReserve     float64       `env:"TRUTHTABLE_GATE_JEV_SYNC_RESERVE"`
	MaxInFlight     int           `env:"TRUTHTABLE_GATE_JEV_MAX_INFLIGHT"`
	BreakerFailures int           `env:"TRUTHTABLE_JEV_BREAKER_FAILURES"`
	BreakerCooldown time.Duration `env:"TRUTHTABLE_JEV_BREAKER_COOLDOWN"`
	AuthCooldown    time.Duration `env:"TRUTHTABLE_JEV_AUTH_COOLDOWN"`
	PricePerMTok    float64       `env:"TRUTHTABLE_JEV_PRICE_PER_MTOK"` // 0 = the price table
	AppURL          string        `env:"TRUTHTABLE_JEV_APP_URL"`
	AppTitle        string        `env:"TRUTHTABLE_JEV_APP_TITLE"`
	ZDR             bool          `env:"TRUTHTABLE_JEV_ZDR"`
	SendSessionID   bool          `env:"TRUTHTABLE_JEV_SEND_SESSION_ID"`
	StartupProbe    bool          `env:"TRUTHTABLE_JEV_STARTUP_PROBE"`
	KeepAlive       time.Duration `env:"TRUTHTABLE_JEV_KEEPALIVE"` // 0 = no keep-warm
	LogPayloads     bool          `env:"TRUTHTABLE_JEV_LOG_PAYLOADS"`
	// SpendEnforcingReserve is TRUTHTABLE_SPEND_ENFORCING_RESERVE and KeyUSDDay
	// TRUTHTABLE_KEY_DECISION_USD_DAY; the daily cap is Config.SpendCapDecisionUSDDay.
	SpendEnforcingReserve float64 `env:"TRUTHTABLE_SPEND_ENFORCING_RESERVE"`
	KeyUSDDay             float64 `env:"TRUTHTABLE_KEY_DECISION_USD_DAY"`
}

// APIKeySecrets returns the secrets of the API keys (for the legacy /api key check).
func (c *Config) APIKeySecrets() []string {
	out := make([]string, 0, len(c.APIKeys))
	for _, k := range c.APIKeys {
		out = append(out, k.Key)
	}
	return out
}

// StrictFailure reports whether TRUTHTABLE_STRICT_CONFIG is on and there is at least one warning:
// the proxy then exits with code 2 (StrictExitCode) instead of starting.
func (c *Config) StrictFailure() bool { return c.StrictConfig && len(c.Diagnostics) > 0 }

// StrictExitCode is the exit code of a strict-mode configuration failure (SPEC §2.7).
const StrictExitCode = 2

// Injecting reports whether an upstream credential is injected (inject mode, SPEC §2.12).
func (c *Config) Injecting() bool { return c.UpstreamAPIKey != "" }

// Entries returns every registry key of the proxy with its effective value and where it came
// from, in registry order. Secret values are included: mask them (MaskSecret) before showing them.
func (c *Config) Entries() []Entry { return append([]Entry(nil), c.entries...) }

// Registry returns the registry the configuration was loaded against.
func (c *Config) Registry() *Registry { return c.registry }

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
	root := findRepoRoot(dir)
	if root == "" {
		return p
	}
	if candidate := filepath.Join(root, p); fileExists(candidate) {
		return candidate
	}
	return p
}

// repoDir places a relative directory (or file inside one) under the repository root when the
// working directory is inside the repository, so host runs share one data directory whatever
// directory they start in. Empty, absolute and out-of-repository paths are returned unchanged.
func repoDir(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	dir, err := os.Getwd()
	if err != nil {
		return p
	}
	if root := findRepoRoot(dir); root != "" {
		return filepath.Join(root, p)
	}
	return p
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
