package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type sourceRecordingRepository struct{ recordings.Repository }

func (sourceRecordingRepository) Get(_ context.Context, id string) (recordings.Recording, error) {
	return recordings.Recording{ID: id, SourceID: strings.Repeat("a", 32), DraftExpiresAt: time.Now().Add(time.Hour)}, nil
}

func TestBrowserSourceCompleteKeepsAcceptedAndRejectsServiceIdentity(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	source := strings.Repeat("a", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/priv/recording-sources/"+source+"/complete" || r.Header.Get("X-HHC-Actor-ID") == "" {
			t.Error("source ownership missing")
		}
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(assetclient.RecordingSource{SourceID: source, RecordingID: id, State: "finalizing"})
	}))
	defer server.Close()
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(sourceRecordingRepository{}, "", nil, time.Now).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	for _, tc := range []struct {
		service bool
		scope   string
		want    int
	}{{false, "cms:recordings:read", 403}, {true, "cms:recordings:write", 403}, {false, "cms:recordings:write", 202}} {
		path := "/api/admin/recordings/" + id + "/source-uploads/" + source + "/complete"
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		trustedBulletinAudit(r, tc.scope)
		if tc.service {
			r = serviceRequest("POST", path)
			r.Header.Set("X-HHC-Scopes", tc.scope)
			r.Body = io.NopCloser(strings.NewReader(`{}`))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v got %d %s", tc, w.Code, w.Body.String())
		}
		if tc.want == 202 && (!strings.Contains(w.Body.String(), `"state":"finalizing"`) || w.Header().Get("Cache-Control") != "private, no-store") {
			t.Fatal("lost accepted receipt")
		}
	}
	if calls != 1 {
		t.Fatalf("unauthorized provider calls=%d", calls)
	}
}
