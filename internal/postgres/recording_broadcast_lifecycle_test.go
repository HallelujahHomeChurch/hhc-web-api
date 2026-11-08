package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestBroadcastScheduledDraftRetentionStartsAfterScheduledTime(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	scheduled := now.Add(30 * 24 * time.Hour)
	_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.ScheduledAt = &scheduled
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(8 * 24 * time.Hour), scheduled, scheduled.Add(7 * 24 * time.Hour)} {
		if err = repo.ReconcileBroadcastCleanup(ctx, at); err != nil {
			t.Fatal(err)
		}
		if _, err = repo.GetBroadcast(ctx, b.View.RecordingID); err != nil {
			t.Fatalf("scheduled draft removed at %s: %v", at, err)
		}
	}
	if err = repo.ReconcileBroadcastCleanup(ctx, scheduled.Add(7*24*time.Hour+time.Second)); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	if err = repo.db.QueryRow(`SELECT deleted_at IS NOT NULL FROM hhc_web.member_recording WHERE id=$1`, b.View.RecordingID).Scan(&deleted); err != nil || !deleted {
		t.Fatalf("abandoned scheduled draft not retired: %v %v", deleted, err)
	}
}

func TestBroadcastReconciliationSkipsSettledHistoryAndKeepsPendingWork(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	// Fill the first batch with old, settled archives; the live capture is newer.
	for i := 0; i < 110; i++ {
		past, err := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Settled history"}, actor.ActorID, now.Add(-24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		recordingCleanup(t, repo.db, past.View.RecordingID)
		past.View.Phase = "archived"
		past.View.Archive.State = "published"
		past.CaptureIntentID = platform.NewID()
		raw, _ := json.Marshal(past)
		if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=$2,checked_at=$3 WHERE recording_id=$1`, past.View.RecordingID, raw, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	b.View.Phase = "live"
	b.CaptureIntentID = platform.NewID()
	raw, _ := json.Marshal(b)
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=$2,checked_at=$3 WHERE recording_id=$1`, b.View.RecordingID, raw, now); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	picked, err := repo.ListBroadcastReconciliation(ctx, now.Add(time.Second), 100)
	t.Logf("110 settled archives + 1 live: picked=%d, query=%s", len(picked), time.Since(started))
	if err != nil {
		t.Fatal(err)
	}
	if len(picked) != 1 || picked[0].View.RecordingID != b.View.RecordingID {
		t.Fatalf("history displaced live work: picked=%d", len(picked))
	}
	// A revoked/deleted archive with authority still pending must never be skipped.
	b.View.Phase = "archived"
	b.Revoked = true
	b.RangePending = true
	raw, _ = json.Marshal(b)
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=$2 WHERE recording_id=$1`, b.View.RecordingID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, b.View.RecordingID, now); err != nil {
		t.Fatal(err)
	}
	picked, err = repo.ListBroadcastReconciliation(ctx, now.Add(2*time.Second), 100)
	if err != nil || len(picked) != 1 || !picked[0].RangePending {
		t.Fatalf("pending revocation skipped: %+v %v", picked, err)
	}
}

func TestBroadcastReconciliationRetainsAuthorityCoverAndBindingWork(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	for _, tc := range []struct {
		name, phase                                                   string
		capture, revoked, pending, custom, expired, unpublished, want bool
	}{
		{name: "idle scheduled draft", phase: "draft"},
		{name: "late binding", phase: "preview", capture: true, want: true},
		{name: "settled archive", phase: "archived", capture: true},
		{name: "cancelled", phase: "cancelled", capture: true, revoked: true},
		{name: "failed", phase: "failed", capture: true, revoked: true},
		{name: "revocation pending", phase: "failed", capture: true, revoked: true, pending: true, want: true},
		{name: "archive cover pending", phase: "archived", custom: true, want: true},
		{name: "revoked cover", phase: "cancelled", revoked: true, custom: true},
		{name: "expired archive authority", phase: "archived", capture: true, expired: true, want: true},
		{name: "unpublished archive authority", phase: "archived", capture: true, unpublished: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := b
			v.View.Phase = tc.phase
			v.Revoked = tc.revoked
			v.RangePending = tc.pending
			if tc.capture {
				v.CaptureIntentID = platform.NewID()
			}
			if tc.phase == "archived" {
				v.MemberState = "vod"
			}
			if tc.custom {
				v.CoverSnapshot.Settings = recordings.LiveCoverSettings{Selection: recordings.LiveCoverSelection{Mode: "custom", UploadID: "pending-cover"}, CoverState: "waiting"}
			}
			raw, _ := json.Marshal(v)
			if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=$2,checked_at=NULL WHERE recording_id=$1`, b.View.RecordingID, raw); err != nil {
				t.Fatal(err)
			}
			expires := now.Add(time.Hour)
			if tc.expired {
				expires = now.Add(-time.Second)
			}
			status := "published"
			if tc.unpublished {
				status = "draft"
			}
			if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET status=$2,uploaded_at=$3,expires_at=$4 WHERE id=$1`, b.View.RecordingID, status, now.Add(-time.Hour), expires); err != nil {
				t.Fatal(err)
			}
			picked, err := repo.ListBroadcastReconciliation(ctx, now, 100)
			if err != nil || (len(picked) == 1) != tc.want || len(picked) > 1 {
				t.Fatalf("picked=%d want=%v err=%v", len(picked), tc.want, err)
			}
		})
	}
	// The rotating checked_at order prevents one eligible item monopolizing a small batch.
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	other, err := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Pending cover"}, actor.ActorID, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, other.View.RecordingID)
	other.CoverSnapshot.Settings = recordings.LiveCoverSettings{Selection: recordings.LiveCoverSelection{Mode: "custom", UploadID: "pending-cover"}, CoverState: "waiting"}
	raw, _ := json.Marshal(other)
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=$2 WHERE recording_id=$1`, other.View.RecordingID, raw); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ListBroadcastReconciliation(ctx, now.Add(time.Second), 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := repo.ListBroadcastReconciliation(ctx, now.Add(2*time.Second), 1)
	if err != nil || len(second) != 1 || first[0].View.RecordingID == second[0].View.RecordingID {
		t.Fatalf("eligible work starved: first=%+v second=%+v %v", first, second, err)
	}
}
