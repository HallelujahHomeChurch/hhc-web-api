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
