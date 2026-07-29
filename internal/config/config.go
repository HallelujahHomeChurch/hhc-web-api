package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                string
	DatabaseURL         string
	DBMaxOpenConns      int
	DBMaxIdleConns      int
	DBConnMaxLifetime   time.Duration
	Environment         string
	ShutdownTimeout     time.Duration
	AssetAPIBaseURL     string
	InternalCallerAppID string
	AdminAllowedCaller  string
	DaprAPIToken        string
	AllowDevCaller      bool
	PublicBaseURL       string
	OutboxMaxAttempts   int
}

func Load() (Config, error) {
	cfg := Config{
		Port: value("PORT", "8082"), DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBMaxOpenConns: 10, DBMaxIdleConns: 5, DBConnMaxLifetime: 30 * time.Minute,
		Environment: value("ENVIRONMENT", "development"), ShutdownTimeout: 10 * time.Second, AssetAPIBaseURL: value("ASSET_API_BASE_URL", "http://127.0.0.1:8083"),
		InternalCallerAppID: value("INTERNAL_CALLER_APP_ID", "hhc-web-api"), AdminAllowedCaller: value("ADMIN_ALLOWED_CALLER_APP_ID", "api-gateway"),
		DaprAPIToken:      strings.TrimSpace(os.Getenv("APP_API_TOKEN")),
		AllowDevCaller:    strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_DEV_CALLER_HEADER")), "true"),
		PublicBaseURL:     value("PUBLIC_BASE_URL", "http://127.0.0.1:8082/api"),
		OutboxMaxAttempts: 20,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if strings.EqualFold(cfg.Environment, "production") && cfg.DaprAPIToken == "" {
		return Config{}, fmt.Errorf("APP_API_TOKEN is required in production")
	}
	var err error
	if cfg.DBMaxOpenConns, err = positiveInt("DB_MAX_OPEN_CONNS", cfg.DBMaxOpenConns); err != nil {
		return Config{}, err
	}
	if cfg.DBMaxIdleConns, err = positiveInt("DB_MAX_IDLE_CONNS", cfg.DBMaxIdleConns); err != nil {
		return Config{}, err
	}
	if cfg.DBMaxIdleConns > cfg.DBMaxOpenConns {
		return Config{}, fmt.Errorf("DB_MAX_IDLE_CONNS cannot exceed DB_MAX_OPEN_CONNS")
	}
	if cfg.DBConnMaxLifetime, err = positiveDuration("DB_CONN_MAX_LIFETIME", cfg.DBConnMaxLifetime); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("SHUTDOWN_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("invalid SHUTDOWN_TIMEOUT")
		}
		cfg.ShutdownTimeout = parsed
	}
	if raw := strings.TrimSpace(os.Getenv("OUTBOX_MAX_ATTEMPTS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("invalid OUTBOX_MAX_ATTEMPTS")
		}
		cfg.OutboxMaxAttempts = parsed
	}
	return cfg, nil
}
func value(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}

func positiveInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return parsed, nil
}

func positiveDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return parsed, nil
}
