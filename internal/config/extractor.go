package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ExtractorConfig struct {
	AssetWorkload                                               AssetWorkloadConfig
	LLMEnabled                                                  bool
	Enabled                                                     bool
	QueueURL, PoisonQueueURL                                    string
	DatabaseURL, AssetAPIBaseURL, AzureClientID                 string
	AzureEndpoint, AzureDeployment, AzureAPIKey, AzureRAIPolicy string
	Timeout                                                     time.Duration
	BatchSize, MaxAttempts                                      int
}

// LoadExtractor intentionally does not load HTTP-server/Dapr configuration.
// Missing AI credentials leave deterministic extraction enabled for review.
func LoadExtractor() (ExtractorConfig, error) {
	var cfg ExtractorConfig
	var err error
	cfg.Enabled, err = strconv.ParseBool(value("BULLETIN_EXTRACTION_ENABLED", "false"))
	if err != nil {
		return cfg, fmt.Errorf("invalid BULLETIN_EXTRACTION_ENABLED")
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	cfg.LLMEnabled, err = strconv.ParseBool(value("BULLETIN_LLM_EXTRACTION_ENABLED", "false"))
	if err != nil {
		return cfg, fmt.Errorf("invalid BULLETIN_LLM_EXTRACTION_ENABLED")
	}
	read := func(name string) string { return strings.TrimSpace(os.Getenv(name)) }
	cfg.DatabaseURL = read("DATABASE_URL")
	cfg.QueueURL, cfg.PoisonQueueURL = read("BULLETIN_EXTRACTION_QUEUE_URL"), read("BULLETIN_EXTRACTION_POISON_QUEUE_URL")
	cfg.AssetAPIBaseURL = read("ASSET_API_BASE_URL")
	cfg.AssetWorkload, err = loadAssetWorkload()
	if err != nil {
		return cfg, err
	}
	cfg.AzureClientID = read("AZURE_CLIENT_ID")
	cfg.AzureEndpoint = read("AZURE_OPENAI_ENDPOINT")
	cfg.AzureDeployment = read("AZURE_OPENAI_DEPLOYMENT")
	cfg.AzureAPIKey = read("AZURE_OPENAI_API_KEY")
	cfg.AzureRAIPolicy = read("AZURE_OPENAI_RAI_POLICY")
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}
	if uuid.Validate(cfg.AzureClientID) != nil {
		return cfg, fmt.Errorf("AZURE_CLIENT_ID must identify the extractor managed identity")
	}
	for name, value := range map[string]string{"ASSET_API_BASE_URL": cfg.AssetAPIBaseURL, "AZURE_OPENAI_ENDPOINT": cfg.AzureEndpoint} {
		if name == "AZURE_OPENAI_ENDPOINT" && value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return cfg, fmt.Errorf("invalid %s", name)
		}
	}
	cfg.Timeout, err = positiveDuration("BULLETIN_EXTRACTION_TIMEOUT", 120*time.Second)
	if err != nil || cfg.Timeout < 40*time.Second || cfg.Timeout > 120*time.Second {
		return cfg, fmt.Errorf("invalid BULLETIN_EXTRACTION_TIMEOUT")
	}
	cfg.BatchSize, err = positiveInt("BULLETIN_EXTRACTION_BATCH_SIZE", 1)
	if err != nil || cfg.BatchSize > 10 {
		return cfg, fmt.Errorf("invalid BULLETIN_EXTRACTION_BATCH_SIZE")
	}
	cfg.MaxAttempts, err = positiveInt("BULLETIN_EXTRACTION_MAX_ATTEMPTS", 3)
	if err != nil || cfg.MaxAttempts > 10 {
		return cfg, fmt.Errorf("invalid BULLETIN_EXTRACTION_MAX_ATTEMPTS")
	}
	if cfg.LLMEnabled && (cfg.AzureEndpoint == "" || cfg.AzureAPIKey == "" || cfg.AzureRAIPolicy == "" || cfg.BatchSize != 1) {
		return cfg, fmt.Errorf("full bulletin recognition requires Azure credentials and a single-document batch")
	}
	return cfg, nil
}
