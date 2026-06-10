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
	WSPort          int
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
		ServerPort:      getEnvInt("TRUTHTABLE_PORT", 8080),
		ReadTimeout:     getEnvDuration("TRUTHTABLE_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:    getEnvDuration("TRUTHTABLE_WRITE_TIMEOUT", 120*time.Second),
		ShutdownTimeout: getEnvDuration("TRUTHTABLE_SHUTDOWN_TIMEOUT", 10*time.Second),
		UpstreamURL:     getEnv("TRUTHTABLE_UPSTREAM_URL", "https://api.openai.com"),
		GRPCAddress:     getEnv("TRUTHTABLE_GRPC_ADDRESS", "localhost:50051"),
		GRPCTimeout:     getEnvDuration("TRUTHTABLE_GRPC_TIMEOUT", 30*time.Second),
		WSPort:          getEnvInt("TRUTHTABLE_WS_PORT", 8081),
		WorkerCount:     getEnvInt("TRUTHTABLE_WORKER_COUNT", 10),
		QueueSize:       getEnvInt("TRUTHTABLE_QUEUE_SIZE", 1000),
		LogLevel:        getEnv("TRUTHTABLE_LOG_LEVEL", "info"),

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
