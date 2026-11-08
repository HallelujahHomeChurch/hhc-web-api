package postgres

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordingPublicIDCollisionAndReplay(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	first := "00000000-0000-4000-8000-000000000001"
	second := "00000000-0000-4000-8000-000000000002"
	fixed := func() (string, error) { return "abcdefghijk", nil }
	id, err := assignRecordingPublicID(ctx, tx, first, fixed)
	if err != nil || id != "abcdefghijk" {
		t.Fatalf("first: %q %v", id, err)
	}
	calls := 0
	replay, err := assignRecordingPublicID(ctx, tx, first, func() (string, error) { calls++; return "ABCDEFGHIJK", nil })
	if err != nil || replay != id || calls != 0 {
		t.Fatalf("replay changed ID: %q %v", replay, err)
	}
	if _, err = assignRecordingPublicID(ctx, tx, second, fixed); err == nil {
		t.Fatal("collision limit ignored")
	}
	// ON CONFLICT must leave the transaction usable after collision exhaustion.
	next, err := assignRecordingPublicID(ctx, tx, second, func() (string, error) { return "ABCDEFGHIJK", nil })
	if err != nil || next != "ABCDEFGHIJK" {
		t.Fatalf("case-sensitive assignment: %q %v", next, err)
	}
}

func TestRecordingPublicIDBackfillBatchesAndReplay(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	repo := NewRecordingRepository(db)
	ids := []string{"00000000-0000-4000-8000-000000000071", "00000000-0000-4000-8000-000000000072"}
	for _, id := range ids {
		_, err := db.ExecContext(ctx, `INSERT INTO hhc_web.member_recording(id,create_title,draft_expires_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1::uuid,'backfill',now()+interval '1 day',$1::text,'test','test',now(),now())`, id)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Exec(`DELETE FROM hhc_web.member_recording WHERE id=$1`, id)
			db.Exec(`DELETE FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, id)
		})
	}
	_, before, err := repo.PlanRecordingPublicIDs(ctx)
	if err != nil || before < 2 {
		t.Fatalf("plan: %d %v", before, err)
	}
	for range 2 {
		n, err := repo.BackfillRecordingPublicIDs(ctx, 1)
		if err != nil || n != 1 {
			t.Fatalf("batch: %d %v", n, err)
		}
	}
	// Existing mappings predate the generation filter and must remain permanent.
	const legacyID = "xxFuCkxxxxx"
	if _, err = db.Exec(`UPDATE hhc_web.member_recording_public_id SET public_id=$2 WHERE recording_id=$1`, ids[0], legacyID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := assignRecordingPublicID(ctx, tx, ids[0], func() (string, error) {
		t.Fatal("existing mapping invoked generator")
		return "", nil
	})
	tx.Rollback()
	if err != nil || replayed != legacyID {
		t.Fatalf("legacy replay: %q %v", replayed, err)
	}
	var original string
	if err = db.QueryRow(`SELECT public_id FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, ids[0]).Scan(&original); err != nil {
		t.Fatal(err)
	}
	resolved, err := repo.RecordingIDByPublicID(ctx, original)
	if err != nil || resolved != ids[0] {
		t.Fatalf("lookup: %q %v", resolved, err)
	}
	if len(original) != 11 {
		t.Fatal("wrong public ID length")
	}
	if _, err = repo.BackfillRecordingPublicIDs(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var replay string
	db.QueryRow(`SELECT public_id FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, ids[0]).Scan(&replay)
	if replay != original {
		t.Fatal("backfill reassigned an existing public ID")
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.member_recording WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordingIDByPublicID(ctx, original); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("deleted lookup: %v", err)
	}
	var reserved string
	if err = db.QueryRow(`SELECT public_id FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, ids[0]).Scan(&reserved); err != nil || reserved != original {
		t.Fatalf("deleted ID lost reservation: %q %v", reserved, err)
	}
}

func TestRecordingPublicIDGeneration(t *testing.T) {
	for range 100 {
		id, err := newRecordingPublicID()
		if err != nil || len(id) != 11 || strings.Trim(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
			t.Fatalf("invalid generated ID %q: %v", id, err)
		}
	}
}

func TestRecordingPublicIDCreateAndBackfillConcurrent(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	actor := "public-id-" + platform.NewID()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := repo.BackfillRecordingPublicIDs(ctx, 100)
			errs <- err
		})
	}
	created, err := repo.Create(ctx, "concurrent public ID", actor, actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM hhc_web.member_recording WHERE id=$1`, created.ID)
		db.Exec(`DELETE FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, created.ID)
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	replay, err := repo.Create(ctx, "concurrent public ID", actor, actor, time.Now())
	if err != nil || created.PublicID == "" || replay.PublicID != created.PublicID {
		t.Fatalf("atomic assignment/replay: %q %q %v", created.PublicID, replay.PublicID, err)
	}
	for _, invalid := range []string{"", "abcdefghij", "abcdefghijkl", "abcdefghij!"} {
		if _, err := db.Exec(`INSERT INTO hhc_web.member_recording_public_id(recording_id,public_id) VALUES($1,$2)`, platform.NewID(), invalid); err == nil {
			t.Fatalf("accepted invalid public ID %q", invalid)
		}
	}
}
