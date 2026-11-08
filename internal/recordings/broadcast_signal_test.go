package recordings

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectSignalCutoffPreservesCaptureAndBoundary(t *testing.T) {
	start, end := 7, 12
	now := time.Now()
	b := Broadcast{SourceManaged: true, StartedAt: &now, View: BroadcastView{Phase: "live", Policy: BroadcastPolicy{AutoStart: true, AutoEnd: true}, Boundaries: BroadcastBoundaries{StartSequence: &start, Revision: 2}}}
	if !b.awaitsDirectSignal() {
		t.Fatal("direct missing")
	}
	changed, e := b.applyDirectSignal(assetclient.BroadcastSignal{State: "ended", EndSequenceExclusive: &end})
	if e != nil || !changed || !b.RangePending || b.View.Phase != "end_pending" || *b.View.Boundaries.EndSequenceExclusive != end || b.EndedAt != nil {
		t.Fatalf("%+v %v", b, e)
	}
	if b.awaitsDirectSignal() {
		t.Fatal("closed range must not move")
	}
	newer := 13
	if _, e = b.applyDirectSignal(assetclient.BroadcastSignal{State: "ended", EndSequenceExclusive: &newer}); e != ErrConflict {
		t.Fatal(e)
	}
}
func TestScheduledAndNeverPublicDoNotUseDirectTimeout(t *testing.T) {
	now := time.Now()
	start := 0
	b := Broadcast{SourceManaged: true, StartedAt: &now, View: BroadcastView{Phase: "live", Policy: BroadcastPolicy{AutoStart: true, AutoEnd: true}, Boundaries: BroadcastBoundaries{StartSequence: &start}}}
	b.View.ScheduledAt = &now
	if b.awaitsDirectSignal() {
		t.Fatal("scheduled")
	}
	b.View.ScheduledAt = nil
	b.StartedAt = nil
	if b.awaitsDirectSignal() {
		t.Fatal("never public")
	}
}

type signalRecoveryRepository struct {
	CaptureRepository
	Repository
	BroadcastRepository
	b Broadcast
	c Capture
}

func (r *signalRecoveryRepository) GetCapture(context.Context, string, string) (Capture, error) {
	return r.c, nil
}
func (r *signalRecoveryRepository) GetBroadcast(context.Context, string) (Broadcast, error) {
	return r.b, nil
}
func (r *signalRecoveryRepository) UpdateBroadcast(ctx context.Context, _, _ string, _ time.Time, fn func(context.Context, *Broadcast, *Recording, *Capture) error) (Broadcast, error) {
	next := r.b
	if err := fn(ctx, &next, &Recording{}, &r.c); err != nil {
		return r.b, err
	}
	r.b = next
	return r.b, nil
}

func TestBroadcastReadRecoversSignalWithoutChangingAuthority(t *testing.T) {
	for _, kind := range []string{"fresh", "still interrupted", "ended", "unavailable", "stopped", "revoked", "scheduled", "healthy", "wrong capture", "wrong epoch", "wrong actor"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
			start := 0
			confirmed := now.Add(-5 * time.Second)
			reason := "signal_grace"
			actor := "018f0000-0000-7000-8000-000000000001"
			capture := strings.Repeat("a", 32)
			repo := &signalRecoveryRepository{b: Broadcast{SourceManaged: true, Epoch: 1, StartedAt: &now, CaptureIntentID: "intent", View: BroadcastView{Revision: 7, Phase: "live", Policy: BroadcastPolicy{AutoStart: true, AutoEnd: true}, Binding: &BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor}, Boundaries: BroadcastBoundaries{Revision: 3, StartSequence: &start}, Health: BroadcastHealth{State: "interrupted", Reason: &reason}}}, c: Capture{IntentID: "intent", ActorType: "user", ActorID: actor, BroadcastEpoch: 1, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: "uploading", ExpiresAt: now.Add(time.Hour)}}}}
			switch kind {
			case "stopped":
				repo.c.Status.StopAcceptedAt = &now
			case "revoked":
				repo.b.Revoked = true
			case "scheduled":
				repo.b.View.ScheduledAt = &now
			case "healthy":
				repo.b.View.Health.State = "healthy"
			case "wrong capture":
				repo.c.Status.CaptureID = strings.Repeat("b", 32)
			case "wrong epoch":
				repo.c.BroadcastEpoch = 2
			case "wrong actor":
				repo.c.ActorID = "other"
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasSuffix(r.URL.Path, "/broadcast-signal") {
					t.Errorf("heavy reconciliation call %s", r.URL.Path)
				}
				var input struct {
					CloseIfIdle bool `json:"closeIfIdle"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.CloseIfIdle {
					t.Errorf("health read can close capture: %v", err)
				}
				if kind == "unavailable" {
					w.WriteHeader(503)
					return
				}
				state := "healthy"
				var end *int
				if kind == "still interrupted" {
					state = "interrupted"
				}
				if kind == "ended" {
					state = "ended"
					cutoff := 9
					end = &cutoff
				}
				json.NewEncoder(w).Encode(assetclient.BroadcastSignal{State: state, LastMediaConfirmedAt: &confirmed, EndSequenceExclusive: end})
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			got, err := svc.GetBroadcast(context.Background(), "recording")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "fresh" {
				if got.Health.State != "healthy" || got.Health.Reason != nil || got.Health.LastConfirmedAt == nil || !got.Health.LastConfirmedAt.Equal(confirmed) || got.Revision != 8 {
					t.Fatalf("not recovered: %+v", got)
				}
			} else if kind != "healthy" && (got.Health.State != "interrupted" || got.Revision != 7) {
				t.Fatalf("unexpected recovery: %+v", got)
			}
			wantCalls := 0
			if kind == "fresh" || kind == "still interrupted" || kind == "ended" || kind == "unavailable" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
			if got.Phase != "live" || got.Boundaries.Revision != 3 || got.Boundaries.EndSequenceExclusive != nil || repo.b.RangePending {
				t.Fatalf("changed authority: %+v", got)
			}
		})
	}
}
