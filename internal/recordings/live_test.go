package recordings

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"strings"
	"testing"
	"time"
)

func TestLiveStateUsesVerifiedWaterlineAndTrustedServerTimeline(t *testing.T) {
	start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                   string
		elapsed, media         float64
		seq                    int
		advanceAgo             time.Duration
		stopped, ended, normal bool
		want                   string
	}{
		{"startup", 60, 0, -1, 0, false, false, false, "starting"},
		{"startup stalled", 90, 0, -1, 0, false, false, false, "interrupted"},
		{"three segments", 100, 90, 2, 0, false, false, false, "live"},
		{"two segments", 89, 60, 1, 0, false, false, false, "starting"},
		{"backlog", 600, 300, 9, 0, false, false, false, "recovering"},
		{"same rate backlog", 660, 360, 11, 0, false, false, false, "recovering"},
		{"healthy two to three minute playback delay", 660, 600, 19, 0, false, false, false, "live"},
		{"safe playback at 180 seconds", 690, 600, 19, 0, false, false, false, "live"},
		{"safe playback over 180 seconds", 691, 600, 19, 0, false, false, false, "recovering"},
		{"stalled", 600, 300, 9, 90 * time.Second, false, false, false, "interrupted"},
		{"stop dominates", 600, 300, 9, 90 * time.Second, true, false, false, "ending"},
		{"crash cannot end", 600, 300, 9, 0, true, true, false, "ending"},
		{"normal tail", 600, 300, 9, 0, true, true, true, "ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := start.Add(time.Duration(tc.elapsed * float64(time.Second)))
			advanced := now.Add(-tc.advanceAgo)
			c := Capture{NormalEnd: tc.normal, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{State: "uploading", CreatedAt: start, ExpiresAt: start.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: tc.seq, MediaEndSeconds: tc.media}}}}
			if tc.seq >= 0 {
				c.Status.Progress.LastAdvancedAt = &advanced
			}
			if tc.stopped {
				c.Status.StopAcceptedAt = &now
			}
			if tc.ended {
				c.Status.Progress.Ended = true
				c.Status.Progress.EndedAt = &now
			}
			c.DeriveLiveState(now)
			if c.Status.LiveState != tc.want {
				t.Fatalf("state=%s want %s", c.Status.LiveState, tc.want)
			}
		})
	}
}

func TestLiveCloseByOtherPublishCapableStaffBindsReceiptAndRedactsInventory(t *testing.T) {
	service, repo, _, id, owner, _ := captureServiceFixture(t, nil)
	capture := strings.Repeat("a", 32)
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: owner, ActorType: "user", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", CreatedAt: service.now(), ExpiresAt: service.now().Add(24 * time.Hour), Objects: []assetclient.RecordingCaptureObject{{RecordingPackageObject: assetclient.RecordingPackageObject{Path: "480p/seg-000000.m4s"}}}}, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	staff := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), staff, "staff-close")
	// HTTP publish middleware supplies this trusted staff authorization.
	got, err := service.CloseCaptureLive(ctx, id, capture, staff, "close", true)
	if err != nil {
		t.Fatalf("other publish-capable staff cannot close: %v", err)
	}
	if got.Capture.LiveEnabled || got.Capture.AutoPublish != "pending" || len(got.Capture.Objects) != 0 {
		t.Fatalf("closure leaked inventory or cancelled auto: %+v", got)
	}
	again, err := service.CloseCaptureLive(ctx, id, capture, staff, "close", true)
	if err != nil || again.Receipt != got.Receipt {
		t.Fatalf("staff replay %v", err)
	}
	if _, err = service.CloseCaptureLive(auditclient.WithProvenance(context.Background(), owner, "other"), id, capture, owner, "close"); !errors.Is(err, ErrConflict) {
		t.Fatalf("other actor replayed staff receipt: %v", err)
	}
	if _, err = service.CancelCaptureAutoPublish(ctx, id, capture, staff, "cancel", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nonowner without publish capability: %v", err)
	}
}
