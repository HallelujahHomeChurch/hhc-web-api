package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestSourceBindingExcludesOtherUploadsAndProjectsOnlyMatchingReady(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	p, err := repo.Create(ctx, "source", platform.NewID(), platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, p.ID)
	id, pkg := strings.Repeat("a", 32), strings.Repeat("b", 32)
	if err := repo.AddSource(ctx, p.ID, id, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSource(ctx, p.ID, id, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSource(ctx, p.ID, strings.Repeat("c", 32), now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("replaced source: %v", err)
	}
	if err := repo.AddPackage(ctx, p.ID, pkg, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("mixed direct HLS: %v", err)
	}
	if _, err := repo.RecordSourceReady(ctx, p.ID, strings.Repeat("c", 32), pkg, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("cross-source ready: %v", err)
	}
	ready, err := repo.RecordSourceReady(ctx, p.ID, id, pkg, now)
	if err != nil || ready.SourceID != id || ready.PackageID != pkg || ready.ReadyAt == nil || !ready.ExpiresAt.Equal(now.Add(720*time.Hour)) {
		t.Fatalf("projection %+v %v", ready, err)
	}
	if _, err := repo.RecordSourceReady(ctx, p.ID, id, pkg, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordSourceReady(ctx, p.ID, id, pkg, now.Add(time.Hour)); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("extended retention: %v", err)
	}
}
