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
	Type       ScheduleType   `json:"type"`
	DaysOfWeek []time.Weekday `json:"daysOfWeek,omitempty"`
	StartTime  string         `json:"startTime,omitempty"`
	StartsAt   time.Time      `json:"startsAt,omitzero"`
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
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ParentID    string `json:"parentId,omitempty"`
	Status      Status `json:"status"`
	Version     int64  `json:"version"`
}

type ChurchUnitInput struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ParentID    string `json:"parentId,omitempty"`
}

type Resource struct {
	ID                 string       `json:"id"`
	Key                string       `json:"key"`
	Name               string       `json:"name"`
	Description        string       `json:"description,omitempty"`
	Kind               ResourceKind `json:"kind"`
	ChurchUnitID       string       `json:"churchUnitId"`
	LocationContentID  string       `json:"locationContentId,omitempty"`
	Timezone           string       `json:"timezone"`
	Visibility         Visibility   `json:"visibility"`
	ReservationEnabled bool         `json:"reservationEnabled"`
	Status             Status       `json:"status"`
	Version            int64        `json:"version"`
}

type ResourceInput struct {
	Key               string       `json:"key"`
	Name              string       `json:"name"`
	Description       string       `json:"description,omitempty"`
	Kind              ResourceKind `json:"kind"`
	ChurchUnitID      string       `json:"churchUnitId"`
	LocationContentID string       `json:"locationContentId,omitempty"`
	Timezone          string       `json:"timezone"`
	Visibility        Visibility   `json:"visibility"`
}

type Meeting struct {
	ID              string     `json:"id"`
	Key             string     `json:"key"`
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	ChurchUnitID    string     `json:"churchUnitId"`
	VenueResourceID string     `json:"venueResourceId"`
	Timezone        string     `json:"timezone"`
	Schedule        Schedule   `json:"schedule"`
	DurationMinutes int        `json:"durationMinutes"`
	Visibility      Visibility `json:"visibility"`
	Status          Status     `json:"status"`
	Version         int64      `json:"version"`
}

type MeetingInput struct {
	Key             string     `json:"key"`
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	ChurchUnitID    string     `json:"churchUnitId"`
	VenueResourceID string     `json:"venueResourceId"`
	Timezone        string     `json:"timezone"`
	Schedule        Schedule   `json:"schedule"`
	DurationMinutes int        `json:"durationMinutes"`
	Visibility      Visibility `json:"visibility"`
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
	NextOccurrence *Occurrence `json:"nextOccurrence,omitempty"`
}

type OccurrenceOverride struct {
	MeetingID       string     `json:"meetingId"`
	OccurrenceDate  string     `json:"occurrenceDate"`
	Cancelled       bool       `json:"cancelled"`
	StartsAt        *time.Time `json:"startsAt,omitempty"`
	DurationMinutes *int       `json:"durationMinutes,omitempty"`
	VenueResourceID *string    `json:"venueResourceId,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Version         int64      `json:"version"`
}

type OccurrenceOverrideInput struct {
	OccurrenceDate  string     `json:"occurrenceDate,omitempty"`
	Cancelled       bool       `json:"cancelled"`
	StartsAt        *time.Time `json:"startsAt,omitempty"`
	DurationMinutes *int       `json:"durationMinutes,omitempty"`
	VenueResourceID *string    `json:"venueResourceId,omitempty"`
	Reason          string     `json:"reason,omitempty"`
}

func (input OccurrenceOverrideInput) Override(meetingID string, version int64) OccurrenceOverride {
	return OccurrenceOverride{
		MeetingID: meetingID, OccurrenceDate: input.OccurrenceDate, Cancelled: input.Cancelled,
		StartsAt: input.StartsAt, DurationMinutes: input.DurationMinutes, VenueResourceID: input.VenueResourceID,
		Reason: input.Reason, Version: version,
	}
}

type Occurrence struct {
	MeetingName     string           `json:"meetingName"`
	OccurrenceDate  string           `json:"occurrenceDate"`
	Timezone        string           `json:"timezone"`
	ID              string           `json:"occurrenceId"`
	MeetingID       string           `json:"meetingId"`
	MeetingKey      string           `json:"meetingKey"`
	ChurchUnitID    string           `json:"churchUnitId"`
	VenueResourceID string           `json:"venueResourceId"`
	StartsAt        time.Time        `json:"startsAt"`
	EndsAt          time.Time        `json:"endsAt"`
	Status          OccurrenceStatus `json:"status"`
	Version         int64            `json:"version"`
}

type OccurrenceQuery struct {
	From       time.Time
	To         time.Time
	PublicOnly bool
}

type MediaSyncWindow struct {
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}
