package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/jackc/pgx/v5/pgconn"
)

type playbackFailureRepository struct {
	recordings.Repository
	recordings.LiveRepository
	capture       recordings.Capture
	lookupFailure func() error
	scopeFailure  func() error
}

func (r *playbackFailureRepository) GetLiveCapture(context.Context, string, string) (recordings.Capture, error) {
	if r.lookupFailure != nil {
		return recordings.Capture{}, r.lookupFailure()
	}
	return r.capture, nil
}

func (r *playbackFailureRepository) InspectCaptureScope(context.Context, string, string, string, string, time.Time, bool) (recordings.Capture, error) {
	return recordings.Capture{}, r.scopeFailure()
}

func TestLivePlaybackCallerCancellation(t *testing.T) {
	const recordingID = "018f0000-0000-4000-8000-000000000001"
	captureID := strings.Repeat("a", 32)
	now := time.Now().UTC()
	status := assetclient.RecordingCaptureStatus{
		CaptureID: captureID, RecordingID: recordingID, State: "uploading",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Progress: assetclient.RecordingLiveProgress{LastSequence: 5, Revision: 1},
	}
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /priv/recording-captures/" + captureID:
			json.NewEncoder(w).Encode(status)
		case "GET /priv/recording-captures/" + captureID + "/progress":
			json.NewEncoder(w).Encode(status.Progress)
		default:
			t.Errorf("unexpected Asset call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer asset.Close()

	for _, tc := range []struct {
		name          string
		atScope       bool
		cancelRequest bool
		err           error
		status        int
	}{
		{"caller cancels during capture lookup", false, true, context.Canceled, 499},
		{"caller cancels during scope inspection", true, true, context.Canceled, 499},
		{"only the dependency cancels", false, false, context.Canceled, 503},
		{"database capacity failure coincides with cancellation", true, true, &pgconn.PgError{Code: "53300"}, 503},
		{"dependency deadline coincides with cancellation", false, true, context.DeadlineExceeded, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := false
			fail := func() error {
				reached = true
				if tc.cancelRequest {
					cancel()
				}
				return fmt.Errorf("repository: %w", tc.err)
			}
			repo := &playbackFailureRepository{capture: recordings.Capture{
				Status: recordings.CaptureStatus{RecordingCaptureStatus: status, LiveEnabled: true},
			}}
			if tc.atScope {
				repo.scopeFailure = fail
			} else {
				repo.lookupFailure = fail
			}
			svc := recordings.NewService(repo, "https://media.invalid", []byte(strings.Repeat("x", 32)), func() time.Time { return now }).
				WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))
			h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).
				WithRecordings(svc).
				WithBulletinEntitlements(entitlementCheckerStub{decision: "allow"}).Routes()
			body := fmt.Sprintf(`{"captureId":"%s","playbackScopeId":"018f0000-0000-4000-8000-000000000002"}`, captureID)
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/member/recordings/"+recordingID+"/live/playback", strings.NewReader(body))
			trusted(req, "")
			req.Header.Set("X-HHC-User-ID", "018f0000-0000-4000-8000-000000000003")
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			wantCode, wantRetry := "capture_unavailable", "5"
			if tc.status == 499 {
				wantCode, wantRetry = "request_canceled", ""
			}
			var envelope struct {
				Data  json.RawMessage
				Error struct{ Code string }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if !reached || response.Code != tc.status || envelope.Error.Code != wantCode || string(envelope.Data) != "null" || response.Header().Get("Retry-After") != wantRetry {
				t.Fatalf("reached stage=%t, got %d, Retry-After=%q, body=%s", reached, response.Code, response.Header().Get("Retry-After"), response.Body.String())
			}
		})
	}
}
