package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestRecordingDeletionVersionVisibilityAndReplay(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	actor := platform.NewID()
	v, err := repo.Create(ctx, "Delete test", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, v.ID)
	if err := repo.Delete(ctx, v.ID, v.Version+1, actor, now, true); !errors.Is(err, recordings.ErrPrecondition) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.member_recording SET status='published',uploaded_at=$2,expires_at=$2::timestamptz+interval '720 hours' WHERE id=$1`, v.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, v.ID, v.Version, actor, now, false); !errors.Is(err, recordings.ErrForbidden) {
		t.Fatalf("published delete without permission: %v", err)
	}
	if err := repo.Delete(ctx, v.ID, v.Version, actor, now, true); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, v.ID); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("still readable: %v", err)
	}
	if _, err := repo.UpdateTitle(ctx, v.ID, v.Version+1, "Changed", actor, now, true, nil); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("deleted edit: %v", err)
	}
	if err := repo.Delete(ctx, v.ID, v.Version, actor, now, true); err != nil {
		t.Fatalf("replay: %v", err)
	}
	pending, err := repo.ListPendingDeletions(ctx)
	if err != nil || len(pending) != 1 || pending[0] != v.ID {
		t.Fatalf("cleanup not durable: %v %v", pending, err)
	}
	if err := repo.MarkDeletionDelivered(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = repo.ListPendingDeletions(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("delivery not saved: %v %v", pending, err)
	}
}

func TestDeletionQueueRotatesFailedBatches(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		value, err := repo.Create(ctx, "Queue test", platform.NewID(), platform.NewID(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		id := value.ID
		if _, err := db.Exec(`UPDATE hhc_web.member_recording SET deleted_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		recordingCleanup(t, db, id)
	}
	first, err := repo.ListPendingDeletions(ctx)
	if err != nil || len(first) != 4 {
		t.Fatalf("first batch: %d %v", len(first), err)
	}
	for _, id := range first[:3] {
		if err := repo.MarkDeletionAttempt(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	second, err := repo.ListPendingDeletions(ctx)
	if err != nil || len(second) != 4 {
		t.Fatalf("second batch: %d %v", len(second), err)
	}
	if second[0] != first[3] {
		t.Fatal("failed oldest batch starved a later deletion")
	}
}

func TestRecordingManagementMigrationRetiresHiddenWithoutPublication(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	now := time.Now().UTC()
	value, err := repo.Create(context.Background(), "Hidden test", platform.NewID(), platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	if _, err := db.Exec(`UPDATE hhc_web.member_recording SET status='published',hidden=true,featured=true,uploaded_at=$2,expires_at=$2::timestamptz+interval '720 hours' WHERE id=$1`, value.ID, now); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile("../migrations/sql/065_recording_deletion.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Reapply only the data transformation to a migrated fixture.
	const marker = "UPDATE hhc_web.member_recording SET status="
	index := strings.Index(string(sql), marker)
	if index < 0 {
		t.Fatal("missing retirement transformation")
	}
	if _, err := db.Exec(string(sql)[index:]); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), value.ID)
	if err != nil || got.Status != "draft" || got.Featured || got.Hidden {
		t.Fatalf("hidden record exposed: %+v %v", got, err)
	}
	var notifications int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='recording.notification.submit' AND aggregate_id=$1`, value.ID).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if notifications != 0 {
		t.Fatal("migration emitted publication notification")
	}
}
