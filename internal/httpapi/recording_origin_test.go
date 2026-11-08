package httpapi

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type originRecordingRepo struct {
	recordings.Repository
	origin string
	calls  int
}

func (r *originRecordingRepo) ListAdmin(_ context.Context, origin string) ([]recordings.Recording, error) {
	r.origin = origin
	r.calls++
	at := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	return []recordings.Recording{{ID: "r1", Broadcast: true, BroadcastStartedAt: &at}}, nil
}
func TestAdminRecordingOriginQuery(t *testing.T) {
	for _, tc := range []struct {
		query, origin string
		status        int
	}{{"", "", 200}, {"?origin=upload", "upload", 200}, {"?origin=live", "live", 200}, {"?origin=archived", "", 400}, {"?origin=LIVE", "", 400}, {"?origin=live&origin=upload", "", 400}} {
		repo := &originRecordingRepo{}
		h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, time.Now)).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
		r := httptest.NewRequest("GET", "/api/admin/recordings"+tc.query, nil)
		trustedBulletinAudit(r, "cms:recordings:read")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%q: %d %s", tc.query, w.Code, w.Body)
		}
		if tc.status == 200 && (repo.calls != 1 || repo.origin != tc.origin) {
			t.Fatalf("filter lost: %q", repo.origin)
		}
		if tc.status == 200 && (!strings.Contains(w.Body.String(), `"isBroadcast":true`) || !strings.Contains(w.Body.String(), `"broadcastStartedAt":"2026-10-16T01:00:00Z"`)) {
			t.Fatalf("admin list metadata missing: %s", w.Body)
		}
		if tc.status == 400 && repo.calls != 0 {
			t.Fatal("invalid filter reached repository")
		}
	}
}
