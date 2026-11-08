package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestRecordingRetentionManagementRequiresHumanAndCorrectScope(t *testing.T) {
	calls := 0
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"retentionDays":30,"revision":1,"activatedAt":null}`))
	}))
	defer asset.Close()
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	for _, tc := range []struct {
		method, path, scope, body string
		service                   bool
		want                      int
	}{
		{"GET", "retention-policy", "cms:recordings:read", "", false, 200},
		{"GET", "retention-policy", "cms:recordings:read", "", true, 403},
		{"PUT", "retention-policy", "cms:recordings:read", `{"retentionDays":14,"expectedRevision":1,"previewId":"preview"}`, false, 403},
		{"PUT", "retention-policy", "cms:recordings:write", `{"retentionDays":14,"expectedRevision":1,"previewId":"preview"}`, false, 200},
		{"POST", "retention-policy/preview", "cms:recordings:write", `{"retentionDays":366}`, false, 400},
		{"POST", "retention-policy/preview", "cms:recordings:write", `{"retentionDays":14}`, true, 403},
	} {
		r := httptest.NewRequest(tc.method, "/api/admin/recordings/"+tc.path, strings.NewReader(tc.body))
		trusted(r, tc.scope)
		r.Header.Set("X-HHC-User-ID", "0199a000-0000-4000-8000-000000000001")
		r.Header.Set("Idempotency-Key", "retention-test")
		if tc.service {
			r = serviceRequest(tc.method, "/api/admin/recordings/"+tc.path)
			r.Body = http.NoBody
			r.Header.Set("X-HHC-Scopes", tc.scope)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("unexpected provider calls %d", calls)
	}
}
