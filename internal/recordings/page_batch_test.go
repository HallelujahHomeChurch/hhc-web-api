package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

func pageBatchFixture(count int, now time.Time) ([]Recording, map[string]assetclient.RecordingPackageStatus) {
	uploaded := now.Add(-time.Hour)
	expiry := uploaded.Add(720 * time.Hour)
	var values []Recording
	packages := map[string]assetclient.RecordingPackageStatus{}
	for i := range count {
		id, pkg := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), fmt.Sprintf("%032x", i+1)
		values = append(values, Recording{ID: id, PackageID: pkg, Status: "published", UploadedAt: &uploaded, ReadyAt: &uploaded, ExpiresAt: &expiry})
		packages[pkg] = assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: id, State: "ready", ReadyAt: &uploaded, MediaExpiresAt: &expiry, Renditions: []assetclient.RecordingRendition{{Name: "720p", DurationSeconds: 42}}}
	}
	return values, packages
}

func TestRecordingPageBatchReducesReadyRequestsAndExcludesLookahead(t *testing.T) {
	for _, count := range []int{1, 25, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			now := time.Now().UTC()
			values, packages := pageBatchFixture(count+1, now)
			repo := &pageRepo{values: values}
			calls, single, bindings := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(2 * time.Millisecond) // Identical fixed HTTP overhead for old and new paths.
				if r.Method == "GET" {
					single++
					pkg := packages[strings.Split(r.URL.Path, "/")[3]]
					_ = json.NewEncoder(w).Encode(pkg)
					return
				}
				calls++
				var in struct {
					Items []assetclient.RecordingLifecycleBinding
				}
				if r.URL.Path != "/priv/recording-packages/ready-batch" || json.NewDecoder(r.Body).Decode(&in) != nil {
					t.Error("unexpected batch request")
				}
				bindings = len(in.Items)
				var results []assetclient.RecordingReadyResult
				for i, binding := range in.Items {
					if i >= count || binding.PackageID != values[i].PackageID || binding.RecordingID != values[i].ID {
						t.Error("lookahead or wrong order included")
					}
					p := packages[binding.PackageID]
					results = append(results, assetclient.RecordingReadyResult{RecordingLifecycleBinding: binding, Status: "ready", Package: &p})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": results})
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			started := time.Now()
			for _, value := range values[:count] {
				if _, err := svc.memberDuration(context.Background(), value); err != nil {
					t.Fatal(err)
				}
			}
			before := time.Since(started)
			started = time.Now()
			items, next, err := svc.ListPage(context.Background(), "", count, "")
			after := time.Since(started)
			if err != nil || len(items) != count || calls != 1 || single != count || bindings != count || next == "" || repo.bound != count+1 {
				t.Fatalf("items=%d batch=%d single=%d bindings=%d next=%q err=%v", len(items), calls, single, bindings, next, err)
			}
			for _, item := range items {
				if item.DurationSeconds != 42 {
					t.Fatal("duration changed")
				}
			}
			t.Logf("synthetic fixed-2ms HTTP overhead: ready requests %d -> %d; elapsed %s -> %s; producer SQL remains per item", single, calls, before, after)
		})
	}
}

type batchProjectionRepo struct {
	pageRepo
	projects int
}

func (r *batchProjectionRepo) RecordPackageReady(ctx context.Context, id, pkg string, ready time.Time) (Recording, error) {
	r.projects++
	return r.pageRepo.RecordPackageReady(ctx, id, pkg, ready)
}

func TestRecordingPageBatchFailsWholePageBeforeAnyProjection(t *testing.T) {
	for _, kind := range []string{"403", "404", "429", "503", "wrong_session", "future_ready"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			values, packages := pageBatchFixture(2, now)
			repo := &batchProjectionRepo{pageRepo: pageRepo{values: values}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if len(kind) == 3 {
					var status int
					_, _ = fmt.Sscan(kind, &status)
					w.WriteHeader(status)
					return
				}
				var results []assetclient.RecordingReadyResult
				for _, value := range values {
					p := packages[value.PackageID]
					results = append(results, assetclient.RecordingReadyResult{RecordingLifecycleBinding: assetclient.RecordingLifecycleBinding{RecordingID: value.ID, PackageID: value.PackageID}, Status: "ready", Package: &p})
				}
				if kind == "wrong_session" {
					results[1].Package.SessionID = strings.Repeat("f", 32)
				} else {
					future := now.Add(time.Hour)
					results[1].Package.ReadyAt = &future
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": results})
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			items, _, err := svc.ListPage(context.Background(), "", 2, "")
			if !errors.Is(err, assetclient.ErrUnavailable) || items != nil || repo.projects != 0 {
				t.Fatalf("partial/empty success: items=%v projections=%d err=%v", items, repo.projects, err)
			}
		})
	}
}

func TestRecordingPageBatchRechecksExpiryAfterHTTPAndKeepsCursor(t *testing.T) {
	now := time.Now().UTC()
	values, packages := pageBatchFixture(2, now)
	repo := &batchProjectionRepo{pageRepo: pageRepo{values: values}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := packages[values[0].PackageID]
		now = *p.MediaExpiresAt
		writeReadyFixture(t, w, r, p)
	}))
	defer server.Close()
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	items, next, err := svc.ListPage(context.Background(), "", 1, "")
	if err != nil || len(items) != 0 || next == "" || repo.projects != 0 {
		t.Fatalf("expired package consumed/cursor lost: %+v %q %v", items, next, err)
	}
}

func TestRecordingPageBatchUsesAuthoritativeExpiryWhenRetentionExtendsDuringHTTP(t *testing.T) {
	now := time.Now().UTC()
	values, packages := pageBatchFixture(2, now)
	oldExpiry := now.Add(time.Second)
	values[0].ExpiresAt = &oldExpiry
	repo := &batchProjectionRepo{pageRepo: pageRepo{values: values}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now = oldExpiry.Add(time.Second)
		writeReadyFixture(t, w, r, packages[values[0].PackageID])
	}))
	defer server.Close()
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	items, next, err := svc.ListPage(context.Background(), "", 1, "")
	if err != nil || len(items) != 1 || next == "" || repo.projects != 1 {
		t.Fatalf("authoritative extension lost to stale SQL expiry: items=%d projects=%d next=%q err=%v", len(items), repo.projects, next, err)
	}
}

type batchLifecycleRepo struct {
	pageRepo
	synced bool
}

func (r *batchLifecycleRepo) LifecycleBindings(context.Context) ([]assetclient.RecordingLifecycleBinding, error) {
	value := r.values[0]
	return []assetclient.RecordingLifecycleBinding{{RecordingID: value.ID, PackageID: value.PackageID}}, nil
}

func (r *batchLifecycleRepo) ApplyLifecycle(_ context.Context, snapshot assetclient.RecordingLifecycleSnapshot) error {
	r.synced = true
	r.values[0].ExpiresAt = snapshot.Items[0].MediaExpiresAt
	return nil
}

func (r *batchLifecycleRepo) ListVisiblePage(ctx context.Context, now time.Time, at *time.Time, id string, limit int, query string) ([]Recording, error) {
	if !r.synced {
		return nil, fmt.Errorf("list ran before lifecycle synchronization")
	}
	if !r.values[0].Visible(now) {
		return nil, nil
	}
	return r.pageRepo.ListVisiblePage(ctx, now, at, id, limit, query)
}

func (r *batchLifecycleRepo) Get(context.Context, string) (Recording, error) {
	return r.values[0], nil
}

func TestRecordingPageBatchSyncsRetentionExtensionBeforeSQLPagination(t *testing.T) {
	now := time.Now().UTC()
	values, packages := pageBatchFixture(1, now)
	expired := now.Add(-time.Second)
	values[0].ExpiresAt = &expired
	p := packages[values[0].PackageID]
	p.UploadedAt, p.RetentionRevision = p.ReadyAt, 2
	repo := &batchLifecycleRepo{pageRepo: pageRepo{values: values}}
	lifecycleCalls, batchCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/priv/recordings/lifecycle" {
			lifecycleCalls++
			_ = json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 2}, Items: []assetclient.RecordingPackageStatus{p}})
			return
		}
		batchCalls++
		writeReadyFixture(t, w, r, p)
	}))
	defer server.Close()
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	items, _, err := svc.ListPage(context.Background(), "", 25, "")
	if err != nil || len(items) != 1 || lifecycleCalls != 1 || batchCalls != 1 {
		t.Fatalf("retention extension lost: items=%d lifecycle=%d batch=%d err=%v", len(items), lifecycleCalls, batchCalls, err)
	}
}
