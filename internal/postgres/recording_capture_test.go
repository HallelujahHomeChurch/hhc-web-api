package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func captureFixture(t *testing.T) (*RecordingRepository, recordings.Capture, time.Time) {
	t.Helper()
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	actor := platform.NewID()
	r, err := repo.Create(context.Background(), "capture", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, r.ID)
	input := recordings.CaptureCreateInput{OperationKey: platform.NewID(), ExpectedVersion: r.Version, AutoPublish: true, LiveEnabled: true}
	raw, _ := json.Marshal(input)
	c := recordings.Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "capture-test", RecordingID: r.ID, Create: input, Status: recordings.CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{RecordingID: r.ID, State: "creating", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Objects: []assetclient.RecordingCaptureObject{}, Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, RecordingVersion: r.Version, LiveState: "starting", LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]recordings.CaptureOperation{input.OperationKey: {Operation: "create", Digest: "digest", Input: raw, Pending: true}}}
	return repo, c, now
}
func TestCaptureCreateReplayAndExclusiveBinding(t *testing.T) {
	repo, c, now := captureFixture(t)
	ctx := context.Background()
	first, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c.IntentID = platform.NewID()
	again, err := repo.PrepareCapture(ctx, c, now)
	if err != nil || again.IntentID != first.IntentID {
		t.Fatalf("replay %+v %v", again, err)
	}
	c.Create.AutoPublish = false
	if _, err = repo.PrepareCapture(ctx, c, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("changed payload %v", err)
	}
	c.Create.AutoPublish = true
	c.Create.OperationKey = platform.NewID()
	if _, err = repo.PrepareCapture(ctx, c, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("second active %v", err)
	}
	if err = repo.AddSource(ctx, c.RecordingID, strings.Repeat("a", 32), now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("mixed source %v", err)
	}
	if err = repo.AddPackage(ctx, c.RecordingID, strings.Repeat("b", 32), now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("mixed package %v", err)
	}
}
func TestStopReceiptFencesNewScopes(t *testing.T) {
	repo, c, now := captureFixture(t)
	ctx := context.Background()
	c, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.State = "uploading"
		c.Status.CaptureID = strings.Repeat("a", 32)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	user, scope := platform.NewID(), platform.NewID()
	if err = repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, scope, now); err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.StopAcceptedAt = &now
		c.Status.LiveState = "ending"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, scope, now); err != nil {
		t.Fatalf("registered scope cannot resume: %v", err)
	}
	if err = repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, platform.NewID(), now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("new scope %v", err)
	}
	if err = repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), scope, now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("cross-user scope %v", err)
	}
	if err = repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, scope, now.Add(24*time.Hour)); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("expired scope %v", err)
	}
	saved, err := repo.GetCapture(ctx, c.IntentID, "")
	if err != nil || saved.Status.AutoPublish != "cancelled" || saved.Status.State != "expired" {
		t.Fatalf("expiry not durable %+v %v", saved, err)
	}
}

func TestStopGrantRace(t *testing.T) {
	repo, c, now := captureFixture(t)
	ctx := context.Background()
	c, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.State = "uploading"
		c.Status.CaptureID = strings.Repeat("b", 32)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, user := platform.NewID(), platform.NewID()
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		done <- repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, scope, now)
	}()
	go func() {
		<-start
		_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
			c.Status.StopAcceptedAt = &now
			c.Status.LiveState = "ending"
			return nil
		})
		done <- err
	}()
	close(start)
	first, second := <-done, <-done
	if first != nil && !errors.Is(first, recordings.ErrNotFound) {
		t.Fatal(first)
	}
	if second != nil && !errors.Is(second, recordings.ErrNotFound) {
		t.Fatal(second)
	}
	var count int
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_capture_scope WHERE capture_intent_id=$1`, c.IntentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 1 && (first != nil || second != nil) {
		t.Fatalf("scope committed with rejection %v %v", first, second)
	}
	if count == 0 && first == nil && second == nil {
		t.Fatal("stop denied scope without rejecting admission")
	}
	if err = repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, platform.NewID(), now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("new scope after race %v", err)
	}
}

func TestCaptureHumanEditsFenceIntentAndDraftUnpublishClosesLive(t *testing.T) {
	repo, c, now := captureFixture(t)
	ctx := context.Background()
	c, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.State = "uploading"
		c.Status.CaptureID = strings.Repeat("e", 32)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.UpdateTitle(ctx, c.RecordingID, 1, "human change", c.ActorID, now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err = repo.db.QueryRow(`SELECT human_edit_generation FROM hhc_web.member_recording WHERE id::text=$1`, c.RecordingID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != c.Generation+1 {
		t.Fatalf("human edit did not fence %d != %d", generation, c.Generation+1)
	}
	if _, err = repo.Unpublish(ctx, c.RecordingID, r.Version, c.ActorID, now); err != nil {
		t.Fatalf("draft live cannot be taken down: %v", err)
	}
	saved, err := repo.GetCapture(ctx, c.IntentID, "")
	if err != nil || saved.Status.LiveEnabled || saved.Status.AutoPublish != "cancelled" {
		t.Fatalf("unpublish closure %+v %v", saved, err)
	}
	if err = repo.Delete(ctx, c.RecordingID, r.Version+1, c.ActorID, now, true); err != nil {
		t.Fatal(err)
	}
	saved, err = repo.GetCapture(ctx, c.IntentID, "")
	if err != nil || saved.Status.LiveEnabled || saved.Status.AutoPublish != "cancelled" {
		t.Fatalf("delete closure %+v %v", saved, err)
	}
}

func TestCaptureCreateIntentSurvivesServiceRestart(t *testing.T) {
	repo, c, now := captureFixture(t)
	ctx := auditclient.WithProvenance(context.Background(), c.ActorID, "capture-restart")
	capture := strings.Repeat("f", 32)
	calls := 0
	status := assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: c.RecordingID, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Objects: []assetclient.RecordingCaptureObject{}, Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HHC-Actor-ID") != c.ActorID {
			t.Error("restart did not use persisted actor")
		}
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(status)
			return
		}
		var input struct {
			OperationKey string `json:"operationKey"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		if input.OperationKey != c.Create.OperationKey {
			t.Error("restart changed create key")
		}
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: status, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: c.Create.OperationKey, Operation: "create", AcceptedAt: now, CaptureID: capture}})
	}))
	defer server.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if _, err := service.CreateCapture(ctx, c.RecordingID, c.ActorID, c.Create, true); !errors.Is(err, assetclient.ErrUnavailable) {
		t.Fatalf("lost create response %v", err)
	}
	restarted := recordings.NewService(NewRecordingRepository(repo.db), "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if err := restarted.ReconcileCaptures(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	result, err := restarted.CreateCapture(ctx, c.RecordingID, c.ActorID, c.Create, true)
	if err != nil || result.Capture.CaptureID != capture || calls != 2 {
		t.Fatalf("restart replay %+v %v calls %d", result, err, calls)
	}
}

func TestCaptureAbortCompletionRacePersistsAcrossRestart(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "ready"
		if deleted {
			name = "deleted"
		}
		t.Run(name, func(t *testing.T) {
			repo, c, now := captureFixture(t)
			ctx := auditclient.WithProvenance(context.Background(), c.ActorID, "abort-race")
			c, err := repo.PrepareCapture(ctx, c, now)
			if err != nil {
				t.Fatal(err)
			}
			capture := strings.Repeat("9", 32)
			c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
				c.Status.State = "validating"
				c.Status.CaptureID = capture
				c.Status.StopAcceptedAt = &now
				c.NormalEnd = true
				c.Operations = map[string]recordings.CaptureOperation{}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			aborts, lookups := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					aborts++
					w.WriteHeader(http.StatusConflict)
					return
				}
				lookups++
				if lookups == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: c.RecordingID, State: "ready", PackageID: &capture, Progress: assetclient.RecordingLiveProgress{Revision: 2, Ended: true, EndedAt: &now}})
			}))
			defer server.Close()
			client := assetclient.New(server.URL, "hhc-web-api", "")
			service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(client)
			result, err := service.AbortCapture(ctx, c.RecordingID, capture, c.ActorID, assetclient.RecordingCaptureAbortInput{OperationKey: "abort", ReasonCode: "user_abort"})
			if err != nil {
				t.Fatal(err)
			}
			if deleted {
				if err = repo.Delete(ctx, c.RecordingID, c.Status.RecordingVersion, c.ActorID, now, true); err != nil {
					t.Fatal(err)
				}
			}
			fresh := NewRecordingRepository(repo.db)
			service = recordings.NewService(fresh, "", nil, func() time.Time { return now }).WithPackages(client)
			if err = service.ReconcileCaptures(ctx, 100); err != nil {
				t.Fatal(err)
			}
			saved, err := fresh.GetCapture(ctx, capture, c.RecordingID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Operations["abort"].Pending || *saved.Operations["abort"].Receipt != result.Receipt || saved.Status.AutoPublish != "cancelled" || saved.Status.LiveEnabled || !saved.NormalEnd {
				t.Fatalf("durable cancellation not settled: %+v", saved)
			}
			if !deleted && (saved.Status.State != "ready" || saved.Status.PackageID == nil) {
				t.Fatalf("completion lost: %+v", saved.Status)
			}
			if deleted && (saved.Status.State != "aborted" || saved.Status.TerminalReason == nil || *saved.Status.TerminalReason != "recording_deleted") {
				t.Fatalf("completion revived deleted recording: %+v", saved.Status)
			}
			before := aborts
			if err = service.ReconcileCaptures(ctx, 100); err != nil || before != aborts {
				t.Fatalf("settled command retried: before=%d after=%d err=%v", before, aborts, err)
			}
		})
	}
}
