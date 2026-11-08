package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type deleteRecordingRepo struct {
	recordings.Repository
	published bool
	calls     int
}

func (r *deleteRecordingRepo) Delete(_ context.Context, _ string, _ int64, _ string, _ time.Time, publish bool) error {
	if r.published && !publish {
		return recordings.ErrForbidden
	}
	r.calls++
	return nil
}
func TestRecordingDeletionRequiresHumanWriteAndPublishedPermission(t *testing.T) {
	for _, tc := range []struct {
		scope     string
		published bool
		match     string
		want      int
	}{
		{"cms:recordings:read", false, `"1"`, 403},
		{"cms:recordings:write", false, "", 428},
		{"cms:recordings:write", false, `"1"`, 204},
		{"cms:recordings:write", true, `"1"`, 403},
		{"cms:recordings:write cms:recordings:publish", true, `"1"`, 204},
	} {
		repo := &deleteRecordingRepo{published: tc.published}
		h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, time.Now)).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
		r := httptest.NewRequest(http.MethodDelete, "/api/admin/recordings/0199a000-0000-4000-8000-000000000003", nil)
		trusted(r, tc.scope)
		r.Header.Set("X-HHC-User-ID", "0199a000-0000-4000-8000-000000000001")
		r.Header.Set("If-Match", tc.match)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
		if tc.want != 204 && repo.calls != 0 {
			t.Fatal("unauthorized deletion reached mutation")
		}
	}
}
