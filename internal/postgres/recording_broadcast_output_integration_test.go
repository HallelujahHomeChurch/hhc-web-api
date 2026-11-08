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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStudioOutputLostReplyResumesOriginalBinding(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, _ := preparedSource(t)
	svc.WithStudioWriters(true)
	allowed := true
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"allowed": allowed})
	}))
	defer accounts.Close()
	svc.WithPublicationAuthorizer(accountclient.New(accounts.URL, "hhc-web-api"))
	output := platform.NewID()
	captureID := strings.Repeat("c", 32)
	attempts := 0
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: captureID, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: output + ".bind", Operation: "create", CaptureID: captureID, AcceptedAt: now}})
	}))
	defer producer.Close()
	svc.WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	input := recordings.BroadcastOutputOpenInput{ProtocolVersion: recordings.BroadcastOutputProtocolVersion, OutputSessionID: output, StreamKey: key}
	if _, err := svc.OpenBroadcastOutput(context.Background(), "first", input); !errors.Is(err, assetclient.ErrUnavailable) {
		t.Fatalf("first failure: %v", err)
	}
	var intent string
	if err := repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_capture WHERE recording_id=$1`, b.View.RecordingID).Scan(&intent); err != nil {
		t.Fatal(err)
	}
	svc.WithStudioWriters(false)
	result, err := svc.OpenBroadcastOutput(context.Background(), "retry", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Broadcast.RecordingID != b.View.RecordingID || result.Session.CaptureID != captureID || result.Broadcast.Binding.ActorID != actor || result.Receipt.State != "completed" || result.Receipt.OperationKey != output+".bind" {
		t.Fatal("incorrect K4 binding identity")
	}
	if _, _, err = svc.AuthorizeBroadcastIngest(ctx, result.Session.Token, b.View.RecordingID, captureID, false); err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(svc).Routes()
	body, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/api/ingest/streams/open", strings.NewReader(string(body)))
	req.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
	req.Header.Set("X-HHC-Request-ID", "retry-http")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || attempts != 2 {
		t.Fatalf("HTTP replay status=%d producer=%d", w.Code, attempts)
	}
	allowed = false
	if _, err = svc.OpenBroadcastOutput(ctx, "revoked", input); !errors.Is(err, recordings.ErrForbidden) {
		t.Fatalf("delegate revocation: %v", err)
	}
	var count int
	repo.db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_capture WHERE recording_id=$1 AND id=$2`, b.View.RecordingID, intent).Scan(&count)
	if count != 1 {
		t.Fatal("original intent lost")
	}
}
