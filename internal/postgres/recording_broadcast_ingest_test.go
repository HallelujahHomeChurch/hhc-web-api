package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKeyOnlyIngestDurableReplayAndRetargetedRecovery(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, bind := preparedSource(t)
	clock := now
	svc = recordings.NewService(repo, "", nil, func() time.Time { return clock }).WithBroadcastWriters(true)
	allowed := true
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"allowed": allowed})
	}))
	defer accounts.Close()
	svc.WithPublicationAuthorizer(accountclient.New(accounts.URL, "hhc-web-api"))
	captureID := strings.Repeat("c", 32)
	producerCalls := 0
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		producerCalls++
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), key) || strings.Contains(string(raw), "hhc_ingest_") {
			t.Error("credential reached Asset")
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: captureID, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: bind.OperationKey, Operation: "create", CaptureID: captureID, AcceptedAt: now}})
	}))
	defer producer.Close()
	svc.WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	result, e := svc.BindBroadcastIngest(context.Background(), "ingest-test", bind)
	if e != nil || result.Session.RecordingID != b.View.RecordingID || result.Session.CaptureID != captureID || result.Broadcast.Binding.ActorID != actor {
		t.Fatalf("bind %v", e)
	}
	again, e := svc.BindBroadcastIngest(context.Background(), "ingest-retry", bind)
	if e != nil || again.Session.CaptureID != captureID || producerCalls != 1 {
		t.Fatalf("duplicate producer %d %v", producerCalls, e)
	}
	// The actual ingest HTTP bootstrap ignores forged human identity and needs no OAuth.
	h := httpapi.NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(svc).Routes()
	rawBind, _ := json.Marshal(bind)
	request := httptest.NewRequest("POST", "/api/ingest/broadcast-source/bindings", strings.NewReader(string(rawBind)))
	request.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
	request.Header.Set("X-HHC-Request-ID", "ingest-http-test")
	request.Header.Set("X-HHC-User-ID", platform.NewID())
	request.Header.Set("X-HHC-Scopes", "*")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("HTTP bootstrap status %d", response.Code)
	}
	var wire struct {
		Data recordings.BroadcastIngestBinding `json:"data"`
	}
	if json.Unmarshal(response.Body.Bytes(), &wire) != nil || wire.Data.Session.CaptureID != captureID || wire.Data.Broadcast.Binding.ActorID != actor {
		t.Fatal("HTTP delegation changed owner/capture")
	}
	owner, epoch, e := svc.AuthorizeBroadcastIngest(context.Background(), result.Session.Token, b.View.RecordingID, captureID, false)
	if e != nil || owner != actor || epoch != 1 {
		t.Fatal(e)
	}
	var persisted string
	if e = repo.db.QueryRow(`SELECT (SELECT jsonb_agg(payload_json)::text FROM hhc_web.member_recording_capture)||coalesce((SELECT jsonb_agg(to_jsonb(o))::text FROM hhc_web.member_recording_broadcast_source_operation o),'')`).Scan(&persisted); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(persisted, key) || strings.Contains(persisted, result.Session.Token) {
		t.Fatal("credential persisted")
	}
	current, e := repo.GetCapture(ctx, captureID, b.View.RecordingID)
	if e != nil {
		t.Fatal(e)
	}
	end := 4
	_, e = repo.UpdateCapture(ctx, current.IntentID, func(c *recordings.Capture) error {
		c.Status.State = "validating"
		c.NormalEnd = true
		c.Status.StopAcceptedAt = &now
		c.SealedEndSequence = &end
		c.Operations["seal"] = recordings.CaptureOperation{Operation: "seal", Receipt: &assetclient.RecordingCaptureReceipt{OperationKey: "seal", Operation: "seal", CaptureID: captureID, AcceptedAt: now}}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	other, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Next event"}, actor, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, other.View.RecordingID)
	if _, e = svc.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "next-target", ExpectedSourceRevision: 3, RecordingID: other.View.RecordingID, ExpectedRevision: other.View.Revision}); e != nil {
		t.Fatal(e)
	}
	clock = now.Add(time.Hour)
	renewed, e := svc.RenewBroadcastIngest(context.Background(), recordings.BroadcastIngestRenewInput{StreamKey: key, SessionToken: result.Session.Token})
	if e != nil || renewed.RecordingID != b.View.RecordingID {
		t.Fatalf("old capture could not recover %v", e)
	}
	if _, _, e = svc.AuthorizeBroadcastIngest(ctx, renewed.Token, other.View.RecordingID, captureID, false); !errors.Is(e, recordings.ErrBroadcastIngestSession) {
		t.Fatal("cross-event token accepted", e)
	}
	allowed = false
	if _, e = svc.RenewBroadcastIngest(ctx, recordings.BroadcastIngestRenewInput{StreamKey: key, SessionToken: renewed.Token}); !errors.Is(e, recordings.ErrForbidden) {
		t.Fatal("revoked owner renewed", e)
	}
	allowed = true
	if _, e = svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "rotate", ExpectedSourceRevision: 4, StreamKey: "hhc_stream_" + strings.Repeat("B", 42) + "A"}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = svc.AuthorizeBroadcastIngest(ctx, renewed.Token, b.View.RecordingID, captureID, false); !errors.Is(e, recordings.ErrBroadcastIngestSession) {
		t.Fatal("old token survived rotation", e)
	}
}

func TestKeyOnlyRevocationAbortReleasesInputAndReplays(t *testing.T) {
	repo, b, ctx, now, svc, actor, _, bind := preparedSource(t)
	captureID := strings.Repeat("d", 32)
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
	defer accounts.Close()
	svc.WithPublicationAuthorizer(accountclient.New(accounts.URL, "hhc-web-api"))
	aborts := 0
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation := "create"
		state := "uploading"
		key := bind.OperationKey
		if strings.HasSuffix(r.URL.Path, "/abort") {
			operation = "abort"
			state = "aborted"
			key = "revoked-abort"
			aborts++
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: captureID, State: state, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: key, Operation: operation, CaptureID: captureID, AcceptedAt: now}})
	}))
	defer producer.Close()
	svc.WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	result, e := svc.BindBroadcastIngest(context.Background(), "bind-revocation", bind)
	if e != nil {
		t.Fatal(e)
	}
	_, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.Revoked = true
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	h := httpapi.NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(svc).Routes()
	base := "/api/ingest/broadcasts/" + b.View.RecordingID + "/captures/" + captureID
	call := func(path, body string, want int) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
		req.Header.Set("X-HHC-Request-ID", "revoked-cleanup")
		req.Header.Set("Authorization", "Bearer "+result.Session.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s status %d", path, w.Code)
		}
	}
	call(base+"/objects", `{}`, 409)
	body := `{"operationKey":"revoked-abort","reasonCode":"user_abort"}`
	call(base+"/abort", body, 200)
	call(base+"/abort", body, 200)
	source, e := svc.GetBroadcastSource(ctx)
	if e != nil || source.InputBusy || aborts != 1 {
		t.Fatalf("cleanup did not release input: busy=%v aborts=%d err=%v", source.InputBusy, aborts, e)
	}
}
