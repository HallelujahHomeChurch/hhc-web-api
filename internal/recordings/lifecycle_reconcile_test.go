package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type cycleRepo struct {
	lifecycleTestRepo
	state        LifecycleCheckpoint
	bindings     []assetclient.RecordingLifecycleBinding
	appliedCount int
	inactive     map[assetclient.RecordingLifecycleBinding]bool
}

func (r *cycleRepo) ClaimLifecycleCycle(context.Context, time.Time) (LifecycleClaim, error) {
	return LifecycleClaim{Token: "lease", State: r.state}, nil
}
func (r *cycleRepo) LifecycleBindingCurrent(_ context.Context, binding assetclient.RecordingLifecycleBinding) (bool, error) {
	return !r.inactive[binding], nil
}
func (r *cycleRepo) ReleaseLifecycleCycle(context.Context, string) error { return nil }
func (r *cycleRepo) LifecycleBindingPage(_ context.Context, cursor string, limit int) ([]assetclient.RecordingLifecycleBinding, error) {
	var out []assetclient.RecordingLifecycleBinding
	for _, b := range r.bindings {
		if b.RecordingID > cursor && len(out) < limit {
			out = append(out, b)
		}
	}
	return out, nil
}
func (r *cycleRepo) CommitLifecycleCycle(_ context.Context, _ string, state LifecycleCheckpoint, v assetclient.RecordingLifecycleSnapshot) error {
	r.state = state
	r.appliedCount += len(v.Items)
	return nil
}
func TestLifecycleCycleWrapsBeyond1000AndDurablyRepairsPoison(t *testing.T) {
	now := time.Now().UTC()
	repo := &cycleRepo{}
	for i := 0; i < 1205; i++ {
		repo.bindings = append(repo.bindings, assetclient.RecordingLifecycleBinding{RecordingID: fmt.Sprintf("%04d", i), PackageID: fmt.Sprintf("%032d", i)})
	}
	poison := true
	maxBatch := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Items []assetclient.RecordingLifecycleBinding
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if len(in.Items) > maxBatch {
			maxBatch = len(in.Items)
		}
		for _, binding := range in.Items {
			if poison && binding.RecordingID == "0001" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}

		out := assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}}
		for _, b := range in.Items {
			if poison && b.RecordingID == "0001" {
				continue
			}
			out.Items = append(out.Items, assetclient.RecordingPackageStatus{RecordingID: b.RecordingID, PackageID: b.PackageID, SessionID: b.PackageID, State: "uploading", RetentionRevision: 1})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	for i := 0; i < 20; i++ {
		// Recreate service each tick: correctness must depend on durable checkpoint.
		s := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
		if _, err := s.ReconcileLifecycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(30 * time.Second)
	}
	if maxBatch > 100 || len(repo.state.Repairs) != 1 || !repo.state.LastSuccessfulCycleAt.IsZero() || repo.appliedCount < 1204 {
		t.Fatalf("incomplete traversal/repair: batch=%d state=%+v applied=%d", maxBatch, repo.state, repo.appliedCount)
	}
	poison = false
	now = now.Add(10 * time.Minute)
	for i := 0; i < 20; i++ {
		s := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
		if _, err := s.ReconcileLifecycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(30 * time.Second)
	}
	if len(repo.state.Repairs) != 0 || repo.state.LastSuccessfulCycleAt.IsZero() {
		t.Fatalf("repair did not complete: %+v", repo.state)
	}
}
func TestLifecycleAuthorityFailureNeverAdvancesCursor(t *testing.T) {
	now := time.Now()
	repo := &cycleRepo{state: LifecycleCheckpoint{Cursor: "0010"}, bindings: []assetclient.RecordingLifecycleBinding{{RecordingID: "0011", PackageID: "pkg"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	s := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if _, err := s.ReconcileLifecycle(context.Background()); err == nil {
		t.Fatal("authority outage reported success")
	}
	if repo.state.Cursor != "0010" || len(repo.state.Repairs) != 0 || !repo.state.NextAttempt.After(now) {
		t.Fatalf("outage lost checkpoint/backoff: %+v", repo.state)
	}
}

func TestSlowRepairCannotStarveNormalTraversal(t *testing.T) {
	now := time.Now().UTC()
	repo := &cycleRepo{state: LifecycleCheckpoint{Cursor: "0005", Repairs: []LifecycleRepair{{Binding: assetclient.RecordingLifecycleBinding{RecordingID: "0001", PackageID: "repair"}, FirstFailureAt: now.Add(-time.Hour), Attempts: 3}}}, bindings: []assetclient.RecordingLifecycleBinding{{RecordingID: "0006", PackageID: "normal"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Items []assetclient.RecordingLifecycleBinding
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Items[0].PackageID == "repair" {
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}, Items: []assetclient.RecordingPackageStatus{{RecordingID: "0006", PackageID: "normal", SessionID: "normal", State: "uploading", RetentionRevision: 1}}})
	}))
	defer server.Close()
	service := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	result, err := service.ReconcileLifecycle(context.Background())
	if err != nil || result.Scanned != 1 || len(repo.state.Repairs) != 1 || !repo.state.Repairs[0].NextAttempt.After(now) {
		t.Fatalf("repair starved normal traversal: %+v %+v %v", result, repo.state, err)
	}
}

func TestReboundRepairRetainsTheNewBindingAndClearsRetiredOwnerProof(t *testing.T) {
	now := time.Now().UTC()
	old := assetclient.RecordingLifecycleBinding{RecordingID: "same", PackageID: "old"}
	current := assetclient.RecordingLifecycleBinding{RecordingID: "same", PackageID: "new"}
	repo := &cycleRepo{state: LifecycleCheckpoint{Repairs: []LifecycleRepair{{Binding: old, Attempts: 3, FirstFailureAt: now.Add(-time.Hour), NextAttempt: now.Add(time.Hour)}}}, bindings: []assetclient.RecordingLifecycleBinding{current}, inactive: map[assetclient.RecordingLifecycleBinding]bool{old: true}}
	broken := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}, Items: []assetclient.RecordingPackageStatus{{RecordingID: "same", PackageID: "new", SessionID: "new", State: "uploading", RetentionRevision: 1}}})
	}))
	defer server.Close()
	for i := 0; i < 3; i++ {
		service := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
		if _, err := service.ReconcileLifecycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(30 * time.Second)
	}
	if len(repo.state.Repairs) != 2 {
		t.Fatal("new binding obligation lost behind old repair", repo.state.Repairs)
	}
	broken = false
	now = now.Add(2 * time.Hour)
	service := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if _, err := service.ReconcileLifecycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.state.Repairs) != 0 {
		t.Fatal("retired/repaired obligations retained", repo.state.Repairs)
	}
}

func TestLifecycleRepairCapacityBackpressuresWithoutDroppingObligations(t *testing.T) {
	now := time.Now().UTC()
	repo := &cycleRepo{state: LifecycleCheckpoint{Cursor: "0000", PendingAttempts: 2, FirstFailureAt: now}, bindings: []assetclient.RecordingLifecycleBinding{{RecordingID: "0001", PackageID: "new"}}}
	for i := 0; i < LifecycleRepairLimit; i++ {
		repo.state.Repairs = append(repo.state.Repairs, LifecycleRepair{Binding: assetclient.RecordingLifecycleBinding{RecordingID: fmt.Sprintf("old-%04d", i), PackageID: "old"}, FirstFailureAt: now, NextAttempt: now.Add(time.Hour)})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer server.Close()
	service := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	_, err := service.ReconcileLifecycle(t.Context())
	if !errors.Is(err, ErrLifecycleRepairCapacity) || repo.state.Cursor != "0000" || len(repo.state.Repairs) != LifecycleRepairLimit || repo.state.PendingAttempts != 2 {
		t.Fatalf("capacity lost obligations: %v %+v", err, repo.state)
	}
}

func TestLifecycleWrapIncludesInsertedBindingAndChangedPolicy(t *testing.T) {
	now := time.Now().UTC()
	repo := &cycleRepo{}
	for i := 1; i <= 101; i++ {
		repo.bindings = append(repo.bindings, assetclient.RecordingLifecycleBinding{RecordingID: fmt.Sprintf("%04d", i), PackageID: fmt.Sprintf("%032d", i)})
	}
	seen := map[string]int64{}
	var revision int64 = 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Items []assetclient.RecordingLifecycleBinding
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		out := assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 60, Revision: revision}}
		for _, b := range in.Items {
			seen[b.RecordingID] = revision
			out.Items = append(out.Items, assetclient.RecordingPackageStatus{RecordingID: b.RecordingID, PackageID: b.PackageID, SessionID: b.PackageID, State: "uploading", RetentionRevision: revision})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	tick := func() {
		t.Helper()
		service := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
		if _, err := service.ReconcileLifecycle(t.Context()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(30 * time.Second)
	}
	tick()
	repo.bindings = append(repo.bindings, assetclient.RecordingLifecycleBinding{RecordingID: "0000", PackageID: "inserted"})
	slices.SortFunc(repo.bindings, func(a, b assetclient.RecordingLifecycleBinding) int {
		return strings.Compare(a.RecordingID, b.RecordingID)
	})
	revision = 2
	tick()
	tick()
	tick()
	if seen["0000"] != 2 || seen["0101"] != 2 || repo.state.LastSuccessfulCycleAt.IsZero() {
		t.Fatal("wrap/new policy did not converge", seen, repo.state)
	}
}
