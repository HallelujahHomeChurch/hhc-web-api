package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOutputRejectsUnknownRecoveryVersion(t *testing.T) {
	var input BroadcastOutputOpenInput
	if e := json.Unmarshal([]byte(`{"protocolVersion":"k4-2026-10-15.rc1","outputSessionId":"018f0000-0000-4000-8000-000000000001","streamKey":"hhc_stream_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","recoveryVersion":"unknown"}`), &input); e != nil {
		t.Fatal(e)
	}
	if e := input.Validate(); !errors.Is(e, ErrInvalid) {
		t.Fatalf("unknown recovery capability accepted: %v", e)
	}
}

func TestRecoverySnapshotDoesNotRegressOrClearEnd(t *testing.T) {
	now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	deadline := now.Add(5 * time.Minute)
	common := 21
	b := Broadcast{}
	signal := assetclient.BroadcastSignal{RecoveryVersion: BroadcastRecoveryVersion, State: "interrupted", Deadline: &deadline, LastCommonConfirmedAt: &now, CommonConfirmedEndSequenceExclusive: &common}
	b.applyRecoverySignal(signal, BroadcastRecoveryVersion)
	if b.Recovery.Version == nil || b.Recovery.State != "interrupted" {
		t.Fatal(b.Recovery)
	}
	older := 20
	signal.CommonConfirmedEndSequenceExclusive = &older
	signal.State = "healthy"
	b.applyRecoverySignal(signal, BroadcastRecoveryVersion)
	if b.Recovery.State != "interrupted" || *b.Recovery.CommonConfirmedEndSequenceExclusive != 21 {
		t.Fatal("old replica result regressed snapshot")
	}
	signal.CommonConfirmedEndSequenceExclusive = &common
	signal.State = "ended"
	b.applyRecoverySignal(signal, BroadcastRecoveryVersion)
	b.recordPublicEnd("idle_timeout", &common)
	signal.State = "healthy"
	b.applyRecoverySignal(signal, BroadcastRecoveryVersion)
	if b.Recovery.State != "ended" {
		t.Fatal("end resurrected")
	}
	b.RevokePublic()
	if b.PublicEnd.Reason != "idle_timeout" {
		t.Fatal("first cause overwritten")
	}
	b.applyRecoverySignal(assetclient.BroadcastSignal{State: "healthy"}, BroadcastRecoveryVersion)
	if b.Recovery.Version != nil || b.Recovery.State != "unsupported" || b.PublicEnd == nil {
		t.Fatal("legacy rollback misrepresented capability")
	}
}

func TestRecoveryCapabilityCannotUpgradeLegacyCapture(t *testing.T) {
	b := Broadcast{}
	b.applyRecoverySignal(assetclient.BroadcastSignal{RecoveryVersion: BroadcastRecoveryVersion, State: "healthy"}, "")
	if b.Recovery.Version != nil || b.Recovery.State != "unsupported" {
		t.Fatal("legacy capture upgraded")
	}
}

func TestD2ControlUsesBoundedExistingRefreshAndAtomicBinding(t *testing.T) {
	for _, state := range []string{"healthy", "interrupted", "stopped", "revoked", "legacy", "wrong-epoch", "unavailable", "recovered"} {
		t.Run(state, func(t *testing.T) {
			now := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
			start, common := 0, 21
			deadline := now.Add(5 * time.Minute)
			reason := "signal_grace"
			actor := "018f0000-0000-7000-8000-000000000001"
			capture := strings.Repeat("a", 32)
			repo := &signalRecoveryRepository{b: Broadcast{SourceManaged: true, Epoch: 1, StartedAt: &now, CaptureIntentID: "intent", View: BroadcastView{RecordingID: "recording", Revision: 7, Phase: "live", Policy: BroadcastPolicy{AutoStart: true, AutoEnd: true}, Binding: &BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor}, Boundaries: BroadcastBoundaries{StartSequence: &start}, Health: BroadcastHealth{State: "interrupted", Reason: &reason}}}, c: Capture{RecoveryVersion: BroadcastRecoveryVersion, IntentID: "intent", ActorType: "user", ActorID: actor, BroadcastEpoch: 1, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: "uploading", ExpiresAt: now.Add(time.Hour)}}}}
			switch state {
			case "healthy":
				repo.b.View.Health.State = "healthy"
			case "stopped":
				repo.c.Status.StopAcceptedAt = &now
			case "revoked":
				repo.b.Revoked = true
			case "legacy":
				repo.c.RecoveryVersion = ""
			case "wrong-epoch":
				repo.b.Epoch = 2
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if state == "unavailable" {
					w.WriteHeader(503)
					return
				}
				var in struct {
					CloseIfIdle bool `json:"closeIfIdle"`
				}
				if e := json.NewDecoder(r.Body).Decode(&in); e != nil || in.CloseIfIdle {
					t.Error("control attempted close")
				}
				signalState := "interrupted"
				if state == "recovered" {
					signalState = "healthy"
				}
				json.NewEncoder(w).Encode(assetclient.BroadcastSignal{RecoveryVersion: BroadcastRecoveryVersion, State: signalState, Deadline: &deadline, LastCommonConfirmedAt: &now, CommonConfirmedEndSequenceExclusive: &common})
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			got, e := svc.GetBroadcastControlD2(context.Background(), "recording", actor, capture, 1)
			if state == "wrong-epoch" {
				if !errors.Is(e, ErrBroadcastEpoch) {
					t.Fatal(e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			wantCalls := 0
			if state == "interrupted" {
				wantCalls = 1
				if got.Recovery.Version == nil || got.Recovery.State != "interrupted" {
					t.Fatal(got)
				}
			}
			if state == "unavailable" || state == "recovered" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
			wantRevision := int64(7)
			if state == "recovered" {
				wantRevision = 8
				if repo.b.View.Health.State != "healthy" || repo.b.View.Health.Reason != nil {
					t.Fatal("refresh did not stop repeated polling")
				}
			}
			if got.Revision != wantRevision || got.ControlVersion != BroadcastControlD2Version {
				t.Fatal("D1 revision or D2 version changed")
			}
		})
	}
}
