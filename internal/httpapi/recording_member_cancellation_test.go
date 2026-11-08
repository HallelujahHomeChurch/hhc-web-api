package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type cancelledMediaRepository struct {
	recordings.Repository
	recordings.LiveRepository
	recordings.CaptureRepository
	recordings.PlaybackMetricsRepository
	recordings.InteractionRepository
	err error
}

func (r *cancelledMediaRepository) WithRecordingSubject(context.Context, string, func(recordings.SubjectReader) error) error {
	return r.err
}
func (r *cancelledMediaRepository) ListLiveCaptures(context.Context, time.Time) ([]recordings.Capture, error) {
	return nil, r.err
}
func (r *cancelledMediaRepository) GetCapture(context.Context, string, string) (recordings.Capture, error) {
	return recordings.Capture{}, r.err
}
func (r *cancelledMediaRepository) RecordPlaybackHeartbeat(context.Context, string, string, recordings.PlaybackHeartbeat, float64, time.Time) error {
	return r.err
}
func (r *cancelledMediaRepository) GetPlaybackMetrics(context.Context, string, string, bool, time.Time) (recordings.PlaybackMetrics, error) {
	return recordings.PlaybackMetrics{}, r.err
}
func (r *cancelledMediaRepository) RecordingInteractions(context.Context, string, string, time.Time) (recordings.InteractionSummary, error) {
	return recordings.InteractionSummary{}, r.err
}

func TestCoreMemberCancellationRequiresRequestAndReturnedError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cancel   bool
		deadline bool
		err      error
		is499    bool
	}{
		{"wrapped request cancellation", true, false, fmt.Errorf("repository: %w", context.Canceled), true},
		{"independent child cancellation", false, false, context.Canceled, false},
		{"independent database failure", true, false, errors.New("database unavailable"), false},
		{"dependency deadline", true, false, context.DeadlineExceeded, false},
		{"request deadline with cancellation", false, true, context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &cancelledMediaRepository{err: tc.err}
			svc := recordings.NewService(repo, "", nil, time.Now).WithVideoAnalytics(true).WithVideoInteractions(true)
			h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(svc)
			for _, route := range []struct {
				name    string
				handler http.HandlerFunc
				status  int
				body    string
				path    string
				admin   bool
			}{
				{"watch", h.memberRecordingWatch, 503, "", "/api/member/recordings/id/watch", false},
				{"live list", h.memberListLiveRecordings, 503, "", "/api/member/recordings/live", false},
				{"heartbeat", h.memberPlaybackHeartbeat, 500, `{"playbackSessionId":"018f0000-0000-4000-8000-000000000002","sequence":1,"state":"playing"}`, "/api/member/recordings/id/playback-heartbeats", false},
				{"metrics", h.recordingMetrics(false), 500, "", "/api/member/recordings/id/metrics", false},
				{"interactions", h.memberRecordingInteractions, 500, "", "/api/member/recordings/id/interactions", false},
				{"live cover", h.memberLiveCoverContent, 500, "", "/api/member/recordings/id/live/capture/cover", false},
				{"admin metrics", h.recordingMetrics(true), 500, "", "/api/admin/recordings/id/metrics", true},
				{"admin cover", func(w http.ResponseWriter, r *http.Request) { h.liveCoverContent(w, r, false) }, 500, "", "/api/admin/recordings/id/live/capture/cover", true},
			} {
				t.Run(route.name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if tc.cancel {
						cancel()
					}
					if tc.deadline {
						var deadlineCancel context.CancelFunc
						ctx, deadlineCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
						defer deadlineCancel()
					}
					ctx = context.WithValue(ctx, principalKey{}, principal{UserID: "018f0000-0000-4000-8000-000000000003"})
					req := httptest.NewRequest("GET", route.path, strings.NewReader(route.body)).WithContext(ctx)
					req.SetPathValue("id", "018f0000-0000-4000-8000-000000000001")
					req.SetPathValue("captureId", strings.Repeat("a", 32))
					w := httptest.NewRecorder()
					captureErrors(route.handler).ServeHTTP(w, req)
					want := route.status
					if tc.is499 && !route.admin {
						want = 499
					}
					if w.Code != want {
						t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body)
					}
					if tc.is499 && !route.admin && (!strings.Contains(w.Body.String(), "request_canceled") || w.Header().Get("Retry-After") != "") {
						t.Fatal("cancellation misrepresented as retryable dependency failure")
					}
				})
			}
		})
	}
}
