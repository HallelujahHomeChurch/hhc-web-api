package bulletinwatermark

import (
	"testing"
	"time"
)

func TestDownloadJobViewMapsPersistedProgress(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		status    string
		progress  int
		errorCode string
		wantState string
		wantStage string
		wantCode  string
	}{
		{name: "queued", status: "queued", progress: 5, wantState: "queued", wantStage: "queued"},
		{name: "preparing", status: "running", progress: 10, wantState: "running", wantStage: "preparing"},
		{name: "rendering start", status: "running", progress: 15, wantState: "running", wantStage: "rendering"},
		{name: "rendering end", status: "running", progress: 80, wantState: "running", wantStage: "rendering"},
		{name: "storing start", status: "running", progress: 82, wantState: "running", wantStage: "storing"},
		{name: "storing end", status: "running", progress: 88, wantState: "running", wantStage: "storing"},
		{name: "security", status: "running", progress: 92, wantState: "running", wantStage: "security_check"},
		{name: "authorizing", status: "running", progress: 98, wantState: "running", wantStage: "authorizing"},
		{name: "ready", status: "ready", progress: 100, wantState: "ready", wantStage: "ready"},
		{name: "failed", status: "failed", progress: 72, errorCode: "render_failed", wantState: "failed", wantStage: "failed", wantCode: "render_failed"},
		{name: "receipt collision", status: "failed", progress: 98, errorCode: "receipt_collision", wantState: "failed", wantStage: "failed", wantCode: "receipt_collision"},
		{name: "expired", status: "expired", progress: 100, wantState: "failed", wantStage: "failed", wantCode: "expired"},
		{name: "clamp low", status: "running", progress: -2, wantState: "running", wantStage: "preparing"},
		{name: "clamp high", status: "running", progress: 120, wantState: "running", wantStage: "authorizing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := (DownloadJob{ID: "job-1", Status: test.status, Progress: test.progress, ErrorCode: test.errorCode, UpdatedAt: now, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}).View()
			progress := view.OperationProgress
			if progress.Status != test.wantState || progress.Stage != test.wantStage || progress.ErrorCode != test.wantCode || progress.Percent < 0 || progress.Percent > 100 || !progress.UpdatedAt.Equal(now) || progress.RetryAfterMS != 2000 {
				t.Fatalf("progress=%#v", progress)
			}
		})
	}
}
