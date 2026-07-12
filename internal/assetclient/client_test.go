package assetclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientUsesInternalIdentityAndStablePublicURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
			t.Errorf("caller=%s", r.Header.Get("X-Internal-Caller-App-Id"))
		}
		_ = json.NewEncoder(w).Encode(Asset{ID: "asset-1", OwnerService: "hhc-web-api", ScanStatus: "clean"})
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/api")
	asset, err := client.Get(context.Background(), "asset-1")
	if err != nil || asset.ScanStatus != "clean" {
		t.Fatalf("asset=%#v err=%v", asset, err)
	}
	if got := client.PublicURL("asset-1"); got != "https://www.alive.org.tw/api/assets/public/asset-1" {
		t.Fatalf("url=%s", got)
	}
}

func TestClientMapsNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/api")

	err := client.RevokeGrant(context.Background(), "asset-1", "grant-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}
