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

func TestClientMapsServerFailureToUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/api")

	_, err := client.Get(context.Background(), "asset-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientDeletesOwnedAsset(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/api")

	if err := client.Delete(context.Background(), "asset-1"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/priv/assets/asset-1" {
		t.Fatalf("method=%s path=%s", method, path)
	}
}

func TestClientRequeuesFailedScan(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/api")

	if err := client.RequeueScan(context.Background(), "asset-1"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/priv/assets/asset-1/scan/requeue" {
		t.Fatalf("method=%s path=%s", method, path)
	}
}
