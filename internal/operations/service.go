package operations

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid      = errors.New("invalid operations input")
	ErrNotFound     = errors.New("operations record not found")
	ErrConflict     = errors.New("operations conflict")
	ErrPrecondition = errors.New("operations version precondition failed")
)

var stableKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Repository interface {
	CreateChurchUnit(context.Context, ChurchUnitInput, string, string, string, time.Time) (ChurchUnit, error)
	ListChurchUnits(context.Context, bool) ([]ChurchUnit, error)
	GetChurchUnit(context.Context, string) (ChurchUnit, error)
	SaveChurchUnit(context.Context, string, int64, ChurchUnitInput, string, string, time.Time) (ChurchUnit, error)
	SetChurchUnitStatus(context.Context, string, int64, Status, string, string, time.Time) (ChurchUnit, error)
	WouldCreateChurchUnitCycle(context.Context, string, string) (bool, error)
	CreateResource(context.Context, ResourceInput, string, string, string, time.Time) (Resource, error)
	ListResources(context.Context, bool) ([]Resource, error)
	GetResource(context.Context, string) (Resource, error)
	SaveResource(context.Context, string, int64, ResourceInput, string, string, time.Time) (Resource, error)
	SetResourceStatus(context.Context, string, int64, Status, string, string, time.Time) (Resource, error)
	CreateMeeting(context.Context, MeetingInput, string, string, string, time.Time) (MeetingMutation, error)
	ListMeetings(context.Context, bool) ([]Meeting, error)
	GetMeeting(context.Context, string) (Meeting, error)
	SaveMeeting(context.Context, string, int64, MeetingInput, string, string, time.Time) (MeetingMutation, error)
	SetMeetingStatus(context.Context, string, int64, Status, string, string, time.Time) (Meeting, error)
	PutOverride(context.Context, string, int64, OccurrenceOverrideInput, string, string, time.Time) (OccurrenceOverride, error)
	DeleteOverride(context.Context, string, int64, string, string, string, time.Time) (Meeting, error)
	ListOverrides(context.Context, string) ([]OccurrenceOverride, error)
	ReplaceMeetingBindings(context.Context, string, int64, []string, string, string, time.Time) (Meeting, error)
	ListMeetingBindings(context.Context, string) ([]string, error)
	ListOccurrences(context.Context, OccurrenceQuery) ([]Occurrence, error)
	ListMediaSyncWindows(context.Context, time.Time, time.Time) ([]MediaSyncWindow, error)
}

func (s *Service) ListChurchUnits(ctx context.Context, includeArchived bool) ([]ChurchUnit, error) {
	return s.repository.ListChurchUnits(ctx, includeArchived)
}

func (s *Service) GetChurchUnit(ctx context.Context, id string) (ChurchUnit, error) {
	return s.repository.GetChurchUnit(ctx, id)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	return &Service{repository: repository, now: now}
}

func (s *Service) CreateChurchUnit(ctx context.Context, input ChurchUnitInput, actor, requestID, idempotencyKey string) (ChurchUnit, error) {
	input = normalizeChurchUnit(input)
	if !validChurchUnit(input) || !validMutationContext(actor, requestID) || strings.TrimSpace(idempotencyKey) == "" {
		return ChurchUnit{}, ErrInvalid
	}
	if input.ParentID != "" {
		if _, err := s.repository.GetChurchUnit(ctx, input.ParentID); err != nil {
			return ChurchUnit{}, err
		}
	}
	return s.repository.CreateChurchUnit(ctx, input, actor, requestID, strings.TrimSpace(idempotencyKey), s.now().UTC())
}

func (s *Service) SaveChurchUnit(ctx context.Context, id string, expectedVersion int64, input ChurchUnitInput, actor, requestID string) (ChurchUnit, error) {
	input = normalizeChurchUnit(input)
	if expectedVersion < 1 || !validChurchUnit(input) || !validMutationContext(actor, requestID) {
		return ChurchUnit{}, ErrInvalid
	}
	current, err := s.repository.GetChurchUnit(ctx, id)
	if err != nil {
		return ChurchUnit{}, err
	}
	if current.Key != input.Key {
		return ChurchUnit{}, ErrInvalid
	}
	if input.ParentID != "" {
		cycle, err := s.repository.WouldCreateChurchUnitCycle(ctx, id, input.ParentID)
		if err != nil {
			return ChurchUnit{}, err
		}
		if cycle {
			return ChurchUnit{}, ErrInvalid
		}
	}
	return s.repository.SaveChurchUnit(ctx, id, expectedVersion, input, actor, requestID, s.now().UTC())
}

func (s *Service) SetChurchUnitStatus(ctx context.Context, id string, expectedVersion int64, status Status, actor, requestID string) (ChurchUnit, error) {
	if expectedVersion < 1 || !validStatus(status) || !validMutationContext(actor, requestID) {
		return ChurchUnit{}, ErrInvalid
	}
	return s.repository.SetChurchUnitStatus(ctx, id, expectedVersion, status, actor, requestID, s.now().UTC())
}

func (s *Service) CreateResource(ctx context.Context, input ResourceInput, actor, requestID, idempotencyKey string) (Resource, error) {
	input = normalizeResource(input)
	if !validResource(input) || !validMutationContext(actor, requestID) || strings.TrimSpace(idempotencyKey) == "" {
		return Resource{}, ErrInvalid
	}
	return s.repository.CreateResource(ctx, input, actor, requestID, strings.TrimSpace(idempotencyKey), s.now().UTC())
}

func (s *Service) ListResources(ctx context.Context, includeArchived bool) ([]Resource, error) {
	return s.repository.ListResources(ctx, includeArchived)
}

func (s *Service) GetResource(ctx context.Context, id string) (Resource, error) {
	return s.repository.GetResource(ctx, id)
}

func (s *Service) SaveResource(ctx context.Context, id string, expectedVersion int64, input ResourceInput, actor, requestID string) (Resource, error) {
	input = normalizeResource(input)
	if expectedVersion < 1 || !validResource(input) || !validMutationContext(actor, requestID) {
		return Resource{}, ErrInvalid
	}
	current, err := s.repository.GetResource(ctx, id)
	if err != nil {
		return Resource{}, err
	}
	if current.Key != input.Key || current.Kind != input.Kind {
		return Resource{}, ErrInvalid
	}
	return s.repository.SaveResource(ctx, id, expectedVersion, input, actor, requestID, s.now().UTC())
}

func (s *Service) SetResourceStatus(ctx context.Context, id string, expectedVersion int64, status Status, actor, requestID string) (Resource, error) {
	if expectedVersion < 1 || !validStatus(status) || !validMutationContext(actor, requestID) {
		return Resource{}, ErrInvalid
	}
	return s.repository.SetResourceStatus(ctx, id, expectedVersion, status, actor, requestID, s.now().UTC())
}

func (s *Service) CreateMeeting(ctx context.Context, input MeetingInput, actor, requestID, idempotencyKey string) (MeetingMutation, error) {
	input = normalizeMeeting(input)
	if !validMeeting(input) || !validMutationContext(actor, requestID) || strings.TrimSpace(idempotencyKey) == "" {
		return MeetingMutation{}, ErrInvalid
	}
	if err := s.validateVenue(ctx, input.VenueResourceID); err != nil {
		return MeetingMutation{}, err
	}
	return s.repository.CreateMeeting(ctx, input, actor, requestID, strings.TrimSpace(idempotencyKey), s.now().UTC())
}

func (s *Service) ListMeetings(ctx context.Context, includeArchived bool) ([]Meeting, error) {
	return s.repository.ListMeetings(ctx, includeArchived)
}

func (s *Service) GetMeeting(ctx context.Context, id string) (Meeting, error) {
	return s.repository.GetMeeting(ctx, id)
}

func (s *Service) SaveMeeting(ctx context.Context, id string, expectedVersion int64, input MeetingInput, actor, requestID string) (MeetingMutation, error) {
	input = normalizeMeeting(input)
	if expectedVersion < 1 || !validMeeting(input) || !validMutationContext(actor, requestID) {
		return MeetingMutation{}, ErrInvalid
	}
	if err := s.validateVenue(ctx, input.VenueResourceID); err != nil {
		return MeetingMutation{}, err
	}
	return s.repository.SaveMeeting(ctx, id, expectedVersion, input, actor, requestID, s.now().UTC())
}

func (s *Service) ReplaceMeetingBindings(ctx context.Context, id string, expectedVersion int64, collectionIDs []string, actor, requestID string) (Meeting, error) {
	if expectedVersion < 1 || !validMutationContext(actor, requestID) {
		return Meeting{}, ErrInvalid
	}
	unique := make(map[string]struct{}, len(collectionIDs))
	for _, collectionID := range collectionIDs {
		collectionID = strings.TrimSpace(collectionID)
		if collectionID == "" || utf8.RuneCountInString(collectionID) > 200 {
			return Meeting{}, ErrInvalid
		}
		unique[collectionID] = struct{}{}
	}
	bindings := make([]string, 0, len(unique))
	for collectionID := range unique {
		bindings = append(bindings, collectionID)
	}
	sort.Strings(bindings)
	return s.repository.ReplaceMeetingBindings(ctx, id, expectedVersion, bindings, actor, requestID, s.now().UTC())
}

func (s *Service) SetMeetingStatus(ctx context.Context, id string, expectedVersion int64, status Status, actor, requestID string) (Meeting, error) {
	if expectedVersion < 1 || !validStatus(status) || !validMutationContext(actor, requestID) {
		return Meeting{}, ErrInvalid
	}
	return s.repository.SetMeetingStatus(ctx, id, expectedVersion, status, actor, requestID, s.now().UTC())
}

func (s *Service) PutOverride(ctx context.Context, meetingID string, expectedVersion int64, input OccurrenceOverrideInput, actor, requestID string) (OccurrenceOverride, error) {
	input.OccurrenceDate, input.Reason = strings.TrimSpace(input.OccurrenceDate), strings.TrimSpace(input.Reason)
	if input.VenueResourceID != nil {
		value := strings.TrimSpace(*input.VenueResourceID)
		input.VenueResourceID = &value
		if value == "" {
			return OccurrenceOverride{}, ErrInvalid
		}
	}
	if expectedVersion < 1 || !validMutationContext(actor, requestID) || len(input.Reason) > 500 {
		return OccurrenceOverride{}, ErrInvalid
	}
	if _, err := time.Parse(time.DateOnly, input.OccurrenceDate); err != nil {
		return OccurrenceOverride{}, ErrInvalid
	}
	if input.DurationMinutes != nil && (*input.DurationMinutes < 1 || *input.DurationMinutes > 1440) {
		return OccurrenceOverride{}, ErrInvalid
	}
	return s.repository.PutOverride(ctx, meetingID, expectedVersion, input, actor, requestID, s.now().UTC())
}

func (s *Service) DeleteOverride(ctx context.Context, meetingID string, expectedVersion int64, occurrenceDate, actor, requestID string) (Meeting, error) {
	if expectedVersion < 1 || !validMutationContext(actor, requestID) {
		return Meeting{}, ErrInvalid
	}
	if _, err := time.Parse(time.DateOnly, occurrenceDate); err != nil {
		return Meeting{}, ErrInvalid
	}
	return s.repository.DeleteOverride(ctx, meetingID, expectedVersion, occurrenceDate, actor, requestID, s.now().UTC())
}

func (s *Service) ListOverrides(ctx context.Context, meetingID string) ([]OccurrenceOverride, error) {
	return s.repository.ListOverrides(ctx, meetingID)
}

func (s *Service) ListMeetingBindings(ctx context.Context, meetingID string) ([]string, error) {
	return s.repository.ListMeetingBindings(ctx, meetingID)
}

func (s *Service) ListOccurrences(ctx context.Context, query OccurrenceQuery) ([]Occurrence, error) {
	if !query.From.Before(query.To) {
		return nil, ErrInvalid
	}
	return s.repository.ListOccurrences(ctx, query)
}

func (s *Service) ListMediaSyncWindows(ctx context.Context, from, to time.Time) ([]MediaSyncWindow, error) {
	if !from.Before(to) {
		return nil, ErrInvalid
	}
	return s.repository.ListMediaSyncWindows(ctx, from, to)
}

func (s *Service) validateVenue(ctx context.Context, id string) error {
	resource, err := s.repository.GetResource(ctx, id)
	if err != nil {
		return err
	}
	if resource.Kind != ResourceVenue {
		return ErrInvalid
	}
	return nil
}

func normalizeChurchUnit(input ChurchUnitInput) ChurchUnitInput {
	input.Key, input.Name = strings.TrimSpace(input.Key), strings.TrimSpace(input.Name)
	input.Description, input.ParentID = strings.TrimSpace(input.Description), strings.TrimSpace(input.ParentID)
	return input
}

func normalizeResource(input ResourceInput) ResourceInput {
	input.Key, input.Name = strings.TrimSpace(input.Key), strings.TrimSpace(input.Name)
	input.Description, input.ChurchUnitID = strings.TrimSpace(input.Description), strings.TrimSpace(input.ChurchUnitID)
	input.LocationContentID, input.Timezone = strings.TrimSpace(input.LocationContentID), strings.TrimSpace(input.Timezone)
	return input
}

func normalizeMeeting(input MeetingInput) MeetingInput {
	input.Key, input.Name = strings.TrimSpace(input.Key), strings.TrimSpace(input.Name)
	input.Description, input.ChurchUnitID = strings.TrimSpace(input.Description), strings.TrimSpace(input.ChurchUnitID)
	input.VenueResourceID, input.Timezone = strings.TrimSpace(input.VenueResourceID), strings.TrimSpace(input.Timezone)
	input.Schedule.StartTime = strings.TrimSpace(input.Schedule.StartTime)
	seen := make(map[time.Weekday]struct{}, len(input.Schedule.DaysOfWeek))
	for _, day := range input.Schedule.DaysOfWeek {
		seen[day] = struct{}{}
	}
	input.Schedule.DaysOfWeek = input.Schedule.DaysOfWeek[:0]
	for day := range seen {
		input.Schedule.DaysOfWeek = append(input.Schedule.DaysOfWeek, day)
	}
	sort.Slice(input.Schedule.DaysOfWeek, func(i, j int) bool { return input.Schedule.DaysOfWeek[i] < input.Schedule.DaysOfWeek[j] })
	return input
}

func validChurchUnit(input ChurchUnitInput) bool {
	return validKeyAndName(input.Key, input.Name) && input.ParentID != "self"
}

func validResource(input ResourceInput) bool {
	if !validKeyAndName(input.Key, input.Name) || input.Kind != ResourceVenue || input.ChurchUnitID == "" || !validVisibility(input.Visibility) {
		return false
	}
	_, err := time.LoadLocation(input.Timezone)
	return err == nil
}

func validMeeting(input MeetingInput) bool {
	if !validKeyAndName(input.Key, input.Name) || input.ChurchUnitID == "" || input.VenueResourceID == "" || !validVisibility(input.Visibility) || input.DurationMinutes < 1 || input.DurationMinutes > 1440 {
		return false
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return false
	}
	switch input.Schedule.Type {
	case ScheduleWeekly:
		if len(input.Schedule.DaysOfWeek) == 0 || input.Schedule.StartsAt != (time.Time{}) {
			return false
		}
		for _, day := range input.Schedule.DaysOfWeek {
			if day < time.Sunday || day > time.Saturday {
				return false
			}
		}
		_, err := time.Parse("15:04", input.Schedule.StartTime)
		return err == nil
	case ScheduleOnce:
		return len(input.Schedule.DaysOfWeek) == 0 && input.Schedule.StartTime == "" && !input.Schedule.StartsAt.IsZero()
	default:
		return false
	}
}

func validKeyAndName(key, name string) bool {
	return len(key) <= 120 && stableKeyPattern.MatchString(key) && name != "" && utf8.RuneCountInString(name) <= 200
}

func validVisibility(value Visibility) bool {
	return value == VisibilityPublic || value == VisibilityInternal
}

func validStatus(value Status) bool {
	return value == StatusActive || value == StatusPaused || value == StatusArchived
}

func validMutationContext(actor, requestID string) bool {
	return strings.TrimSpace(actor) != "" && strings.TrimSpace(requestID) != ""
}
