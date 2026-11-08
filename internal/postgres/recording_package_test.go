package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestRecordingPackageBindingAndReadyAreImmutable(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	value, err := repo.Create(ctx, "HLS gathering", platform.NewID(), platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	packageID := strings.ReplaceAll(platform.NewID(), "-", "")
	if err := repo.AddPackage(ctx, value.ID, packageID, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddPackage(ctx, value.ID, packageID, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddPackage(ctx, value.ID, strings.Repeat("b", 32), now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("replacement accepted: %v", err)
	}
	value, err = repo.Get(ctx, value.ID)
	if err != nil || value.PackageID != packageID || value.ReadyAt != nil {
		t.Fatalf("attachment claimed readiness: %+v %v", value, err)
	}
	if _, err := repo.RecordPackageReady(ctx, value.ID, packageID, now); err != nil {
		t.Fatal(err)
	}
	ready, err := repo.RecordPackageReady(ctx, value.ID, packageID, now)
	if err != nil || ready.ReadyAt == nil || !ready.ReadyAt.Equal(now) || !ready.ExpiresAt.Equal(now.Add(720*time.Hour)) {
		t.Fatalf("ready projection: %+v %v", ready, err)
	}
	if _, err := repo.RecordPackageReady(ctx, value.ID, packageID, now.Add(time.Hour)); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("retention extended: %v", err)
	}
}

func TestHLSPublishReceiptSurvivesManualUnpublish(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	ctx := auditclient.WithPrincipalProvenance(context.Background(), "service", actor, "hls-publish-"+platform.NewID())
	now := time.Now().UTC().Truncate(time.Microsecond)
	value, err := repo.Create(ctx, "HLS", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	if err := repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	key := platform.NewID()
	if _, err := repo.PublishOperation(ctx, value.ID, 1, actor, key, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("unready package published: %v", err)
	}
	if _, err := repo.RecordPackageReady(ctx, value.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	first, err := repo.PublishOperation(ctx, value.ID, 1, actor, key, now)
	if err != nil || first.Outcome != "published" || first.Receipt.PackageID != pkg {
		t.Fatalf("publication: %+v %v", first, err)
	}
	if _, err := repo.Unpublish(ctx, value.ID, first.Current.Version, actor, now); err != nil {
		t.Fatal(err)
	}
	replay, err := repo.PublishOperation(ctx, value.ID, 1, actor, key, now)
	if err != nil || replay.Outcome != "state_changed" || replay.Current.Status != "draft" || replay.Receipt.PackageID != pkg {
		t.Fatalf("republished intent: %+v %v", replay, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='recording.notification.submit' AND aggregate_id=$1`, value.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("notification count=%d err=%v", count, err)
	}
}
