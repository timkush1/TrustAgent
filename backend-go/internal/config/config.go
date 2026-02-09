package config

import (
	"os"
	"strconv"
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
	}
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
