package config

import "testing"

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
