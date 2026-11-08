package postgres

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"testing"
	"time"
)

func TestAdminRecordingOriginSurvivesReplay(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	upload, err := repo.Create(ctx, "Upload", actor.ActorID, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, upload.ID)
	if _, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Phase = "archived"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Publication and archive phase must not turn a broadcast into an upload.
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET status='published',uploaded_at=$2,expires_at=$2::timestamptz+interval '720 hours' WHERE id=$1`, b.View.RecordingID, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		origin       string
		live, upload bool
	}{{"", true, true}, {"live", true, false}, {"upload", false, true}} {
		values, err := repo.ListAdmin(ctx, tc.origin)
		if err != nil {
			t.Fatal(err)
		}
		liveFound, uploadFound := false, false
		for _, v := range values {
			liveFound = liveFound || v.ID == b.View.RecordingID
			uploadFound = uploadFound || v.ID == upload.ID
		}
		if liveFound != tc.live || uploadFound != tc.upload {
			t.Fatalf("origin=%q live=%v upload=%v", tc.origin, liveFound, uploadFound)
		}
	}
}

func TestLiveRecordingLibraryContainsOnlyCompletedEvents(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	for _, phase := range []string{"draft", "scheduled", "preview", "start_pending", "live", "end_pending", "processing", "archived"} {
		if _, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			b.View.Phase = phase
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		rows, err := repo.ListAdmin(ctx, "live")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r.ID == b.View.RecordingID {
				found = true
			}
		}
		if found != (phase == "processing" || phase == "archived") {
			t.Fatalf("phase %s visible=%v", phase, found)
		}
	}
}
func TestRecordingAdminMetadataUsesActualPublicStart(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	started := now.Add(-time.Hour)
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{StartedAt}',to_jsonb($2::text)) WHERE recording_id=$1`, b.View.RecordingID, started.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	record, err := repo.Get(ctx, b.View.RecordingID)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Broadcast || record.BroadcastStartedAt == nil || !record.BroadcastStartedAt.Equal(started) {
		t.Fatalf("missing metadata: %+v", record)
	}
}

func TestHistoricalStartRequiresRetainedPreLiveEvidence(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	for _, phase := range []string{"live", "processing", "archived"} {
		_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			b.View.Phase = phase
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err := repo.Get(ctx, b.View.RecordingID)
	if err != nil || record.BroadcastStartedAt == nil {
		t.Fatalf("retained start missing: %+v %v", record, err)
	}
	if _, err = repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_signal WHERE recording_id=$1 AND phase='draft'`, b.View.RecordingID); err != nil {
		t.Fatal(err)
	}
	record, err = repo.Get(ctx, b.View.RecordingID)
	if err != nil || record.BroadcastStartedAt != nil {
		t.Fatal("incomplete history invented a start", err)
	}
}
