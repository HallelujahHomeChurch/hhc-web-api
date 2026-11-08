package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type previewHTTPRepository struct {
	recordings.Repository
	recordings.BroadcastRepository
	b recordings.Broadcast
	c recordings.Capture
}

func (r *previewHTTPRepository) UpdateBroadcast(ctx context.Context, _ string, _ string, _ time.Time, fn func(context.Context, *recordings.Broadcast, *recordings.Recording, *recordings.Capture) error) (recordings.Broadcast, error) {
	e := fn(ctx, &r.b, &recordings.Recording{}, &r.c)
	return r.b, e
}
func TestPreviewHTTPOptionalScopeAndInvalidReuse(t *testing.T) {
	now := time.Now().UTC()
	id := "018f0000-0000-4000-8000-000000000001"
	capture := strings.Repeat("a", 32)
	repo := &previewHTTPRepository{b: recordings.Broadcast{CaptureIntentID: "intent", Epoch: 1, View: recordings.BroadcastView{RecordingID: id, Binding: &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1}}}, c: recordings.Capture{IntentID: "intent", BroadcastEpoch: 1, Status: recordings.CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: "uploading", ExpiresAt: now.Add(time.Hour)}}}}
	calls := 0
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in assetclient.BroadcastPreviewGrantInput
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(assetclient.BroadcastPreviewGrant{RecordingID: id, CaptureID: capture, Epoch: 1, Purpose: "staff-preview", Scope: in.PlaybackScopeID, MediaPath: "/videos/" + id + "/captures/" + capture + "/previews/" + in.PlaybackScopeID + "/master.m3u8", ExchangeCredential: "synthetic", IssuedAt: now, ExpiresAt: in.ExpiresAt})
	}))
	defer producer.Close()
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))).Routes()
	post := func(scope string, want int) recordings.BroadcastPreviewAccess {
		t.Helper()
		body := map[string]any{"captureId": capture, "epoch": 1}
		if scope != "" {
			body["playbackScopeId"] = scope
		}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/admin/broadcasts/"+id+"/preview-access", strings.NewReader(string(raw)))
		trustedBulletinAudit(req, "cms:recordings:read")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		var result struct {
			Data recordings.BroadcastPreviewAccess `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		return result.Data
	}
	first := post("", 200)
	now = now.Add(4 * time.Minute)
	renew := post(first.PlaybackScopeID, 200)
	if first.PlaybackScopeID == "" || first.MediaURL != renew.MediaURL || !renew.ExpiresAt.After(first.ExpiresAt) {
		t.Fatal("renewal changed route or failed to extend")
	}
	now = renew.ExpiresAt
	post(first.PlaybackScopeID, 404)
	if calls != 2 {
		t.Fatal("expired renewal reached producer or created fallback")
	}
}
