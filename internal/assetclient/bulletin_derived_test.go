package assetclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteDerivedBulletinSendsSourceBindingToDedicatedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/priv/assets/bulletin-derivatives/output/complete" {
			t.Errorf("unexpected operation: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["sourceAssetId"] != "source" || body["sourceSha256"] != "source-hash" || body["checksumSha256"] != "output-hash" || body["sizeBytes"] != float64(123) || body["mimeType"] != "application/pdf" {
			t.Errorf("source/output binding lost: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(Asset{ID: "output", ScanStatus: "clean", ScanSignature: "trusted-bulletin-watermark-v1"})
	}))
	defer server.Close()
	asset, err := New(server.URL, "hhc-web-api", "").CompleteDerivedBulletin(context.Background(), "output", "source", "source-hash", CompleteUploadInput{SizeBytes: 123, ChecksumSHA256: "output-hash", MIMEType: "application/pdf"})
	if err != nil || asset.ScanSignature != "trusted-bulletin-watermark-v1" {
		t.Fatalf("asset=%+v err=%v", asset, err)
	}
}
