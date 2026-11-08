package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestLiveCoverLateAutoCannotReplaceCustomAndAutoCanBeReused(t *testing.T) {
	repo, c, now := captureFixture(t)
	resetLiveCoverDefault(t, repo)
	repo.WithAudit(NewAuditOutbox(repo.db))
	ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
	c, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.Repeat("c", 32)
		c.Status.State = "uploading"
		c.LiveCover.AutoUploadID = "auto-image"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var gets atomic.Int32
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auto-image") {
			if gets.Add(1) == 1 {
				close(entered)
				<-release
			}
			json.NewEncoder(w).Encode(assetclient.RecordingCoverUpload{UploadID: "auto-image", State: "ready"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/custom-image") {
			json.NewEncoder(w).Encode(assetclient.RecordingCoverUpload{UploadID: "custom-image", State: "ready"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/retain") || r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		t.Errorf("unexpected Asset operation %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}))
	defer asset.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))
	result := make(chan error, 1)
	go func() { result <- service.ReconcileLiveCovers(ctx, 100) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("auto was not read")
	}
	settings, err := service.SetLiveCoverSettings(ctx, c.RecordingID, c.Status.CaptureID, 1, recordings.LiveCoverSelection{Mode: "custom", UploadID: "custom-image"}, c.ActorID, platform.NewID())
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetCapture(ctx, c.IntentID, "")
	if err != nil || saved.LiveCover.Settings.CoverID != "custom-image" || saved.LiveCover.AutoState != "ready" {
		t.Fatalf("late auto changed active image %+v %v", saved.LiveCover, err)
	}
	settings, err = service.SetLiveCoverSettings(ctx, c.RecordingID, c.Status.CaptureID, settings.Revision, recordings.LiveCoverSelection{Mode: "auto"}, c.ActorID, platform.NewID())
	if err != nil || settings.CoverID != "auto-image" || settings.CoverState != "ready" {
		t.Fatalf("auto result was not reused %+v %v", settings, err)
	}
}

func TestLiveCoverPromotionProtectsSelectionBeforeAutomaticPublication(t *testing.T) {
	repo, c, now := captureFixture(t)
	resetLiveCoverDefault(t, repo)
	repo.WithAudit(NewAuditOutbox(repo.db))
	ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
	c, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.Repeat("d", 32)
		c.Status.State = "uploading"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	readyAt := now
	var ready atomic.Bool
	var retained atomic.Int32
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/uploads/custom-image"):
			json.NewEncoder(w).Encode(assetclient.RecordingCoverUpload{UploadID: "custom-image", State: "ready"})
		case strings.HasSuffix(path, "/promote"):
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(assetclient.LiveCoverPromotion{CoverID: "vod-cover-0", State: "pending"})
		case strings.HasSuffix(path, "/covers"):
			state := "pending"
			if ready.Load() {
				state = "ready"
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "vod-cover-0", UploadID: "vod-cover", Kind: "custom", State: state}, {ID: "ordinary-auto-1", UploadID: "ordinary-auto", Kind: "auto", State: "ready"}}})
		case r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/retain"):
			retained.Add(1)
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/ready"):
			until := readyAt.Add(720 * time.Hour)
			json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: c.Status.CaptureID, SessionID: c.Status.CaptureID, RecordingID: c.RecordingID, State: "ready", ReadyAt: &readyAt, MediaExpiresAt: &until})
		default:
			t.Errorf("unexpected %s %s", r.Method, path)
			w.WriteHeader(500)
		}
	}))
	defer asset.Close()
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
	defer account.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", "")).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	if _, err = service.SetLiveCoverSettings(ctx, c.RecordingID, c.Status.CaptureID, 1, recordings.LiveCoverSelection{Mode: "custom", UploadID: "custom-image"}, c.ActorID, platform.NewID()); err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.State = "ready"
		c.Status.PackageID = &c.Status.CaptureID
		c.Status.StopAcceptedAt = &now
		c.NormalEnd = true
		c.VerifiedReadyAt = &now
		c.Operations = map[string]recordings.CaptureOperation{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RecordPackageReady(ctx, c.RecordingID, c.Status.CaptureID, now); err != nil {
		t.Fatal(err)
	}
	if err = service.ReconcileAutoPublication(ctx, 100); !errors.Is(err, recordings.ErrCoverPending) {
		t.Fatal("expected typed promotion pending", err)
	}
	value, _ := repo.Get(ctx, c.RecordingID)
	if value.Status != "draft" || value.SelectedCoverID != "" {
		t.Fatal("selected or published pending cover", value)
	}
	ready.Store(true)
	now = now.Add(10 * time.Second)
	if err = service.ReconcileLiveCovers(ctx, 100); err != nil {
		t.Fatal(err)
	}
	inherited, err := repo.GetCapture(ctx, c.IntentID, c.RecordingID)
	if err != nil || inherited.AutoNextAttemptAt != nil {
		t.Fatalf("ready cover retained stale publication backoff: %+v %v", inherited.AutoNextAttemptAt, err)
	}
	if err = service.ReconcileAutoPublication(ctx, 100); err != nil {
		t.Fatal(err)
	}
	value, _ = repo.Get(ctx, c.RecordingID)
	if value.Status != "published" || value.SelectedCoverID != "vod-cover-0" {
		t.Fatal("lost inherited custom selection", value)
	}
	if err = service.ReconcileAutoPublication(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if retained.Load() != 2 {
		t.Fatal("retained/published again", retained.Load())
	}
	var notifications int
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='recording.notification.submit' AND aggregate_id::text=$1`, c.RecordingID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("notifications %d %v", notifications, err)
	}
}

func TestLiveCoverMemberReadFencesAStopDuringAssetFetch(t *testing.T) {
	repo, c, now := captureFixture(t)
	resetLiveCoverDefault(t, repo)
	var err error
	c, err = repo.PrepareCapture(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(context.Background(), c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.Repeat("e", 32)
		c.Status.State = "uploading"
		c.LiveCover.Settings.CoverState = "ready"
		c.LiveCover.Settings.CoverID = "auto-image"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := repo.UpdateCapture(context.Background(), c.IntentID, func(c *recordings.Capture) error { c.Status.StopAcceptedAt = &now; return nil })
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg"))
	}))
	defer asset.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))
	data, err := service.LiveCoverContent(context.Background(), c.RecordingID, c.Status.CaptureID, "", true)
	if !errors.Is(err, recordings.ErrNotFound) || len(data) != 0 {
		t.Fatalf("returned stopped live bytes %q %v", data, err)
	}
}
func TestLiveCoverAutoPublicationAfterIndependentInheritanceCleanup(t *testing.T) {
	repo, c, now := captureFixture(t)
	resetLiveCoverDefault(t, repo)
	repo.WithAudit(NewAuditOutbox(repo.db))
	ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
	c, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.Repeat("d", 32)
		c.Status.State = "uploading"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	readyAt := now
	var ready atomic.Bool
	var retained atomic.Int32
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/uploads/auto-image") || strings.HasSuffix(path, "/auto"):
			json.NewEncoder(w).Encode(assetclient.RecordingCoverUpload{UploadID: "auto-image", State: "ready"})
		case strings.HasSuffix(path, "/promote"):
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(assetclient.LiveCoverPromotion{CoverID: "vod-cover-1", State: "pending"})
		case strings.HasSuffix(path, "/covers"):
			state := "pending"
			if ready.Load() {
				state = "ready"
			}
			json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "vod-cover-1", UploadID: "vod-cover", Kind: "auto", State: state}, {ID: "ordinary-auto-1", UploadID: "ordinary-auto", Kind: "auto", State: "ready"}}})
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/retain"):
			retained.Add(1)
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/ready"):
			until := readyAt.Add(720 * time.Hour)
			json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: c.Status.CaptureID, SessionID: c.Status.CaptureID, RecordingID: c.RecordingID, State: "ready", ReadyAt: &readyAt, MediaExpiresAt: &until})
		default:
			t.Errorf("unexpected %s %s", r.Method, path)
			w.WriteHeader(500)
		}
	}))
	defer asset.Close()
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
	defer account.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", "")).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.State = "ready"
		c.Status.PackageID = &c.Status.CaptureID
		c.Status.StopAcceptedAt = &now
		c.NormalEnd = true
		c.VerifiedReadyAt = &now
		c.Operations = map[string]recordings.CaptureOperation{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RecordPackageReady(ctx, c.RecordingID, c.Status.CaptureID, now); err != nil {
		t.Fatal(err)
	}
	if err = service.ReconcileAutoPublication(ctx, 100); !errors.Is(err, recordings.ErrCoverPending) {
		t.Fatal("expected typed promotion pending", err)
	}
	value, _ := repo.Get(ctx, c.RecordingID)
	if value.Status != "draft" || value.SelectedCoverID != "" {
		t.Fatal("selected or published pending cover", value)
	}
	ready.Store(true)
	now = now.Add(10 * time.Minute)
	if err = service.ReconcileLiveCovers(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var released bool
	if err = repo.db.QueryRow(`SELECT released_at IS NOT NULL FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, c.LiveCover.AutoReferenceID).Scan(&released); err != nil || !released {
		t.Fatal("auto reference not cleaned", released, err)
	}
	if err = service.ReconcileAutoPublication(ctx, 100); err != nil {
		t.Fatal(err)
	}
	value, _ = repo.Get(ctx, c.RecordingID)
	if value.Status != "published" || value.SelectedCoverID != "vod-cover-1" {
		t.Fatal("lost inherited custom selection", value)
	}
	if err = service.ReconcileAutoPublication(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if retained.Load() != 2 {
		t.Fatal("retained/published again", retained.Load())
	}
	var notifications int
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='recording.notification.submit' AND aggregate_id::text=$1`, c.RecordingID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("notifications %d %v", notifications, err)
	}
}
