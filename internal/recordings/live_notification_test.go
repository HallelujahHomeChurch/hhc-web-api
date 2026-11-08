package recordings

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"testing"
	"time"
)

func TestLiveNotificationRequiresPlayablePublicRange(t *testing.T) {
	now := time.Now()
	start := 7
	duration := 30.03
	b := Broadcast{Epoch: 1, MemberState: "live", RangeApplied: 3, View: BroadcastView{Announced: true, Phase: "live", Boundaries: BroadcastBoundaries{Revision: 3, StartSequence: &start}}, Projection: &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 3, State: "ready", StartSequence: &start, DurationSeconds: &duration}}
	c := Capture{Status: CaptureStatus{LiveEnabled: true, RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: "capture", State: "uploading", ExpiresAt: now.Add(time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: 8}}}}
	if b.LiveNotificationReady(c, now) {
		t.Fatal("only two public segments")
	}
	c.Status.Progress.LastSequence = 9
	if !b.LiveNotificationReady(c, now) {
		t.Fatal("playable range not ready")
	}
	b.RangePending = true
	if b.LiveNotificationReady(c, now) {
		t.Fatal("unapplied authority")
	}
	b.RangePending = false
	b.View.Phase = "processing"
	if b.LiveNotificationReady(c, now) {
		t.Fatal("archive cannot notify live")
	}
}
