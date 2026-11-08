package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type broadcastReconcileRepository struct {
	*captureTestRepository
	BroadcastRepository
	broadcast Broadcast
	recording Recording
}

func (r *broadcastReconcileRepository) ListBroadcastReconciliation(context.Context, time.Time, int) ([]Broadcast, error) {
	return []Broadcast{r.broadcast}, nil
}
func (r *broadcastReconcileRepository) ReconcileBroadcastCleanup(context.Context, time.Time) error {
	return nil
}
func (r *broadcastReconcileRepository) GetBroadcast(context.Context, string) (Broadcast, error) {
	raw, _ := json.Marshal(r.broadcast)
	var b Broadcast
	json.Unmarshal(raw, &b)
	b.SourceManaged = r.broadcast.SourceManaged
	return b, nil
}
func (r *broadcastReconcileRepository) UpdateBroadcast(ctx context.Context, _, _ string, _ time.Time, fn func(context.Context, *Broadcast, *Recording, *Capture) error) (Broadcast, error) {
	raw, _ := json.Marshal(r.broadcast)
	var b Broadcast
	json.Unmarshal(raw, &b)
	b.SourceManaged = r.broadcast.SourceManaged
	c, recording := cloneCapture(r.capture), r.recording
	if err := fn(ctx, &b, &recording, &c); err != nil {
		return b, err
	}
	r.broadcast, r.capture, r.recording = b, c, recording
	return b, nil
}

func TestBroadcastTerminalAuthorityPrecedesProducerSync(t *testing.T) {
	for _, tc := range []struct{ state, phase, wantPhase string }{
		{"failed", "live", "failed"},
		{"expired", "failed", "failed"},
		{"aborted", "cancelled", "cancelled"},
	} {
		for _, batch := range []bool{false, true} {
			t.Run(tc.state+map[bool]string{false: "/direct", true: "/batch"}[batch], func(t *testing.T) {
				now := time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC)
				start := 8
				b := boundBroadcast()
				b.View.RecordingID, b.CaptureIntentID, b.Epoch = "recording", "intent", 1
				b.View.Phase, b.MemberState, b.RangePending = tc.phase, "live", true
				b.StartedAt = &now
				b.View.Boundaries = BroadcastBoundaries{Revision: 3, StartSequence: &start}
				reason := "capture_incomplete"
				repo := &broadcastReconcileRepository{captureTestRepository: &captureTestRepository{capture: Capture{
					IntentID: "intent", RecordingID: "recording", ActorID: "uploader", ActorType: "user", RequestID: "test",
					Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: b.View.Binding.CaptureID, State: tc.state, TerminalReason: &reason, TerminalAt: &now}, AutoPublish: "cancelled"},
				}}, broadcast: b, recording: Recording{ID: "recording", Status: "draft", Broadcast: true}}
				unavailable := true
				var authority assetclient.BroadcastRangeInput
				producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPut {
						var next assetclient.BroadcastRangeInput
						if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
							t.Error(err)
						}
						// Asset fences non-revoked writes after capture termination.
						if unavailable || !next.Revoked || next.MemberState != "blocked" {
							w.WriteHeader(http.StatusConflict)
							return
						}
						authority = next
						json.NewEncoder(w).Encode(next)
						return
					}
					json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: authority.RangeRevision, State: "failed", StartSequence: &start})
				}))
				t.Cleanup(producer.Close)
				svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
				reconcile := func() error {
					if batch {
						return svc.ReconcileBroadcasts(context.Background(), 100)
					}
					return svc.ReconcileBroadcast(context.Background(), "recording")
				}
				if err := reconcile(); !errors.Is(err, assetclient.ErrConflict) {
					t.Fatalf("producer conflict was hidden: %v", err)
				}
				if !repo.broadcast.Revoked || repo.broadcast.MemberState != "blocked" || repo.broadcast.View.Phase != tc.wantPhase || !repo.broadcast.RangePending || repo.broadcast.View.Boundaries.Revision != 4 {
					t.Fatalf("producer conflict prevented durable terminal authority: %+v", repo.broadcast)
				}
				unavailable = false
				if err := reconcile(); err != nil {
					t.Fatalf("retry did not converge: %v", err)
				}
				if repo.broadcast.RangePending || repo.broadcast.RangeApplied != 4 || (tc.phase == "live" && repo.broadcast.EndedAt == nil) || *repo.broadcast.View.Boundaries.StartSequence != 8 || repo.broadcast.View.Boundaries.EndSequenceExclusive != nil {
					t.Fatalf("terminal range did not converge without changing media boundaries: %+v", repo.broadcast)
				}
				revision := repo.broadcast.View.Revision
				if err := reconcile(); err != nil || repo.broadcast.View.Revision != revision || repo.capture.Status.State != tc.state || repo.capture.Status.LiveEnabled || repo.capture.Status.AutoPublish != "cancelled" || repo.recording.Status != "draft" || repo.broadcast.LiveNotificationReady(repo.capture, now) {
					t.Fatalf("retry reopened terminal capture or publication: broadcast=%+v capture=%+v err=%v", repo.broadcast, repo.capture, err)
				}
			})
		}
	}
}

func TestBroadcastTerminalCreateWithoutProducerCaptureDoesNotSyncRange(t *testing.T) {
	b := boundBroadcast()
	b.View.Binding = nil
	b.View.RecordingID, b.CaptureIntentID, b.MemberState = "recording", "intent", "blocked"
	repo := &broadcastReconcileRepository{captureTestRepository: &captureTestRepository{capture: Capture{IntentID: "intent", RecordingID: "recording", Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{State: "expired"}}}}, broadcast: b, recording: Recording{Status: "draft", Broadcast: true}}
	svc := NewService(repo, "", nil, time.Now)
	if err := svc.ReconcileBroadcast(context.Background(), "recording"); err != nil || repo.broadcast.RangePending {
		t.Fatalf("capture without a producer identity queued an undeliverable range: %+v %v", repo.broadcast, err)
	}
}

func pendingBroadcastCaptureFixture() (*broadcastReconcileRepository, time.Time, int) {
	now := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	start := 8
	b := boundBroadcast()
	b.View.RecordingID, b.CaptureIntentID, b.Epoch = "recording", "intent", 1
	b.View.Phase, b.MemberState, b.RangePending = "live", "live", true
	b.View.Boundaries = BroadcastBoundaries{Revision: 3, StartSequence: &start}
	repo := &broadcastReconcileRepository{captureTestRepository: &captureTestRepository{capture: Capture{
		IntentID: "intent", RecordingID: "recording", ActorID: "uploader", ActorType: "user", RequestID: "test", NormalEnd: true, Operations: map[string]CaptureOperation{},
		Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: b.View.Binding.CaptureID, RecordingID: "recording", State: "ready", ExpiresAt: now.Add(time.Hour)}, AutoPublish: "pending"},
	}}, broadcast: b, recording: Recording{ID: "recording", Status: "draft", Broadcast: true}}
	return repo, now, start
}

func TestBroadcastPendingRangeRefreshesInactiveCapture(t *testing.T) {
	repo, now, start := pendingBroadcastCaptureFixture()
	capture := repo.capture.Status.CaptureID
	if repo.capture.Active() || repo.capture.LiveAvailable(now) {
		t.Fatal("fixture must be outside the capture/live refresh selection")
	}
	var authority assetclient.BroadcastRangeInput
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			json.NewDecoder(r.Body).Decode(&authority)
			if !authority.Revoked || authority.MemberState != "blocked" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if repo.capture.Status.State != "failed" || !repo.broadcast.Revoked || repo.broadcast.MemberState != "blocked" {
				t.Error("terminal producer state and revoked authority were not persisted before PUT")
			}
			json.NewEncoder(w).Encode(authority)
		case r.URL.Path == "/priv/recording-captures/"+capture:
			reason := "package_failed"
			json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: "recording", State: "failed", TerminalAt: &now, TerminalReason: &reason})
		case r.URL.Path == "/priv/recording-captures/"+capture+"/broadcast-projection":
			json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: authority.RangeRevision, State: "failed", StartSequence: &start})
		default:
			t.Errorf("unexpected producer call: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer producer.Close()
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	if err := svc.ReconcileBroadcasts(context.Background(), 100); err != nil {
		t.Fatalf("inactive stale capture could not converge: %v", err)
	}
	if repo.capture.Status.State != "failed" || repo.capture.Status.LiveEnabled || repo.capture.Status.AutoPublish != "cancelled" || !repo.broadcast.Revoked || repo.broadcast.RangePending || repo.broadcast.RangeApplied != 4 || repo.recording.Status != "draft" {
		t.Fatalf("trusted terminal state did not close broadcast authority: capture=%+v broadcast=%+v", repo.capture, repo.broadcast)
	}
}

func TestBroadcastCaptureRefreshPreservesAuthorityFences(t *testing.T) {
	for _, mode := range []string{"nonterminal conflict", "unavailable", "late abort", "bounded concurrent commands"} {
		t.Run(mode, func(t *testing.T) {
			repo, now, start := pendingBroadcastCaptureFixture()
			capture := repo.capture.Status.CaptureID
			puts, gets := 0, 0
			var authority assetclient.BroadcastRangeInput
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-HHC-Actor-ID") != "uploader" || r.Header.Get("X-HHC-Request-ID") != "test" {
					t.Error("producer refresh lost persisted uploader provenance")
				}
				switch {
				case r.Method == http.MethodPut:
					puts++
					json.NewDecoder(r.Body).Decode(&authority)
					if mode == "nonterminal conflict" || mode == "unavailable" || mode == "late abort" && !authority.Revoked {
						w.WriteHeader(http.StatusConflict)
						return
					}
					json.NewEncoder(w).Encode(authority)
					if mode == "bounded concurrent commands" {
						repo.broadcast.View.Boundaries.Revision++
					}
				case r.URL.Path == "/priv/recording-captures/"+capture:
					gets++
					if r.URL.Query().Get("limit") != "1" {
						t.Error("authority refresh fetched an unbounded object inventory")
					}
					if mode == "unavailable" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if mode == "late abort" {
						if _, err := repo.UpdateCapture(context.Background(), "intent", func(c *Capture) error { c.Terminate("aborted", "user_abort", now); return nil }); err != nil {
							t.Error(err)
						}
					}

					json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: "recording", State: "ready"})
				default:
					state := "ready"
					if authority.Revoked {
						state = "failed"
					}
					json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: authority.RangeRevision, State: state, StartSequence: &start})
				}
			}))
			defer producer.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			err := svc.ReconcileBroadcast(context.Background(), "recording")
			if mode == "late abort" {
				if err != nil || repo.capture.Status.State != "aborted" || !repo.broadcast.Revoked || repo.broadcast.RangePending {
					t.Fatalf("late ready response revived terminated authority: %+v %v", repo.broadcast, err)
				}
				return
			}
			want := assetclient.ErrConflict
			if mode == "unavailable" || mode == "bounded concurrent commands" {
				want = assetclient.ErrUnavailable
			}
			if !errors.Is(err, want) || repo.broadcast.Revoked || !repo.broadcast.RangePending || repo.recording.Status != "draft" {
				t.Fatalf("failed producer refresh changed authority: broadcast=%+v err=%v", repo.broadcast, err)
			}
			if mode == "bounded concurrent commands" && (puts != 3 || gets != 0) {
				t.Fatalf("successful ranges exceeded three fenced attempts or added producer GETs: puts=%d gets=%d", puts, gets)
			}
			if repo.capture.Status.State != "ready" {
				t.Fatal("conflict or dependency failure inferred terminal capture state")
			}

		})
	}
}

type reboundBroadcastRepository struct {
	*broadcastReconcileRepository
	previous Capture
}

func (r *reboundBroadcastRepository) GetCapture(ctx context.Context, id, recording string) (Capture, error) {
	if id == r.previous.IntentID || id == r.previous.Status.CaptureID {
		return cloneCapture(r.previous), nil
	}
	return r.captureTestRepository.GetCapture(ctx, id, recording)
}
func (r *reboundBroadcastRepository) UpdateCapture(ctx context.Context, id string, fn func(*Capture) error) (Capture, error) {
	if id == r.previous.IntentID {
		c := cloneCapture(r.previous)
		if err := fn(&c); err != nil {
			return c, err
		}
		r.previous = cloneCapture(c)
		return c, nil
	}
	return r.captureTestRepository.UpdateCapture(ctx, id, fn)
}

func TestBroadcastTerminalRefreshRepreparesNewEpochAndOwner(t *testing.T) {
	base, now, _ := pendingBroadcastCaptureFixture()
	repo := &reboundBroadcastRepository{broadcastReconcileRepository: base}
	oldID, nextID := repo.capture.Status.CaptureID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	puts := 0
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			var authority assetclient.BroadcastRangeInput
			json.NewDecoder(r.Body).Decode(&authority)
			if r.URL.Path == "/priv/recording-captures/"+oldID+"/broadcast-range" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if authority.Epoch != 2 || authority.RangeRevision != 1 || authority.Revoked || authority.StartSequence != nil || authority.MemberState != "blocked" || r.Header.Get("X-HHC-Actor-ID") != "replacement" || r.Header.Get("X-HHC-Request-ID") != "new-request" {
				t.Error("old capture authority or provenance was reused after epoch changed")
			}
			json.NewEncoder(w).Encode(authority)
			return
		}
		if r.URL.Path == "/priv/recording-captures/"+oldID {
			if r.Header.Get("X-HHC-Actor-ID") != "uploader" || r.Header.Get("X-HHC-Request-ID") != "test" || r.URL.Query().Get("limit") != "1" {
				t.Error("old capture lookup lost its own bounded provenance")
			}
			repo.previous = cloneCapture(repo.capture)
			repo.capture = cloneCapture(repo.capture)
			repo.capture.IntentID, repo.capture.ActorID, repo.capture.RequestID = "new-intent", "replacement", "new-request"
			repo.capture.Status.CaptureID, repo.capture.Status.State = nextID, "uploading"
			repo.capture.NormalEnd, repo.capture.Status.AutoPublish = false, "cancelled"
			repo.capture.Status.PackageID, repo.capture.Status.StopAcceptedAt = nil, nil
			repo.broadcast.Epoch, repo.broadcast.CaptureIntentID = 2, "new-intent"
			repo.broadcast.View.Phase, repo.broadcast.MemberState = "preview", "blocked"
			repo.broadcast.View.Boundaries = BroadcastBoundaries{Revision: 1}
			repo.broadcast.View.Binding = &BroadcastBinding{CaptureID: nextID, Epoch: 2, ActorID: "replacement"}
			reason := "package_failed"
			json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: oldID, RecordingID: "recording", State: "failed", TerminalAt: &now, TerminalReason: &reason})
			return
		}
		if r.URL.Path != "/priv/recording-captures/"+nextID+"/broadcast-projection" || r.Header.Get("X-HHC-Actor-ID") != "replacement" {
			t.Error("new epoch projection used stale capture or owner")
		}
		json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 2, RangeRevision: 1, State: "pending"})
	}))
	defer producer.Close()
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	if err := svc.ReconcileBroadcast(context.Background(), "recording"); err != nil {
		t.Fatal(err)
	}
	if puts != 2 || repo.previous.Status.State != "failed" || repo.capture.Status.CaptureID != nextID || repo.capture.Status.State != "uploading" || repo.broadcast.Epoch != 2 || repo.broadcast.Revoked || repo.broadcast.RangePending || repo.broadcast.RangeApplied != 1 {
		t.Fatalf("old terminal response overwrote or acknowledged the new epoch: broadcast=%+v err=%v", repo.broadcast, repo.capture.Status.State)
	}
}

func TestBroadcastConflictIdentifiesSafeReconciliationStage(t *testing.T) {
	for _, stage := range []string{"range_apply", "capture_refresh", "range_projection", "projection_poll", "signal_reconcile"} {
		t.Run(stage, func(t *testing.T) {
			repo, now, _ := pendingBroadcastCaptureFixture()
			const id = "private-recording-for-stage-test"
			repo.broadcast.View.RecordingID, repo.capture.RecordingID, repo.capture.Status.RecordingID, repo.recording.ID = id, id, id, id
			if stage == "projection_poll" || stage == "signal_reconcile" {
				repo.broadcast.RangePending, repo.broadcast.RangeApplied = false, 3
			}
			repo.broadcast.SourceManaged = stage == "signal_reconcile"
			if stage == "signal_reconcile" {
				repo.broadcast.StartedAt = &now
				repo.broadcast.View.ScheduledAt = nil
				repo.broadcast.View.Policy.AutoStart, repo.broadcast.View.Policy.AutoEnd = true, true
			}
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPut && stage == "range_projection":
					var authority assetclient.BroadcastRangeInput
					json.NewDecoder(r.Body).Decode(&authority)
					json.NewEncoder(w).Encode(authority)
				case r.Method == http.MethodGet && r.URL.Path == "/priv/recording-captures/"+repo.capture.Status.CaptureID && stage == "range_apply":
					json.NewEncoder(w).Encode(repo.capture.Status.RecordingCaptureStatus)
				default:
					w.WriteHeader(http.StatusConflict)
				}
			}))
			defer producer.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			var err error
			if stage == "projection_poll" || stage == "signal_reconcile" {
				err = svc.ReconcileBroadcasts(context.Background(), 100)
			} else {
				err = svc.ReconcileBroadcast(context.Background(), id)
			}
			if !errors.Is(err, assetclient.ErrConflict) {
				t.Fatalf("conflict classification changed: %v", err)
			}
			if !strings.Contains(err.Error(), "stage="+stage) || !strings.Contains(err.Error(), "recording_ref=") {
				t.Fatalf("conflict stage is indistinguishable: %v", err)
			}
			for _, private := range []string{id, repo.capture.Status.CaptureID, repo.capture.ActorID, repo.capture.RequestID} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("diagnostic included a raw identifier or provenance")
				}
			}
			if repo.broadcast.Revoked || repo.capture.Status.State != "ready" || repo.recording.Status != "draft" {
				t.Fatal("diagnostic changed business authority")
			}
		})
	}
}

func TestBroadcastConflictDiagnosticLeavesOtherErrorsUnchanged(t *testing.T) {
	repo, _, _ := pendingBroadcastCaptureFixture()
	for _, err := range []error{nil, assetclient.ErrUnavailable, context.Canceled, ErrConflict} {
		if got := broadcastReconciliationError("range_apply", repo.broadcast, &repo.capture, err); got != err {
			t.Fatalf("non-asset-conflict error changed: %v", got)
		}
	}
}

func TestBroadcastProjectionPollDiscardsChangedAuthority(t *testing.T) {
	cases := map[string]func(*broadcastReconcileRepository){
		"cancel_auto_publish": func(r *broadcastReconcileRepository) {
			r.capture.Status.AutoPublish = "cancelled"
			r.capture.Operations["cancel-test"] = CaptureOperation{Operation: "cancel_auto_publish"}
		},
		"producer_error_after_stop": func(r *broadcastReconcileRepository) { at := time.Now().UTC(); r.capture.Status.StopAcceptedAt = &at },
		"stop":                      func(r *broadcastReconcileRepository) { at := time.Now().UTC(); r.capture.Status.StopAcceptedAt = &at },
		"terminal":                  func(r *broadcastReconcileRepository) { r.capture.Status.State = "failed" },
		"normal_end":                func(r *broadcastReconcileRepository) { r.capture.NormalEnd = !r.capture.NormalEnd },
		"revoke":                    func(r *broadcastReconcileRepository) { r.broadcast.Revoked = true },
		"epoch":                     func(r *broadcastReconcileRepository) { r.broadcast.Epoch++ },
		"capture":                   func(r *broadcastReconcileRepository) { r.capture.Status.CaptureID = strings.Repeat("f", 32) },
		"binding":                   func(r *broadcastReconcileRepository) { r.broadcast.View.Binding.ActorID = "different-actor" },
		"range_revision":            func(r *broadcastReconcileRepository) { r.broadcast.View.Boundaries.Revision++ },
		"range_boundary": func(r *broadcastReconcileRepository) {
			end := 30
			r.broadcast.View.Boundaries.EndSequenceExclusive = &end
		},
		"newer_projection": func(r *broadcastReconcileRepository) {
			r.broadcast.Projection = &assetclient.BroadcastProjection{Epoch: r.broadcast.Epoch, RangeRevision: r.broadcast.View.Boundaries.Revision, Revision: 9}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			repo, now, start := pendingBroadcastCaptureFixture()
			repo.broadcast.RangePending = false
			repo.broadcast.RangeApplied = repo.broadcast.View.Boundaries.Revision
			var end *int
			if name == "cancel_auto_publish" {
				value := 30
				end = &value
				repo.broadcast.View.Boundaries.EndSequenceExclusive = end
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			calls := 0
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					close(entered)
					<-release
					if name == "producer_error_after_stop" {
						w.WriteHeader(503)
						return
					}
					json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 3, State: "ready", StartSequence: &start, EndSequenceExclusive: end, Revision: 7})
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer producer.Close()
			defer unblock()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			done := make(chan error, 1)
			go func() { done <- svc.ReconcileBroadcasts(context.Background(), 100) }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("no projection read: %v", err)
			case <-time.After(time.Second):
				t.Fatal("projection did not start")
			}
			change(repo)
			unblock()
			err := <-done
			if name != "cancel_auto_publish" && repo.broadcast.Projection != nil && repo.broadcast.Projection.Revision == 7 {
				t.Fatal("stale projection applied after authority changed")
			}
			if name == "cancel_auto_publish" && (err != nil || repo.capture.Status.AutoPublish != "cancelled" || !repo.broadcast.AutoPublicationActivated) {
				t.Fatalf("cancelled auto-publication overwritten or not exercised: %v %+v", err, repo.capture.Status)
			}
			if name == "producer_error_after_stop" && err != nil {
				t.Fatalf("stale producer error blocked current authority: %v", err)
			}
			if name == "newer_projection" && (repo.broadcast.Projection == nil || repo.broadcast.Projection.Revision != 9) {
				t.Fatal("projection revision regressed")
			}
		})
	}
}
