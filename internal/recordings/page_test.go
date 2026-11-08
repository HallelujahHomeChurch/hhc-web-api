package recordings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type pageRepo struct {
	Repository
	values []Recording
	at     *time.Time
	after  string
	bound  int
	query  string
}

func (r *pageRepo) ListVisiblePage(_ context.Context, _ time.Time, at *time.Time, id string, limit int, query string) ([]Recording, error) {
	r.at, r.after, r.bound, r.query = at, id, limit, query
	return r.values, nil
}
func (r *pageRepo) RecordPackageReady(_ context.Context, id, _ string, _ time.Time) (Recording, error) {
	for _, v := range r.values {
		if v.ID == id {
			return v, nil
		}
	}
	return Recording{}, ErrNotFound
}

func TestRecordingPageAdvancesPastMissingMediaWithoutCheckingLookahead(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	uploaded := now.Add(-time.Hour)
	expiry := uploaded.Add(720 * time.Hour)
	first := "00000000-0000-4000-8000-000000000003"
	second := "00000000-0000-4000-8000-000000000002"
	repo := &pageRepo{values: []Recording{{ID: first, PackageID: strings.Repeat("a", 32), Status: "published", UploadedAt: &uploaded, ReadyAt: &uploaded, ExpiresAt: &expiry}, {ID: second, PackageID: strings.Repeat("b", 32), Status: "published", UploadedAt: &uploaded, ReadyAt: &uploaded, ExpiresAt: &expiry}}}
	calls := 0
	missing := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if missing {
			json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingReadyResult{{RecordingLifecycleBinding: assetclient.RecordingLifecycleBinding{RecordingID: first, PackageID: strings.Repeat("a", 32)}, Status: "not_found"}}})
			return
		}
		writeReadyFixture(t, w, r, assetclient.RecordingPackageStatus{PackageID: strings.Repeat("a", 32), SessionID: strings.Repeat("a", 32), RecordingID: first, State: "ready", ReadyAt: &uploaded, MediaExpiresAt: &expiry})
	}))
	defer server.Close()
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	items, next, err := svc.ListPage(context.Background(), "", 1, "")
	if err != nil || len(items) != 0 || next == "" || calls != 1 || repo.bound != 2 {
		t.Fatalf("page=%+v cursor=%s calls=%d err=%v", items, next, calls, err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(next)
	var decoded pageCursor
	json.Unmarshal(raw, &decoded)
	if decoded.ID != first || !decoded.UploadedAt.Equal(uploaded) {
		t.Fatalf("cursor=%+v", decoded)
	}
	repo.values = nil
	items, end, err := svc.ListPage(context.Background(), next, 1, "")
	if err != nil || len(items) != 0 || end != "" || repo.at == nil || !repo.at.Equal(uploaded) || repo.after != first {
		t.Fatalf("resume failed: %+v %s %v", items, end, err)
	}
	repo.values = []Recording{{ID: first, PackageID: strings.Repeat("a", 32), Status: "published", UploadedAt: &uploaded, ReadyAt: &uploaded, ExpiresAt: &expiry}}
	missing = false
	items, _, err = svc.ListPage(context.Background(), "", 1, "")
	if err != nil || len(items) != 1 || items[0].ID != first {
		t.Fatalf("ready not listed: %+v %v", items, err)
	}
}

func writeReadyFixture(t *testing.T, w http.ResponseWriter, r *http.Request, pkg assetclient.RecordingPackageStatus) {
	t.Helper()
	if r.Method == "POST" && r.URL.Path == "/priv/recording-packages/ready-batch" {
		var input struct {
			Items []assetclient.RecordingLifecycleBinding
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Items) != 1 || input.Items[0].PackageID != pkg.PackageID || input.Items[0].RecordingID != pkg.RecordingID {
			t.Error("wrong ready fixture binding")
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingReadyResult{{RecordingLifecycleBinding: input.Items[0], Status: "ready", Package: &pkg}}})
		return
	}
	json.NewEncoder(w).Encode(pkg)
}

func TestRecordingSearchCursorBindsNormalizedQueryAndKeepsLegacyEmptyQuery(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 123456789, time.UTC)
	repo := &pageRepo{}
	svc := NewService(repo, "", nil, func() time.Time { return now })
	makeCursor := func(q string, version int) string {
		data, _ := json.Marshal(pageCursor{UploadedAt: now, ID: "00000000-0000-4000-8000-000000000003", Version: version, Query: searchDigest(q)})
		return base64.RawURLEncoding.EncodeToString(data)
	}
	for _, tc := range []struct {
		cursor, q string
		valid     bool
	}{
		{makeCursor("faith 主日", 1), " Faith\t主日 ", true},
		{makeCursor("faith 主日", 1), "hope 主日", false},
		{makeCursor("faith 主日", 1), "", false},
		{makeCursor("", 0), "", true}, {makeCursor("", 0), "faith", false},
		{makeCursor("", 2), "", false},
	} {
		if len(tc.cursor) > 256 {
			t.Fatal("cursor exceeds existing bound")
		}
		_, _, err := svc.ListPage(context.Background(), tc.cursor, 12, tc.q)
		if (err == nil) != tc.valid {
			t.Fatalf("query cursor validity: %v", err)
		}
	}
}
