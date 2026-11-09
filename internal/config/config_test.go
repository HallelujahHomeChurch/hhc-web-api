package config

import (
	"strings"
	"testing"
	"time"
)

func TestRecordingTraceRequiresExplicitEnablement(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	for _, value := range []string{"", "false", "invalid", "true"} {
		t.Setenv("RECORDING_WATERMARK_TRACE_ENABLED", value)
		cfg, err := Load()
		if err != nil || cfg.RecordingWatermarkTraceEnabled != (value == "true") {
			t.Fatalf("trace enablement %q: %v", value, err)
		}
	}
}

func TestProductionRequiresDaprAPIToken(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("APP_API_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected production config to require APP_API_TOKEN")
	}
}

func TestWorkerConfigNeedsNoDaprTokenAndDefaultsToSmallPool(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("APP_API_TOKEN", "")
	t.Setenv("DB_MAX_OPEN_CONNS", "")
	t.Setenv("DB_MAX_IDLE_CONNS", "")
	cfg, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxOpenConns != 2 || cfg.DBMaxIdleConns != 1 {
		t.Fatalf("unexpected worker pool defaults: %d/%d", cfg.DBMaxOpenConns, cfg.DBMaxIdleConns)
	}
	t.Setenv("DB_MAX_OPEN_CONNS", "1")
	t.Setenv("DB_MAX_IDLE_CONNS", "1")
	if cfg, err = LoadWorker(); err != nil || cfg.DBMaxOpenConns != 1 || cfg.DBMaxIdleConns != 1 {
		t.Fatalf("worker pool override: %d/%d %v", cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, err)
	}
	t.Setenv("DB_MAX_IDLE_CONNS", "2")
	if _, err = LoadWorker(); err == nil {
		t.Fatal("worker idle pool above open pool was accepted")
	}
}

func TestDatabasePoolConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("DB_MAX_OPEN_CONNS", "20")
	t.Setenv("DB_MAX_IDLE_CONNS", "10")
	t.Setenv("DB_CONN_MAX_LIFETIME", "20m")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxOpenConns != 20 || cfg.DBMaxIdleConns != 10 || cfg.DBConnMaxLifetime.String() != "20m0s" {
		t.Fatalf("unexpected pool config: %#v", cfg)
	}
}

func TestEngagementUsesDaprInvocationByDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ENGAGEMENT_API_BASE_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EngagementAPIBaseURL != "http://127.0.0.1:3500/v1.0/invoke/engagement-api/method" {
		t.Fatalf("engagement base URL=%q", cfg.EngagementAPIBaseURL)
	}
}

func TestOperationsUsesDaprInvocationByDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("OPERATIONS_API_BASE_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OperationsAPIBaseURL != "http://127.0.0.1:3500/v1.0/invoke/operations-api/method" {
		t.Fatalf("operations base URL=%q", cfg.OperationsAPIBaseURL)
	}
}

func TestFiveLocaleBulletinNotificationsRequireExplicitFluentReviewEnablement(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ENABLE_FIVE_LOCALE_BULLETIN_NOTIFICATIONS_AFTER_FLUENT_REVIEW", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableFiveLocaleBulletinNotificationsAfterFluentReview {
		t.Fatal("five-locale bulletin notifications must default off")
	}

	t.Setenv("ENABLE_FIVE_LOCALE_BULLETIN_NOTIFICATIONS_AFTER_FLUENT_REVIEW", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableFiveLocaleBulletinNotificationsAfterFluentReview {
		t.Fatal("explicit fluent-review enablement was ignored")
	}
}

func TestTranslationConfigurationDefaultsDisabledWithoutAzureValues(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("CMS_TRANSLATION_ENABLED", "")
	t.Setenv("AZURE_OPENAI_ENDPOINT", "")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT", "")
	t.Setenv("AZURE_OPENAI_API_KEY", "")
	t.Setenv("AZURE_OPENAI_RAI_POLICY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := TranslationConfig{
		ProviderTimeout:      40 * time.Second,
		HandlerTimeout:       45 * time.Second,
		WriteDeadline:        50 * time.Second,
		SourceCharLimit:      20000,
		ActorLimit:           10,
		DeploymentLimit:      60,
		ActorDailyLimit:      30,
		DeploymentDailyLimit: 300,
		Cooldown:             10 * time.Minute,
	}
	if cfg.Translation != want {
		t.Fatalf("translation config = %#v, want %#v", cfg.Translation, want)
	}
}

func TestTranslationEnabledRequiresAzureConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		deployment string
		key        string
		policy     string
		wantErr    bool
	}{
		{name: "valid", endpoint: "https://example.openai.azure.com", deployment: "cms-translator", key: "test-key", policy: "hhc-cms-translation-v1"},
		{name: "missing endpoint", deployment: "cms-translator", key: "test-key", policy: "hhc-cms-translation-v1", wantErr: true},
		{name: "non-HTTPS endpoint", endpoint: "http://example.openai.azure.com", deployment: "cms-translator", key: "test-key", policy: "hhc-cms-translation-v1", wantErr: true},
		{name: "missing deployment", endpoint: "https://example.openai.azure.com", key: "test-key", policy: "hhc-cms-translation-v1", wantErr: true},
		{name: "missing key", endpoint: "https://example.openai.azure.com", deployment: "cms-translator", policy: "hhc-cms-translation-v1", wantErr: true},
		{name: "missing policy", endpoint: "https://example.openai.azure.com", deployment: "cms-translator", key: "test-key", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://example")
			t.Setenv("CMS_TRANSLATION_ENABLED", "true")
			t.Setenv("AZURE_OPENAI_ENDPOINT", tt.endpoint)
			t.Setenv("AZURE_OPENAI_DEPLOYMENT", tt.deployment)
			t.Setenv("AZURE_OPENAI_API_KEY", tt.key)
			t.Setenv("AZURE_OPENAI_RAI_POLICY", tt.policy)

			cfg, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %t", err, tt.wantErr)
			}
			if err == nil && (!cfg.Translation.Enabled || cfg.Translation.AzureEndpoint != tt.endpoint || cfg.Translation.AzureDeployment != tt.deployment || cfg.Translation.AzureAPIKey != tt.key || cfg.Translation.AzureRAIPolicy != tt.policy) {
				t.Fatalf("unexpected translation config: %#v", cfg.Translation)
			}
		})
	}
}

func TestTranslationConfigRejectsInvalidLimits(t *testing.T) {
	valid := TranslationConfig{
		ProviderTimeout:      40 * time.Second,
		HandlerTimeout:       45 * time.Second,
		WriteDeadline:        50 * time.Second,
		SourceCharLimit:      20000,
		ActorLimit:           10,
		DeploymentLimit:      60,
		ActorDailyLimit:      30,
		DeploymentDailyLimit: 300,
		Cooldown:             10 * time.Minute,
	}
	tests := []struct {
		name   string
		change func(*TranslationConfig)
	}{
		{name: "provider timeout", change: func(cfg *TranslationConfig) { cfg.ProviderTimeout = 0 }},
		{name: "handler timeout", change: func(cfg *TranslationConfig) { cfg.HandlerTimeout = cfg.ProviderTimeout }},
		{name: "write deadline", change: func(cfg *TranslationConfig) { cfg.WriteDeadline = cfg.HandlerTimeout }},
		{name: "gateway deadline", change: func(cfg *TranslationConfig) { cfg.WriteDeadline = 60 * time.Second }},
		{name: "source character limit", change: func(cfg *TranslationConfig) { cfg.SourceCharLimit = 0 }},
		{name: "actor limit", change: func(cfg *TranslationConfig) { cfg.ActorLimit = 0 }},
		{name: "deployment limit", change: func(cfg *TranslationConfig) { cfg.DeploymentLimit = 0 }},
		{name: "actor daily limit", change: func(cfg *TranslationConfig) { cfg.ActorDailyLimit = 0 }},
		{name: "deployment daily limit", change: func(cfg *TranslationConfig) { cfg.DeploymentDailyLimit = 0 }},
		{name: "cooldown", change: func(cfg *TranslationConfig) { cfg.Cooldown = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.change(&cfg)
			if err := cfg.validate(); err == nil {
				t.Fatal("expected invalid translation limits to be rejected")
			}
		})
	}
}

func TestAuditDispatchDefaultsDisabledAndValidatesEnabledConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("AUDIT_DISPATCH_ENABLED", "")
	cfg, err := Load()
	if err != nil || cfg.AuditDispatchEnabled {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	t.Setenv("AUDIT_DISPATCH_ENABLED", "true")
	t.Setenv("AUDIT_APP_ID", "audit-log")
	t.Setenv("AUDIT_TOKEN", "secret")
	t.Setenv("DAPR_HTTP_PORT", "3500")
	cfg, err = Load()
	if err != nil || !cfg.AuditDispatchEnabled || cfg.DaprHTTPPort != 3500 {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	t.Setenv("AUDIT_APP_ID", "wrong")
	if _, err = Load(); err == nil {
		t.Fatal("expected invalid audit configuration")
	}
}

func TestBulletinAutomaticExtractionRequiresExplicitActivation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	for _, tc := range []struct {
		value            string
		enabled, invalid bool
	}{{"", false, false}, {"true", true, false}, {"false", false, false}, {"invalid", false, true}} {
		t.Setenv("BULLETIN_AUTO_EXTRACTION_ENABLED", tc.value)
		cfg, err := Load()
		if (err != nil) != tc.invalid || err == nil && cfg.BulletinAutoExtractionEnabled != tc.enabled {
			t.Fatalf("value=%q enabled=%t err=%v", tc.value, cfg.BulletinAutoExtractionEnabled, err)
		}
	}
}

func TestProductionStudioRequiresEncryption(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("APP_API_TOKEN", "test-only")
	t.Setenv("STUDIO_WRITERS_ENABLED", "true")
	t.Setenv("STREAM_KEY_ENCRYPTION_ID", "")
	t.Setenv("STREAM_KEY_ENCRYPTION_KEYS", "")
	if _, err := Load(); err == nil {
		t.Fatal("accepted unencrypted production Studio")
	}
	t.Setenv("STREAM_KEY_ENCRYPTION_ID", "v1")
	t.Setenv("STREAM_KEY_ENCRYPTION_KEYS", `{"v1":"0101010101010101010101010101010101010101010101010101010101010101"}`)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STREAM_KEY_ENCRYPTION_KEYS", `{"v1":"secret-invalid"}`)
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "secret-invalid") {
		t.Fatal("key validation or redaction failed")
	}
}

func TestDaprPortWithoutAudit(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("AUDIT_DISPATCH_ENABLED", "false")
	for _, raw := range []string{"3500", "0", "65536", "invalid", ""} {
		t.Setenv("DAPR_HTTP_PORT", raw)
		cfg, err := Load()
		invalid := raw != "3500" && raw != ""
		if (err != nil) != invalid {
			t.Fatalf("port=%q error=%v", raw, err)
		}
		if raw == "3500" && cfg.DaprHTTPPort != 3500 {
			t.Fatal("port only parsed when Audit enabled")
		}
	}
}

func TestDiagnosticsEnablement(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	for _, value := range []string{"", "true", "false", "invalid"} {
		t.Setenv("CMS_DIAGNOSTICS_ENABLED", value)
		cfg, err := Load()
		if value == "invalid" {
			if err == nil {
				t.Fatal("invalid setting accepted")
			}
			continue
		}
		if err != nil || cfg.DiagnosticsEnabled != (value != "false") {
			t.Fatalf("setting %q: %v", value, err)
		}
	}
}

func TestGatewayLocationsCredential(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("A", 64), false},
		{strings.Repeat("a", 63), false},
		{strings.Repeat("a", 64) + " ", false},
		{strings.Repeat("z", 64), false},
	} {
		t.Setenv("CMS_GATEWAY_LOCATIONS_TOKEN", tc.value)
		cfg, err := Load()
		if (err == nil) != tc.valid {
			t.Fatalf("credential length %d accepted=%t, want accepted=%t", len(tc.value), err == nil, tc.valid)
		}
		if tc.valid && cfg.GatewayLocationsToken != tc.value {
			t.Fatal("runtime credential differs from configured value")
		}
		worker, err := LoadWorker()
		if err != nil || worker.GatewayLocationsToken != "" {
			t.Fatal("worker unexpectedly depends on ingress credential")
		}
	}
}

func TestPrivateServiceCredentialScopes(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	gateway := strings.Repeat("a", 64)
	account := strings.Repeat("b", 64)
	t.Setenv("CMS_GATEWAY_LOCATIONS_TOKEN", gateway)
	t.Setenv("CMS_ACCOUNT_PRIVATE_TOKEN", account)
	t.Setenv("CMS_OPERATIONS_PRIVATE_TOKEN", strings.Repeat("c", 64))
	t.Setenv("CMS_ENGAGEMENT_PRIVATE_TOKEN", strings.Repeat("d", 64))
	t.Setenv("CMS_LINE_PRIVATE_TOKEN", strings.Repeat("e", 64))
	cfg, err := Load()
	if err != nil || len(cfg.PrivateServiceTokens) != 4 || cfg.PrivateServiceTokens["account-api"] != account {
		t.Fatal("private service credentials not scoped to exact callers")
	}
	for _, value := range []string{account, gateway, strings.ToUpper(account), account[:63]} {
		t.Setenv("CMS_ENGAGEMENT_PRIVATE_TOKEN", value)
		if _, err := Load(); err == nil {
			t.Fatal("invalid or duplicate private credential accepted")
		}
	}
	worker, err := LoadWorker()
	if err != nil || len(worker.PrivateServiceTokens) != 0 {
		t.Fatal("worker inherited private service credentials")
	}
}

func TestDaprOffRequiresAllPrivateTransports(t *testing.T) {
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("APP_API_TOKEN", "")
	t.Setenv("DAPR_HTTP_PORT", "")
	t.Setenv("CMS_DAPR_ENABLED", "false")
	t.Setenv("AUDIT_DISPATCH_ENABLED", "true")
	t.Setenv("AUDIT_APP_ID", "audit-log")
	t.Setenv("AUDIT_TOKEN", "fixture")
	t.Setenv("AUDIT_PRIVATE_HTTPS", "true")
	t.Setenv("ASSET_PRIVATE_HTTPS", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Dapr disabled without private bindings")
	}
	for i, name := range []string{
		"CMS_GATEWAY_LOCATIONS_TOKEN", "CMS_GATEWAY_PRIVATE_TOKEN", "CMS_GATEWAY_LIVE_PRIVATE_TOKEN",
		"CMS_ACCOUNT_PRIVATE_TOKEN", "CMS_OPERATIONS_PRIVATE_TOKEN", "CMS_ENGAGEMENT_PRIVATE_TOKEN", "CMS_LINE_PRIVATE_TOKEN",
		"ACCOUNT_CMS_PRIVATE_TOKEN", "OPERATIONS_CMS_PRIVATE_TOKEN", "ENGAGEMENT_CMS_PRIVATE_TOKEN",
	} {
		t.Setenv(name, strings.Repeat(string("0123456789"[i]), 64))
	}
	cfg, err := Load()
	if err != nil || cfg.DaprEnabled || cfg.DaprAPIToken != "" {
		t.Fatalf("Dapr-off startup rejected: %v", err)
	}
	t.Setenv("ALLOW_DEV_CALLER_HEADER", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Dapr-off startup accepted legacy development caller headers")
	}
}

func TestFullRecognitionProducerSwitchIsExplicit(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://example")
			t.Setenv("BULLETIN_LLM_EXTRACTION_ENABLED", value)
			cfg, err := Load()
			if value == "invalid" {
				if err == nil {
					t.Fatal("invalid producer switch accepted")
				}
				return
			}
			if err != nil || cfg.BulletinLLMExtractionEnabled != (value == "true") {
				t.Fatal("producer switch not explicit", err)
			}
		})
	}
}

func TestInboxRequiresExplicitCutover(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("WEBSITE_INBOX_ENABLED", "")
	t.Setenv("WEBSITE_INBOX_PUBLICATIONS_AFTER", "")
	cfg, err := Load()
	if err != nil || cfg.InboxEnabled || !cfg.InboxPublicationsAfter.IsZero() {
		t.Fatal("inbox default enabled", err)
	}
	t.Setenv("WEBSITE_INBOX_ENABLED", "true")
	if _, err = Load(); err == nil {
		t.Fatal("missing no-backfill watermark accepted")
	}
	t.Setenv("WEBSITE_INBOX_PUBLICATIONS_AFTER", "2026-10-19T00:00:00Z")
	cfg, err = Load()
	if err != nil || !cfg.InboxEnabled || cfg.InboxPublicationsAfter.IsZero() {
		t.Fatal("explicit cutover rejected", err)
	}
	t.Setenv("WEBSITE_INBOX_ENABLED", "maybe")
	if _, err = Load(); err == nil {
		t.Fatal("invalid flag accepted")
	}
}

func TestInboxSelectsMatchingNotificationEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("WEBSITE_INBOX_ENABLED", "true")
	t.Setenv("WEBSITE_INBOX_PUBLICATIONS_AFTER", "2026-10-19T00:00:00Z")
	t.Setenv("INTERNAL_CALLER_APP_ID", "hhc-web-api-test")
	t.Setenv("NOTIFICATION_APP_ID", "notification-api-test")
	cfg, e := Load()
	if e != nil || cfg.NotificationAppID != "notification-api-test" {
		t.Fatal(e)
	}
	for _, id := range []string{"notification-api", "notification-api-test-extra"} {
		t.Setenv("NOTIFICATION_APP_ID", id)
		if _, e = Load(); e == nil {
			t.Fatalf("accepted %s", id)
		}
	}
}
