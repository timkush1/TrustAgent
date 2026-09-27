package config

import (
	"os"
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
	APIKeys            []string // empty = auth disabled (dev mode)
	AllowedOrigins     []string // CORS + WebSocket origin allowlist
	RedisURL           string   // rate-limiter backend; empty = in-memory
	DatabaseURL        string   // audit persistence; empty = disabled
	RateLimitPerMinute int      // per client (IP or API key)
	UploadLimitPerMin  int      // stricter limit for /api/upload
	MaxBodyBytes       int64    // request body cap (non-upload routes)
	MaxUploadBytes     int64    // request body cap for /api/upload
	MaxTextChars       int      // max chars for query/response fields
}

func Load() *Config {
	return &Config{
		ServerPort:  getEnvInt("TRUTHTABLE_PORT", 8080),
		ReadTimeout: getEnvDuration("TRUTHTABLE_READ_TIMEOUT", 30*time.Second),
		// The write deadline must cover the slowest responses the proxy serves:
		// streamed LLM completions and /api/upload (Gate-1 ingestion runs LLM
		// decomposition + entailment per claim — minutes on a slow local judge;
		// the ingest gRPC call itself is allowed 4x GRPCTimeout). 120s cut those
		// off mid-flight even when the backend succeeded.
		WriteTimeout:    getEnvDuration("TRUTHTABLE_WRITE_TIMEOUT", 8*time.Minute),
		ShutdownTimeout: getEnvDuration("TRUTHTABLE_SHUTDOWN_TIMEOUT", 10*time.Second),
		UpstreamURL:     getEnv("TRUTHTABLE_UPSTREAM_URL", "https://api.openai.com"),
		GRPCAddress:     getEnv("TRUTHTABLE_GRPC_ADDRESS", "localhost:50051"),
		// Audits run the full LangGraph pipeline (decompose + per-claim verify +
		// score). A slow local judge with several claims can exceed 30s, so the
		// default is generous; a fast cloud judge (Claude Haiku) finishes in ~1-3s.
		GRPCTimeout: getEnvDuration("TRUTHTABLE_GRPC_TIMEOUT", 120*time.Second),
		WorkerCount: getEnvInt("TRUTHTABLE_WORKER_COUNT", 10),
		QueueSize:   getEnvInt("TRUTHTABLE_QUEUE_SIZE", 1000),
		LogLevel:    getEnv("TRUTHTABLE_LOG_LEVEL", "info"),

		APIKeys: getEnvList("TRUTHTABLE_API_KEYS", nil),
		AllowedOrigins: getEnvList("TRUTHTABLE_ALLOWED_ORIGINS", []string{
			"http://localhost:5173",
			"http://localhost:3000",
		}),
		RedisURL:           getEnv("REDIS_URL", ""),
		DatabaseURL:        getEnv("TRUTHTABLE_DATABASE_URL", ""),
		RateLimitPerMinute: getEnvInt("TRUTHTABLE_RATE_LIMIT_PER_MINUTE", 120),
		UploadLimitPerMin:  getEnvInt("TRUTHTABLE_UPLOAD_LIMIT_PER_MINUTE", 10),
		MaxBodyBytes:       int64(getEnvInt("TRUTHTABLE_MAX_BODY_BYTES", 1<<20)),    // 1 MiB
		MaxUploadBytes:     int64(getEnvInt("TRUTHTABLE_MAX_UPLOAD_BYTES", 10<<20)), // 10 MiB
		MaxTextChars:       getEnvInt("TRUTHTABLE_MAX_TEXT_CHARS", 20000),
	}
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

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}
