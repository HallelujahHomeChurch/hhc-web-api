package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestLifecycleCheckpointLeaseAndAtomicProjection(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, _ = db.Exec(`DELETE FROM hhc_web.member_recording_lifecycle_checkpoint`)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.member_recording_lifecycle_checkpoint`) })
	record, err := repo.Create(ctx, "lifecycle", platform.NewID(), platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, record.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	if err = repo.AddPackage(ctx, record.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimLifecycleCycle(ctx, now)
	if err != nil || claim.Token == "" {
		t.Fatal(claim, err)
	}
	other, err := repo.ClaimLifecycleCycle(ctx, now)
	if err != nil || other.Token != "" {
		t.Fatal("duplicate owner", other, err)
	}
	expires := now.Add(60 * 24 * time.Hour)
	snapshot := assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{{RecordingID: record.ID, PackageID: pkg, SessionID: pkg, State: "ready", UploadedAt: &now, ReadyAt: &now, MediaExpiresAt: &expires, RetentionRevision: 2}}}
	state := recordings.LifecycleCheckpoint{Cursor: record.ID, Repairs: []recordings.LifecycleRepair{{Binding: assetclient.RecordingLifecycleBinding{RecordingID: record.ID, PackageID: pkg}, Attempts: 3, FirstFailureAt: now, NextAttempt: now.Add(time.Minute), Reason: "missing"}}}
	if err = repo.CommitLifecycleCycle(ctx, claim.Token, state, snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, record.ID)
	if err != nil || got.ReadyAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatal("projection", got, err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.member_recording_lifecycle_checkpoint SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	newer, err := repo.ClaimLifecycleCycle(ctx, now)
	if err != nil || newer.Token == "" || newer.State.Cursor != record.ID || len(newer.State.Repairs) != 1 {
		t.Fatal("restart", newer, err)
	}
	state.Cursor = ""
	expires = expires.Add(24 * time.Hour)
	snapshot.Items[0].RetentionRevision = 3
	if err = repo.CommitLifecycleCycle(ctx, claim.Token, state, snapshot); err == nil {
		t.Fatal("stale lease applied")
	}
	got, err = repo.Get(ctx, record.ID)
	if err != nil || got.ExpiresAt.Equal(expires) {
		t.Fatal("stale projection escaped")
	}
	// The checkpoint constraint fails after the projection statement: both roll back.
	if _, err = db.Exec(`ALTER TABLE hhc_web.member_recording_lifecycle_checkpoint ADD CONSTRAINT reject_checkpoint CHECK((checkpoint->>'cursor') IS DISTINCT FROM 'blocked')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`ALTER TABLE hhc_web.member_recording_lifecycle_checkpoint DROP CONSTRAINT IF EXISTS reject_checkpoint`)
	})
	state.Cursor = "blocked"
	if err = repo.CommitLifecycleCycle(ctx, newer.Token, state, snapshot); err == nil {
		t.Fatal("checkpoint failure accepted")
	}
	got, _ = repo.Get(ctx, record.ID)
	if got.ExpiresAt.Equal(expires) {
		t.Fatal("projection committed without checkpoint")
	}
	var raw []byte
	if err = db.QueryRow(`SELECT checkpoint FROM hhc_web.member_recording_lifecycle_checkpoint`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved recordings.LifecycleCheckpoint
	_ = json.Unmarshal(raw, &saved)
	if saved.Cursor != record.ID || len(saved.Repairs) != 1 {
		t.Fatal("repair obligation lost")
	}
}
func TestLifecycleKeysetIncludesExpiredAndUnreadyBeyond1000(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	prefix := platform.NewID()
	_, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,create_title,draft_expires_at,idempotency_key,created_by,updated_by,created_at,updated_at,package_id) SELECT gen_random_uuid(),'keyset',clock_timestamp()-interval '1 day',$1||':'||i,'fixture','fixture',clock_timestamp(),clock_timestamp(),md5($1||i) FROM generate_series(1,1205) i`, prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.member_recording WHERE idempotency_key LIKE $1`, prefix+":%")
	})
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := repo.LifecycleBindingPage(ctx, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, b := range page {
			if seen[b.RecordingID] || b.RecordingID <= cursor {
				t.Fatal("unstable keyset")
			}
			seen[b.RecordingID] = true
			cursor = b.RecordingID
		}
		if len(seen) > 2000 {
			t.Fatal("keyset did not terminate")
		}
	}
	if len(seen) < 1205 {
		t.Fatal(fmt.Sprintf("only %d bindings", len(seen)))
	}
}

func TestLifecycleProjectionConstraintRetainsCheckpointAndBinding(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, _ = db.Exec(`DELETE FROM hhc_web.member_recording_lifecycle_checkpoint`)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.member_recording_lifecycle_checkpoint`) })
	record, err := repo.Create(ctx, "constraint", platform.NewID(), platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, record.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	if err = repo.AddPackage(ctx, record.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimLifecycleCycle(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// A real permanent SQL projection constraint must roll back before classification.
	_, err = db.Exec(`ALTER TABLE hhc_web.member_recording ADD CONSTRAINT lifecycle_fixture_reject CHECK(id <> '` + record.ID + `'::uuid OR ready_at IS NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`ALTER TABLE hhc_web.member_recording DROP CONSTRAINT IF EXISTS lifecycle_fixture_reject`)
	})
	expires := now.Add(30 * 24 * time.Hour)
	snapshot := assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{{RecordingID: record.ID, PackageID: pkg, SessionID: pkg, State: "ready", UploadedAt: &now, ReadyAt: &now, MediaExpiresAt: &expires, RetentionRevision: 1}}}
	state := recordings.LifecycleCheckpoint{Cursor: record.ID}
	err = repo.CommitLifecycleCycle(ctx, claim.Token, state, snapshot)
	var invalid *recordings.LifecycleApplyFailure
	if !errors.As(err, &invalid) {
		t.Fatalf("projection constraint classification: %v", err)
	}
	got, err := repo.Get(ctx, record.ID)
	if err != nil || got.ReadyAt != nil {
		t.Fatal("failed projection escaped", got, err)
	}
	state.Repairs = []recordings.LifecycleRepair{{Binding: assetclient.RecordingLifecycleBinding{RecordingID: record.ID, PackageID: pkg}, Attempts: 3, FirstFailureAt: now, Reason: "projection_constraint"}}
	if err = repo.CommitLifecycleCycle(ctx, claim.Token, state, assetclient.RecordingLifecycleSnapshot{}); err != nil {
		t.Fatal(err)
	}
	current, err := repo.LifecycleBindingCurrent(ctx, state.Repairs[0].Binding)
	if err != nil || !current {
		t.Fatal("live repair binding lost", current, err)
	}
	_, err = db.Exec(`UPDATE hhc_web.member_recording SET media_terminal=true WHERE id=$1`, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = repo.LifecycleBindingCurrent(ctx, state.Repairs[0].Binding)
	if err != nil || current {
		t.Fatal("terminal binding retained", current, err)
	}
}
