package operations

import "time"

type ScheduleType string

const (
	ScheduleWeekly ScheduleType = "weekly"
	ScheduleOnce   ScheduleType = "once"
)

type OccurrenceStatus string

const (
	OccurrenceScheduled OccurrenceStatus = "scheduled"
	OccurrenceCancelled OccurrenceStatus = "cancelled"
)

type Schedule struct {
	Type       ScheduleType
	DaysOfWeek []time.Weekday
	StartTime  string
	StartsAt   time.Time
}

type Status string

const (
	StatusActive   Status = "active"
	StatusPaused   Status = "paused"
	StatusArchived Status = "archived"
)

type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityInternal Visibility = "internal"
)

type ResourceKind string

const ResourceVenue ResourceKind = "venue"

type ChurchUnit struct {
	ID          string
	Key         string
	Name        string
	Description string
	ParentID    string
	Status      Status
	Version     int64
}

type ChurchUnitInput struct {
	Key         string
	Name        string
	Description string
	ParentID    string
}

type Resource struct {
	ID                 string
	Key                string
	Name               string
	Description        string
	Kind               ResourceKind
	ChurchUnitID       string
	LocationContentID  string
	Timezone           string
	Visibility         Visibility
	ReservationEnabled bool
	Status             Status
	Version            int64
}

type ResourceInput struct {
	Key               string
	Name              string
	Description       string
	Kind              ResourceKind
	ChurchUnitID      string
	LocationContentID string
	Timezone          string
	Visibility        Visibility
}

type Meeting struct {
	ID              string
	Key             string
	Name            string
	Description     string
	ChurchUnitID    string
	VenueResourceID string
	Timezone        string
	Schedule        Schedule
	DurationMinutes int
	Visibility      Visibility
	Status          Status
	Version         int64
}

type MeetingInput struct {
	Key             string
	Name            string
	Description     string
	ChurchUnitID    string
	VenueResourceID string
	Timezone        string
	Schedule        Schedule
	DurationMinutes int
	Visibility      Visibility
}

func (input MeetingInput) Meeting(id string, version int64) Meeting {
	return Meeting{
		ID: id, Key: input.Key, Name: input.Name, Description: input.Description,
		ChurchUnitID: input.ChurchUnitID, VenueResourceID: input.VenueResourceID,
		Timezone: input.Timezone, Schedule: input.Schedule, DurationMinutes: input.DurationMinutes,
		Visibility: input.Visibility, Status: StatusActive, Version: version,
	}
}

type MeetingMutation struct {
	Meeting
	NextOccurrence *Occurrence
}

type OccurrenceOverride struct {
	MeetingID       string
	OccurrenceDate  string
	Cancelled       bool
	StartsAt        *time.Time
	DurationMinutes *int
	VenueResourceID *string
	Reason          string
	Version         int64
}

type OccurrenceOverrideInput struct {
	OccurrenceDate  string
	Cancelled       bool
	StartsAt        *time.Time
	DurationMinutes *int
	VenueResourceID *string
	Reason          string
}

func (input OccurrenceOverrideInput) Override(meetingID string, version int64) OccurrenceOverride {
	return OccurrenceOverride{
		MeetingID: meetingID, OccurrenceDate: input.OccurrenceDate, Cancelled: input.Cancelled,
		StartsAt: input.StartsAt, DurationMinutes: input.DurationMinutes, VenueResourceID: input.VenueResourceID,
		Reason: input.Reason, Version: version,
	}
}

type Occurrence struct {
	ID              string
	MeetingID       string
	MeetingKey      string
	ChurchUnitID    string
	VenueResourceID string
	StartsAt        time.Time
	EndsAt          time.Time
	Status          OccurrenceStatus
	Version         int64
}

type OccurrenceQuery struct {
	From       time.Time
	To         time.Time
	PublicOnly bool
}

type MediaSyncWindow struct {
	StartsAt time.Time
	EndsAt   time.Time
}
