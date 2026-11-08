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

type coverRecordingRepository struct {
	recordings.Repository
	recordings.CoverRepository
	value recordings.Recording
}

func (r coverRecordingRepository) Get(context.Context, string) (recordings.Recording, error) {
	return r.value, nil
}
func (r coverRecordingRepository) GetCoverOperation(context.Context, string, int64, recordings.CoverSelection, string, string, time.Time) (recordings.CoverResult, error) {
	return recordings.CoverResult{}, recordings.ErrNotFound
}

func TestCoverHTTPServiceScopeMatrixAndMemberDenial(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	for _, tc := range []struct{ method, path, scope string }{{"GET", "covers", "cms:recordings:read"}, {"POST", "cover-uploads", "cms:recordings:write"}, {"PUT", "cover", "cms:recordings:write"}, {"GET", "covers/cover-1/content", "cms:recordings:read"}} {
		for _, allowed := range []bool{false, true} {
			r := serviceRequest(tc.method, "/api/admin/recordings/"+id+"/"+tc.path)
			scope := tc.scope
			if !allowed {
				scope = "cms:recordings:read"
				if tc.scope == scope {
					scope = "cms:recordings:write"
				}
			}
			r.Header.Set("X-HHC-Scopes", scope)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := 503
			if !allowed {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("%s %s %d %s", tc.method, tc.path, w.Code, w.Body)
			}
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, serviceRequest("GET", "/api/member/recordings/"+id+"/cover"))
	if w.Code != 403 {
		t.Fatal("service member cover", w.Code)
	}
}

func TestCoverContentBeforePlaybackAndVisibilityChecks(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	now := time.Now()
	expiry := now.Add(time.Hour)
	calls := 0
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.URL.Path, "grant") || r.URL.Query().Get("recordingId") != id {
			t.Error("unsafe cover read")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg fixture"))
	}))
	defer asset.Close()
	for _, published := range []bool{false, true} {
		status := "draft"
		if published {
			status = "published"
		}
		repo := coverRecordingRepository{value: recordings.Recording{ID: id, Status: status, PackageID: strings.Repeat("a", 32), ReadyAt: &now, UploadedAt: &now, ExpiresAt: &expiry, SelectedCoverID: "cover-1", Version: 3}}
		s := recordings.NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))
		data, err := s.CoverContent(context.Background(), id, "", true)
		if published && (err != nil || string(data) != "jpeg fixture") {
			t.Fatal(err)
		}
		if !published && err == nil {
			t.Fatal("draft member access")
		}
	}
	if calls != 1 {
		t.Fatal("draft reached asset", calls)
	}
}

func TestCoverListWireAndPublishedWriteNeedsPublish(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	now := time.Now()
	expiry := now.Add(time.Hour)
	calls := 0
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "cover-1", UploadID: "upload-1", Kind: "auto", State: "ready"}}})
	}))
	defer asset.Close()
	repo := coverRecordingRepository{value: recordings.Recording{ID: id, Status: "published", PackageID: strings.Repeat("a", 32), ReadyAt: &now, UploadedAt: &now, ExpiresAt: &expiry, Version: 3}}
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	r := serviceRequest("GET", "/api/admin/recordings/"+id+"/covers")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"selectedCoverId":"cover-1"`) || !strings.Contains(w.Body.String(), `"recordingVersion":3`) {
		t.Fatal(w.Code, w.Body.String())
	}
	r = serviceRequest("PUT", "/api/admin/recordings/"+id+"/cover")
	r.Header.Set("X-HHC-Scopes", "cms:recordings:write")
	r.Header.Set("If-Match", `"3"`)
	r.Header.Set("Idempotency-Key", "selection")
	r.Body = io.NopCloser(strings.NewReader(`{"mode":"auto","candidateId":"cover-1"}`))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 || calls != 1 {
		t.Fatal("published selection reached Asset", w.Code, calls)
	}
}
