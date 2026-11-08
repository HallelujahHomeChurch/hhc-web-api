package postgres

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"testing"
)

func TestLiveNotificationCommitsOnceAfterPublicMedia(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	provenance, _ := auditclient.ProvenanceFromContext(ctx)
	actor := provenance.ActorID
	_, _, err := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor, recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: b.View.Revision}}, now)
	if err != nil {
		t.Fatal(err)
	}
	start := 0
	duration := 30.03
	update := func(last int) error {
		_, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, r *recordings.Recording, c *recordings.Capture) error {
			b.View.Announced = true
			b.View.Phase = "live"
			b.StartedAt = &now
			b.MemberState = "live"
			b.RangePending = false
			b.RangeApplied = b.View.Boundaries.Revision
			b.View.Boundaries.StartSequence = &start
			b.Projection = &assetclient.BroadcastProjection{State: "ready", Epoch: b.Epoch, RangeRevision: b.RangeApplied, StartSequence: &start, DurationSeconds: &duration}
			c.Status.CaptureID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			c.Status.LiveEnabled = true
			c.Status.Progress.LastSequence = last
			return nil
		})
		return e
	}
	if err = update(1); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if e := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='recording.notification.submit'`, b.View.RecordingID).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("notification before three segments")
	}
	if err = update(2); err != nil {
		t.Fatal(err)
	}
	if err = update(3); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("live notification missing or duplicated")
	}
}

func TestBroadcastVODDoesNotEnqueueRecordingNotification(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	packageID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET package_id=$2 WHERE id=$1`, b.View.RecordingID, packageID); err != nil {
		t.Fatal(err)
	}
	end := 3
	_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Phase = "processing"
		b.View.Boundaries.EndSequenceExclusive = &end
		b.RangePending = false
		b.Projection = &assetclient.BroadcastProjection{State: "ready"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := repo.RecordPackageReady(ctx, b.View.RecordingID, packageID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Publish(ctx, value.ID, value.Version, actor.ActorID, now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='recording.notification.submit'`, value.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("VOD notification=%d err=%v", n, err)
	}
}
