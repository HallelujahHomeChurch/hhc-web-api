package hhcwebapi

import "testing"

func TestRecordingInteractionWireBoundaries(t *testing.T) {
	heartbeat := compileCandidateSchema(t, "openapi.yaml", "RecordingHeartbeatInput")
	good := map[string]any{"playbackSessionId": "00000000-0000-4000-8000-000000000001", "sequence": float64(1), "state": "playing", "position": float64(120), "deltaPlayingMs": float64(15000)}
	for _, tc := range []struct {
		field string
		value any
	}{{"sequence", 1.5}, {"deltaPlayingMs", float64(30001)}, {"position", float64(-1)}, {"state", "seek"}, {"accountId", "other"}} {
		v := map[string]any{}
		for k, x := range good {
			v[k] = x
		}
		v[tc.field] = tc.value
		if heartbeat.Validate(v) == nil {
			t.Fatalf("accepted %s=%v", tc.field, tc.value)
		}
	}
	if e := heartbeat.Validate(good); e != nil {
		t.Fatal(e)
	}
	envelope := compileCandidateSchema(t, "openapi.yaml", "RecordingInteractionSummaryEnvelope")
	if e := envelope.Validate(map[string]any{"data": map[string]any{"liked": false, "likes": float64(0), "commentsEnabled": true}, "meta": map[string]any{}, "error": nil}); e != nil {
		t.Fatal(e)
	}
}
