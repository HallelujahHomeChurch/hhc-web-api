package postgres

import (
	"context"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestUnchangedCaptureKeepsTupleAndPersistsChanges(t *testing.T) {
	repo, capture, now := captureFixture(t)
	ctx := context.Background()
	if _, err := repo.PrepareCapture(ctx, capture, now); err != nil {
		t.Fatal(err)
	}
	noop := func(*recordings.Capture) error { return nil }
	if _, err := repo.UpdateCapture(ctx, capture.IntentID, noop); err != nil {
		t.Fatal(err)
	}
	tuple := func() string {
		t.Helper()
		var value string
		if err := repo.db.QueryRow(`SELECT xmin::text || ':' || ctid::text FROM hhc_web.member_recording_capture WHERE id=$1`, capture.IntentID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := tuple()
	if _, err := repo.UpdateCapture(ctx, capture.IntentID, noop); err != nil {
		t.Fatal(err)
	}
	if tuple() != before {
		t.Error("unchanged capture rewrote its tuple")
	}
	if _, err := repo.UpdateCapture(ctx, capture.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = "0123456789abcdef0123456789abcdef"
		c.Terminate("aborted", "test", now)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var id, state string
	var active bool
	if err := repo.db.QueryRow(`SELECT capture_id,active,payload_json->'Status'->>'state' FROM hhc_web.member_recording_capture WHERE id=$1`, capture.IntentID).Scan(&id, &active, &state); err != nil {
		t.Fatal(err)
	}
	if id != "0123456789abcdef0123456789abcdef" || active || state != "aborted" || tuple() == before {
		t.Fatal("changed capture or its projections were not persisted")
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_capture SET capture_id=NULL,active=true WHERE id=$1`, capture.IntentID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateCapture(ctx, capture.IntentID, noop); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.QueryRow(`SELECT capture_id,active FROM hhc_web.member_recording_capture WHERE id=$1`, capture.IntentID).Scan(&id, &active); err != nil || id == "" || active {
		t.Fatalf("unchanged payload did not repair projections: id=%q active=%v err=%v", id, active, err)
	}
}

func TestUnchangedBroadcastKeepsTupleAndPersistsChanges(t *testing.T) {
	repo, broadcast, ctx, now := broadcastFixture(t)
	noop := func(context.Context, *recordings.Broadcast, *recordings.Recording, *recordings.Capture) error {
		return nil
	}
	if _, err := repo.UpdateBroadcast(ctx, broadcast.View.RecordingID, "", now, noop); err != nil {
		t.Fatal(err)
	}
	tuple := func() string {
		t.Helper()
		var value string
		if err := repo.db.QueryRow(`SELECT xmin::text || ':' || ctid::text FROM hhc_web.member_recording_broadcast WHERE recording_id=$1`, broadcast.View.RecordingID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := tuple()
	if _, err := repo.UpdateBroadcast(ctx, broadcast.View.RecordingID, "", now, noop); err != nil {
		t.Fatal(err)
	}
	if tuple() != before {
		t.Error("unchanged broadcast rewrote its tuple")
	}
	if _, err := repo.UpdateBroadcast(ctx, broadcast.View.RecordingID, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := repo.GetBroadcast(ctx, broadcast.View.RecordingID)
	if err != nil || fresh.View.Revision != broadcast.View.Revision+1 || tuple() == before {
		t.Fatalf("changed broadcast not persisted: revision=%d err=%v", fresh.View.Revision, err)
	}
}
