package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type shareRecordingRepository struct {
	recordings.Repository
	value recordings.Recording
	err   error
}

func (r *shareRecordingRepository) Get(context.Context, string) (recordings.Recording, error) {
	return r.value, r.err
}

func TestShareAndMemberAutomaticCoverKeepFirstReadyCandidate(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	now := time.Now()
	expires := now.Add(time.Hour)
	for _, selected := range []string{"", "chosen"} {
		t.Run("selected="+selected, func(t *testing.T) {
			pkg := strings.Repeat("a", 32)
			want := selected
			if want == "" {
				want = "first"
			}
			wantPath := "/priv/recording-packages/" + pkg + "/covers/" + want + "/content"
			asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("recordingId") != id {
					t.Error("lost recording binding")
				}
				if strings.HasSuffix(r.URL.Path, "/covers") {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{
						{ID: "pending", UploadID: "pending", Kind: "auto", State: "pending"},
						{ID: "custom", UploadID: "custom", Kind: "custom", State: "ready"},
						{ID: "first", UploadID: "first", Kind: "auto", State: "ready"},
						{ID: "last", UploadID: "last", Kind: "auto", State: "ready"},
					}})
					return
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write([]byte(r.URL.Path))
			}))
			defer asset.Close()
			repo := &shareRecordingRepository{value: recordings.Recording{ID: id, Status: "published", PackageID: pkg, ReadyAt: &now, UploadedAt: &now, ExpiresAt: &expires, SelectedCoverID: selected}}
			service := recordings.NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))
			for _, member := range []bool{false, true} {
				var data []byte
				var err error
				if member {
					data, err = service.CoverContent(context.Background(), id, "", true)
				} else {
					data, err = service.ShareCover(context.Background(), id)
				}
				if err != nil || string(data) != wantPath {
					t.Fatalf("member=%v: cover %s, error %v; want %s", member, data, err, wantPath)
				}
			}
		})
	}
}

func TestRecordingSharePublicAllowlistAndVisibility(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	now := time.Now()
	expires := now.Add(time.Hour)
	value := recordings.Recording{ID: id, Title: "Public title", Description: "PRIVATE description", Status: "published", PackageID: strings.Repeat("a", 32), SourceID: "PRIVATE source", SelectedCoverID: "selected-cover", ReadyAt: &now, UploadedAt: &now, ExpiresAt: &expires}
	calls := 0
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/priv/recording-packages/"+value.PackageID+"/covers/selected-cover/content" || r.URL.Query().Get("recordingId") != id {
			t.Errorf("unexpected asset access: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg fixture"))
	}))
	defer asset.Close()
	repo := &shareRecordingRepository{value: value}
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", false).WithRecordings(service).Routes()
	for _, ua := range []string{"line-poker/1.0", "Mozilla/5.0 LINE/15.0.0", "OtherBot"} {
		for _, suffix := range []string{"", "/cover"} {
			request := httptest.NewRequest("GET", "/api/recording-previews/"+id+suffix, nil)
			request.Header.Set("User-Agent", ua)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, request)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
				t.Fatalf("public preview: %d %s", w.Code, w.Body)
			}
			if suffix == "" {
				var body struct {
					Data map[string]any `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data) != 1 || body.Data["title"] != value.Title || strings.Contains(w.Body.String(), "PRIVATE") {
					t.Fatalf("public field leak: %s", w.Body)
				}
			} else if w.Body.String() != "jpeg fixture" || w.Header().Get("Content-Type") != "image/jpeg" {
				t.Fatal("incorrect share cover")
			}
		}
	}
	for _, state := range []string{"draft", "hidden", "expired", "terminal", "not-ready", "missing-package", "deleted"} {
		t.Run(state, func(t *testing.T) {
			repo.value, repo.err = value, nil
			switch state {
			case "draft":
				repo.value.Status = "draft"
			case "hidden":
				repo.value.Hidden = true
			case "expired":
				repo.value.ExpiresAt = &now
			case "terminal":
				repo.value.MediaTerminal = true
			case "not-ready":
				repo.value.ReadyAt = nil
			case "missing-package":
				repo.value.PackageID = ""
			case "deleted":
				repo.err = recordings.ErrNotFound
			}
			before := calls
			for _, suffix := range []string{"", "/cover"} {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/recording-previews/"+id+suffix, nil))
				if w.Code != 404 || strings.Contains(w.Body.String(), value.Title) {
					t.Fatalf("hidden preview: %d %s", w.Code, w.Body)
				}
			}
			if calls != before {
				t.Fatal("unavailable record reached Asset")
			}
		})
	}
	for _, path := range []string{"/api/recording-previews", "/api/recording-previews/not-an-id", "/api/recording-previews/" + id + "?token=ignored", "/api/recording-previews/" + id + "/playback", "/api/recording-previews/" + id + "/covers/other/content"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Errorf("unexpected public route %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/member/recordings/" + id + "/cover", "/api/member/recordings/" + id + "/playback"} {
		w := httptest.NewRecorder()
		method := "GET"
		if strings.HasSuffix(path, "/playback") {
			method = "POST"
		}
		handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != 401 {
			t.Fatalf("private media opened: %d", w.Code)
		}
	}
}

func (r *shareRecordingRepository) RecordingIDByPublicID(_ context.Context, publicID string) (string, error) {
	if publicID != r.value.PublicID {
		return "", recordings.ErrNotFound
	}
	return r.value.ID, r.err
}

func TestPublicVideoPreviewUsesSameEligibilityAndCanonicalHeader(t *testing.T) {
	now := time.Now()
	expires := now.Add(time.Hour)
	repo := &shareRecordingRepository{value: recordings.Recording{ID: "0199a000-0000-4000-8000-000000000003", PublicID: "FA0AoBO02uU", Title: "public", Description: "private", Status: "published", PackageID: strings.Repeat("a", 32), UploadedAt: &now, ReadyAt: &now, ExpiresAt: &expires}}
	service := recordings.NewService(repo, "", nil, func() time.Time { return now })
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", false).WithRecordings(service).Routes()
	for _, path := range []string{"/api/video-previews/FA0AoBO02uU", "/api/recording-previews/" + repo.value.ID} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("Link") != "</video/FA0AoBO02uU>; rel=\"canonical\"" {
			t.Fatalf("preview: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "publicId") || strings.Contains(w.Body.String(), repo.value.ID) || strings.Contains(w.Body.String(), "private") {
			t.Fatal("preview exposed identity or private fields")
		}
	}
	for _, path := range []string{"/api/video-previews/fa0AoBO02uU", "/api/video-previews/short", "/api/video-previews/FA0AoBO02uU/extra", "/api/video-previews/FA0AoBO02uU?token=x", "/api/video-previews/%46A0AoBO02uU"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 || w.Header().Get("Link") != "" {
			t.Fatalf("invalid exposed preview: %s %d", path, w.Code)
		}
	}
	repo.value.Hidden = true
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/video-previews/FA0AoBO02uU", nil))
	if w.Code != 404 || w.Header().Get("Link") != "" {
		t.Fatal("hidden mapping exposed")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/member/videos/FA0AoBO02uU/watch", nil))
	if w.Code != 401 {
		t.Fatalf("unauthenticated watch: %d", w.Code)
	}
}

type waitingShareRepository struct {
	shareRecordingRepository
	recordings.BroadcastRepository
	broadcast recordings.Broadcast
}

func (r *waitingShareRepository) GetBroadcast(context.Context, string) (recordings.Broadcast, error) {
	return r.broadcast, nil
}

func TestWaitingAutomaticBroadcastShareWorksWithoutCaptureOrAsset(t *testing.T) {
	branding, err := os.ReadFile("../recordings/assets/church-live-v1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"draft", "scheduled", "preview", "start_pending"} {
		for _, publicID := range []string{"_FFgZxgQ_ft", "Ab_012-xyzQ"} {
			t.Run(phase+"/"+publicID, func(t *testing.T) {
				repo := &waitingShareRepository{shareRecordingRepository: shareRecordingRepository{value: recordings.Recording{ID: "0199a000-0000-4000-8000-000000000003", PublicID: publicID, Title: "Saved public title", Description: "PRIVATE description", Broadcast: true, Status: "draft"}}, broadcast: recordings.Broadcast{View: recordings.BroadcastView{Phase: phase, Announced: true}, CoverSnapshot: recordings.CaptureLiveCover{Settings: recordings.LiveCoverSettings{Selection: recordings.LiveCoverSelection{Mode: "auto"}, CoverState: "waiting"}}}}
				// No package client, capture, binding or media exists before the stream starts.
				service := recordings.NewService(repo, "", nil, time.Now)
				handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", false).WithRecordings(service).Routes()
				for _, base := range []string{"/api/video-previews/" + publicID, "/api/recording-previews/" + repo.value.ID} {
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, httptest.NewRequest("GET", base, nil))
					var body struct {
						Data map[string]any `json:"data"`
					}
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data) != 1 || body.Data["title"] != repo.value.Title || strings.Contains(w.Body.String(), "PRIVATE") {
						t.Fatalf("title: %d %s", w.Code, w.Body)
					}
					if w.Header().Get("Link") != "</video/"+publicID+">; rel=\"canonical\"" {
						t.Fatal("canonical mapping missing")
					}
					w = httptest.NewRecorder()
					handler.ServeHTTP(w, httptest.NewRequest("GET", base+"/cover", nil))
					if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), branding) {
						t.Fatalf("automatic cover: %d", w.Code)
					}
					if _, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
						t.Fatalf("invalid automatic JPEG: %v", err)
					}
				}
				for _, announced := range []bool{true, false} {
					repo.broadcast.View.Announced = announced
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/member/videos/"+publicID+"/watch", nil))
					if w.Code != 401 {
						t.Fatalf("announced=%v anonymous playback: %d", announced, w.Code)
					}
				}
				for _, base := range []string{"/api/video-previews/" + publicID, "/api/recording-previews/" + repo.value.ID} {
					for _, suffix := range []string{"", "/cover"} {
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, httptest.NewRequest("GET", base+suffix, nil))
						if w.Code != 404 || strings.Contains(w.Body.String(), repo.value.Title) {
							t.Fatalf("unannounced preview %s: %d", base+suffix, w.Code)
						}
					}
				}
			})
		}
	}
}
