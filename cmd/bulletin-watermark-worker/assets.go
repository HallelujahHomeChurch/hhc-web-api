package main

import (
	"errors"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
)

// The legacy workload mode is retained for existing Job configuration.
const (
	assetAuthEnv    = "BULLETIN_WORKER_ASSET_AUTH"
	assetBaseURLEnv = "BULLETIN_WORKER_ASSET_API_BASE_URL"
)

var errAssetAuthConfig = errors.New("bulletin worker asset authentication configuration is invalid")

func newAssetClient(cfg config.Config, getenv func(string) string) (*assetclient.Client, error) {
	switch mode := strings.TrimSpace(getenv(assetAuthEnv)); mode {
	case "", "dapr":
		return assetclient.New(cfg.AssetAPIBaseURL, cfg.InternalCallerAppID, cfg.PublicBaseURL), nil
	case "workload", "internal":
		client, err := assetclient.NewWatermarkInternal(strings.TrimSpace(getenv(assetBaseURLEnv)))
		if err != nil {
			return nil, errAssetAuthConfig
		}
		return client, nil
	default:
		return nil, errAssetAuthConfig
	}
}
