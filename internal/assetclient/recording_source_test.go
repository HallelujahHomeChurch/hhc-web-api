package assetclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

func TestRecordingSourceClientKeepsByteBudgetAndOwnerContract(t *testing.T) {
	id := strings.Repeat("a", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-HHC-Actor-ID") != "actor-a" || r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
			t.Error("missing owner provenance")
		}
		switch r.URL.Path {
		case "/priv/recording-sources":
			var in RecordingSourceInput
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.SizeBytes != 50_000_000_000 || in.RecordingID != "recording-a" || r.Header.Get("Idempotency-Key") != "source-operation" {
				t.Error("wrong source body")
			}
			json.NewEncoder(w).Encode(RecordingSource{SourceID: id, RecordingID: "recording-a", State: "uploading"})
		case "/priv/recording-sources/" + id:
			if r.URL.Query().Get("cursor") != "1000" || r.URL.Query().Get("limit") != "1000" {
				t.Error("lost paging")
			}
			json.NewEncoder(w).Encode(RecordingSourceStatus{RecordingSource: RecordingSource{SourceID: id}, ConfirmedBlocks: []int{1001}, NextCursor: 1001})
		case "/priv/recording-sources/" + id + "/sign":
			json.NewEncoder(w).Encode([]SignedRecordingSourceBlock{{Number: 1, UploadTarget: UploadTarget{Method: "PUT"}}})
		case "/priv/recording-sources/" + id + "/complete", "/priv/recording-sources/" + id + "/retry-processing":
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(RecordingSource{SourceID: id, State: "finalizing"})
		case "/priv/recording-sources/" + id + "/ready":
			if r.URL.Query().Get("recordingId") != "recording-a" {
				t.Error("missing recording binding")
			}
			json.NewEncoder(w).Encode(RecordingSource{SourceID: id, RecordingID: "recording-a", State: "ready", PackageID: strings.Repeat("b", 32)})
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := New(server.URL, "hhc-web-api", "")
	ctx := auditclient.WithProvenance(context.Background(), "actor-a", "request-a")
	if _, err := c.CreateRecordingSource(ctx, RecordingSourceInput{RecordingID: "recording-a", FileName: "source.mp4", SizeBytes: 50_000_000_000, ChecksumSHA256: strings.Repeat("a", 64)}, "source-operation"); err != nil {
		t.Fatal(err)
	}
	if page, err := c.GetRecordingSource(ctx, id, 1000, 1000); err != nil || page.NextCursor != 1001 {
		t.Fatal(err)
	}
	if _, err := c.SignRecordingSource(ctx, id, []int{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompleteRecordingSource(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RetryRecordingSource(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetReadyRecordingSource(ctx, id, "recording-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SignRecordingSource(ctx, "../asset", []int{1}); err == nil {
		t.Fatal("unsafe ID")
	}
	if _, err := c.SignRecordingSource(ctx, id, make([]int, 101)); err == nil {
		t.Fatal("unbounded sign batch")
	}
	if calls != 6 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestSourceProviderErrorsAreRedactedAndCapacityIsDistinct(t *testing.T) {
	status := 413
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte("private provider capability"))
	}))
	defer server.Close()
	c := New(server.URL, "hhc-web-api", "")
	if _, err := c.CreateRecordingSource(context.Background(), RecordingSourceInput{}, "key"); err != ErrSourceTooLarge {
		t.Fatalf("source capacity: %v", err)
	}
	status = 422
	if _, err := c.CompleteRecordingSource(context.Background(), strings.Repeat("a", 32)); err != ErrInvalid {
		t.Fatalf("invalid blocks: %v", err)
	}
	status = 418
	_, err := c.CreateRecordingSource(context.Background(), RecordingSourceInput{}, "key")
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("leaked provider response: %v", err)
	}
}
