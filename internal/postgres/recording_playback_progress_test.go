package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestProgressFirstClaimAndLateWrites(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	id, user := platform.NewID(), platform.NewID()
	const pkg = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,'Video','Video',$2,$2::timestamptz+interval '720 hours',$2,'published',$3,$2,$4,'test','test',$2,$2)`, id, now, pkg, platform.NewID())
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, id)
	version := "vod:" + pkg
	a := recordings.ProgressClaim{SessionID: platform.NewID(), ClaimID: platform.NewID(), MediaVersion: version, StartKind: "resume", ObservedPositionSeconds: 600}
	b := recordings.ProgressClaim{SessionID: platform.NewID(), ClaimID: platform.NewID(), MediaVersion: version, StartKind: "resume", ObservedPositionSeconds: 600}
	type result struct {
		claim recordings.ProgressClaim
		owner recordings.ProgressOwner
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, claim := range []recordings.ProgressClaim{a, b} {
		wg.Go(func() {
			owner, err := repo.ClaimPlaybackProgress(ctx, id, user, claim, 660, now)
			results <- result{claim, owner, err}
		})
	}
	wg.Wait()
	close(results)
	var winner, loser result
	for r := range results {
		switch {
		case r.err == nil:
			winner = r
		case errors.Is(r.err, recordings.ErrProgressConflict):
			loser = r
		default:
			t.Fatal(r.err)
		}
	}
	if winner.owner.Generation == "" || loser.claim.ClaimID == "" {
		t.Fatalf("expected one successful claim and one conflict")
	}
	loser.claim.ExpectedGeneration = &winner.owner.Generation
	loser.claim.ClaimID = platform.NewID()
	next, err := repo.ClaimPlaybackProgress(ctx, id, user, loser.claim, 660, now)
	if err != nil {
		t.Fatal(err)
	}
	rewind := recordings.ProgressUpdate{SessionID: loser.claim.SessionID, Generation: next.Generation, Sequence: 1, MediaVersion: version, PositionSeconds: 480, State: "playing"}
	if result, err := repo.UpdatePlaybackProgress(ctx, id, user, rewind, now); err != nil || !result.Accepted {
		t.Fatalf("rewind: %+v %v", result, err)
	}
	rewind.PositionSeconds = 600
	if result, err := repo.UpdatePlaybackProgress(ctx, id, user, rewind, now); err != nil || result.Accepted || result.Sequence != 1 {
		t.Fatalf("duplicate: %+v %v", result, err)
	}
	late := recordings.ProgressUpdate{SessionID: winner.claim.SessionID, Generation: winner.owner.Generation, Sequence: 2, MediaVersion: version, PositionSeconds: 660, State: "ended"}
	if _, err := repo.UpdatePlaybackProgress(ctx, id, user, late, now); !errors.Is(err, recordings.ErrProgressSuperseded) {
		t.Fatalf("old ended: %v", err)
	}
	if _, err := repo.ClaimPlaybackProgress(ctx, id, user, winner.claim, 660, now); !errors.Is(err, recordings.ErrProgressConflict) {
		t.Fatalf("old claim retry: %v", err)
	}
	if result, err := repo.ClaimPlaybackProgress(ctx, id, user, loser.claim, 660, now); err != nil || result.Generation != next.Generation || result.PositionSeconds != 480 || result.Sequence != 1 {
		t.Fatalf("current claim retry: %+v %v", result, err)
	}
	snapshot, err := repo.GetPlaybackProgress(ctx, id, user, now)
	if err != nil || snapshot.Progress == nil || snapshot.Progress.Completed || snapshot.Progress.PositionSeconds != 480 || snapshot.MediaVersion != version {
		t.Fatalf("snapshot after old ended: %+v %v", snapshot, err)
	}
	item, err := repo.GetContinueWatching(ctx, user, now)
	if err != nil || item == nil || item.RecordingID != id || item.PositionSeconds != 480 || item.SessionID != loser.claim.SessionID || item.Generation != next.Generation {
		t.Fatalf("resume item after rewind: %+v %v", item, err)
	}
	if other, err := repo.GetContinueWatching(ctx, platform.NewID(), now); err != nil || other != nil {
		t.Fatalf("another account received resume owner: %+v %v", other, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.member_recording SET hidden=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if item, err = repo.GetContinueWatching(ctx, user, now); err != nil || item != nil {
		t.Fatalf("hidden resume item leaked: %+v %v", item, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.member_recording SET hidden=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	ended := rewind
	ended.Sequence, ended.PositionSeconds, ended.State = 2, 660, "ended"
	if _, err := repo.UpdatePlaybackProgress(ctx, id, user, ended, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err = repo.GetPlaybackProgress(ctx, id, user, now)
	if err != nil || snapshot.Progress == nil || !snapshot.Progress.Completed {
		t.Fatalf("owner ended did not complete: %+v %v", snapshot, err)
	}
	const replacement = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := db.Exec(`UPDATE hhc_web.member_recording SET package_id=$2 WHERE id=$1`, id, replacement); err != nil {
		t.Fatal(err)
	}
	snapshot, err = repo.GetPlaybackProgress(ctx, id, user, now)
	if err != nil || snapshot.Progress != nil || snapshot.MediaVersion != "vod:"+replacement {
		t.Fatalf("old progress leaked onto replacement: %+v %v", snapshot, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.member_recording SET package_id=$2 WHERE id=$1`, id, pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.member_recording_progress SET expires_at=$3 WHERE account_id=$1 AND recording_id=$2`, user, id, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Repository{db: db}).PurgeWatermarkRecords(ctx, now, 100); err != nil {
		t.Fatal(err)
	}
	var progressRemaining int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_progress WHERE account_id=$1 AND recording_id=$2`, user, id).Scan(&progressRemaining); err != nil || progressRemaining != 0 {
		t.Fatalf("expired progress remaining=%d err=%v", progressRemaining, err)
	}

	rewind.Sequence++
	if _, err := repo.UpdatePlaybackProgress(ctx, id, user, rewind, now); !errors.Is(err, recordings.ErrProgressSuperseded) {
		t.Fatalf("late write recreated deleted row: %v", err)
	}

	if _, err := repo.ClaimPlaybackProgress(ctx, id, user, winner.claim, 660, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1`, watermarkSubjectHash(user)); err != nil {
			t.Error(err)
		}
	})
	if _, _, err := (&Repository{db: db}).CleanupWatermarkAccount(ctx, user, platform.NewID()); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_progress WHERE account_id=$1`, user).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("erased progress remaining=%d err=%v", remaining, err)
	}
	if _, err := repo.ClaimPlaybackProgress(ctx, id, user, winner.claim, 660, now); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("erased user reclaimed progress: %v", err)
	}

}

func TestProgressLiveBlocksWhenBroadcastAuthorityCloses(t *testing.T) {
	repo, broadcast, ctx, now := subjectLiveFixture(t)
	id, user := broadcast.View.RecordingID, platform.NewID()
	setAuthority := func(value string) {
		t.Helper()
		_, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{MemberState}',to_jsonb($2::text)) WHERE recording_id=$1`, id, value)
		if err != nil {
			t.Fatal(err)
		}
	}
	setAuthority("live")
	version := "live:" + broadcast.View.Binding.CaptureID
	claim := recordings.ProgressClaim{SessionID: platform.NewID(), ClaimID: platform.NewID(), MediaVersion: version, StartKind: "resume", ObservedPositionSeconds: 120}
	owner, err := repo.ClaimPlaybackProgress(ctx, id, user, claim, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdatePlaybackProgress(ctx, id, user, recordings.ProgressUpdate{SessionID: claim.SessionID, Generation: owner.Generation, MediaVersion: version, Sequence: 1, PositionSeconds: 121, State: "playing"}, now); err != nil {
		t.Fatal(err)
	}
	item, err := repo.GetContinueWatching(ctx, user, now)
	if err != nil || item == nil || item.SessionID != claim.SessionID || item.Mode != "live" || item.CaptureID != broadcast.View.Binding.CaptureID || item.DurationSeconds != nil {
		t.Fatalf("live resume item: %+v %v", item, err)
	}
	setAuthority("blocked")
	if item, err = repo.GetContinueWatching(ctx, user, now); err != nil || item != nil {
		t.Fatalf("blocked live title leaked: %+v %v", item, err)
	}
	update := recordings.ProgressUpdate{SessionID: claim.SessionID, Generation: owner.Generation, MediaVersion: version, Sequence: 1, PositionSeconds: 121, State: "playing"}
	if _, err = repo.UpdatePlaybackProgress(ctx, id, user, update, now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("blocked live wrote progress: %v", err)
	}
}

func TestContinueWatchingSkipsNewerEmptyClaims(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	user := platform.NewID()

	var validID string
	for i, kind := range []string{"valid", "zero", "claim-only"} {
		id := platform.NewID()
		pkg := strings.Repeat(string(rune('a'+i)), 32)
		_, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,'Fixture','Fixture',$2,$2::timestamptz+interval '720 hours',$2,'published',$3,$2,$4,'test','test',$2,$2)`, id, now, pkg, platform.NewID())
		if err != nil {
			t.Fatal(err)
		}
		recordingCleanup(t, db, id)
		claimed := now.Add(time.Duration(i) * time.Minute)
		claim := recordings.ProgressClaim{SessionID: platform.NewID(), ClaimID: platform.NewID(), MediaVersion: "vod:" + pkg, StartKind: "resume", ObservedPositionSeconds: 65}
		owner, err := repo.ClaimPlaybackProgress(ctx, id, user, claim, 660, claimed)
		if err != nil {
			t.Fatal(err)
		}
		if kind != "claim-only" {
			position := 65.0
			if kind == "zero" {
				position = 0
			}
			_, err = repo.UpdatePlaybackProgress(ctx, id, user, recordings.ProgressUpdate{SessionID: claim.SessionID, Generation: owner.Generation, MediaVersion: claim.MediaVersion, Sequence: 1, PositionSeconds: position, State: "paused"}, claimed.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			// A nonzero observed starting position is still not an accepted progress update.
			if _, err := db.Exec(`UPDATE hhc_web.member_recording_progress SET position_seconds=65 WHERE account_id=$1 AND recording_id=$2`, user, id); err != nil {
				t.Fatal(err)
			}
		}
		if kind == "valid" {
			validID = id
		}
	}
	item, err := repo.GetContinueWatching(ctx, user, now.Add(3*time.Minute))
	if err != nil || item == nil || item.RecordingID != validID {
		t.Fatalf("new empty claim hid valid resume: %+v %v", item, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.member_recording_progress SET completed=true WHERE account_id=$1 AND recording_id=$2`, user, validID); err != nil {
		t.Fatal(err)
	}
	if item, err := repo.GetContinueWatching(ctx, user, now.Add(3*time.Minute)); err != nil || item != nil {
		t.Fatalf("completed or empty candidate leaked: %+v %v", item, err)
	}
}
