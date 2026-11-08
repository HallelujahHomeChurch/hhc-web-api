package main

import (
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
	"testing"
)

func TestAssetClientModes(t *testing.T) {
	for _, mode := range []string{"", "dapr", "workload", "internal"} {
		env := map[string]string{assetAuthEnv: mode, assetBaseURLEnv: "https://asset.internal.example"}
		c, err := newAssetClient(config.Config{AssetAPIBaseURL: "http://localhost:3500/v1.0/invoke/asset-api/method", InternalCallerAppID: "hhc-web-api"}, func(k string) string { return env[k] })
		if err != nil || c == nil {
			t.Fatalf("mode=%s err=%v", mode, err)
		}
	}
}
func TestAssetClientRejectsUnsafeConfiguration(t *testing.T) {
	for _, env := range []map[string]string{
		{assetAuthEnv: "unknown"},
		{assetAuthEnv: "internal"},
		{assetAuthEnv: "workload", assetBaseURLEnv: "http://asset.internal.example"},
		{assetAuthEnv: "internal", assetBaseURLEnv: "https://user:pass@asset.internal.example"},
	} {
		if _, err := newAssetClient(config.Config{}, func(k string) string { return env[k] }); !errors.Is(err, errAssetAuthConfig) {
			t.Fatalf("err=%v", err)
		}
	}
}
