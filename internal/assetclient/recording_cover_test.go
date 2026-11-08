package assetclient

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCoverReceiptListBound(t *testing.T) {
	for _, count := range []int{1000, 1001} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			items := make([]RecordingCover, count)
			for i := range items {
				items[i] = RecordingCover{ID: "cover-1", UploadID: "upload-1", Kind: "custom", State: "expired"}
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
		}))
		_, err := New(server.URL, "hhc-web-api", "").ListRecordingCovers(context.Background(), "0123456789abcdef0123456789abcdef", "recording-1")
		server.Close()
		if (count == 1000 && err != nil) || (count == 1001 && !errors.Is(err, ErrUnavailable)) {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
}

func TestRecordingCoverRawUploadAndOwnerWire(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("recordingId") != "recording-1" || r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
			t.Error("owner boundary lost")
		}
		switch r.Method {
		case "POST":
			data, _ := io.ReadAll(r.Body)
			if string(data) != "raw-image" || r.Header.Get("Idempotency-Key") != "upload-key" || r.Header.Get("X-HHC-Actor-ID") != "actor" || r.Header.Get("Content-Type") != "image/png" {
				t.Error("raw upload/provenance changed")
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"uploadId":"upload-1","state":"uploading"}`)
		case "GET":
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "jpeg")
		case "DELETE":
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "")
	ctx := auditclient.WithProvenance(context.Background(), "actor", "request")
	result, err := client.UploadRecordingCover(ctx, pkg, "recording-1", "image/png", "upload-key", []byte("raw-image"))
	if err != nil || result.State != "uploading" {
		t.Fatal(result, err)
	}
	data, err := client.ReadRecordingCover(ctx, pkg, "recording-1", "cover-1")
	if err != nil || string(data) != "jpeg" {
		t.Fatal(err)
	}
	if err := client.ReleaseRecordingCover(ctx, pkg, "recording-1", "cover-1", "reference-1"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}

func TestRecordingCoverReadRejectsRedirectsAndUnboundedBytes(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	for _, mode := range []string{"redirect", "oversized", "wrong-mime"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					w.Header().Set("Location", "/unexpected")
					w.WriteHeader(302)
				case "oversized":
					w.Header().Set("Content-Type", "image/jpeg")
					io.WriteString(w, strings.Repeat("x", (1<<20)+1))
				case "wrong-mime":
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, "unsafe")
				}
			}))
			defer server.Close()
			_, err := New(server.URL, "hhc-web-api", "").ReadRecordingCover(context.Background(), pkg, "recording-1", "cover-1")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
}
