package recordings

import (
	"encoding/json"
	"testing"
)

func TestCaptureInventoryEndAfterPersistedSeal(t *testing.T) {
	end := 10
	before := Capture{SealedEndSequence: &end, Operations: map[string]CaptureOperation{"seal": {Operation: "seal", Pending: false, Input: nil}}}
	raw, e := json.Marshal(before)
	if e != nil {
		t.Fatal(e)
	}
	var saved Capture
	if e = json.Unmarshal(raw, &saved); e != nil {
		t.Fatal(e)
	}
	got, e := captureInventoryEnd(saved)
	if e != nil || got != 10 {
		t.Fatalf("persisted accepted seal lost boundary: end=%d err=%v", got, e)
	}
	saved.Operations["seal"] = CaptureOperation{Operation: "seal", Pending: true, Input: json.RawMessage(`{"inventory":{}}`)}
	if _, e = captureInventoryEnd(saved); e == nil {
		t.Fatal("accepted pending seal")
	}
}
