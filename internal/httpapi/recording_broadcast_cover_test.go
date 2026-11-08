package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type broadcastCoverRepository struct{ broadcastSummaryRepository }

func (r *broadcastCoverRepository) GetCapture(_ context.Context, capture, recording string) (recordings.Capture, error) {
	if capture != r.capture.Status.CaptureID || recording != r.capture.RecordingID {
		return recordings.Capture{}, recordings.ErrNotFound
	}
	return r.capture, nil
}
func (r *broadcastCoverRepository) GetBroadcast(_ context.Context, id string) (recordings.Broadcast, error) {
	if id != r.recording.ID {
		return recordings.Broadcast{}, recordings.ErrNotFound
	}
	return r.broadcast, nil
}

func TestBroadcastCoverHTTPReadinessAndOwnership(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	const capture = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UTC()
	repo := &broadcastCoverRepository{broadcastSummaryRepository: broadcastSummaryRepository{
		recording: recordings.Recording{ID: id, Broadcast: true, Status: "draft"},
		capture:   recordings.Capture{BroadcastEpoch: 1, RecordingID: id, LiveCover: &recordings.CaptureLiveCover{}, Status: recordings.CaptureStatus{LiveEnabled: true, RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: "validating", ExpiresAt: now.Add(time.Hour)}}},
		broadcast: recordings.Broadcast{CoverSnapshot: recordings.CaptureLiveCover{Scope: "broadcast-" + id, Settings: recordings.LiveCoverSettings{Revision: 8, Selection: recordings.LiveCoverSelection{Mode: "custom", UploadID: "selected-cover"}, CoverState: "pending"}}},
	}}
	calls := 0
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/priv/recording-live-covers/broadcast-"+id+"/uploads/selected-cover/content" || r.URL.Query().Get("recordingId") != id {
			t.Errorf("wrong cover owner: %s", r.URL)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("selected jpeg"))
	}))
	defer asset.Close()
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	request := func(path, scope string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		trustedBulletinAudit(req, scope)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	path := "/api/admin/broadcasts/" + id + "/cover/content"
	pending := request(path, "cms:recordings:read")
	if pending.Code != 404 || !strings.Contains(pending.Body.String(), "broadcast_not_found") || calls != 0 {
		t.Fatalf("pending: %d %s calls=%d", pending.Code, pending.Body, calls)
	}
	repo.broadcast.CoverSnapshot.Settings.CoverState = "ready"
	repo.broadcast.CoverSnapshot.Settings.CoverID = "selected-cover"
	ready := request(path, "cms:recordings:read")
	if ready.Code != 200 || ready.Body.String() != "selected jpeg" || ready.Header().Get("Cache-Control") != "private, no-store" || calls != 1 {
		t.Fatalf("ready: %d %s calls=%d", ready.Code, ready.Body, calls)
	}
	service := httptest.NewRecorder()
	handler.ServeHTTP(service, serviceRequest(http.MethodGet, path))
	if service.Code != 403 || calls != 1 {
		t.Fatalf("service cover: %d %s calls=%d", service.Code, service.Body, calls)
	}
	for _, tc := range []struct {
		path, scope, code string
		status            int
	}{
		{path, "cms:recordings:write", "forbidden", 403},
		{"/api/admin/broadcasts/0199a000-0000-4000-8000-000000000004/cover/content", "cms:recordings:read", "broadcast_not_found", 404},
		{"/api/admin/recordings/" + id + "/captures/" + capture + "/cover-settings", "cms:recordings:read", "capture_not_found", 404},
	} {
		got := request(tc.path, tc.scope)
		if got.Code != tc.status || !strings.Contains(got.Body.String(), tc.code) || calls != 1 {
			t.Fatalf("guard %s: %d %s calls=%d", tc.path, got.Code, got.Body, calls)
		}
	}
}
