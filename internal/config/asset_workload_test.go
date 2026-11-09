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
