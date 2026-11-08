package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestRecordingLifecycleProjectionExtendsBeforeFilteringAndNeverRevivesTerminal(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	v, err := repo.Create(ctx, "retention", platform.NewID(), platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, v.ID)
	pkg := "0123456789abcdef0123456789abcdef"
	if err := repo.AddPackage(ctx, v.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	uploaded := now.Add(-40 * 24 * time.Hour)
	ready := uploaded.Add(time.Hour)
	expiry := uploaded.Add(60 * 24 * time.Hour)
	p := assetclient.RecordingPackageStatus{PackageID: pkg, RecordingID: v.ID, State: "ready", UploadedAt: &uploaded, ReadyAt: &ready, MediaExpiresAt: &expiry, RetentionRevision: 2}
	if err := repo.ApplyLifecycle(ctx, assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{p}}); err != nil {
		t.Fatal(err)
	}
	v, err = repo.Get(ctx, v.ID)
	if err != nil || !v.UploadedAt.Equal(uploaded) || !v.ReadyAt.Equal(ready) || !v.ExpiresAt.Equal(expiry) {
		t.Fatalf("projection: %+v %v", v, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.member_recording SET status='published' WHERE id=$1`, v.ID); err != nil {
		t.Fatal(err)
	}
	values, err := repo.ListVisible(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range values {
		if item.ID == v.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("extended recording omitted before pagination")
	}
	olderExpiry := uploaded.Add(14 * 24 * time.Hour)
	p.MediaExpiresAt = &olderExpiry
	p.RetentionRevision = 1
	if err := repo.ApplyLifecycle(ctx, assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{p}}); err != nil {
		t.Fatal(err)
	}
	v, err = repo.Get(ctx, v.ID)
	if err != nil || !v.ExpiresAt.Equal(expiry) {
		t.Fatal("older policy overwrote latest projection")
	}
	p.RetentionRevision = 3
	p.State = "expired"
	if err := repo.ApplyLifecycle(ctx, assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{p}}); err != nil {
		t.Fatal(err)
	}
	p.State = "ready"
	p.RetentionRevision = 4
	p.MediaExpiresAt = &expiry
	if err := repo.ApplyLifecycle(ctx, assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{p}}); err != nil {
		t.Fatal(err)
	}
	v, err = repo.Get(ctx, v.ID)
	if err != nil || !v.MediaTerminal || v.Visible(now) {
		t.Fatalf("terminal package revived: %+v %v", v, err)
	}
}

// A repeated authoritative snapshot must not compete for a row lock merely to
// write the same projection again. Changed snapshots still need that lock.
func TestRecordingLifecycleUnchangedSnapshotDoesNotWaitForRow(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "terminal_without_ready"}[terminal], func(t *testing.T) {
			db := accessDB(t)
			repo := NewRecordingRepository(db)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			value, err := repo.Create(ctx, "no-op projection", platform.NewID(), platform.NewID(), now)
			if err != nil {
				t.Fatal(err)
			}
			recordingCleanup(t, db, value.ID)
			pkg := "0123456789abcdef0123456789abcdef"
			if err = repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
				t.Fatal(err)
			}
			expiry := now.Add(24 * time.Hour)
			item := assetclient.RecordingPackageStatus{PackageID: pkg, RecordingID: value.ID, State: "ready", UploadedAt: &now, ReadyAt: &now, MediaExpiresAt: &expiry, RetentionRevision: 1}
			if terminal {
				item.State = "expired"
				item.ReadyAt = nil
			}
			snapshot := assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{item}}
			if err = repo.ApplyLifecycle(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			var before string
			if err = db.QueryRowContext(ctx, `SELECT xmin::text FROM hhc_web.member_recording WHERE id=$1`, value.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			blocker, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			if _, err = blocker.ExecContext(ctx, `SELECT id FROM hhc_web.member_recording WHERE id=$1 FOR UPDATE`, value.ID); err != nil {
				t.Fatal(err)
			}
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if err = repo.ApplyLifecycle(bounded, snapshot); err != nil {
				t.Fatalf("identical snapshot waited behind row lock: %v", err)
			}
			var after string
			if err = db.QueryRowContext(ctx, `SELECT xmin::text FROM hhc_web.member_recording WHERE id=$1`, value.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatal("identical snapshot created a new row version")
			}
			if err = blocker.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecordingLifecycleChangesAndBatchRollback(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	makeItem := func() assetclient.RecordingPackageStatus {
		value, err := repo.Create(ctx, "changed projection", platform.NewID(), platform.NewID(), now)
		if err != nil {
			t.Fatal(err)
		}
		recordingCleanup(t, db, value.ID)
		pkg := strings.ReplaceAll(platform.NewID(), "-", "")
		if err = repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
			t.Fatal(err)
		}
		expiry := now.Add(24 * time.Hour)
		return assetclient.RecordingPackageStatus{PackageID: pkg, RecordingID: value.ID, State: "ready", UploadedAt: &now, ReadyAt: &now, MediaExpiresAt: &expiry, RetentionRevision: 1}
	}
	first, second := makeItem(), makeItem()
	apply := func(items ...assetclient.RecordingPackageStatus) error {
		return repo.ApplyLifecycle(ctx, assetclient.RecordingLifecycleSnapshot{Items: items})
	}
	if err := apply(first, second); err != nil {
		t.Fatal(err)
	}
	// Same revision with genuinely different authoritative values must still write.
	ready, expiry := now.Add(time.Minute), now.Add(48*time.Hour)
	changed := first
	changed.ReadyAt = &ready
	changed.MediaExpiresAt = &expiry
	if err := apply(changed); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, first.RecordingID)
	if err != nil || !got.ReadyAt.Equal(ready) || !got.ExpiresAt.Equal(expiry) {
		t.Fatalf("changed fields not applied: %v", err)
	}
	changed.RetentionRevision = 2
	if err = apply(changed); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"package", "upload", "older_revision"} {
		stale := changed
		badExpiry := expiry.Add(time.Hour)
		stale.MediaExpiresAt = &badExpiry
		switch kind {
		case "package":
			stale.PackageID = strings.Repeat("f", 32)
		case "upload":
			different := now.Add(-time.Hour)
			stale.UploadedAt = &different
		case "older_revision":
			stale.RetentionRevision = 1
		}
		if err = apply(stale); err != nil {
			t.Fatalf("%s fence: %v", kind, err)
		}
		got, err = repo.Get(ctx, first.RecordingID)
		if err != nil || !got.ExpiresAt.Equal(expiry) {
			t.Fatalf("%s fence overwritten", kind)
		}
	}
	// A later item failure must roll back earlier changes in the same batch.
	newerExpiry := now.Add(72 * time.Hour)
	newer := changed
	newer.MediaExpiresAt = &newerExpiry
	invalid := second
	invalid.UploadedAt = nil
	if err = apply(newer, invalid); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("invalid batch: %v", err)
	}
	got, err = repo.Get(ctx, first.RecordingID)
	if err != nil || !got.ExpiresAt.Equal(expiry) {
		t.Fatal("failed batch partially committed")
	}
	// Changed data still waits for row authority and cancellation rolls back the batch.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `SELECT id FROM hhc_web.member_recording WHERE id=$1 FOR UPDATE`, second.RecordingID); err != nil {
		t.Fatal(err)
	}
	secondChanged := second
	secondChanged.MediaExpiresAt = &newerExpiry
	bounded, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err = repo.ApplyLifecycle(bounded, assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{newer, secondChanged}}); err == nil || bounded.Err() == nil {
		t.Fatalf("changed snapshot bypassed lock/cancellation: %v", err)
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err = repo.Get(ctx, first.RecordingID)
	if err != nil || !got.ExpiresAt.Equal(expiry) {
		t.Fatal("cancelled batch partially committed")
	}
	if err = apply(newer, secondChanged); err != nil {
		t.Fatalf("cleanup prevented retry: %v", err)
	}
}

func TestRecordingLifecycleUnchangedSnapshotDoesNotNeedWriteAdmission(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "terminal"}[terminal], func(t *testing.T) {
			db := accessDB(t)
			repo := NewRecordingRepository(db)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			value, err := repo.Create(ctx, "unchanged under admission saturation", platform.NewID(), platform.NewID(), now)
			if err != nil {
				t.Fatal(err)
			}
			recordingCleanup(t, db, value.ID)
			pkg := strings.ReplaceAll(platform.NewID(), "-", "")
			if err = repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
				t.Fatal(err)
			}
			expiry := now.Add(24 * time.Hour)
			item := assetclient.RecordingPackageStatus{RecordingID: value.ID, PackageID: pkg, State: "ready", UploadedAt: &now, ReadyAt: &now, MediaExpiresAt: &expiry, RetentionRevision: 1}
			if terminal {
				item.State = "expired"
				item.ReadyAt = nil
			}
			snapshot := assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{item}}
			if err = repo.ApplyLifecycle(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(2)
			_, finish, err := repo.beginRecordingTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer finish()
			bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			if err = repo.ApplyLifecycle(bounded, snapshot); err != nil {
				t.Fatalf("unchanged authoritative projection consumed write admission: %v", err)
			}
			if !terminal {
				changed := item
				later := expiry.Add(time.Hour)
				changed.MediaExpiresAt = &later
				if err = repo.ApplyLifecycle(ctx, assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{changed}}); !errors.Is(err, recordings.ErrTransactionCapacity) {
					t.Fatalf("actual change bypassed finite admission: %v", err)
				}
				got, err := repo.Get(ctx, value.ID)
				if err != nil || !got.ExpiresAt.Equal(expiry) {
					t.Fatal("rejected change mutated primary projection")
				}
			}
			canceled, stop := context.WithCancel(ctx)
			stop()
			if err = repo.ApplyLifecycle(canceled, snapshot); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
