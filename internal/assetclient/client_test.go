package assetclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/assets")
	asset, err := client.Get(context.Background(), "asset-1")
	if err != nil || asset.ScanStatus != "clean" {
		t.Fatalf("asset=%#v err=%v", asset, err)
	}
	if got := client.PublicURL("asset-1"); got != "https://www.alive.org.tw/assets/asset-1" {
		t.Fatalf("url=%s", got)
	}
}

func TestClientCreatesNewsUploadWithPurpose(t *testing.T) {
	var purpose string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		purpose, _ = body["purpose"].(string)
		_ = json.NewEncoder(w).Encode(CreatedUpload{Asset: Asset{ID: "asset-1"}, UploadTarget: UploadTarget{URL: "https://upload.test", Method: http.MethodPut}})
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/assets")
	if _, err := client.CreateNewsCoverUpload(context.Background(), "news-1", "news_home_cover", "home.jpg", "image/jpeg", 128, "key"); err != nil {
		t.Fatal(err)
	}
	if purpose != "news_home_cover" {
		t.Fatalf("purpose=%q", purpose)
	}
}

func TestClientCreatesExactHomeBannerUpload(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(CreatedUpload{Asset: Asset{ID: "banner-1"}})
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/assets")
	if _, err := client.CreateHomeBannerUpload(context.Background(), "page-1", "banner.jpg", "image/jpeg", 128, "key"); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"namespace": "cms.home.banner", "ownerService": "hhc-web-api", "ownerType": "page", "ownerId": "page-1",
		"purpose": "home_banner", "originalFileName": "banner.jpg", "expectedMimeType": "image/jpeg", "maxSizeBytes": float64(128), "visibility": "public",
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body=%#v", body)
	}
}

func TestClientDecodesDetectedMIMEType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"banner-1","detectedMimeType":"image/jpeg"}`))
	}))
	defer server.Close()
	asset, err := New(server.URL, "hhc-web-api", "https://www.alive.org.tw/assets").Get(context.Background(), "banner-1")
	if err != nil || asset.DetectedMIMEType != "image/jpeg" {
		t.Fatalf("asset=%#v err=%v", asset, err)
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
