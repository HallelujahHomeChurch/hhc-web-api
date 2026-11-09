package config

import "testing"

func TestAssetWorkloadConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, audience, id string
		valid                       bool
	}{
		{"default off", "", "", "", true},
		{"selected identity", "true", "api://asset", "88340bcf-5198-4219-90b4-dfca455a1ee3", true},
		{"malformed flag", "yes", "", "", false},
		{"implicit identity", "true", "api://asset", "", false},
		{"scope not audience", "true", "api://asset/.default", "88340bcf-5198-4219-90b4-dfca455a1ee3", false},
		{"audience whitespace", "true", "api://asset\n", "88340bcf-5198-4219-90b4-dfca455a1ee3", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ASSET_WORKLOAD_AUTH_ENABLED", tc.enabled)
			t.Setenv("ASSET_WORKLOAD_AUDIENCE", tc.audience)
			t.Setenv("ASSET_WORKLOAD_CLIENT_ID", tc.id)
			cfg, err := loadAssetWorkload()
			if (err == nil) != tc.valid || (err == nil && cfg.Enabled != (tc.enabled == "true")) {
				t.Fatalf("cfg=%#v err=%v", cfg, err)
			}
		})
	}
}

func TestVerifiedCapabilitiesRequireWorkloadAndStrictFlags(t *testing.T) {
	for _, key := range []string{"ASSET_OWNER_CONTENT_ENABLED", "ASSET_TRUSTED_DERIVATION_ENABLED"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("ASSET_WORKLOAD_AUTH_ENABLED", "false")
			t.Setenv(key, "true")
			if _, err := loadAssetWorkload(); err == nil {
				t.Fatal("legacy capability enabled")
			}
			t.Setenv("ASSET_WORKLOAD_AUTH_ENABLED", "true")
			t.Setenv("ASSET_WORKLOAD_AUDIENCE", "api://asset")
			t.Setenv("ASSET_WORKLOAD_CLIENT_ID", "88340bcf-5198-4219-90b4-dfca455a1ee3")
			if _, err := loadAssetWorkload(); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, "typo")
			if _, err := loadAssetWorkload(); err == nil {
				t.Fatal("malformed capability accepted")
			}
		})
	}
}
