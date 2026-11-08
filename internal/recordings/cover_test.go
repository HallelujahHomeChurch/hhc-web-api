package recordings

import (
	"testing"
	"time"
)

func TestCoverSelectionValidation(t *testing.T) {
	for _, v := range []CoverSelection{{Mode: "auto", CandidateID: "cover-1"}, {Mode: "custom", UploadID: "upload-1"}} {
		if !v.Valid() {
			t.Fatal(v)
		}
	}
	for _, v := range []CoverSelection{{Mode: "auto"}, {Mode: "custom", UploadID: "../other"}, {Mode: "auto", CandidateID: "cover-1", UploadID: "upload-1"}} {
		if v.Valid() {
			t.Fatal("accepted", v)
		}
	}
}

func TestCoverReceiptDoesNotRestoreChangedState(t *testing.T) {
	receipt := CoverReceipt{CoverID: "cover-1", RecordingVersion: 5}
	now := time.Now()
	for _, v := range []Recording{{Version: 6, SelectedCoverID: "cover-1"}, {Version: 5, SelectedCoverID: "cover-2"}} {
		if NewCoverResult(receipt, v, now).Outcome != "state_changed" {
			t.Fatal("historical receipt became current")
		}
	}
	if NewCoverResult(receipt, Recording{Version: 5, SelectedCoverID: "cover-1", DraftExpiresAt: now.Add(time.Hour)}, now).Outcome != "selected" {
		t.Fatal("current selection not accepted")
	}
	if NewCoverResult(receipt, Recording{Version: 5, SelectedCoverID: "cover-1", DraftExpiresAt: now}, now).Outcome != "state_changed" {
		t.Fatal("expired receipt accepted")
	}
}
