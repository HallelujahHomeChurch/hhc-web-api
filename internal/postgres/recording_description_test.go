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

func TestRecordingDescriptionPreservesOmissionAndFencesHumanEdits(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	now := time.Now().UTC()
	ctx := auditclient.WithProvenance(context.Background(), actor, "description")
	value, err := repo.Create(ctx, "Meeting", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	description := strings.Repeat("😀", 5000)
	value, err = repo.UpdateTitle(ctx, value.ID, value.Version, value.Title, actor, now, true, &description)
	if err != nil || value.Description != description {
		t.Fatalf("persist unicode: %v", err)
	}
	long := description + "x"
	if _, err = repo.UpdateTitle(ctx, value.ID, value.Version, value.Title, actor, now, true, &long); !errors.Is(err, recordings.ErrInvalid) {
		t.Fatalf("oversize: %v", err)
	}
	value, err = repo.UpdateTitle(ctx, value.ID, value.Version, "New title", actor, now, true, nil)
	if err != nil || value.Description != description {
		t.Fatalf("omission: %v", err)
	}
	empty := ""
	value, err = repo.UpdateTitle(ctx, value.ID, value.Version, value.Title, actor, now, true, &empty)
	if err != nil || value.Description != "" {
		t.Fatalf("clear: %v", err)
	}
	var generation int
	if err = db.QueryRow(`SELECT human_edit_generation FROM hhc_web.member_recording WHERE id=$1`, value.ID).Scan(&generation); err != nil || generation != 3 {
		t.Fatalf("human fence=%d: %v", generation, err)
	}
	if _, err = repo.UpdateTitle(ctx, value.ID, value.Version-1, value.Title, actor, now, true, &description); !errors.Is(err, recordings.ErrPrecondition) {
		t.Fatalf("stale: %v", err)
	}
}
