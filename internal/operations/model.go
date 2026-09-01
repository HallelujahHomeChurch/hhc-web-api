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

type Meeting struct {
	ID              string
	Key             string
	ChurchUnitID    string
	VenueResourceID string
	Timezone        string
	Schedule        Schedule
	DurationMinutes int
	Version         int64
}

type OccurrenceOverride struct {
	OccurrenceDate  string
	Cancelled       bool
	StartsAt        *time.Time
	DurationMinutes *int
	VenueResourceID *string
	Version         int64
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
