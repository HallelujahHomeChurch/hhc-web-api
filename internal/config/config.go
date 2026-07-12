package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	Port                string
	DatabaseURL         string
	RedisURL            string
	Environment         string
	ShutdownTimeout     time.Duration
	AssetAPIBaseURL     string
	InternalCallerAppID string
	PublicBaseURL       string
}

func Load() (Config, error) {
	cfg := Config{
		Port: value("PORT", "8082"), DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")), RedisURL: strings.TrimSpace(os.Getenv("REDIS_URL")),
		Environment: value("ENVIRONMENT", "development"), ShutdownTimeout: 10 * time.Second, AssetAPIBaseURL: value("ASSET_API_BASE_URL", "http://127.0.0.1:8083"),
		InternalCallerAppID: value("INTERNAL_CALLER_APP_ID", "hhc-web-api"), PublicBaseURL: value("PUBLIC_BASE_URL", "http://127.0.0.1:8082/api"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if raw := strings.TrimSpace(os.Getenv("SHUTDOWN_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("invalid SHUTDOWN_TIMEOUT")
		}
		cfg.ShutdownTimeout = parsed
	}
	return cfg, nil
}
func value(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}
