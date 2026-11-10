package config

import (
	"fmt"
	"github.com/google/uuid"
	"net/url"
	"os"
	"strings"
)

type AssetWorkloadConfig struct {
	Enabled                         bool
	OwnerContent, TrustedDerivation bool
	Audience, ClientID              string
}

func loadAssetWorkload() (AssetWorkloadConfig, error) {
	var cfg AssetWorkloadConfig
	for key, target := range map[string]*bool{"ASSET_OWNER_CONTENT_ENABLED": &cfg.OwnerContent, "ASSET_TRUSTED_DERIVATION_ENABLED": &cfg.TrustedDerivation} {
		switch value(key, "false") {
		case "true":
			*target = true
		case "false":
		default:
			return cfg, fmt.Errorf("%s must be true or false", key)
		}
	}
	switch value("ASSET_WORKLOAD_AUTH_ENABLED", "false") {
	case "false":
		if cfg.OwnerContent || cfg.TrustedDerivation {
			return cfg, fmt.Errorf("Asset capabilities require verified workload authentication")
		}
		return cfg, nil
	case "true":
		cfg.Enabled = true
	default:
		return cfg, fmt.Errorf("ASSET_WORKLOAD_AUTH_ENABLED must be true or false")
	}
	cfg.Audience = os.Getenv("ASSET_WORKLOAD_AUDIENCE")
	cfg.ClientID = os.Getenv("ASSET_WORKLOAD_CLIENT_ID")
	u, err := url.Parse(cfg.Audience)
	if err != nil || (u.Scheme != "api" && u.Scheme != "https") || u.Host == "" || len(cfg.Audience) > 255 || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(cfg.Audience, " \r\n\t") || strings.HasSuffix(cfg.Audience, "/") || strings.HasSuffix(cfg.Audience, "/.default") || uuid.Validate(cfg.ClientID) != nil {
		return cfg, fmt.Errorf("Asset workload authentication requires an audience and an explicit managed identity client ID")
	}
	return cfg, nil
}
