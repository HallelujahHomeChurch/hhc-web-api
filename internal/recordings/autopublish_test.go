package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type autoPendingRepo struct {
	captureTestRepository
	recording    Recording
	beforeUpdate func(*Capture)
}

func (r *autoPendingRepo) Get(context.Context, string) (Recording, error) { return r.recording, nil }
func (r *autoPendingRepo) RecordPackageReady(context.Context, string, string, time.Time) (Recording, error) {
	return r.recording, nil
}
func (r *autoPendingRepo) ListAutoPublicationCaptures(context.Context, time.Time, int) ([]Capture, error) {
	return []Capture{cloneCapture(r.capture)}, nil
}
func (r *autoPendingRepo) PublishCapture(context.Context, string, time.Time, bool, time.Time) error {
	panic("pending cover must not publish")
}
func (r *autoPendingRepo) UpdateCapture(ctx context.Context, id string, fn func(*Capture) error) (Capture, error) {
	if r.beforeUpdate != nil {
		r.beforeUpdate(&r.capture)
	}
	return r.captureTestRepository.UpdateCapture(ctx, id, fn)
}
func TestAutoPublicationCoverPendingRetry(t *testing.T) {
	for _, scenario := range []string{"pending", "failed", "expired", "unavailable", "conflict", "cancelled", "epoch", "capture"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
			pkg := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			repo := &autoPendingRepo{recording: Recording{ID: "recording", PackageID: pkg}}
			repo.capture = Capture{IntentID: "intent", RecordingID: "recording", NormalEnd: true, BroadcastEpoch: 1, AutoAttempts: 3, Status: CaptureStatus{AutoPublish: "pending", StopAcceptedAt: &now, RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: pkg, PackageID: &pkg, State: "ready"}}}
			if scenario == "conflict" {
				repo.recording.PackageID = "replacement"
			}
			switch scenario {
			case "cancelled":
				repo.beforeUpdate = func(c *Capture) { c.Status.AutoPublish = "cancelled" }
			case "epoch":
				repo.beforeUpdate = func(c *Capture) { c.BroadcastEpoch++ }
			case "capture":
				repo.beforeUpdate = func(c *Capture) { c.Status.CaptureID = "replacement" }
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/priv/recording-packages/"+pkg+"/ready" {
					expiry := now.Add(720 * time.Hour)
					json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: "recording", State: "ready", ReadyAt: &now, MediaExpiresAt: &expiry})
					return
				}
				if scenario == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if scenario == "failed" || scenario == "expired" {
					json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "auto-cover", UploadID: "auto-upload", Kind: "auto", State: scenario}}})
					return
				}
				w.Write([]byte(`{"items":[]}`))
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			err := svc.ReconcileAutoPublication(context.Background(), 100)
			if scenario == "unavailable" || scenario == "conflict" || scenario == "failed" || scenario == "expired" {
				if err == nil || errors.Is(err, ErrCoverPending) || repo.capture.AutoAttempts != 4 || repo.capture.AutoNextAttemptAt == nil || !repo.capture.AutoNextAttemptAt.Equal(now.Add(8*time.Minute)) {
					t.Fatalf("failure retry: %+v %v", repo.capture, err)
				}
				return
			}
			if !errors.Is(err, ErrCoverPending) {
				t.Fatalf("expected typed pending, got %v", err)
			}
			if repo.capture.AutoAttempts != 3 {
				t.Fatal("pending consumed failure attempt")
			}
			if scenario == "pending" {
				if repo.capture.AutoNextAttemptAt == nil || !repo.capture.AutoNextAttemptAt.Equal(now.Add(30*time.Second)) {
					t.Fatal("pending not scheduled at next reconciliation")
				}
			} else if repo.capture.AutoNextAttemptAt != nil {
				t.Fatal("stale attempt changed retry")
			}
		})
	}
}
