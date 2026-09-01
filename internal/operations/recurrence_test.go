package operations

import (
	"testing"
	"time"
)

func TestResolveWeeklyOccurrencesUsesLocalTimezoneAndMultipleWeekdays(t *testing.T) {
	meeting := Meeting{
		ID:              "11111111-1111-4111-8111-111111111111",
		Timezone:        "Asia/Taipei",
		Schedule:        Schedule{Type: ScheduleWeekly, DaysOfWeek: []time.Weekday{time.Tuesday, time.Sunday}, StartTime: "09:30"},
		DurationMinutes: 90,
	}
	from := time.Date(2026, 9, 5, 16, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)

	got, err := ResolveOccurrences(meeting, nil, from, to)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{
		time.Date(2026, 9, 6, 1, 30, 0, 0, time.UTC),
		time.Date(2026, 9, 8, 1, 30, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d occurrences: %+v", len(got), got)
	}
	for i := range want {
		if !got[i].StartsAt.Equal(want[i]) || !got[i].EndsAt.Equal(want[i].Add(90*time.Minute)) {
			t.Fatalf("occurrence %d = %s..%s", i, got[i].StartsAt, got[i].EndsAt)
		}
	}
}

func TestResolveOnceOccurrenceUsesHalfOpenRange(t *testing.T) {
	startsAt := time.Date(2026, 11, 28, 14, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	meeting := Meeting{
		ID:              "22222222-2222-4222-8222-222222222222",
		Timezone:        "Asia/Taipei",
		Schedule:        Schedule{Type: ScheduleOnce, StartsAt: startsAt},
		DurationMinutes: 60,
	}

	got, err := ResolveOccurrences(meeting, nil, startsAt.Add(time.Hour), startsAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("occurrence ending at range start must be excluded: %+v", got)
	}

	got, err = ResolveOccurrences(meeting, nil, startsAt.Add(-time.Hour), startsAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("occurrence starting at range end must be excluded: %+v", got)
	}
}

func TestMovedOverrideKeepsOccurrenceID(t *testing.T) {
	meeting := Meeting{
		ID:              "33333333-3333-4333-8333-333333333333",
		Timezone:        "Asia/Taipei",
		Schedule:        Schedule{Type: ScheduleWeekly, DaysOfWeek: []time.Weekday{time.Sunday}, StartTime: "09:30"},
		DurationMinutes: 60,
	}
	from := time.Date(2026, 9, 6, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	to := from.Add(48 * time.Hour)
	base, err := ResolveOccurrences(meeting, nil, from, to)
	if err != nil {
		t.Fatal(err)
	}
	movedStart := from.Add(33 * time.Hour)
	moved, err := ResolveOccurrences(meeting, []OccurrenceOverride{{OccurrenceDate: "2026-09-06", StartsAt: &movedStart}}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 1 || len(moved) != 1 || base[0].ID != moved[0].ID {
		t.Fatalf("base=%+v moved=%+v", base, moved)
	}
	if !moved[0].StartsAt.Equal(movedStart) {
		t.Fatalf("moved start = %s", moved[0].StartsAt)
	}
}

func TestCancelledOverrideIsIncluded(t *testing.T) {
	meeting := Meeting{
		ID:              "44444444-4444-4444-8444-444444444444",
		Timezone:        "Asia/Taipei",
		Schedule:        Schedule{Type: ScheduleWeekly, DaysOfWeek: []time.Weekday{time.Sunday}, StartTime: "09:30"},
		DurationMinutes: 60,
	}
	from := time.Date(2026, 9, 6, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	got, err := ResolveOccurrences(meeting, []OccurrenceOverride{{OccurrenceDate: "2026-09-06", Cancelled: true}}, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != OccurrenceCancelled {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveOccurrencesUsesIANADaylightSavingRules(t *testing.T) {
	meeting := Meeting{
		ID:              "55555555-5555-4555-8555-555555555555",
		Timezone:        "America/New_York",
		Schedule:        Schedule{Type: ScheduleWeekly, DaysOfWeek: []time.Weekday{time.Sunday}, StartTime: "09:30"},
		DurationMinutes: 60,
	}
	from := time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC)
	got, err := ResolveOccurrences(meeting, nil, from, from.Add(72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 8, 13, 30, 0, 0, time.UTC)
	if len(got) != 1 || !got[0].StartsAt.Equal(want) {
		t.Fatalf("got %+v, want start %s", got, want)
	}
}

func TestOccurrenceIDsAndSortingAreDeterministic(t *testing.T) {
	meeting := Meeting{
		ID:              "66666666-6666-4666-8666-666666666666",
		Timezone:        "Asia/Taipei",
		Schedule:        Schedule{Type: ScheduleWeekly, DaysOfWeek: []time.Weekday{time.Monday, time.Sunday}, StartTime: "09:30"},
		DurationMinutes: 60,
	}
	from := time.Date(2026, 9, 6, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	sameStart := from.Add(12 * time.Hour)
	overrides := []OccurrenceOverride{
		{OccurrenceDate: "2026-09-07", StartsAt: &sameStart},
		{OccurrenceDate: "2026-09-06", StartsAt: &sameStart},
	}

	first, err := ResolveOccurrences(meeting, overrides, from, from.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveOccurrences(meeting, []OccurrenceOverride{overrides[1], overrides[0]}, from, from.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("non-deterministic order: first=%+v second=%+v", first, second)
		}
	}
	if first[0].ID >= first[1].ID {
		t.Fatalf("tie must sort by ID: %+v", first)
	}
}
