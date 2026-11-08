package assetclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

func TestRecordingPackageClientUsesOwnerContractAndServiceProvenance(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	const actor = "0199a000-0000-7000-8000-000000000001"
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("X-HHC-Actor-ID") != actor || r.Header.Get("X-HHC-Request-ID") != "package-request" || r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
			t.Errorf("missing trusted provenance")
		}
		switch r.URL.Path {
		case "/priv/recording-packages":
			if r.Header.Get("Idempotency-Key") != "operation-package" {
				t.Error("missing idempotency")
			}
			var body struct {
				RecordingID string                    `json:"recordingId"`
				Inventory   RecordingPackageInventory `json:"inventory"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.RecordingID != "recording-1" || body.Inventory.InventoryDigest != "digest" {
				t.Error("wrong create body")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(RecordingPackage{PackageID: id, RecordingID: "recording-1", State: "uploading"})
		case "/priv/recording-packages/" + id:
			_ = json.NewEncoder(w).Encode(RecordingPackageStatus{PackageID: id, State: "uploading", ConfirmedObjects: []string{"master.m3u8"}})
		case "/priv/recording-packages/" + id + "/sign":
			_ = json.NewEncoder(w).Encode([]SignedRecordingObject{{Path: "master.m3u8", URL: "https://upload.example/object", Method: "PUT"}})
		case "/priv/recording-packages/" + id + "/complete":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(RecordingPackage{PackageID: id, State: "freezing"})
		case "/priv/recording-packages/" + id + "/grant":
			_ = json.NewEncoder(w).Encode(RecordingGrant{ExchangeCredential: "exchange", ExpiresAt: time.Now().Add(time.Hour)})
		case "/priv/recording-packages/" + id + "/ready":
			if r.URL.Query().Get("recordingId") != "recording-1" {
				t.Error("ready is not recording-bound")
			}
			_ = json.NewEncoder(w).Encode(RecordingPackageStatus{PackageID: id, RecordingID: "recording-1", State: "ready"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := New(server.URL, "hhc-web-api", "")
	ctx := auditclient.WithPrincipalProvenance(context.Background(), "service", actor, "package-request")
	if _, err := c.CreateRecordingPackage(ctx, "recording-1", RecordingPackageInventory{InventoryDigest: "digest"}, "operation-package"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRecordingPackage(ctx, id, "720p/index.m3u8", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SignRecordingPackage(ctx, id, []string{"master.m3u8"}); err != nil {
		t.Fatal(err)
	}
	complete, err := c.CompleteRecordingPackage(ctx, id)
	if err != nil || complete.State != "freezing" {
		t.Fatalf("accepted is not ready: %+v %v", complete, err)
	}
	if _, err := c.IssueRecordingPackageGrant(ctx, id, "recording-1", "scope-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetReadyRecordingPackage(ctx, id, "recording-1"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 6 || calls[1] != "GET /priv/recording-packages/"+id+"?cursor=720p%2Findex.m3u8&limit=100" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRecordingPackageClientRejectsUnsafeIDAndBatchBeforeRequest(t *testing.T) {
	c := New("http://127.0.0.1:1", "hhc-web-api", "")
	ctx := context.Background()
	for _, id := range []string{"../outside", "", "0123456789ABCDEF0123456789abcdef"} {
		if _, err := c.GetRecordingPackage(ctx, id, "", 100); err != ErrInvalid {
			t.Errorf("status accepted %q: %v", id, err)
		}
		if _, err := c.GetReadyRecordingPackage(ctx, id, "recording"); err != ErrInvalid {
			t.Errorf("ready accepted %q: %v", id, err)
		}
		if _, err := c.SignRecordingPackage(ctx, id, []string{"master.m3u8"}); err != ErrInvalid {
			t.Errorf("sign accepted %q: %v", id, err)
		}
		if _, err := c.CompleteRecordingPackage(ctx, id); err != ErrInvalid {
			t.Errorf("complete accepted %q: %v", id, err)
		}
	}
	if _, err := c.SignRecordingPackage(ctx, "0123456789abcdef0123456789abcdef", make([]string, 101)); err != ErrInvalid {
		t.Errorf("sign batch: %v", err)
	}
}

func TestRecordingPackageClientAcceptsBoundedInventoryAboveOneMiB(t *testing.T) {
	objects := make([]RecordingPackageObject, 13000)
	for i := range objects {
		objects[i] = RecordingPackageObject{Path: "720p/seg-000000.m4s", SizeBytes: 1, SHA256: strings.Repeat("a", 64)}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(RecordingPackage{PackageID: "package", Inventory: RecordingPackageInventory{Objects: objects}})
	}))
	defer server.Close()
	value, err := New(server.URL, "hhc-web-api", "").CreateRecordingPackage(context.Background(), "recording", RecordingPackageInventory{}, "key")
	if err != nil || len(value.Inventory.Objects) != 13000 {
		t.Fatalf("bounded large response: count=%d err=%v", len(value.Inventory.Objects), err)
	}
}

func TestRecordingPackageClientSanitizesProviderErrorsAndPreservesCapacityCode(t *testing.T) {
	status := http.StatusRequestEntityTooLarge
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("private-provider-url-and-diagnostic"))
	}))
	defer server.Close()
	c := New(server.URL, "hhc-web-api", "")
	if _, err := c.CreateRecordingPackage(context.Background(), "recording", RecordingPackageInventory{}, "key"); err != ErrPackageTooLarge {
		t.Fatalf("capacity: %v", err)
	}
	status = http.StatusUnauthorized
	_, err := c.CreateRecordingPackage(context.Background(), "recording", RecordingPackageInventory{}, "key")
	if err == nil || strings.Contains(err.Error(), "private-provider") {
		t.Fatalf("leaked provider response: %v", err)
	}
}
