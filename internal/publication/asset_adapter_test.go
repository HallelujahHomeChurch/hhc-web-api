package publication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

func TestAssetAdapterForwardsPurposeAndDetectedMIME(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(assetclient.Asset{ID: "banner-1", Purpose: "home_banner", DetectedMIMEType: "image/jpeg"})
	}))
	defer server.Close()
	asset, err := NewAssetAdapter(assetclient.New(server.URL, "hhc-web-api", "https://www.alive.org.tw/assets")).Get(context.Background(), "banner-1")
	if err != nil || asset.Purpose != "home_banner" || asset.DetectedMIMEType != "image/jpeg" {
		t.Fatalf("asset=%#v err=%v", asset, err)
	}
}
