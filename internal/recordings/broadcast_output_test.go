package recordings

import (
	"errors"
	"testing"
)

func TestBroadcastOutputInputValidation(t *testing.T) {
	good := BroadcastOutputOpenInput{ProtocolVersion: "k4-2026-10-15.rc1", OutputSessionID: "018f0000-0000-4000-8000-000000000001", StreamKey: "hhc_stream_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	for _, tc := range []struct {
		name   string
		mutate func(*BroadcastOutputOpenInput)
		want   error
	}{
		{"valid", func(*BroadcastOutputOpenInput) {}, nil},
		{"old protocol", func(v *BroadcastOutputOpenInput) { v.ProtocolVersion = BroadcastIngestProtocolVersion }, ErrBroadcastProtocol},
		{"missing output", func(v *BroadcastOutputOpenInput) { v.OutputSessionID = "" }, ErrInvalid},
		{"path output", func(v *BroadcastOutputOpenInput) { v.OutputSessionID = "../capture" }, ErrInvalid},
		{"upper output", func(v *BroadcastOutputOpenInput) { v.OutputSessionID = "AAAAAAAA-0000-4000-8000-000000000001" }, ErrInvalid},
		{"nil output", func(v *BroadcastOutputOpenInput) { v.OutputSessionID = "00000000-0000-0000-0000-000000000000" }, ErrInvalid},
		{"bad key", func(v *BroadcastOutputOpenInput) { v.StreamKey = "bad" }, ErrBroadcastStreamKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := good
			tc.mutate(&in)
			if err := in.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}
