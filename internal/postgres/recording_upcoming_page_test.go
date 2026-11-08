package postgres

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"testing"
	"time"
)

func TestUpcomingMemberOrderingAndSearch(t *testing.T) {
	repo, _, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	want := []string{}
	for _, hours := range []int{3, 1, 2} {
		at := now.Add(time.Duration(hours) * time.Hour)
		b, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Future meeting", ScheduledAt: &at}, p.ActorID, now)
		if e != nil {
			t.Fatal(e)
		}
		recordingCleanup(t, repo.db, b.View.RecordingID)
		if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(jsonb_set(payload_json,'{View,announced}','true'),'{View,phase}','"scheduled"') WHERE recording_id=$1`, b.View.RecordingID); e != nil {
			t.Fatal(e)
		}
		if hours == 1 {
			want = append(want, b.View.RecordingID)
		}
	}
	first, e := repo.ListUpcomingBroadcastPage(ctx, now, nil, "", 1, "future meeting")
	if e != nil || len(first) != 1 || first[0].View.RecordingID != want[0] {
		t.Fatalf("first=%+v %v", first, e)
	}
	next, e := repo.ListUpcomingBroadcastPage(ctx, now, first[0].View.ScheduledAt, first[0].View.RecordingID, 10, "future meeting")
	if e != nil || len(next) != 2 || !next[0].View.ScheduledAt.Before(*next[1].View.ScheduledAt) {
		t.Fatalf("next=%+v %v", next, e)
	}
	empty, e := repo.ListUpcomingBroadcastPage(ctx, now, nil, "", 10, "unmatched")
	if e != nil || len(empty) != 0 {
		t.Fatalf("search %+v %v", empty, e)
	}
}
