package recordings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

type packageTestRepo struct{ testRepo }

func TestPlaybackGrantClockOffsetDiagnostics(t *testing.T) {
	for _, offset := range []time.Duration{-500 * time.Millisecond, -time.Nanosecond, 0, time.Nanosecond, 500 * time.Microsecond, time.Millisecond, 500 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			var logs bytes.Buffer
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(oldLogger)
			const pkg = "0123456789abcdef0123456789abcdef"
			now := time.Date(2026, 10, 29, 2, 46, 0, 0, time.UTC)
			ready := now.Add(-time.Hour)
			expiry := ready.Add(720 * time.Hour)
			repo := &packageTestRepo{testRepo{recording: Recording{ID: "rec-a", PackageID: pkg, Status: "published", ReadyAt: &ready, UploadedAt: &ready, ExpiresAt: &expiry}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/grant") {
					var input struct {
						RecordingExpiresAt time.Time `json:"recordingExpiresAt"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
						return
					}
					// Asset's real signer caps its one-hour lifetime by the caller's deadline.
					grantExpiry := now.Add(offset).Add(time.Hour)
					if input.RecordingExpiresAt.Before(grantExpiry) {
						grantExpiry = input.RecordingExpiresAt
					}
					json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "secret-exchange", ExpiresAt: grantExpiry})
					return
				}
				json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: "rec-a", State: "ready", ReadyAt: &ready, MediaExpiresAt: &expiry})
			}))
			defer server.Close()
			svc := NewService(repo, "https://media.example.test", make([]byte, 32), func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			ctx := auditclient.WithProvenance(context.Background(), "secret-member", "request-clock")
			value, err := svc.Playback(ctx, "rec-a", "scope-a", pkg, "secret-member")
			if offset > 0 {
				if !errors.Is(err, ErrConflict) || value.ExchangeCredential != "" {
					t.Fatalf("conflict behavior changed: %v", err)
				}
				for _, want := range []string{`"exceeds_max_lifetime":true`, fmt.Sprintf(`"lifetime_excess_ms":%d`, offset.Milliseconds()), fmt.Sprintf(`"lifetime_excess_ns":%d`, offset.Nanoseconds()), `"request_id":"request-clock"`} {
					if !strings.Contains(logs.String(), want) {
						t.Fatalf("missing %s in safe diagnostics", want)
					}
				}
				if strings.Contains(logs.String(), "secret") {
					t.Fatal("diagnostics leaked credentials or subject")
				}
				return
			}
			if logs.Len() != 0 {
				t.Fatal("healthy grants must not log diagnostics")
			}
			if err != nil {
				t.Fatalf("valid provider grant rejected: %v", err)
			}
			if value.ExchangeCredential == "" || value.ExpiresAt.After(now.Add(time.Hour)) {
				t.Fatal("grant exceeded the caller's one-hour limit")
			}
		})
	}
}

type failedWatermarkRepo struct{ packageTestRepo }

func (r *failedWatermarkRepo) EnsureRecordingWatermark(context.Context, string, string, string, time.Time) (WatermarkReceipt, error) {
	return WatermarkReceipt{}, errors.New("receipt write failed")
}

func TestPlaybackWithholdsCredentialWhenWatermarkPersistenceFails(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC().Truncate(time.Second)
	ready := now.Add(-time.Hour)
	expiry := ready.Add(720 * time.Hour)
	repo := &failedWatermarkRepo{packageTestRepo{testRepo{recording: Recording{ID: "rec-a", Title: "Meeting", PackageID: pkg, Status: "published", ReadyAt: &ready, UploadedAt: &ready, ExpiresAt: &expiry}}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/grant") {
			json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "must-not-escape", ExpiresAt: now.Add(time.Hour)})
			return
		}
		json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: "rec-a", State: "ready", ReadyAt: &ready, MediaExpiresAt: &expiry})
	}))
	defer server.Close()
	svc := NewService(repo, "https://media.example.test", make([]byte, 32), func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	legacy, err := svc.Playback(context.Background(), "rec-a", "scope-a", pkg, "member-a")
	if err != nil || len(legacy.WatermarkCode) != 8 || legacy.ExchangeCredential == "" {
		t.Fatalf("disabled trace changed legacy playback or persisted a receipt: %+v %v", legacy, err)
	}
	if _, err := svc.LookupWatermark(context.Background(), "admin", "rec-a", "01234ABCDE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled trace allowed lookup: %v", err)
	}
	svc.WithWatermarkTrace(true)
	value, err := svc.Playback(context.Background(), "rec-a", "scope-a", pkg, "member-a")
	if err == nil || value.ExchangeCredential != "" || value.WatermarkCode != "" {
		t.Fatalf("uncommitted receipt leaked playback: %+v %v", value, err)
	}
}

func (r *packageTestRepo) EnsureRecordingWatermark(_ context.Context, user, id, pkg string, now time.Time) (WatermarkReceipt, error) {
	return WatermarkReceipt{ID: "receipt", Code: "01234ABCDE", UserID: user, RecordingID: id, PackageID: pkg, IssuedAt: now, ExpiresAt: now.Add(WatermarkRetention)}, nil
}

type disappearingPackageRepo struct {
	packageTestRepo
	deleted bool
}

func (r *disappearingPackageRepo) Get(ctx context.Context, id string) (Recording, error) {
	if r.deleted {
		return Recording{}, ErrNotFound
	}
	return r.packageTestRepo.Get(ctx, id)
}

func (r *packageTestRepo) AddPackage(_ context.Context, id, pkg string, _ time.Time) error {
	if r.recording.PackageID != "" && r.recording.PackageID != pkg {
		return ErrConflict
	}
	r.recording.PackageID = pkg
	return nil
}
func (r *packageTestRepo) RecordPackageReady(ctx context.Context, id, pkg string, at time.Time) (Recording, error) {
	r.recording.ReadyAt = &at
	return r.RecordUploadCompletion(ctx, id, pkg, at)
}

func TestPackageStatusNeverInfersReadyAndRejectsWrongBinding(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC().Truncate(time.Second)
	ready := now.Add(-time.Minute)
	expiry := ready.Add(720 * time.Hour)
	status := assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: "rec-a", State: "freezing", ExpiresAt: now.Add(time.Hour)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; json.NewEncoder(w).Encode(status) }))
	defer server.Close()
	repo := &packageTestRepo{testRepo{recording: Recording{ID: "rec-a", PackageID: pkg, DraftExpiresAt: now.Add(time.Hour)}}}
	service := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	ctx := context.Background()
	if _, err := service.PackageStatus(ctx, "rec-a", pkg, "", 100); err != nil || repo.completions != 0 {
		t.Fatalf("accepted became ready: %v", err)
	}
	if _, err := service.PackageStatus(ctx, "rec-a", strings.Repeat("b", 32), "", 100); !errors.Is(err, ErrNotFound) || calls != 1 {
		t.Fatalf("cross package reached provider: %v", err)
	}
	status.State = "ready"
	if _, err := service.PackageStatus(ctx, "rec-a", pkg, "", 100); !errors.Is(err, ErrConflict) || repo.completions != 0 {
		t.Fatalf("missing timestamp accepted: %v", err)
	}
	status.ReadyAt, status.MediaExpiresAt = &ready, &expiry
	status.RecordingID = "another"
	if _, err := service.PackageStatus(ctx, "rec-a", pkg, "", 100); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong owner accepted: %v", err)
	}
	status.RecordingID = "rec-a"
	if _, err := service.PackageStatus(ctx, "rec-a", pkg, "", 100); err != nil || repo.completions != 1 || !repo.recording.ExpiresAt.Equal(expiry) {
		t.Fatalf("ready not projected: %v", err)
	}
}

func TestHLSPublishAndPlaybackRequireOwnerReadyProjection(t *testing.T) {
	const pkg = "0123456789abcdef0123456789abcdef"
	now := time.Now().UTC().Truncate(time.Second)
	ready := now.Add(-time.Hour)
	expiry := ready.Add(720 * time.Hour)
	status := assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: "rec-a", State: "validating", ReadyAt: &ready, MediaExpiresAt: &expiry, Renditions: []assetclient.RecordingRendition{{Name: "720p", DurationSeconds: 65}}}
	grants := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/grant") {
			grants++
			json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "exchange", ExpiresAt: now.Add(time.Hour)})
			return
		}
		if r.URL.Query().Get("recordingId") != "rec-a" {
			t.Error("unbound owner readiness lookup")
		}
		json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	repo := &packageTestRepo{testRepo{recording: Recording{ID: "rec-a", Title: "聚會", PackageID: pkg, Status: "draft", Version: 1, DraftExpiresAt: now.Add(time.Hour)}}}
	service := NewService(repo, "https://media.example.test", make([]byte, 32), func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	ctx := context.Background()
	if _, err := service.Publish(ctx, "rec-a", 1, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatalf("unready publish: %v", err)
	}
	status.State = "ready"
	if _, err := service.Publish(ctx, "rec-a", 1, "admin"); err != nil {
		t.Fatal(err)
	}
	playback, err := service.Playback(ctx, "rec-a", "scope-a", pkg, "member-a")
	if err != nil || playback.MediaURL != "https://media.example.test/videos/rec-a/packages/"+pkg+"/sessions/scope-a/master.m3u8" || playback.PackageID != pkg || len(playback.Renditions) != 1 || grants != 1 {
		t.Fatalf("playback: %+v %v grants=%d", playback, err, grants)
	}
	status.RecordingID = "another"
	if _, err := service.Playback(ctx, "rec-a", "scope-a", pkg, "member-a"); err == nil || grants != 1 {
		t.Fatal("wrong owner received grant")
	}
}

func TestPlaybackWithholdsCredentialWhenUnpublishedDuringGrant(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%t", deleted), func(t *testing.T) {
			const pkg = "0123456789abcdef0123456789abcdef"
			now := time.Now().UTC().Truncate(time.Second)
			ready := now.Add(-time.Hour)
			expiry := ready.Add(720 * time.Hour)
			repo := &disappearingPackageRepo{packageTestRepo: packageTestRepo{testRepo{recording: Recording{ID: "rec-a", Title: "Meeting", PackageID: pkg, Status: "published", ReadyAt: &ready, UploadedAt: &ready, ExpiresAt: &expiry}}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/grant") {
					repo.recording.Status = "draft"
					repo.deleted = deleted
					json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "must-not-escape", ExpiresAt: now.Add(time.Hour)})
					return
				}
				json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: "rec-a", State: "ready", ReadyAt: &ready, MediaExpiresAt: &expiry})
			}))
			defer server.Close()
			svc := NewService(repo, "https://media.example.test", make([]byte, 32), func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			value, err := svc.Playback(context.Background(), "rec-a", "scope-a", pkg, "member-a")
			if !errors.Is(err, ErrNotFound) || value.ExchangeCredential != "" {
				t.Fatalf("credential escaped: %+v %v", value, err)
			}
		})
	}
}

func TestPlaybackOnlyExposesExactReadyAudioDerivative(t *testing.T) {
	for _, tc := range []struct {
		name, state, version, path string
		want                       bool
		wantState                  string
	}{
		{"ready", "ready", "aac-hls-v1", "audio/aac-hls-v1/index.m3u8", true, "ready"},
		{"pending", "pending", "aac-hls-v1", "", false, "pending"}, {"failed", "failed", "aac-hls-v1", "", false, "failed"},
		{"foreign path", "ready", "aac-hls-v1", "https://evil.example/audio.m3u8", false, "ready"},
		{"future version", "ready", "aac-hls-v2", "audio/aac-hls-v2/index.m3u8", false, ""},
		{"processing", "processing", "aac-hls-v1", "", false, "processing"},
		{"unknown state", "other", "aac-hls-v1", "", false, ""},
		{"absent", "", "", "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const pkg = "0123456789abcdef0123456789abcdef"
			now := time.Now().UTC()
			ready := now.Add(-time.Hour)
			expiry := ready.Add(720 * time.Hour)
			repo := &packageTestRepo{testRepo{recording: Recording{ID: "rec-a", PackageID: pkg, Status: "published", ReadyAt: &ready, UploadedAt: &ready, ExpiresAt: &expiry}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/grant") {
					json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "exchange", ExpiresAt: now.Add(time.Hour)})
					return
				}
				response := map[string]any{"packageId": pkg, "sessionId": pkg, "recordingId": "rec-a", "state": "ready", "readyAt": ready, "mediaExpiresAt": expiry}
				if tc.name != "absent" {
					response["audio"] = map[string]string{"state": tc.state, "version": tc.version, "playlistPath": tc.path}
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			svc := NewService(repo, "https://media.example", make([]byte, 32), func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			value, err := svc.Playback(context.Background(), "rec-a", "scope-a", pkg, "member-a")
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(value)
			var got map[string]any
			json.Unmarshal(encoded, &got)
			state, present := got["audioState"]
			if present != (tc.wantState != "") || present && state != tc.wantState {
				t.Fatalf("audioState=%v present=%v want=%q", state, present, tc.wantState)
			}
			audio, ok := got["audio"].(map[string]any)
			if ok != tc.want {
				t.Fatalf("audio presence=%v want=%v", ok, tc.want)
			}
			if ok && (audio["version"] != "aac-hls-v1" || audio["playlistPath"] != "audio/aac-hls-v1/index.m3u8" || len(audio) != 2) {
				t.Fatalf("invalid audio projection: %v", audio)
			}
			if value.ExpiresAt.After(now.Add(time.Hour)) || value.ExchangeCredential == "" {
				t.Fatal("existing session grant changed")
			}
		})
	}
}
