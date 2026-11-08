package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

type captureTestRepository struct {
	Repository
	mu      sync.Mutex
	capture Capture
}

func cloneCapture(c Capture) Capture {
	raw, _ := json.Marshal(c)
	var copy Capture
	json.Unmarshal(raw, &copy)
	return copy
}
func (r *captureTestRepository) PrepareCapture(_ context.Context, c Capture, _ time.Time) (Capture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.capture.IntentID != "" {
		if r.capture.Create != c.Create || r.capture.RecordingID != c.RecordingID || r.capture.ActorID != c.ActorID {
			return c, ErrConflict
		}
		return cloneCapture(r.capture), nil
	}
	r.capture = cloneCapture(c)
	return c, nil
}
func (r *captureTestRepository) GetCapture(_ context.Context, id, recording string) (Capture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (id != r.capture.IntentID && id != r.capture.Status.CaptureID) || (recording != "" && recording != r.capture.RecordingID) {
		return Capture{}, ErrNotFound
	}
	return cloneCapture(r.capture), nil
}
func (r *captureTestRepository) UpdateCapture(_ context.Context, id string, fn func(*Capture) error) (Capture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := cloneCapture(r.capture)
	if c.IntentID != id {
		return c, ErrNotFound
	}
	if err := fn(&c); err != nil {
		return c, err
	}
	r.capture = cloneCapture(c)
	return c, nil
}
func (r *captureTestRepository) ListPendingCaptures(context.Context, time.Time, int) ([]Capture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []Capture{cloneCapture(r.capture)}, nil
}

func captureServiceFixture(t *testing.T, asset http.HandlerFunc) (*Service, *captureTestRepository, context.Context, string, string, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	actor, id := platform.NewID(), platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "capture-test")
	repo := &captureTestRepository{}
	server := httptest.NewServer(asset)
	t.Cleanup(server.Close)
	service := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	return service, repo, ctx, id, actor, &now
}
func TestCaptureCreateResponseLostReplaysOriginalIntent(t *testing.T) {
	calls := 0
	var intake string
	capture := strings.Repeat("a", 32)
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct{ RecordingID, OperationKey string }
		json.NewDecoder(r.Body).Decode(&input)
		calls++
		if intake == "" {
			intake = input.OperationKey
		}
		if intake != input.OperationKey {
			t.Error("create replay used new key")
		}
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: input.RecordingID, State: "uploading", CreatedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), Objects: []assetclient.RecordingCaptureObject{}, Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: input.OperationKey, Operation: "create", CaptureID: capture, AcceptedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}})
	})
	input := CaptureCreateInput{OperationKey: "original-create", ExpectedVersion: 1, AutoPublish: true, LiveEnabled: true}
	if _, err := service.CreateCapture(ctx, id, actor, input, true); !errors.Is(err, assetclient.ErrUnavailable) {
		t.Fatalf("lost response: %v", err)
	}
	intent := repo.capture.IntentID
	result, err := service.CreateCapture(ctx, id, actor, input, true)
	if err != nil || result.Capture.CaptureID != capture || repo.capture.IntentID != intent {
		t.Fatalf("replay %+v %v", result, err)
	}
	original := result.Receipt
	*now = now.Add(time.Minute)
	result, err = service.CreateCapture(ctx, id, actor, input, true)
	if err != nil || result.Receipt != original || calls != 2 {
		t.Fatalf("completed create replay %+v %v calls %d", result, err, calls)
	}
	input.LiveEnabled = false
	if _, err = service.CreateCapture(ctx, id, actor, input, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed create %v", err)
	}
	if _, err = service.StopCapture(ctx, id, capture, platform.NewID(), "stop-other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user %v", err)
	}
}
func TestCaptureStopReplayDoesNotFreezeAndSealRequiresStop(t *testing.T) {
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("local control contacted Asset")
		w.WriteHeader(503)
	})
	capture := strings.Repeat("b", 32)
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", ExpiresAt: now.Add(24 * time.Hour)}, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	input := assetclient.RecordingCaptureSealInput{OperationKey: "seal", NormalEnd: true, Inventory: assetclient.RecordingPackageInventory{InventoryDigest: "digest", Renditions: make([]assetclient.RecordingRendition, 3)}}
	if _, err := service.SealCapture(ctx, id, capture, actor, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("seal before stop %v", err)
	}
	stop, err := service.StopCapture(ctx, id, capture, actor, "stop")
	if err != nil || stop.Capture.StopAcceptedAt == nil || stop.Capture.State != "uploading" || stop.Capture.LiveState != "ending" {
		t.Fatalf("stop %+v %v", stop, err)
	}
	*now = now.Add(time.Minute)
	again, err := service.StopCapture(ctx, id, capture, actor, "stop")
	if err != nil || again.Receipt != stop.Receipt {
		t.Fatalf("stop replay %+v %v", again, err)
	}
	if _, err = service.CancelCaptureAutoPublish(ctx, id, capture, actor, "stop"); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused operation key %v", err)
	}
	cancel, err := service.CancelCaptureAutoPublish(ctx, id, capture, actor, "cancel")
	if err != nil || cancel.Capture.AutoPublish != "cancelled" || !cancel.Capture.LiveEnabled {
		t.Fatalf("cancel touched live %+v %v", cancel, err)
	}
	close, err := service.CloseCaptureLive(ctx, id, capture, actor, "close")
	if err != nil || close.Capture.LiveEnabled || close.Capture.State != "uploading" {
		t.Fatalf("close stopped capture %+v %v", close, err)
	}
	again, err = service.StopCapture(ctx, id, capture, actor, "stop")
	if err != nil || again.Capture.LiveEnabled {
		t.Fatalf("old stop reopened live %+v %v", again, err)
	}
}
func TestCaptureLateCreateAfterExpiryQueuesCompensation(t *testing.T) {
	service, repo, ctx, id, actor, now := captureServiceFixture(t, nil)
	create := CaptureCreateInput{OperationKey: "create", ExpectedVersion: 1, AutoPublish: true, LiveEnabled: true}
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Create: create, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{State: "creating", RecordingID: id, ExpiresAt: *now}, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{"create": {Operation: "create", Digest: "digest", Pending: true}}}
	aborts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation, key := "create", "create"
		state := "uploading"
		if strings.HasSuffix(r.URL.Path, "/abort") {
			operation = "abort"
			state = "aborted"
			var input assetclient.RecordingCaptureAbortInput
			json.NewDecoder(r.Body).Decode(&input)
			key = input.OperationKey
			aborts++
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{CaptureID: strings.Repeat("c", 32), RecordingID: id, State: state, ExpiresAt: now.Add(24 * time.Hour)}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: key, Operation: operation, CaptureID: strings.Repeat("c", 32), AcceptedAt: *now}})
	}))
	defer server.Close()
	service.WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if err := service.ReconcileCaptures(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if repo.capture.Status.State != "expired" || repo.capture.Status.LiveEnabled || repo.capture.Status.AutoPublish != "cancelled" || aborts != 1 {
		t.Fatalf("stale create revived capture %+v aborts %d", repo.capture, aborts)
	}
	if repo.capture.Status.ExpiresAt.After(*now) {
		t.Fatal("create retry extended 24h deadline")
	}
}
func TestCaptureAbortIsDurableWhenAssetUnavailable(t *testing.T) {
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	capture := strings.Repeat("d", 32)
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", ExpiresAt: now.Add(24 * time.Hour)}, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	input := assetclient.RecordingCaptureAbortInput{OperationKey: "abort", ReasonCode: "encoder_failure"}
	result, err := service.AbortCapture(ctx, id, capture, actor, input)
	if err != nil || result.Capture.State != "aborted" || result.Capture.AutoPublish != "cancelled" || result.Capture.LiveEnabled || !repo.capture.Operations["abort"].Pending {
		t.Fatalf("abort %+v %v", result, err)
	}
	replay, err := service.AbortCapture(ctx, id, capture, actor, input)
	if err != nil || replay.Receipt != result.Receipt {
		t.Fatalf("replay %+v %v", replay, err)
	}
	stale := assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading"}
	if err = mergeCaptureStatus(&repo.capture, stale, *now); err != nil || repo.capture.Status.State != "aborted" {
		t.Fatalf("stale response %v", err)
	}
}
func TestCaptureNormalEndRequiresVerifiedAssetCompletion(t *testing.T) {
	now := time.Now().UTC()
	stop := now.Add(-time.Minute)
	capture := strings.Repeat("f", 32)
	c := Capture{RecordingID: "recording", Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: "freezing", ExpiresAt: now.Add(24 * time.Hour)}, StopAcceptedAt: &stop, LiveState: "ending"}, Operations: map[string]CaptureOperation{}}
	asset := assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: "recording", State: "ready", Progress: assetclient.RecordingLiveProgress{Revision: 1, LastSequence: 299, MediaEndSeconds: 9000, Ended: true, EndedAt: &now}}
	if err := mergeCaptureStatus(&c, asset, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("ready without seal normal end %v", err)
	}
	c.NormalEnd = true
	if err := mergeCaptureStatus(&c, asset, now); err != nil || c.Status.LiveState != "ended" || c.Status.ReplayUntil == nil || !c.Status.ReplayUntil.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("bounded replay %+v %v", c.Status, err)
	}
	c.Expire(now.Add(24 * time.Hour))
	if c.Status.State != "ready" || c.Status.LiveState != "ended" {
		t.Fatal("completed capture was expired as failure")
	}
}

func TestCaptureReconciliationObservesAssetPermanentFailure(t *testing.T) {
	capture := strings.Repeat("e", 32)
	recording := ""
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reason := "media_invalid"
			json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: recording, State: "failed", TerminalReason: &reason})
			return
		}
		w.WriteHeader(410)
	})
	recording = id
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "validating", ExpiresAt: now.Add(24 * time.Hour)}, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	if err := service.ReconcileCaptures(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if repo.capture.Status.State != "failed" || repo.capture.Status.AutoPublish != "cancelled" || repo.capture.Status.LiveEnabled {
		t.Fatalf("Asset terminal result not synchronized %+v", repo.capture)
	}
}

func TestCaptureCreateRequiresPublishForEitherExposureIntent(t *testing.T) {
	service, _, ctx, id, actor, _ := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unauthorized intent reached Asset") })
	for _, input := range []CaptureCreateInput{{OperationKey: "auto", ExpectedVersion: 1, AutoPublish: true}, {OperationKey: "live", ExpectedVersion: 1, LiveEnabled: true}} {
		if _, err := service.CreateCapture(ctx, id, actor, input, false); !errors.Is(err, ErrForbidden) {
			t.Fatalf("intent %+v: %v", input, err)
		}
	}
	serviceCtx := auditclient.WithPrincipalProvenance(ctx, "service", actor, "service-capture")
	if _, err := service.CreateCapture(serviceCtx, id, actor, CaptureCreateInput{OperationKey: "service", ExpectedVersion: 1}, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("service uploader accepted: %v", err)
	}
}

func TestCaptureSigningWithholdsCapabilityWhenAbortWins(t *testing.T) {
	var repo *captureTestRepository
	service, fixture, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		repo.mu.Lock()
		repo.capture.Terminate("aborted", "user_abort", time.Now().UTC())
		repo.mu.Unlock()
		json.NewEncoder(w).Encode([]assetclient.SignedRecordingObject{{Path: "480p/init.mp4", URL: "must-not-escape", Method: "PUT"}})
	})
	repo = fixture
	capture := strings.Repeat("f", 32)
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", ExpiresAt: now.Add(24 * time.Hour)}}, Operations: map[string]CaptureOperation{}}
	urls, err := service.SignCapture(ctx, id, capture, actor, []string{"480p/init.mp4"})
	if err == nil || len(urls) > 0 {
		t.Fatalf("abort race exposed write capability %+v %v", urls, err)
	}
}

func TestCaptureAbortCommandAcknowledgesAlreadyTerminalAsset(t *testing.T) {
	capture := strings.Repeat("1", 32)
	recording := ""
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: recording, State: "failed"})
			return
		}
		w.WriteHeader(409)
	})
	recording = id
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "failed", ExpiresAt: now.Add(24 * time.Hour)}}, Operations: map[string]CaptureOperation{}}
	repo.capture.QueueAbort("terminal", "capture_incomplete")
	if err := service.ReconcileCaptures(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if repo.capture.Operations["terminal"].Pending {
		t.Fatal("already terminal Asset left command pending")
	}
}

func TestCaptureNormalEndedBeforePackageReadySurvivesLiveDeadline(t *testing.T) {
	now := time.Now().UTC()
	ended := now.Add(-time.Hour)
	c := Capture{NormalEnd: true, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{State: "validating", ExpiresAt: now, Progress: assetclient.RecordingLiveProgress{Ended: true, EndedAt: &ended}}, LiveState: "ended", AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	c.Expire(now)
	if c.Status.State != "validating" || c.Status.AutoPublish != "pending" || c.Terminal() {
		t.Fatalf("normal ended background publication cancelled at live cutoff %+v", c)
	}
}

func TestCaptureDeadlineRefreshesNormalEndBeforeExpiringStaleCMS(t *testing.T) {
	capture := strings.Repeat("2", 32)
	recording := ""
	ended := time.Date(2026, 10, 7, 11, 59, 0, 0, time.UTC)
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: recording, State: "validating", Progress: assetclient.RecordingLiveProgress{Revision: 1, Ended: true, EndedAt: &ended}})
	})
	recording = id
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, NormalEnd: true, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "validating", ExpiresAt: *now}, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	if _, err := service.CaptureStatus(ctx, id, capture, actor, "", 1000); err != nil {
		t.Fatal(err)
	}
	if repo.capture.Terminal() || repo.capture.Status.AutoPublish != "pending" {
		t.Fatalf("stale CMS cancelled normal completion %+v", repo.capture)
	}
}

func TestCaptureLostSealReceiptRecoversNormalEndAcrossDeadline(t *testing.T) {
	capture := strings.Repeat("3", 32)
	recording := ""
	ended := time.Date(2026, 10, 7, 11, 59, 0, 0, time.UTC)
	calls := 0
	status := assetclient.RecordingCaptureStatus{CaptureID: capture, State: "validating", Progress: assetclient.RecordingLiveProgress{Revision: 1, Ended: true, EndedAt: &ended}}
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		status.RecordingID = recording
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(status)
			return
		}
		calls++
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: status, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: "seal", Operation: "seal", AcceptedAt: ended, CaptureID: capture}})
	})
	recording = id
	input := assetclient.RecordingCaptureSealInput{OperationKey: "seal", NormalEnd: true, Inventory: assetclient.RecordingPackageInventory{InventoryDigest: "digest", Renditions: make([]assetclient.RecordingRendition, 3)}}
	raw, _ := json.Marshal(input)
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", ExpiresAt: *now}, StopAcceptedAt: &ended, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{"seal": {Operation: "seal", Digest: captureDigest(input), Input: raw, Pending: true}}}
	result, err := service.SealCapture(ctx, id, capture, actor, input)
	if err != nil || result.Capture.State != "validating" || result.Capture.AutoPublish != "pending" || !repo.capture.NormalEnd || calls != 1 {
		t.Fatalf("lost seal normal-end recovery %+v %v calls %d", result, err, calls)
	}
}

func TestCaptureRejectsCrossPackageBinding(t *testing.T) {
	now := time.Now().UTC()
	capture := strings.Repeat("a", 32)
	other := strings.Repeat("b", 32)
	c := Capture{RecordingID: "recording", NormalEnd: true, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: "uploading", ExpiresAt: now.Add(time.Hour)}}, Operations: map[string]CaptureOperation{}}
	value := assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: "recording", State: "freezing", PackageID: &other}
	if err := mergeCaptureStatus(&c, value, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross package bound: %+v %v", c, err)
	}
}

func TestCaptureCompletedCreateReplayRecoversNormalEndAtDeadline(t *testing.T) {
	for _, lostSeal := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost_seal_%t", lostSeal), func(t *testing.T) {
			capture := strings.Repeat("4", 32)
			recording := ""
			ended := time.Date(2026, 10, 7, 11, 59, 0, 0, time.UTC)
			status := assetclient.RecordingCaptureStatus{CaptureID: capture, State: "validating", Progress: assetclient.RecordingLiveProgress{Revision: 1, Ended: true, EndedAt: &ended}}
			service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
				status.RecordingID = recording
				if r.Method == http.MethodGet {
					json.NewEncoder(w).Encode(status)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/seal") {
					t.Fatal("completed create contacted create endpoint")
				}
				json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: status, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: "seal", Operation: "seal", AcceptedAt: ended, CaptureID: capture}})
			})
			recording = id
			input := CaptureCreateInput{OperationKey: "create", ExpectedVersion: 1, AutoPublish: true, LiveEnabled: true}
			original := assetclient.RecordingCaptureReceipt{OperationKey: "create", Operation: "create", AcceptedAt: now.Add(-24 * time.Hour), CaptureID: capture}
			repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Create: input, NormalEnd: !lostSeal, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "validating", ExpiresAt: *now}, StopAcceptedAt: &ended, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{"create": {Operation: "create", Digest: captureDigest(input), Receipt: &original}}}
			if lostSeal {
				seal := assetclient.RecordingCaptureSealInput{OperationKey: "seal", NormalEnd: true}
				raw, _ := json.Marshal(seal)
				repo.capture.Status.State = "uploading"
				repo.capture.Operations["seal"] = CaptureOperation{Operation: "seal", Digest: captureDigest(seal), Input: raw, Pending: true}
			}
			result, err := service.CreateCapture(ctx, id, actor, input, true)
			if err != nil || result.Receipt != original || result.Capture.State != "validating" || result.Capture.AutoPublish != "pending" || !repo.capture.NormalEnd {
				t.Fatalf("completed create replay lost normal end: state=%s auto=%s normal=%t receipt=%+v err=%v", result.Capture.State, result.Capture.AutoPublish, repo.capture.NormalEnd, result.Receipt, err)
			}
		})
	}
}

func TestCaptureUploadLimitPreservesLifecycleCapacity(t *testing.T) {
	for _, operation := range []string{"seal", "abort", "close_live", "cancel_auto_publish"} {
		t.Run(operation, func(t *testing.T) {
			capture := strings.Repeat("5", 32)
			recording := ""
			service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					OperationKey string `json:"operationKey"`
				}
				json.NewDecoder(r.Body).Decode(&input)
				state, accepted := "freezing", "seal"
				if strings.HasSuffix(r.URL.Path, "/abort") {
					state, accepted = "aborted", "abort"
				}
				json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: recording, State: state}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: input.OperationKey, Operation: accepted, CaptureID: capture, AcceptedAt: time.Now().UTC()}})
			})
			recording = id
			operations := map[string]CaptureOperation{"create": {Operation: "create"}}
			for i := range 13000 {
				operations[fmt.Sprintf("declare-%d", i)] = CaptureOperation{Operation: "declare"}
				operations[fmt.Sprintf("confirm-%d", i)] = CaptureOperation{Operation: "confirm"}
			}
			repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", ExpiresAt: now.Add(24 * time.Hour)}, LiveEnabled: true, AutoPublish: "pending"}, Operations: operations}
			if _, err := service.StopCapture(ctx, id, capture, actor, "stop"); err != nil {
				t.Fatal(err)
			}
			var result CaptureResult
			var err error
			switch operation {
			case "seal":
				result, err = service.SealCapture(ctx, id, capture, actor, assetclient.RecordingCaptureSealInput{OperationKey: "seal", NormalEnd: true, Inventory: assetclient.RecordingPackageInventory{InventoryDigest: "digest", Renditions: make([]assetclient.RecordingRendition, 3)}})
			case "abort":
				result, err = service.AbortCapture(ctx, id, capture, actor, assetclient.RecordingCaptureAbortInput{OperationKey: "abort", ReasonCode: "user_abort"})
			case "close_live":
				result, err = service.CloseCaptureLive(ctx, id, capture, actor, "close")
			case "cancel_auto_publish":
				result, err = service.CancelCaptureAutoPublish(ctx, id, capture, actor, "cancel")
			}
			if err != nil || result.Receipt.Operation != operation {
				t.Fatalf("%s blocked at upload limit: receipt=%+v err=%v", operation, result.Receipt, err)
			}
		})
	}
}

func TestCaptureRejectsAbortOfReadyCapture(t *testing.T) {
	calls := 0
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(409) })
	capture := strings.Repeat("6", 32)
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, NormalEnd: true, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "ready", ExpiresAt: now.Add(24 * time.Hour)}, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	if _, err := service.AbortCapture(ctx, id, capture, actor, assetclient.RecordingCaptureAbortInput{OperationKey: "abort", ReasonCode: "user_abort"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ready abort accepted: %v", err)
	}
	if repo.capture.Status.State != "ready" || repo.capture.Status.AutoPublish != "pending" || len(repo.capture.Operations) != 0 || calls != 0 {
		t.Fatalf("rejected abort changed ready capture: state=%s auto=%s operations=%d calls=%d", repo.capture.Status.State, repo.capture.Status.AutoPublish, len(repo.capture.Operations), calls)
	}
}

func TestCaptureControlCapacityRemainsBoundedAndIndependent(t *testing.T) {
	service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	capture := strings.Repeat("7", 32)
	repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", ExpiresAt: now.Add(24 * time.Hour)}, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
	var original CaptureResult
	for i := range 32 {
		result, err := service.StopCapture(ctx, id, capture, actor, fmt.Sprintf("stop-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			original = result
		}
	}
	if _, err := service.StopCapture(ctx, id, capture, actor, "stop-extra"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stop reserve unbounded: %v", err)
	}
	replay, err := service.StopCapture(ctx, id, capture, actor, "stop-0")
	if err != nil || replay.Receipt != original.Receipt {
		t.Fatalf("full reserve denied immutable replay: %+v %v", replay.Receipt, err)
	}
	if _, err = service.CloseCaptureLive(ctx, id, capture, actor, "close"); err != nil {
		t.Fatalf("stop reserve consumed close capacity: %v", err)
	}
	if _, err = service.CancelCaptureAutoPublish(ctx, id, capture, actor, "cancel"); err != nil {
		t.Fatalf("stop reserve consumed cancel capacity: %v", err)
	}
	if _, err = service.AbortCapture(ctx, id, capture, actor, assetclient.RecordingCaptureAbortInput{OperationKey: "abort", ReasonCode: "user_abort"}); err != nil {
		t.Fatalf("stop reserve consumed abort capacity: %v", err)
	}
	if len(repo.capture.Operations) != 35 {
		t.Fatalf("unexpected bounded operation count %d", len(repo.capture.Operations))
	}
}

func TestCaptureAbortSettlesWhenAssetCompletionWins(t *testing.T) {
	for _, deferLookup := range []bool{false, true} {
		t.Run(fmt.Sprintf("deferred_lookup_%t", deferLookup), func(t *testing.T) {
			capture := strings.Repeat("8", 32)
			recording, aborts, lookups := "", 0, 0
			ended := time.Date(2026, 10, 7, 11, 59, 0, 0, time.UTC)
			service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					aborts++
					w.WriteHeader(http.StatusConflict)
					return
				}
				lookups++
				if deferLookup && lookups == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: recording, State: "ready", PackageID: &capture, Progress: assetclient.RecordingLiveProgress{Revision: 2, Ended: true, EndedAt: &ended}})
			})
			recording = id
			repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, NormalEnd: true, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "validating", CreatedAt: *now, ExpiresAt: now.Add(24 * time.Hour)}, StopAcceptedAt: &ended, LiveEnabled: true, AutoPublish: "pending"}, Operations: map[string]CaptureOperation{}}
			input := assetclient.RecordingCaptureAbortInput{OperationKey: "abort", ReasonCode: "user_abort"}
			result, err := service.AbortCapture(ctx, id, capture, actor, input)
			if err != nil || result.Receipt.Operation != "abort" || result.Receipt.AcceptedAt != *now {
				t.Fatalf("local abort acceptance lost: %+v %v", result, err)
			}
			if deferLookup {
				if !repo.capture.Operations["abort"].Pending || repo.capture.Status.LiveEnabled || repo.capture.Status.AutoPublish != "cancelled" {
					t.Fatal("unavailable lookup lost durable abort or its local controls")
				}
				// Restore persisted JSON into a new service to exercise durable replay.
				repo.capture = cloneCapture(repo.capture)
				service = NewService(repo, "", nil, func() time.Time { return *now }).WithPackages(service.packages)
				if err := service.ReconcileCaptures(ctx, 100); err != nil {
					t.Fatal(err)
				}
			}
			c := repo.capture
			op := c.Operations["abort"]
			if c.Status.State != "ready" || c.Status.PackageID == nil || *c.Status.PackageID != capture || !c.NormalEnd || c.Status.LiveEnabled || c.Status.LiveState != "aborted" || c.Status.AutoPublish != "cancelled" || c.Status.TerminalAt != nil || c.Status.TerminalReason != nil || op.Pending || (len(op.Input) != 0 && string(op.Input) != "null") || op.Receipt == nil || *op.Receipt != result.Receipt {
				t.Fatalf("completion race did not settle: status=%+v normal=%t op=%+v", c.Status, c.NormalEnd, op)
			}
			before := aborts
			if err := service.ReconcileCaptures(ctx, 100); err != nil {
				t.Fatal(err)
			}
			replay, err := service.AbortCapture(ctx, id, capture, actor, input)
			if err != nil || replay.Receipt != result.Receipt || aborts != before || repo.capture.Status.AutoPublish != "cancelled" || repo.capture.Status.LiveEnabled || repo.capture.Status.LiveState != "aborted" {
				t.Fatalf("settled abort revived or retried: receipt=%+v aborts=%d before=%d err=%v", replay.Receipt, aborts, before, err)
			}
		})
	}
}

func TestAutoPublishRejectsUnsealedOrMismatchedCaptureBeforeDependencies(t *testing.T) {
	now := time.Now().UTC()
	pkg := strings.Repeat("a", 32)
	for _, name := range []string{"no_normal_end", "no_stop", "validating", "wrong_package"} {
		t.Run(name, func(t *testing.T) {
			c := Capture{NormalEnd: true, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{State: "ready", CaptureID: pkg, PackageID: &pkg}, StopAcceptedAt: &now}}
			switch name {
			case "no_normal_end":
				c.NormalEnd = false
			case "no_stop":
				c.Status.StopAcceptedAt = nil
			case "validating":
				c.Status.State = "validating"
			case "wrong_package":
				other := strings.Repeat("b", 32)
				c.Status.PackageID = &other
			}
			_, err := (&Service{}).autoPublishCapture(context.Background(), nil, c)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("%s reached publication: %v", name, err)
			}
		})
	}
}

func TestCaptureUploadOperationCeilingMatchesS10Capacity(t *testing.T) {
	for _, tc := range []struct {
		count    int
		accepted bool
	}{{26000, true}, {26002, false}} {
		t.Run(fmt.Sprint(tc.count), func(t *testing.T) {
			capture := strings.Repeat("8", 32)
			recording := ""
			service, repo, ctx, id, actor, now := captureServiceFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/confirm") {
					t.Error("unexpected producer operation")
					w.WriteHeader(500)
					return
				}
				json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: recording, State: "uploading"}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: "last-confirm", Operation: "confirm", CaptureID: capture}})
			})
			recording = id
			ops := map[string]CaptureOperation{}
			for i := range tc.count {
				ops[fmt.Sprint(i)] = CaptureOperation{Operation: "confirm"}
			}
			repo.capture = Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: "user", RequestID: "test", RecordingID: id, SegmentFrames: 300, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: id, State: "uploading", ExpiresAt: now.Add(24 * time.Hour)}}, Operations: ops}
			input := assetclient.RecordingCaptureConfirmInput{OperationKey: "last-confirm", Paths: []string{"1080p/seg-004319.m4s"}}
			result, err := service.ConfirmCapture(ctx, id, capture, actor, input)
			if tc.accepted {
				if err != nil || result.Receipt.Operation != "confirm" || len(repo.capture.Operations) != tc.count+1 {
					t.Fatalf("last confirmation rejected: %v", err)
				}
				if _, err = service.ConfirmCapture(ctx, id, capture, actor, input); err != nil || len(repo.capture.Operations) != tc.count+1 {
					t.Fatalf("replay used another receipt: %v", err)
				}
			} else if !errors.Is(err, ErrConflict) || len(repo.capture.Operations) != tc.count {
				t.Fatalf("overflow must fail closed: %v", err)
			}
		})
	}
}
