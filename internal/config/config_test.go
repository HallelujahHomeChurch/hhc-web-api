package config

import (
	"testing"
	"time"
)

func TestProductionRequiresDaprAPIToken(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("APP_API_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected production config to require APP_API_TOKEN")
	}
}

func TestDatabasePoolConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("DB_MAX_OPEN_CONNS", "12")
	t.Setenv("DB_MAX_IDLE_CONNS", "4")
	t.Setenv("DB_CONN_MAX_LIFETIME", "20m")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxOpenConns != 12 || cfg.DBMaxIdleConns != 4 || cfg.DBConnMaxLifetime.String() != "20m0s" {
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

func TestOperationsAllowedCallerAppIDs(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("OPERATIONS_ALLOWED_CALLER_APP_IDS", " asset-api, hhc-line-function-bot,asset-api ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"asset-api", "hhc-line-function-bot"}
	if len(cfg.OperationsAllowedCallerAppIDs) != len(want) {
		t.Fatalf("allowed callers = %#v, want %#v", cfg.OperationsAllowedCallerAppIDs, want)
	}
	for i := range want {
		if cfg.OperationsAllowedCallerAppIDs[i] != want[i] {
			t.Fatalf("allowed callers = %#v, want %#v", cfg.OperationsAllowedCallerAppIDs, want)
		}
	}
}

func TestOperationsWorkloadAuthRequiresCompleteConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("OPERATIONS_WORKLOAD_TENANT_ID", "tenant")
	if _, err := Load(); err == nil {
		t.Fatal("expected incomplete workload auth error")
	}
	for name, value := range map[string]string{
		"OPERATIONS_WORKLOAD_ISSUER": "https://sts.windows.net/tenant/", "OPERATIONS_WORKLOAD_AUDIENCE": "api://meeting",
		"OPERATIONS_WORKLOAD_CLIENT_ID": "client", "OPERATIONS_WORKLOAD_OBJECT_ID": "object",
	} {
		t.Setenv(name, value)
	}
	cfg, err := Load()
	if err != nil || cfg.OperationsWorkloadClientID != "client" || cfg.OperationsWorkloadObjectID != "object" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
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
