package recordings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type deletionRepo struct {
	Repository
	pending   []string
	delivered []string
	attempted []string
}

func TestDeletionTimeoutDoesNotAdvanceUnattemptedCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &deletionRepo{pending: []string{"recording-a", "recording-b"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cancel(); w.WriteHeader(503) }))
	defer server.Close()
	svc := NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if err := svc.ReconcileDeletions(ctx); err == nil {
		t.Fatal("cancellation lost")
	}
	if len(repo.attempted) != 1 || repo.attempted[0] != "recording-a" {
		t.Fatalf("unattempted commands advanced: %v", repo.attempted)
	}
	repo.pending = []string{"recording-b", "recording-a"}
	if err := svc.ReconcileDeletions(context.Background()); err == nil {
		t.Fatal("provider failure lost")
	}
	if len(repo.attempted) != 3 || repo.attempted[1] != "recording-b" {
		t.Fatalf("later command not attempted next: %v", repo.attempted)
	}
}

func (r *deletionRepo) ListPendingDeletions(context.Context) ([]string, error) { return r.pending, nil }
func (r *deletionRepo) MarkDeletionAttempt(_ context.Context, id string) error {
	r.attempted = append(r.attempted, id)
	return nil
}
func (r *deletionRepo) MarkDeletionDelivered(_ context.Context, id string) error {
	r.delivered = append(r.delivered, id)
	return nil
}

func TestDeletionDeliveryRetriesProviderFailureBeforeAcknowledging(t *testing.T) {
	fail := true
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/priv/recordings/recording-a" {
			t.Errorf("unexpected command %s %s", r.Method, r.URL.Path)
		}
		calls++
		if fail {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	repo := &deletionRepo{pending: []string{"recording-a"}}
	svc := NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if err := svc.ReconcileDeletions(context.Background()); err == nil || len(repo.delivered) != 0 {
		t.Fatalf("failed command acknowledged: %v %v", err, repo.delivered)
	}
	fail = false
	if err := svc.ReconcileDeletions(context.Background()); err != nil || len(repo.delivered) != 1 || calls != 2 {
		t.Fatalf("retry failed: %v %v %d", err, repo.delivered, calls)
	}
}
