package recordings

import (
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"testing"
	"time"
)

func TestScheduledBroadcastSurvivesUploadDraftExpiry(t *testing.T) {
	now := time.Now()
	r := Recording{DraftExpiresAt: now.Add(-time.Hour), Broadcast: true}
	if r.Expired(now) {
		t.Fatal("scheduled broadcast expired as upload draft")
	}
}
func TestCLIDraftStillExpires(t *testing.T) {
	now := time.Now()
	if !(Recording{DraftExpiresAt: now.Add(-time.Hour)}).Expired(now) {
		t.Fatal("CLI expiry lost")
	}
}
func boundBroadcast() Broadcast {
	return Broadcast{View: BroadcastView{Revision: 2, Phase: "preview", Announced: true, Binding: &BroadcastBinding{CaptureID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Epoch: 1, ActorID: "uploader"}, Boundaries: BroadcastBoundaries{Revision: 1}}, Commands: map[string]BroadcastCommand{}}
}
func TestAckSurvivesMetadataRevisionChange(t *testing.T) {
	b := boundBroadcast()
	now := time.Now()
	c, e := b.RequestCommand("start", now)
	if e != nil {
		t.Fatal(e)
	}
	b.View.Revision += 10
	e = b.Acknowledge(c.CommandID, "uploader", BroadcastMarkerAckInput{CaptureID: c.CaptureID, Epoch: 1, BoundarySequence: 8}, false, now)
	if e != nil || b.View.Boundaries.StartSequence == nil || *b.View.Boundaries.StartSequence != 8 {
		t.Fatalf("ack lost: %+v %v", b, e)
	}
	if b.View.Phase != "start_pending" {
		t.Fatal("unverified ack became live")
	}
}
func TestStopEndRaceKeepsOneBoundary(t *testing.T) {
	b := boundBroadcast()
	start := 8
	b.View.Boundaries.StartSequence = &start
	b.View.Phase = "live"
	c, e := b.RequestCommand("end", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	input := BroadcastMarkerAckInput{CaptureID: c.CaptureID, Epoch: 1, BoundarySequence: 20}
	if e = b.Acknowledge(c.CommandID, "uploader", input, true, time.Now()); e != nil {
		t.Fatal(e)
	}
	input.BoundarySequence = 21
	if e = b.Acknowledge(c.CommandID, "uploader", input, true, time.Now()); !errors.Is(e, ErrConflict) {
		t.Fatalf("boundary changed %v", e)
	}
	if *b.View.Boundaries.EndSequenceExclusive != 20 {
		t.Fatal("boundary overwritten")
	}
}
func TestBoundPolicyImmutableAndNoImplicitAutoStart(t *testing.T) {
	b := boundBroadcast()
	if b.CanChangePolicy() {
		t.Fatal("bound policy mutable")
	}
	b.View.Announced = false
	if _, e := b.RequestCommand("start", time.Now()); !errors.Is(e, ErrConflict) {
		t.Fatal("unannounced start accepted")
	}
}
func TestMarkerOwnerEpochAndStoppedStart(t *testing.T) {
	for _, tc := range []struct {
		actor   string
		epoch   int64
		stopped bool
	}{{"other", 1, false}, {"uploader", 2, false}, {"uploader", 1, true}} {
		b := boundBroadcast()
		c, _ := b.RequestCommand("start", time.Now())
		if e := b.Acknowledge(c.CommandID, tc.actor, BroadcastMarkerAckInput{CaptureID: c.CaptureID, Epoch: tc.epoch, BoundarySequence: 0}, tc.stopped, time.Now()); e == nil {
			t.Fatal("invalid marker accepted")
		}
	}
}

func TestBroadcastArchivePublicationReason(t *testing.T) {
	next := time.Now().Add(time.Minute)
	for _, tc := range []struct {
		name, status       string
		enabled, published bool
		retry              *time.Time
		reason             string
	}{
		{name: "pending", status: "pending", enabled: true},
		{name: "retry", status: "pending", enabled: true, retry: &next, reason: "auto_publish_retrying"},
		{name: "disabled bridge", status: "cancelled", reason: "auto_publish_disabled"},
		{name: "cancelled", status: "cancelled", enabled: true, reason: "auto_publish_cancelled"},
		{name: "blocked", status: "blocked", enabled: true, reason: "auto_publish_blocked"},
		{name: "published capture", status: "published", enabled: true},
		{name: "manually published", status: "cancelled", published: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stale := "auto_publish_blocked"
			b := Broadcast{View: BroadcastView{Policy: BroadcastPolicy{AutoPublish: tc.enabled}, Archive: BroadcastArchive{Reason: &stale}}}
			r := Recording{Status: "draft"}
			if tc.published {
				r.Status = "published"
			}
			c := Capture{Status: CaptureStatus{AutoPublish: tc.status}, AutoNextAttemptAt: tc.retry}
			b.updateArchivePublication(r, c)
			got := ""
			if b.View.Archive.Reason != nil {
				got = *b.View.Archive.Reason
			}
			if got != tc.reason {
				t.Fatalf("reason=%q want=%q", got, tc.reason)
			}
			published := tc.published || tc.status == "published"
			if published && (b.View.Phase != "archived" || b.View.Archive.State != "published") || !published && (b.View.Phase != "processing" || b.View.Archive.State != "ready") {
				t.Fatalf("wrong publication projection %+v", b.View)
			}
			if c.Status.AutoPublish != tc.status {
				t.Fatal("reason changed publication intent")
			}
		})
	}
}

func TestPublicStartIsStableAcrossRecovery(t *testing.T) {
	b := boundBroadcast()
	now := time.Now().UTC()
	b.markLive(now)
	b.markLive(now.Add(time.Hour))
	if b.StartedAt == nil || !b.StartedAt.Equal(now) || b.View.Phase != "live" {
		t.Fatal("first public start changed")
	}
}

func TestPublicEndRequiresConfirmedBoundaryAndPreservesFirstObservation(t *testing.T) {
	now := time.Now().UTC()
	start, end := 7, 16
	for _, tc := range []struct {
		name              string
		started           bool
		phase, projection string
		end               *int
		revoked           bool
		want              bool
	}{
		{"pending boundary", true, "end_pending", "pending", &end, false, false},
		{"ongoing", true, "live", "ready", nil, false, false},
		{"confirmed", true, "end_pending", "ready", &end, false, true},
		{"never public", false, "processing", "ready", &end, false, false},
		{"historical archive", true, "archived", "ready", &end, false, false},
		{"historical archive withdrawal", true, "archived", "failed", &end, true, false},
		{"revocation confirmed", true, "live", "failed", nil, true, true},
		{"revocation pending", true, "live", "pending", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := boundBroadcast()
			if tc.started {
				at := now.Add(-time.Hour)
				b.StartedAt = &at
			}
			b.View.Phase = tc.phase
			if tc.revoked {
				b.RevokePublic()
				b.RangePending = true
				b.View.Phase = "processing"
			}
			b.View.Boundaries.StartSequence = &start
			b.View.Boundaries.EndSequenceExclusive = tc.end
			b.Projection = &assetclient.BroadcastProjection{State: tc.projection}
			b.observePublicEnd(now)
			b.observePublicEnd(now.Add(time.Minute))
			if (b.EndedAt != nil) != tc.want || b.EndedAt != nil && !b.EndedAt.Equal(now) {
				t.Fatalf("wrong public end: %v", b.EndedAt)
			}
		})
	}
}
