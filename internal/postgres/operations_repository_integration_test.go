package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestOperationsRepositoryIntegration(t *testing.T) {
	databaseURL := os.Getenv("HHW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.operations_audit,hhc_web.meeting_collection_binding,hhc_web.meeting_occurrence_override,hhc_web.meeting,hhc_web.resource,hhc_web.church_unit CASCADE`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	repository := NewOperationsRepository(db)
	service := operations.NewService(repository, func() time.Time { return now })
	unit, err := service.CreateChurchUnit(ctx, operations.ChurchUnitInput{Key: "taipei", Name: "Taipei"}, "admin", "request-1", "unit-taipei")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.CreateChurchUnit(ctx, operations.ChurchUnitInput{Key: "taipei", Name: "Taipei"}, "admin", "request-1", "unit-taipei")
	if err != nil || retry.ID != unit.ID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	resource, err := service.CreateResource(ctx, operations.ResourceInput{Key: "main-hall", Name: "Main Hall", Kind: operations.ResourceVenue, ChurchUnitID: unit.ID, Timezone: "Asia/Taipei", Visibility: operations.VisibilityPublic}, "admin", "request-2", "resource-main-hall")
	if err != nil {
		t.Fatal(err)
	}
	startsAt := now.Add(time.Hour)
	meeting, err := service.CreateMeeting(ctx, operations.MeetingInput{Key: "special-meeting", Name: "Special Meeting", ChurchUnitID: unit.ID, VenueResourceID: resource.ID, Timezone: "Asia/Taipei", Schedule: operations.Schedule{Type: operations.ScheduleOnce, StartsAt: startsAt}, DurationMinutes: 60, Visibility: operations.VisibilityPublic}, "admin", "request-3", "meeting-special")
	if err != nil {
		t.Fatal(err)
	}
	if meeting.NextOccurrence == nil || !meeting.NextOccurrence.StartsAt.Equal(startsAt) {
		t.Fatalf("meeting=%+v", meeting)
	}
	if _, err := service.SaveMeeting(ctx, meeting.ID, 99, operations.MeetingInput{Key: meeting.Key, Name: meeting.Name, ChurchUnitID: unit.ID, VenueResourceID: resource.ID, Timezone: meeting.Timezone, Schedule: meeting.Schedule, DurationMinutes: 60, Visibility: meeting.Visibility}, "admin", "request-4"); !errors.Is(err, operations.ErrPrecondition) {
		t.Fatalf("stale err=%v", err)
	}
	meetingRecord, err := service.ReplaceMeetingBindings(ctx, meeting.ID, meeting.Version, []string{"collection-1"}, "admin", "request-5")
	if err != nil {
		t.Fatal(err)
	}
	windows, err := service.ListMediaSyncWindows(ctx, now, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 || !windows[0].StartsAt.Equal(startsAt) {
		t.Fatalf("windows=%+v", windows)
	}
	duration := 30
	if _, err := service.PutOverride(ctx, meeting.ID, meetingRecord.Version, operations.OccurrenceOverrideInput{OccurrenceDate: "2026-09-01", DurationMinutes: &duration}, "admin", "request-6"); err != nil {
		t.Fatal(err)
	}
	var auditCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.operations_audit`).Scan(&auditCount); err != nil || auditCount != 5 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}
}
