package recordings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type lifecycleTestRepo struct {
	packageTestRepo
	applied bool
}

func TestAdminProcessingProgressAcrossUploader(t *testing.T) {
	const source = "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC()
	repo := &lifecycleTestRepo{packageTestRepo: packageTestRepo{testRepo{recording: Recording{ID: "rec-a", SourceID: source}}}}
	progress := &assetclient.RecordingProcessingProgress{Attempt: 1, Phase: "encoding", AttemptStartedAt: now, PhaseStartedAt: now, LastProgressAt: now, HeartbeatAt: now}
	oldWorker := false
	legacyAPI := false
	sourceState := "processing"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ready") {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if r.URL.Path != "/priv/recordings/lifecycle" {
			t.Errorf("uploader capability requested: %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if legacyAPI {
			var legacy struct {
				Items []assetclient.RecordingLifecycleBinding `json:"items"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&legacy); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "AST_INVALID_REQUEST", "message": "invalid request body"}})
				return
			}
			json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}, Items: []assetclient.RecordingPackageStatus{}})
			return
		}
		var request struct {
			SourceItems []assetclient.RecordingSourceLifecycleBinding `json:"sourceItems"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.SourceItems) != 1 || request.SourceItems[0].RecordingID != "rec-a" || request.SourceItems[0].SourceID != source {
			t.Errorf("binding not from CMS: %+v", request)
		}
		response := assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}, Items: []assetclient.RecordingPackageStatus{}}
		if !oldWorker {
			response.SourceItems = []assetclient.RecordingSourceLifecycleStatus{{RecordingID: "rec-a", SourceID: source, State: sourceState, ProcessingProgress: progress}}
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	s := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	value, err := s.Get(context.Background(), "rec-a")
	if err != nil || value.ProcessingProgress == nil || value.ProcessingProgress.Phase != "encoding" || value.ReadyAt != nil {
		t.Fatalf("progress/ready: %+v %v", value, err)
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "processingProgress") {
		t.Fatalf("member serialization leaked progress: %s", encoded)
	}
	for _, state := range []string{"failed", "expired"} {
		sourceState = state
		value, err = s.Get(context.Background(), "rec-a")
		if err != nil || value.ProcessingProgress != nil {
			t.Fatalf("%s source masked uploader status: %+v %v", state, value, err)
		}
	}
	oldWorker = true
	value, err = s.Get(context.Background(), "rec-a")
	if err != nil || value.ProcessingProgress != nil {
		t.Fatalf("missing old-worker summary became fake progress: %+v %v", value, err)
	}
	// A strict legacy decoder rejects the additive request field. Safe fallback
	// reads only the existing package lifecycle; source progress remains unknown.
	legacyAPI = true
	if value, err := s.Get(context.Background(), "rec-a"); err != nil || value.ProcessingProgress != nil {
		t.Fatalf("strict legacy Asset API broke safe metadata reads: %+v %v", value, err)
	}
}

func (r *lifecycleTestRepo) LifecycleBindings(context.Context) ([]assetclient.RecordingLifecycleBinding, error) {
	if r.recording.PackageID == "" {
		return nil, nil
	}
	return []assetclient.RecordingLifecycleBinding{{RecordingID: r.recording.ID, PackageID: r.recording.PackageID}}, nil
}
func (r *lifecycleTestRepo) ApplyLifecycle(_ context.Context, v assetclient.RecordingLifecycleSnapshot) error {
	r.applied = true
	if len(v.Items) == 0 {
		return nil
	}
	r.recording.UploadedAt = v.Items[0].UploadedAt
	r.recording.ExpiresAt = v.Items[0].MediaExpiresAt
	return nil
}

func TestRetentionSnapshotAppliedBeforeAdminListAndRejectsWrongBinding(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC()
	uploaded := now.Add(-20 * 24 * time.Hour)
	ready := uploaded.Add(time.Hour)
	expiry := uploaded.Add(60 * 24 * time.Hour)
	repo := &lifecycleTestRepo{packageTestRepo: packageTestRepo{testRepo{recording: Recording{ID: "rec-a", PackageID: pkg, Status: "published", UploadedAt: &ready, ReadyAt: &ready}}}}
	wrong := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/priv/recordings/lifecycle" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		id := "rec-a"
		if wrong {
			id = "other"
		}
		json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 60, Revision: 2, ActivatedAt: &now}, Items: []assetclient.RecordingPackageStatus{{PackageID: pkg, SessionID: pkg, RecordingID: id, State: "ready", UploadedAt: &uploaded, ReadyAt: &ready, MediaExpiresAt: &expiry, RetentionRevision: 2}}})
	}))
	defer server.Close()
	s := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if err := s.syncLifecycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repo.applied || !repo.recording.UploadedAt.Equal(uploaded) || !repo.recording.ExpiresAt.Equal(expiry) {
		t.Fatal("retention not applied")
	}
	wrong = true
	repo.applied = false
	if err := s.syncLifecycle(context.Background()); err == nil || repo.applied {
		t.Fatal("mismatched owner projection accepted")
	}
}

type notificationLifecycleRepo struct{ lifecycleTestRepo }

func (*notificationLifecycleRepo) LifecycleBindings(context.Context) ([]assetclient.RecordingLifecycleBinding, error) {
	panic("a notification must not load the whole catalogue")
}

func TestNotificationLifecycleIsScopedAndStillChecksRetention(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC()
	uploaded, ready, oldExpiry := now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-time.Second)
	expiry := uploaded.Add(30 * 24 * time.Hour)
	repo := &notificationLifecycleRepo{lifecycleTestRepo{packageTestRepo: packageTestRepo{testRepo{recording: Recording{ID: "rec-a", PackageID: pkg, Status: "published", UploadedAt: &uploaded, ReadyAt: &ready, ExpiresAt: &oldExpiry}}}}}
	wrong := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: "rec-a", State: "ready", UploadedAt: &uploaded, ReadyAt: &ready, MediaExpiresAt: &expiry, RetentionRevision: 1}
		if r.URL.Path == "/priv/recordings/lifecycle" {
			var in struct {
				Items []assetclient.RecordingLifecycleBinding `json:"items"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Items) != 1 || in.Items[0].RecordingID != "rec-a" || in.Items[0].PackageID != pkg {
				t.Errorf("unscoped lifecycle request: %+v %v", in, err)
			}
			if wrong {
				status.RecordingID = "other"
			}
			json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}, Items: []assetclient.RecordingPackageStatus{status}})
			return
		}
		json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if available, err := svc.NotificationAvailable(context.Background(), "rec-a"); err != nil || !available || !repo.recording.ExpiresAt.Equal(expiry) {
		t.Fatalf("current retention not checked: available=%v err=%v", available, err)
	}
	wrong = true
	if available, err := svc.NotificationAvailable(context.Background(), "rec-a"); err != ErrConflict || available {
		t.Fatalf("wrong authority accepted: available=%v err=%v", available, err)
	}
}
