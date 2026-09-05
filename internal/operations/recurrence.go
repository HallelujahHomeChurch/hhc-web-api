package operations

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

var occurrenceNamespace = uuid.MustParse("17afce48-c9c9-5a15-9b93-76099e67bd6d")

func ResolveOccurrences(meeting Meeting, overrides []OccurrenceOverride, from, to time.Time) ([]Occurrence, error) {
	if !from.Before(to) {
		return nil, nil
	}
	location, err := time.LoadLocation(meeting.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load meeting timezone: %w", err)
	}
	overridesByDate := make(map[string]OccurrenceOverride, len(overrides))
	for _, override := range overrides {
		if _, err := time.ParseInLocation(time.DateOnly, override.OccurrenceDate, location); err != nil {
			return nil, fmt.Errorf("parse override date %q: %w", override.OccurrenceDate, err)
		}
		if _, exists := overridesByDate[override.OccurrenceDate]; exists {
			return nil, fmt.Errorf("duplicate override date %q", override.OccurrenceDate)
		}
		overridesByDate[override.OccurrenceDate] = override
	}

	var dates []string
	switch meeting.Schedule.Type {
	case ScheduleOnce:
		if meeting.Schedule.StartsAt.IsZero() {
			return nil, fmt.Errorf("once schedule requires startsAt")
		}
		dates = []string{meeting.Schedule.StartsAt.In(location).Format(time.DateOnly)}
	case ScheduleWeekly:
		if _, err := time.Parse("15:04", meeting.Schedule.StartTime); err != nil {
			return nil, fmt.Errorf("parse weekly start time: %w", err)
		}
		weekdays := make(map[time.Weekday]struct{}, len(meeting.Schedule.DaysOfWeek))
		for _, weekday := range meeting.Schedule.DaysOfWeek {
			if weekday < time.Sunday || weekday > time.Saturday {
				return nil, fmt.Errorf("invalid weekday %d", weekday)
			}
			weekdays[weekday] = struct{}{}
		}
		if len(weekdays) == 0 {
			return nil, fmt.Errorf("weekly schedule requires weekdays")
		}
		candidateDates := map[string]struct{}{}
		startDate := from.In(location).AddDate(0, 0, -1)
		endDate := to.In(location)
		for date := time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, location); !date.After(endDate); date = date.AddDate(0, 0, 1) {
			if _, ok := weekdays[date.Weekday()]; ok {
				candidateDates[date.Format(time.DateOnly)] = struct{}{}
			}
		}
		for date := range overridesByDate {
			parsed, _ := time.ParseInLocation(time.DateOnly, date, location)
			if _, ok := weekdays[parsed.Weekday()]; ok {
				candidateDates[date] = struct{}{}
			}
		}
		dates = make([]string, 0, len(candidateDates))
		for date := range candidateDates {
			dates = append(dates, date)
		}
		sort.Strings(dates)
	default:
		return nil, fmt.Errorf("unsupported schedule type %q", meeting.Schedule.Type)
	}

	result := make([]Occurrence, 0, len(dates))
	for _, localDate := range dates {
		startsAt, err := scheduledStart(meeting.Schedule, localDate, location)
		if err != nil {
			return nil, err
		}
		duration := meeting.DurationMinutes
		venueID := meeting.VenueResourceID
		status := OccurrenceScheduled
		version := meeting.Version
		if override, ok := overridesByDate[localDate]; ok {
			if override.StartsAt != nil {
				startsAt = *override.StartsAt
			}
			if override.DurationMinutes != nil {
				duration = *override.DurationMinutes
			}
			if override.VenueResourceID != nil {
				venueID = *override.VenueResourceID
			}
			if override.Cancelled {
				status = OccurrenceCancelled
			}
			if override.Version > version {
				version = override.Version
			}
		}
		if duration < 1 || duration > 1440 {
			return nil, fmt.Errorf("duration minutes must be between 1 and 1440")
		}
		endsAt := startsAt.Add(time.Duration(duration) * time.Minute)
		if startsAt.Before(to) && endsAt.After(from) {
			result = append(result, Occurrence{
				MeetingName:     meeting.Name,
				OccurrenceDate:  localDate,
				Timezone:        meeting.Timezone,
				ID:              occurrenceID(meeting.ID, localDate),
				MeetingID:       meeting.ID,
				MeetingKey:      meeting.Key,
				ChurchUnitID:    meeting.ChurchUnitID,
				VenueResourceID: venueID,
				StartsAt:        startsAt,
				EndsAt:          endsAt,
				Status:          status,
				Version:         version,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartsAt.Equal(result[j].StartsAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].StartsAt.Before(result[j].StartsAt)
	})
	return result, nil
}

func scheduledStart(schedule Schedule, localDate string, location *time.Location) (time.Time, error) {
	if schedule.Type == ScheduleOnce {
		return schedule.StartsAt, nil
	}
	date, err := time.ParseInLocation(time.DateOnly, localDate, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse occurrence date: %w", err)
	}
	clock, err := time.Parse("15:04", schedule.StartTime)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse weekly start time: %w", err)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, location), nil
}

func occurrenceID(meetingID, localDate string) string {
	return uuid.NewSHA1(occurrenceNamespace, []byte(meetingID+"|"+localDate)).String()
}
