package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSaveChurchUnitRejectsImmutableKeyAndParentCycle(t *testing.T) {
	repository := &serviceRepository{unit: ChurchUnit{ID: "unit-1", Key: "taipei", Version: 2}}
	service := NewService(repository, time.Now)

	if _, err := service.SaveChurchUnit(context.Background(), "unit-1", 2, ChurchUnitInput{Key: "renamed", Name: "Taipei"}, "admin", "request"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("immutable key err=%v", err)
	}
	repository.cycle = true
	if _, err := service.SaveChurchUnit(context.Background(), "unit-1", 2, ChurchUnitInput{Key: "taipei", Name: "Taipei", ParentID: "unit-2"}, "admin", "request"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle err=%v", err)
	}
}

func TestCreateChurchUnitRequiresIdempotencyKey(t *testing.T) {
	repository := &serviceRepository{}
	service := NewService(repository, time.Now)
	input := ChurchUnitInput{Key: "taipei", Name: " Taipei "}
	if _, err := service.CreateChurchUnit(context.Background(), input, "admin", "request", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
	got, err := service.CreateChurchUnit(context.Background(), input, "admin", "request", "create-taipei")
	if err != nil || got.Name != "Taipei" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestCreateResourceAcceptsOnlyVenueWithIANATimezone(t *testing.T) {
	repository := &serviceRepository{}
	service := NewService(repository, time.Now)
	input := ResourceInput{Key: "main-hall", Name: " Main Hall ", Kind: ResourceVenue, ChurchUnitID: "unit-1", Timezone: "Asia/Taipei", Visibility: VisibilityPublic}

	got, err := service.CreateResource(context.Background(), input, "admin", "request", "idempotency")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Main Hall" || repository.resourceInput.Name != "Main Hall" {
		t.Fatalf("got=%+v input=%+v", got, repository.resourceInput)
	}
	input.Kind = "vehicle"
	if _, err := service.CreateResource(context.Background(), input, "admin", "request", "idempotency"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("kind err=%v", err)
	}
	input.Kind, input.Timezone = ResourceVenue, "Not/AZone"
	if _, err := service.CreateResource(context.Background(), input, "admin", "request", "idempotency"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("timezone err=%v", err)
	}
}

func TestSaveResourceRejectsImmutableKey(t *testing.T) {
	repository := &serviceRepository{resource: Resource{ID: "resource-1", Key: "main-hall", Kind: ResourceVenue, Version: 1}}
	service := NewService(repository, time.Now)
	input := ResourceInput{Key: "renamed", Name: "Main Hall", Kind: ResourceVenue, ChurchUnitID: "unit-1", Timezone: "Asia/Taipei", Visibility: VisibilityPublic}
	if _, err := service.SaveResource(context.Background(), "resource-1", 1, input, "admin", "request"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateMeetingNormalizesWeeklyDaysAndReturnsNextOccurrence(t *testing.T) {
	repository := &serviceRepository{resource: Resource{ID: "venue-1", Kind: ResourceVenue}}
	service := NewService(repository, func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) })
	input := MeetingInput{
		Key: "sunday-service", Name: " Sunday Service ", ChurchUnitID: "unit-1", VenueResourceID: "venue-1",
		Timezone: "Asia/Taipei", Schedule: Schedule{Type: ScheduleWeekly, DaysOfWeek: []time.Weekday{time.Sunday, time.Sunday}, StartTime: "09:30"},
		DurationMinutes: 90, Visibility: VisibilityPublic,
	}

	got, err := service.CreateMeeting(context.Background(), input, "admin", "request", "idempotency")
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.meetingInput.Schedule.DaysOfWeek) != 1 || got.NextOccurrence == nil {
		t.Fatalf("input=%+v result=%+v", repository.meetingInput, got)
	}
}

func TestSaveMeetingPropagatesStaleVersion(t *testing.T) {
	repository := &serviceRepository{saveMeetingErr: ErrPrecondition, resource: Resource{ID: "venue-1", Kind: ResourceVenue}}
	service := NewService(repository, time.Now)
	input := MeetingInput{Key: "meeting", Name: "Meeting", ChurchUnitID: "unit-1", VenueResourceID: "venue-1", Timezone: "Asia/Taipei", Schedule: Schedule{Type: ScheduleOnce, StartsAt: time.Now().Add(time.Hour)}, DurationMinutes: 60, Visibility: VisibilityInternal}

	if _, err := service.SaveMeeting(context.Background(), "meeting-1", 1, input, "admin", "request"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("err=%v", err)
	}
}

func TestReplaceMeetingBindingsIsAtomicAndNormalized(t *testing.T) {
	repository := &serviceRepository{}
	service := NewService(repository, time.Now)
	got, err := service.ReplaceMeetingBindings(context.Background(), "meeting-1", 3, []string{" collection-2 ", "collection-1", "collection-2"}, "admin", "request")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 4 || len(repository.bindings) != 2 || repository.bindings[0] != "collection-1" || repository.bindings[1] != "collection-2" {
		t.Fatalf("got=%+v bindings=%v", got, repository.bindings)
	}
}

func TestSetMeetingStatusArchivesAndRestores(t *testing.T) {
	repository := &serviceRepository{}
	service := NewService(repository, time.Now)
	for _, status := range []Status{StatusArchived, StatusActive} {
		got, err := service.SetMeetingStatus(context.Background(), "meeting-1", 3, status, "admin", "request")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != status {
			t.Fatalf("status=%q", got.Status)
		}
	}
	if _, err := service.SetMeetingStatus(context.Background(), "meeting-1", 3, "deleted", "admin", "request"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid status err=%v", err)
	}
}

func TestPutOverrideKeepsOriginalDate(t *testing.T) {
	repository := &serviceRepository{}
	service := NewService(repository, time.Now)
	moved := time.Date(2026, 9, 7, 9, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	got, err := service.PutOverride(context.Background(), "meeting-1", 2, OccurrenceOverrideInput{OccurrenceDate: "2026-09-06", StartsAt: &moved}, "admin", "request")
	if err != nil {
		t.Fatal(err)
	}
	if got.OccurrenceDate != "2026-09-06" || !got.StartsAt.Equal(moved) {
		t.Fatalf("override=%+v", got)
	}
}

func TestListMediaSyncWindowsReturnsRepositoryUnion(t *testing.T) {
	windows := []MediaSyncWindow{{StartsAt: time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC), EndsAt: time.Date(2026, 9, 6, 3, 0, 0, 0, time.UTC)}}
	repository := &serviceRepository{windows: windows}
	got, err := NewService(repository, time.Now).ListMediaSyncWindows(context.Background(), windows[0].StartsAt, windows[0].EndsAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != windows[0] {
		t.Fatalf("windows=%+v", got)
	}
}

type serviceRepository struct {
	unit           ChurchUnit
	resource       Resource
	cycle          bool
	resourceInput  ResourceInput
	meetingInput   MeetingInput
	bindings       []string
	saveMeetingErr error
	windows        []MediaSyncWindow
}

func (r *serviceRepository) CreateChurchUnit(_ context.Context, input ChurchUnitInput, _, _, _ string, _ time.Time) (ChurchUnit, error) {
	r.unit = ChurchUnit{ID: "unit-1", Key: input.Key, Name: input.Name, ParentID: input.ParentID, Status: StatusActive, Version: 1}
	return r.unit, nil
}

func (r *serviceRepository) GetChurchUnit(context.Context, string) (ChurchUnit, error) {
	return r.unit, nil
}
func (r *serviceRepository) SaveChurchUnit(_ context.Context, _ string, _ int64, input ChurchUnitInput, _, _ string, _ time.Time) (ChurchUnit, error) {
	r.unit.Name, r.unit.ParentID, r.unit.Version = input.Name, input.ParentID, r.unit.Version+1
	return r.unit, nil
}
func (r *serviceRepository) WouldCreateChurchUnitCycle(context.Context, string, string) (bool, error) {
	return r.cycle, nil
}
func (r *serviceRepository) CreateResource(_ context.Context, input ResourceInput, _, _, _ string, _ time.Time) (Resource, error) {
	r.resourceInput = input
	return Resource{ID: "resource-1", Key: input.Key, Name: input.Name, Kind: input.Kind, Version: 1}, nil
}
func (r *serviceRepository) GetResource(context.Context, string) (Resource, error) {
	return r.resource, nil
}
func (r *serviceRepository) SaveResource(_ context.Context, _ string, expected int64, input ResourceInput, _, _ string, _ time.Time) (Resource, error) {
	return Resource{ID: "resource-1", Key: input.Key, Name: input.Name, Kind: input.Kind, ChurchUnitID: input.ChurchUnitID, Timezone: input.Timezone, Visibility: input.Visibility, Version: expected + 1}, nil
}
func (r *serviceRepository) SetChurchUnitStatus(_ context.Context, id string, expected int64, status Status, _, _ string, _ time.Time) (ChurchUnit, error) {
	return ChurchUnit{ID: id, Status: status, Version: expected + 1}, nil
}
func (r *serviceRepository) SetResourceStatus(_ context.Context, id string, expected int64, status Status, _, _ string, _ time.Time) (Resource, error) {
	return Resource{ID: id, Status: status, Version: expected + 1}, nil
}
func (r *serviceRepository) ListChurchUnits(context.Context, bool) ([]ChurchUnit, error) {
	return nil, nil
}
func (r *serviceRepository) ListResources(context.Context, bool) ([]Resource, error) { return nil, nil }
func (r *serviceRepository) GetMeeting(context.Context, string) (Meeting, error) {
	return Meeting{}, nil
}
func (r *serviceRepository) ListMeetings(context.Context, bool) ([]Meeting, error) { return nil, nil }
func (r *serviceRepository) ListOverrides(context.Context, string) ([]OccurrenceOverride, error) {
	return nil, nil
}
func (r *serviceRepository) ListMeetingBindings(context.Context, string) ([]string, error) {
	return nil, nil
}
func (r *serviceRepository) CreateMeeting(_ context.Context, input MeetingInput, _, _, _ string, now time.Time) (MeetingMutation, error) {
	r.meetingInput = input
	meeting := input.Meeting("meeting-1", 1)
	occurrences, err := ResolveOccurrences(meeting, nil, now, now.AddDate(1, 0, 0))
	if err != nil {
		return MeetingMutation{}, err
	}
	result := MeetingMutation{Meeting: meeting}
	if len(occurrences) > 0 {
		result.NextOccurrence = &occurrences[0]
	}
	return result, nil
}
func (r *serviceRepository) SaveMeeting(_ context.Context, _ string, _ int64, input MeetingInput, _, _ string, now time.Time) (MeetingMutation, error) {
	if r.saveMeetingErr != nil {
		return MeetingMutation{}, r.saveMeetingErr
	}
	return r.CreateMeeting(context.Background(), input, "", "", "", now)
}
func (r *serviceRepository) ReplaceMeetingBindings(_ context.Context, _ string, expected int64, bindings []string, _, _ string, _ time.Time) (Meeting, error) {
	r.bindings = append([]string(nil), bindings...)
	return Meeting{ID: "meeting-1", Version: expected + 1}, nil
}
func (r *serviceRepository) SetMeetingStatus(_ context.Context, id string, expected int64, status Status, _, _ string, _ time.Time) (Meeting, error) {
	return Meeting{ID: id, Status: status, Version: expected + 1}, nil
}
func (r *serviceRepository) PutOverride(_ context.Context, meetingID string, expected int64, input OccurrenceOverrideInput, _, _ string, _ time.Time) (OccurrenceOverride, error) {
	return input.Override(meetingID, expected+1), nil
}
func (r *serviceRepository) DeleteOverride(context.Context, string, int64, string, string, string, time.Time) (Meeting, error) {
	return Meeting{}, nil
}
func (r *serviceRepository) ListOccurrences(context.Context, OccurrenceQuery) ([]Occurrence, error) {
	return nil, nil
}
func (r *serviceRepository) ListMediaSyncWindows(context.Context, time.Time, time.Time) ([]MediaSyncWindow, error) {
	return r.windows, nil
}
