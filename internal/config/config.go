package config

import (
	"encoding/hex"
	"encoding/json"
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
	GatewayLocationsToken                                  string
	PrivateServiceTokens                                   map[string]string
	GatewayPrivateToken                                    string
	GatewayLivePrivateToken                                string
	AccountPrivateToken                                    string
	EngagementPrivateToken                                 string
	OperationsPrivateToken                                 string
	AuditPrivateHTTPS                                      bool
	AssetPrivateHTTPS                                      bool
	InboxEnabled                                           bool
	NotificationAppID                                      string
	InboxPublicationsAfter                                 time.Time
	StreamKeyEncryptionID                                  string
	StreamKeyEncryptionKeys                                map[string][]byte
	VideoInteractionsEnabled                               bool
	VideoAnalyticsEnabled                                  bool
	BroadcastWritersEnabled                                bool
	StudioWritersEnabled                                   bool
	MemberLegalEnforcementEnabled                          bool
	Port                                                   string
	DatabaseURL                                            string
	DBMaxOpenConns                                         int
	DBMaxIdleConns                                         int
	DBConnMaxLifetime                                      time.Duration
	Environment                                            string
	ShutdownTimeout                                        time.Duration
	AssetAPIBaseURL                                        string
	EngagementAPIBaseURL                                   string
	AccountAPIBaseURL                                      string
	OperationsAPIBaseURL                                   string
	MediaBaseURL                                           string
	MediaWatermarkKeyHex                                   string
	RecordingWatermarkTraceEnabled                         bool
	InternalCallerAppID                                    string
	AdminAllowedCaller                                     string
	DaprAPIToken                                           string
	DaprEnabled                                            bool
	AllowDevCaller                                         bool
	EnableFiveLocaleBulletinNotificationsAfterFluentReview bool
	PublicBaseURL                                          string
	OutboxMaxAttempts                                      int
	BulletinAutoExtractionEnabled                          bool
	BulletinLLMExtractionEnabled                           bool
	BulletinExtractionQueueURL                             string
	BulletinExtractionPoisonQueueURL                       string
	BulletinWatermarkQueueURL                              string
	BulletinWatermarkPoisonQueueURL                        string
	AuditDispatchEnabled                                   bool
	DiagnosticsEnabled                                     bool
	AuditAppID                                             string
	AuditToken                                             string
	DaprHTTPPort                                           int
	Translation                                            TranslationConfig
}

func Load() (Config, error) {
	cfg, err := load(false)
	if err != nil {
		return Config{}, err
	}
	token := os.Getenv("CMS_GATEWAY_LOCATIONS_TOKEN")
	if token != "" {
		if _, err := hex.DecodeString(token); err != nil || len(token) != 64 || token != strings.ToLower(token) {
			return Config{}, fmt.Errorf("CMS_GATEWAY_LOCATIONS_TOKEN must contain 64 lowercase hex characters")
		}
	}
	cfg.GatewayLocationsToken = token
	cfg.PrivateServiceTokens = make(map[string]string, 4)
	seen := map[string]bool{token: true}
	for _, scope := range []struct{ caller, variable string }{
		{"account-api", "CMS_ACCOUNT_PRIVATE_TOKEN"},
		{"operations-api", "CMS_OPERATIONS_PRIVATE_TOKEN"},
		{"engagement-api", "CMS_ENGAGEMENT_PRIVATE_TOKEN"},
		{"hhc-line-function-bot", "CMS_LINE_PRIVATE_TOKEN"},
	} {
		credential := os.Getenv(scope.variable)
		if credential == "" {
			continue
		}
		if _, err := hex.DecodeString(credential); err != nil || len(credential) != 64 || credential != strings.ToLower(credential) || seen[credential] {
			return Config{}, fmt.Errorf("%s must contain a distinct 64-character lowercase hex credential", scope.variable)
		}
		cfg.PrivateServiceTokens[scope.caller] = credential
		seen[credential] = true
	}
	for _, binding := range []struct {
		variable string
		dst      *string
	}{
		{"CMS_GATEWAY_PRIVATE_TOKEN", &cfg.GatewayPrivateToken},
		{"CMS_GATEWAY_LIVE_PRIVATE_TOKEN", &cfg.GatewayLivePrivateToken},
		{"ACCOUNT_CMS_PRIVATE_TOKEN", &cfg.AccountPrivateToken},
		{"ENGAGEMENT_CMS_PRIVATE_TOKEN", &cfg.EngagementPrivateToken},
		{"OPERATIONS_CMS_PRIVATE_TOKEN", &cfg.OperationsPrivateToken},
	} {
		credential := os.Getenv(binding.variable)
		if credential == "" {
			continue
		}
		if _, err := hex.DecodeString(credential); err != nil || len(credential) != 64 || credential != strings.ToLower(credential) || seen[credential] {
			return Config{}, fmt.Errorf("%s must contain a distinct 64-character lowercase hex credential", binding.variable)
		}
		*binding.dst = credential
		seen[credential] = true
	}
	for _, setting := range []struct {
		name string
		dst  *bool
	}{
		{"ASSET_PRIVATE_HTTPS", &cfg.AssetPrivateHTTPS},
	} {
		switch value(setting.name, "false") {
		case "true":
			*setting.dst = true
		case "false":
		default:
			return Config{}, fmt.Errorf("%s must be true or false", setting.name)
		}
	}
	if !cfg.DaprEnabled && (cfg.AllowDevCaller || len(cfg.PrivateServiceTokens) != 4 ||
		cfg.GatewayLocationsToken == "" || cfg.GatewayPrivateToken == "" || cfg.GatewayLivePrivateToken == "" ||
		cfg.AccountPrivateToken == "" || cfg.EngagementPrivateToken == "" || cfg.OperationsPrivateToken == "" ||
		!cfg.AuditDispatchEnabled || !cfg.AuditPrivateHTTPS || !cfg.AssetPrivateHTTPS) {
		return Config{}, fmt.Errorf("CMS without Dapr requires all private transports")
	}
	return cfg, nil
}

// LoadWorker loads the configuration for the bulletin watermark worker. The
// worker serves no inbound HTTP, so it does not require the Dapr app token
// (a Container Apps Job has no Dapr sidecar to inject it), and its connection
// pool defaults to 2 open / 1 idle unless DB_MAX_OPEN_CONNS/DB_MAX_IDLE_CONNS
// are set.
func LoadWorker() (Config, error) {
	return load(true)
}

func load(worker bool) (Config, error) {
	cfg := Config{
		VideoInteractionsEnabled:      strings.EqualFold(strings.TrimSpace(os.Getenv("VIDEO_INTERACTIONS_ENABLED")), "true"),
		VideoAnalyticsEnabled:         strings.EqualFold(strings.TrimSpace(os.Getenv("VIDEO_ANALYTICS_ENABLED")), "true"),
		StudioWritersEnabled:          strings.EqualFold(strings.TrimSpace(os.Getenv("STUDIO_WRITERS_ENABLED")), "true"),
		BroadcastWritersEnabled:       strings.EqualFold(strings.TrimSpace(os.Getenv("BROADCAST_WRITERS_ENABLED")), "true"),
		MemberLegalEnforcementEnabled: strings.EqualFold(strings.TrimSpace(os.Getenv("MEMBER_LEGAL_ENFORCEMENT_ENABLED")), "true"),
		Port:                          value("PORT", "8082"), DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DiagnosticsEnabled: true,
		DBMaxOpenConns:     10, DBMaxIdleConns: 5, DBConnMaxLifetime: 30 * time.Minute,
		Environment: value("ENVIRONMENT", "development"), ShutdownTimeout: 10 * time.Second, AssetAPIBaseURL: value("ASSET_API_BASE_URL", "http://127.0.0.1:8083"),
		EngagementAPIBaseURL:           value("ENGAGEMENT_API_BASE_URL", "http://127.0.0.1:3500/v1.0/invoke/engagement-api/method"),
		AccountAPIBaseURL:              value("ACCOUNT_API_BASE_URL", "http://127.0.0.1:3500/v1.0/invoke/account-api/method"),
		OperationsAPIBaseURL:           value("OPERATIONS_API_BASE_URL", "http://127.0.0.1:3500/v1.0/invoke/operations-api/method"),
		MediaBaseURL:                   strings.TrimSpace(os.Getenv("VIDEO_MEDIA_BASE_URL")),
		MediaWatermarkKeyHex:           strings.TrimSpace(os.Getenv("VIDEO_WATERMARK_KEY_HEX")),
		RecordingWatermarkTraceEnabled: strings.EqualFold(strings.TrimSpace(os.Getenv("RECORDING_WATERMARK_TRACE_ENABLED")), "true"),
		NotificationAppID:              value("NOTIFICATION_APP_ID", "notification-api"),
		InternalCallerAppID:            value("INTERNAL_CALLER_APP_ID", "hhc-web-api"), AdminAllowedCaller: value("ADMIN_ALLOWED_CALLER_APP_ID", "api-gateway"),
		DaprAPIToken:   strings.TrimSpace(os.Getenv("APP_API_TOKEN")),
		AllowDevCaller: strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_DEV_CALLER_HEADER")), "true"),
		EnableFiveLocaleBulletinNotificationsAfterFluentReview: strings.EqualFold(strings.TrimSpace(os.Getenv("ENABLE_FIVE_LOCALE_BULLETIN_NOTIFICATIONS_AFTER_FLUENT_REVIEW")), "true"),
		PublicBaseURL:                    value("PUBLIC_BASE_URL", "http://127.0.0.1:8082/assets"),
		OutboxMaxAttempts:                20,
		BulletinExtractionQueueURL:       strings.TrimSpace(os.Getenv("BULLETIN_EXTRACTION_QUEUE_URL")),
		BulletinExtractionPoisonQueueURL: strings.TrimSpace(os.Getenv("BULLETIN_EXTRACTION_POISON_QUEUE_URL")),
		BulletinWatermarkQueueURL:        strings.TrimSpace(os.Getenv("BULLETIN_WATERMARK_QUEUE_URL")),
		BulletinWatermarkPoisonQueueURL:  strings.TrimSpace(os.Getenv("BULLETIN_WATERMARK_POISON_QUEUE_URL")),
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

	if raw := strings.TrimSpace(os.Getenv("WEBSITE_INBOX_ENABLED")); raw != "" {
		enabled, e := strconv.ParseBool(raw)
		if e != nil {
			return Config{}, fmt.Errorf("invalid WEBSITE_INBOX_ENABLED")
		}
		cfg.InboxEnabled = enabled
	}
	if cfg.InboxEnabled {
		if (cfg.NotificationAppID != "notification-api" && cfg.NotificationAppID != "notification-api-test") || (cfg.InternalCallerAppID != "hhc-web-api" && cfg.InternalCallerAppID != "hhc-web-api-test") || (cfg.NotificationAppID == "notification-api-test") != (cfg.InternalCallerAppID == "hhc-web-api-test") {
			return Config{}, fmt.Errorf("invalid same-environment inbox Notification identity")
		}
		after, e := time.Parse(time.RFC3339, strings.TrimSpace(os.Getenv("WEBSITE_INBOX_PUBLICATIONS_AFTER")))
		if e != nil || after.IsZero() {
			return Config{}, fmt.Errorf("WEBSITE_INBOX_PUBLICATIONS_AFTER is required when inbox is enabled")
		}
		cfg.InboxPublicationsAfter = after.UTC()
	}
	if worker {
		cfg.DBMaxOpenConns, cfg.DBMaxIdleConns = 2, 1
	}
	cfg.StreamKeyEncryptionID = strings.TrimSpace(os.Getenv("STREAM_KEY_ENCRYPTION_ID"))
	rawKeys := strings.TrimSpace(os.Getenv("STREAM_KEY_ENCRYPTION_KEYS"))
	if cfg.StreamKeyEncryptionID != "" || rawKeys != "" {
		var encoded map[string]string
		if json.Unmarshal([]byte(rawKeys), &encoded) != nil {
			return Config{}, fmt.Errorf("invalid stream key encryption keyring")
		}
		cfg.StreamKeyEncryptionKeys = map[string][]byte{}
		for id, raw := range encoded {
			key, e := hex.DecodeString(raw)
			if id == "" || e != nil || len(key) != 32 {
				return Config{}, fmt.Errorf("invalid stream key encryption keyring")
			}
			cfg.StreamKeyEncryptionKeys[id] = key
		}
		if len(cfg.StreamKeyEncryptionKeys[cfg.StreamKeyEncryptionID]) != 32 {
			return Config{}, fmt.Errorf("active stream key encryption key is missing")
		}
	}
	if cfg.StudioWritersEnabled && strings.EqualFold(cfg.Environment, "production") && cfg.StreamKeyEncryptionID == "" {
		return Config{}, fmt.Errorf("stream key encryption is required for production Studio")
	}
	if raw := strings.TrimSpace(os.Getenv("BULLETIN_AUTO_EXTRACTION_ENABLED")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid BULLETIN_AUTO_EXTRACTION_ENABLED")
		}
		cfg.BulletinAutoExtractionEnabled = enabled
	}
	if raw := strings.TrimSpace(os.Getenv("BULLETIN_LLM_EXTRACTION_ENABLED")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid BULLETIN_LLM_EXTRACTION_ENABLED")
		}
		cfg.BulletinLLMExtractionEnabled = enabled
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if (cfg.MediaBaseURL == "") != (cfg.MediaWatermarkKeyHex == "") {
		return Config{}, fmt.Errorf("video media configuration is incomplete")
	}
	if cfg.MediaBaseURL != "" {
		parsed, err := url.Parse(cfg.MediaBaseURL)
		key, keyErr := hex.DecodeString(cfg.MediaWatermarkKeyHex)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || keyErr != nil || len(key) < 32 {
			return Config{}, fmt.Errorf("invalid video media configuration")
		}
	}
	cfg.DaprEnabled = true
	if !worker {
		for _, setting := range []struct {
			name, fallback string
			dst            *bool
		}{
			{"CMS_DAPR_ENABLED", "true", &cfg.DaprEnabled},
			{"AUDIT_PRIVATE_HTTPS", "false", &cfg.AuditPrivateHTTPS},
		} {
			switch value(setting.name, setting.fallback) {
			case "true":
				*setting.dst = true
			case "false":
				*setting.dst = false
			default:
				return Config{}, fmt.Errorf("%s must be true or false", setting.name)
			}
		}
		if cfg.DaprEnabled && strings.EqualFold(cfg.Environment, "production") && cfg.DaprAPIToken == "" {
			return Config{}, fmt.Errorf("APP_API_TOKEN is required in production with Dapr")
		}
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
	if raw := strings.TrimSpace(os.Getenv("CMS_DIAGNOSTICS_ENABLED")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid CMS_DIAGNOSTICS_ENABLED")
		}
		cfg.DiagnosticsEnabled = enabled
	}
	if raw := strings.TrimSpace(os.Getenv("AUDIT_DISPATCH_ENABLED")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid AUDIT_DISPATCH_ENABLED")
		}
		cfg.AuditDispatchEnabled = enabled
	}
	if raw := strings.TrimSpace(os.Getenv("DAPR_HTTP_PORT")); raw != "" {
		cfg.DaprHTTPPort, err = strconv.Atoi(raw)
		if err != nil || cfg.DaprHTTPPort < 1 || cfg.DaprHTTPPort > 65535 {
			return Config{}, fmt.Errorf("invalid DAPR_HTTP_PORT")
		}
	}
	if cfg.AuditDispatchEnabled {
		cfg.AuditAppID = strings.TrimSpace(os.Getenv("AUDIT_APP_ID"))
		cfg.AuditToken = strings.TrimSpace(os.Getenv("AUDIT_TOKEN"))
		if !cfg.AuditPrivateHTTPS && cfg.DaprHTTPPort == 0 || cfg.AuditAppID != "audit-log" && cfg.AuditAppID != "audit-log-test" || cfg.AuditToken == "" || strings.ContainsAny(cfg.AuditToken, "\r\n") {
			return Config{}, fmt.Errorf("invalid audit dispatch configuration")
		}
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
