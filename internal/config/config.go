package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const translationGatewayDeadline = 60 * time.Second

type TranslationConfig struct {
	Enabled              bool
	AzureEndpoint        string
	AzureDeployment      string
	AzureAPIKey          string
	AzureRAIPolicy       string
	ProviderTimeout      time.Duration
	HandlerTimeout       time.Duration
	WriteDeadline        time.Duration
	SourceCharLimit      int
	ActorLimit           int
	DeploymentLimit      int
	ActorDailyLimit      int
	DeploymentDailyLimit int
	Cooldown             time.Duration
}

type Config struct {
	Port                                                   string
	DatabaseURL                                            string
	DBMaxOpenConns                                         int
	DBMaxIdleConns                                         int
	DBConnMaxLifetime                                      time.Duration
	Environment                                            string
	ShutdownTimeout                                        time.Duration
	AssetAPIBaseURL                                        string
	EngagementAPIBaseURL                                   string
	InternalCallerAppID                                    string
	AdminAllowedCaller                                     string
	OperationsAllowedCallerAppIDs                          []string
	DaprAPIToken                                           string
	AllowDevCaller                                         bool
	EnableFiveLocaleBulletinNotificationsAfterFluentReview bool
	PublicBaseURL                                          string
	OutboxMaxAttempts                                      int
	Translation                                            TranslationConfig
}

func Load() (Config, error) {
	cfg := Config{
		Port: value("PORT", "8082"), DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBMaxOpenConns: 10, DBMaxIdleConns: 5, DBConnMaxLifetime: 30 * time.Minute,
		Environment: value("ENVIRONMENT", "development"), ShutdownTimeout: 10 * time.Second, AssetAPIBaseURL: value("ASSET_API_BASE_URL", "http://127.0.0.1:8083"),
		EngagementAPIBaseURL: value("ENGAGEMENT_API_BASE_URL", "http://127.0.0.1:3500/v1.0/invoke/engagement-api/method"),
		InternalCallerAppID:  value("INTERNAL_CALLER_APP_ID", "hhc-web-api"), AdminAllowedCaller: value("ADMIN_ALLOWED_CALLER_APP_ID", "api-gateway"),
		OperationsAllowedCallerAppIDs: commaSeparated("OPERATIONS_ALLOWED_CALLER_APP_IDS", "asset-api,hhc-line-function-bot"),
		DaprAPIToken:                  strings.TrimSpace(os.Getenv("APP_API_TOKEN")),
		AllowDevCaller:                strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_DEV_CALLER_HEADER")), "true"),
		EnableFiveLocaleBulletinNotificationsAfterFluentReview: strings.EqualFold(strings.TrimSpace(os.Getenv("ENABLE_FIVE_LOCALE_BULLETIN_NOTIFICATIONS_AFTER_FLUENT_REVIEW")), "true"),
		PublicBaseURL:     value("PUBLIC_BASE_URL", "http://127.0.0.1:8082/assets"),
		OutboxMaxAttempts: 20,
		Translation: TranslationConfig{
			Enabled:              strings.EqualFold(strings.TrimSpace(os.Getenv("CMS_TRANSLATION_ENABLED")), "true"),
			AzureEndpoint:        strings.TrimSpace(os.Getenv("AZURE_OPENAI_ENDPOINT")),
			AzureDeployment:      strings.TrimSpace(os.Getenv("AZURE_OPENAI_DEPLOYMENT")),
			AzureAPIKey:          strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_KEY")),
			AzureRAIPolicy:       strings.TrimSpace(os.Getenv("AZURE_OPENAI_RAI_POLICY")),
			ProviderTimeout:      40 * time.Second,
			HandlerTimeout:       45 * time.Second,
			WriteDeadline:        50 * time.Second,
			SourceCharLimit:      20000,
			ActorLimit:           10,
			DeploymentLimit:      60,
			ActorDailyLimit:      30,
			DeploymentDailyLimit: 300,
			Cooldown:             10 * time.Minute,
		},
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if strings.EqualFold(cfg.Environment, "production") && cfg.DaprAPIToken == "" {
		return Config{}, fmt.Errorf("APP_API_TOKEN is required in production")
	}
	if err := cfg.Translation.validate(); err != nil {
		return Config{}, err
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

func (cfg TranslationConfig) validate() error {
	if cfg.ProviderTimeout <= 0 || cfg.HandlerTimeout <= cfg.ProviderTimeout || cfg.WriteDeadline <= cfg.HandlerTimeout || cfg.WriteDeadline >= translationGatewayDeadline {
		return fmt.Errorf("invalid translation timeout ordering")
	}
	if cfg.SourceCharLimit <= 0 || cfg.ActorLimit <= 0 || cfg.DeploymentLimit <= 0 || cfg.ActorDailyLimit <= 0 || cfg.DeploymentDailyLimit <= 0 || cfg.Cooldown <= 0 {
		return fmt.Errorf("invalid translation limits")
	}
	if !cfg.Enabled {
		return nil
	}
	endpoint, err := url.Parse(cfg.AzureEndpoint)
	if err != nil || !strings.EqualFold(endpoint.Scheme, "https") || endpoint.Host == "" {
		return fmt.Errorf("AZURE_OPENAI_ENDPOINT must be an HTTPS URL when CMS translation is enabled")
	}
	if cfg.AzureDeployment == "" {
		return fmt.Errorf("AZURE_OPENAI_DEPLOYMENT is required when CMS translation is enabled")
	}
	if cfg.AzureAPIKey == "" {
		return fmt.Errorf("AZURE_OPENAI_API_KEY is required when CMS translation is enabled")
	}
	if cfg.AzureRAIPolicy == "" {
		return fmt.Errorf("AZURE_OPENAI_RAI_POLICY is required when CMS translation is enabled")
	}
	return nil
}

func value(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}

func commaSeparated(key, fallback string) []string {
	seen := map[string]bool{}
	values := make([]string, 0)
	for _, item := range strings.Split(value(key, fallback), ",") {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			values = append(values, item)
		}
	}
	return values
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
