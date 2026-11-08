package recordings

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestProgressPosition(t *testing.T) {
	duration := 100.0
	for _, tc := range []struct {
		name     string
		position float64
		duration *float64
		want     float64
		invalid  bool
	}{
		{"zero", 0, &duration, 0, false},
		{"clamp rounding", 101, &duration, 100, false},
		{"over duration", 101.01, &duration, 0, true},
		{"live", 120, nil, 120, false},
		{"negative", -0.1, nil, 0, true},
		{"infinite", math.Inf(1), nil, 0, true},
		{"NaN", math.NaN(), &duration, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProgressPosition(tc.position, tc.duration)
			if tc.invalid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("got %v, want ErrInvalid", err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestProgressMutationValidation(t *testing.T) {
	const id = "24657821-dbc1-400a-8143-2a8cd54bb4e6"
	const version = "vod:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claim := ProgressClaim{SessionID: id, ClaimID: id, MediaVersion: version, StartKind: "restart"}
	update := ProgressUpdate{SessionID: id, Generation: id, MediaVersion: version, Sequence: 1, State: "paused"}

	if err := claim.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := update.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		change   func()
		validate func() error
	}{
		{"invalid expected generation", func() { v := ""; claim.ExpectedGeneration = &v }, func() error { return claim.Validate() }},
		{"invalid start kind", func() { claim.StartKind = "auto" }, func() error { return claim.Validate() }},
		{"missing sequence", func() { update.Sequence = 0 }, func() error { return update.Validate() }},
		{"unsafe sequence", func() { update.Sequence = 1 << 53 }, func() error { return update.Validate() }},
		{"invalid state", func() { update.State = "closed" }, func() error { return update.Validate() }},
		{"invalid version", func() { update.MediaVersion = "vod:" }, func() error { return update.Validate() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldClaim, oldUpdate := claim, update
			defer func() { claim, update = oldClaim, oldUpdate }()
			tc.change()
			if err := tc.validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
		})
	}
}

func TestProgressIntentExpiresAndBindsSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	s := &Service{watermarkKey: make([]byte, 32), now: func() time.Time { return now }}
	const version = "vod:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	token := s.progressIntent("recording", "member", version, nil, now.Add(2*time.Minute).Unix())
	if !s.validProgressIntent("recording", "member", version, nil, token) {
		t.Fatal("fresh first claim intent rejected")
	}
	generation := "a-new-owner"
	if s.validProgressIntent("recording", "member", version, &generation, token) ||
		s.validProgressIntent("recording", "other", version, nil, token) {
		t.Fatal("intent replayed against a different snapshot or account")
	}
	now = now.Add(31 * 24 * time.Hour)
	if s.validProgressIntent("recording", "member", version, nil, token) {
		t.Fatal("old first claim intent can recreate a purged row")
	}
}

func TestResumeItemPreservesOwnerSessionIdentity(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	var item ResumeItem
	if err := json.Unmarshal([]byte(`{"sessionId":"`+session+`"}`), &item); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["sessionId"] != session {
		t.Fatalf("missing resume owner session: %s", encoded)
	}
}
