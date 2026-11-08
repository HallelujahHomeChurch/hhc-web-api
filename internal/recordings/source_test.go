package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type sourceTestRepo struct{ packageTestRepo }

func TestSourceCreateNormalizesProducerBindingAndReplaysAfterLostAttachment(t *testing.T) {
	source := strings.Repeat("a", 32)
	checksum := strings.Repeat("b", 64)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input assetclient.RecordingSourceInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("Idempotency-Key") != "same-source" {
			t.Error("lost idempotency")
		}
		json.NewEncoder(w).Encode(assetclient.RecordingSource{SourceID: source, RecordingID: "rec-a", FileName: strings.TrimSpace(input.FileName), SizeBytes: input.SizeBytes, ChecksumSHA256: strings.ToLower(input.ChecksumSHA256), State: "uploading"})
	}))
	defer server.Close()
	repo := &sourceTestRepo{packageTestRepo{testRepo{recording: Recording{ID: "rec-a", DraftExpiresAt: time.Now().Add(time.Hour)}}}}
	svc := NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	input := assetclient.RecordingSourceInput{FileName: " sermon.mp4 ", SizeBytes: 50_000_000_000, ChecksumSHA256: checksum}
	for range 2 {
		value, err := svc.CreateSource(context.Background(), "rec-a", input, "same-source")
		if err != nil || value.SourceID != source || repo.recording.SourceID != source {
			t.Fatalf("normalized replay: %+v %v", value, err)
		}
		// A durable producer receipt can be replayed even if the CMS attachment
		// was lost with the prior response/transaction.
		repo.recording.SourceID = ""
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func (r *sourceTestRepo) AddSource(_ context.Context, _, source string, _ time.Time) error {
	r.recording.SourceID = source
	return nil
}
func (r *sourceTestRepo) RecordSourceReady(ctx context.Context, id, source, pkg string, at time.Time) (Recording, error) {
	if r.recording.SourceID != source {
		return Recording{}, ErrConflict
	}
	r.recording.PackageID = pkg
	return r.RecordPackageReady(ctx, id, pkg, at)
}

func TestSourceReadyRequiresIndependentPackageValidationAndOriginalBinding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	at, expiry := now.Add(-time.Minute), now.Add(-time.Minute).Add(720*time.Hour)
	source, pkg := strings.Repeat("a", 32), strings.Repeat("b", 32)
	state := "processing"
	packageRecording := "rec-a"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasPrefix(r.URL.Path, "/priv/recording-sources/") {
			if state != "ready" {
				w.WriteHeader(409)
				return
			}
			json.NewEncoder(w).Encode(assetclient.RecordingSource{SourceID: source, RecordingID: "rec-a", State: state, PackageID: pkg})
			return
		}
		json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: packageRecording, State: "ready", ReadyAt: &at, MediaExpiresAt: &expiry})
	}))
	defer server.Close()
	repo := &sourceTestRepo{packageTestRepo{testRepo{recording: Recording{ID: "rec-a", SourceID: source, Title: "saved title", Version: 7, DraftExpiresAt: now.Add(-time.Hour)}}}}
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	value, err := svc.Get(context.Background(), "rec-a")
	if err != nil || value.ReadyAt != nil || repo.completions != 0 {
		t.Fatalf("pending source projected: %+v %v", value, err)
	}
	state = "ready"
	packageRecording = "other"
	if _, err := svc.Get(context.Background(), "rec-a"); !errors.Is(err, ErrConflict) || repo.completions != 0 {
		t.Fatalf("cross-recording package: %v", err)
	}
	packageRecording = "rec-a"
	if err := svc.ReconcileCompletions(context.Background()); err != nil {
		t.Fatal(err)
	}
	value, err = repo.Get(context.Background(), "rec-a")
	if err != nil || value.ReadyAt == nil || value.PackageID != pkg || value.Version != 7 || value.Title != "saved title" || !value.ExpiresAt.Equal(expiry) {
		t.Fatalf("projection %+v %v", value, err)
	}
	before := calls
	if _, err := svc.SignSource(context.Background(), "rec-a", strings.Repeat("c", 32), []int{1}); !errors.Is(err, ErrNotFound) || calls != before {
		t.Fatalf("unbound source reached provider: %v", err)
	}
}
