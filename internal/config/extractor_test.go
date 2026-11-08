package config

import (
	"testing"
	"time"
)

func extractorEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{"BULLETIN_EXTRACTION_ENABLED": "true", "BULLETIN_LLM_EXTRACTION_ENABLED": "", "DATABASE_URL": "postgres://example", "ASSET_API_BASE_URL": "https://asset.internal.example", "ASSET_API_AUDIENCE": "api://asset", "AZURE_CLIENT_ID": "88340bcf-5198-4219-90b4-dfca455a1ee3", "BULLETIN_EXTRACTOR_ASSET_ORIGIN": "https://www.alive.org.tw", "APP_API_TOKEN": "", "AZURE_OPENAI_ENDPOINT": "", "AZURE_OPENAI_DEPLOYMENT": "", "AZURE_OPENAI_API_KEY": "", "AZURE_OPENAI_RAI_POLICY": "", "BULLETIN_EXTRACTION_TIMEOUT": "", "BULLETIN_EXTRACTION_BATCH_SIZE": "", "BULLETIN_EXTRACTION_MAX_ATTEMPTS": ""} {
		t.Setenv(key, value)
	}
}

func TestExtractorConfigurationIsSeparateFromServerAndAIOptional(t *testing.T) {
	extractorEnvironment(t)
	t.Setenv("BULLETIN_EXTRACTOR_ASSET_ORIGIN", "")
	t.Setenv("ASSET_API_AUDIENCE", "")
	cfg, err := LoadExtractor()
	if err != nil || !cfg.Enabled || cfg.Timeout != 120*time.Second || cfg.BatchSize != 1 || cfg.MaxAttempts != 3 || cfg.AzureAPIKey != "" {
		t.Fatal("standalone config requires server or AI credentials", err)
	}
	t.Setenv("BULLETIN_EXTRACTION_ENABLED", "false")
	t.Setenv("DATABASE_URL", "")
	if cfg, err := LoadExtractor(); err != nil || cfg.Enabled {
		t.Fatal("disabled extractor requires runtime credentials")
	}
}

func TestExtractorConfigurationBoundsAndWorkloadIdentity(t *testing.T) {
	for _, test := range []struct{ key, value string }{{"BULLETIN_EXTRACTION_TIMEOUT", "121s"}, {"BULLETIN_EXTRACTION_TIMEOUT", "30s"}, {"BULLETIN_EXTRACTION_BATCH_SIZE", "11"}, {"BULLETIN_EXTRACTION_MAX_ATTEMPTS", "0"}, {"ASSET_API_BASE_URL", "http://asset.internal.example"}, {"ASSET_API_BASE_URL", "https://user:pass@asset.internal.example"}, {"AZURE_CLIENT_ID", ""}, {"AZURE_OPENAI_ENDPOINT", "http://provider.example"}} {
		t.Run(test.key+test.value, func(t *testing.T) {
			extractorEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadExtractor(); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestFullRecognitionConfigurationNeedsAzureAndSingleDocumentBatch(t *testing.T) {
	for _, scenario := range []string{"valid", "missing_key", "batch", "invalid_flag"} {
		t.Run(scenario, func(t *testing.T) {
			extractorEnvironment(t)
			t.Setenv("BULLETIN_LLM_EXTRACTION_ENABLED", "true")
			t.Setenv("AZURE_OPENAI_ENDPOINT", "https://example.cognitiveservices.azure.com")
			t.Setenv("AZURE_OPENAI_API_KEY", "test-key")
			t.Setenv("AZURE_OPENAI_RAI_POLICY", "cms-policy")
			switch scenario {
			case "missing_key":
				t.Setenv("AZURE_OPENAI_API_KEY", "")
			case "batch":
				t.Setenv("BULLETIN_EXTRACTION_BATCH_SIZE", "2")
			case "invalid_flag":
				t.Setenv("BULLETIN_LLM_EXTRACTION_ENABLED", "maybe")
			}
			cfg, err := LoadExtractor()
			if scenario == "valid" {
				if err != nil || !cfg.LLMEnabled {
					t.Fatal("full recognition disabled", err)
				}
			} else if err == nil {
				t.Fatal("invalid full recognition configuration accepted")
			}
		})
	}
}
