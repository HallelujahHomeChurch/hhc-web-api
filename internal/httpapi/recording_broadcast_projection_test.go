package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type broadcastSummaryRepository struct {
	progressCalls int
	recordings.Repository
	recordings.BroadcastRepository
	recordings.LiveRepository
	recordings.CaptureRepository
	recording recordings.Recording
	capture   recordings.Capture
	broadcast recordings.Broadcast
}

func (r *broadcastSummaryRepository) Get(context.Context, string) (recordings.Recording, error) {
	return r.recording, nil
}
func (r *broadcastSummaryRepository) GetCapture(context.Context, string, string) (recordings.Capture, error) {
	return r.capture, nil
}
func (r *broadcastSummaryRepository) LatestRecordingCapture(context.Context, string) (recordings.Capture, error) {
	return r.capture, nil
}
func (r *broadcastSummaryRepository) GetBroadcast(context.Context, string) (recordings.Broadcast, error) {
	return r.broadcast, nil
}
func (r *broadcastSummaryRepository) UpdateCapture(_ context.Context, _ string, fn func(*recordings.Capture) error) (recordings.Capture, error) {
	err := fn(&r.capture)
	return r.capture, err
}

func TestAdminBroadcastSummaryHTTPContractKeepsCapturePrivate(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	const capture = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UTC()
	repo := &broadcastSummaryRepository{
		recording: recordings.Recording{ID: id, Broadcast: true, Title: "Broadcast", Status: "draft", Version: 1},
		capture:   recordings.Capture{BroadcastEpoch: 1, RecordingID: id, Status: recordings.CaptureStatus{AutoPublish: "cancelled", RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Objects: []assetclient.RecordingCaptureObject{{RecordingPackageObject: assetclient.RecordingPackageObject{Path: "private-segment"}}}}}},
		broadcast: recordings.Broadcast{Epoch: 1, View: recordings.BroadcastView{Phase: "live", Policy: recordings.BroadcastPolicy{AutoPublish: true}, Binding: &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1}}},
	}
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, func() time.Time { return now })).WithAuditOutbox(&bulletinAuditOutbox{}, func() time.Time { return now }).Routes()
	for _, scope := range []string{"cms:recordings:write", "cms:recordings:read"} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/recordings/"+id, nil)
		trustedBulletinAudit(req, scope)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if scope == "cms:recordings:write" {
			if response.Code != 403 {
				t.Fatalf("write-only read=%d", response.Code)
			}
			continue
		}
		if response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body)
		}
		var body struct {
			Data struct {
				Capture map[string]any `json:"capture"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		got := body.Data.Capture
		if len(got) != 4 || got["captureId"] != capture || got["autoPublish"] != "pending" || got["liveEnabled"] != false || got["liveState"] != "starting" {
			t.Fatalf("unsafe or incorrect summary=%+v", got)
		}
		if repo.capture.Status.AutoPublish != "cancelled" {
			t.Fatal("read activated C1 execution")
		}
	}
}

func (r *broadcastSummaryRepository) ListVisible(context.Context, time.Time) ([]recordings.Recording, error) {
	return []recordings.Recording{r.recording}, nil
}
func (r *broadcastSummaryRepository) ListVisiblePage(context.Context, time.Time, *time.Time, string, int, string) ([]recordings.Recording, error) {
	return []recordings.Recording{r.recording}, nil
}
func (r *broadcastSummaryRepository) RecordPackageReady(context.Context, string, string, time.Time) (recordings.Recording, error) {
	return r.recording, nil
}

func (r *broadcastSummaryRepository) GetCardProgress(_ context.Context, user string, ids []string, _ time.Time) (map[string]recordings.CardPlayback, error) {
	r.progressCalls++
	if user != "11111111-1111-4111-8111-111111111111" || len(ids) != 1 || ids[0] != r.recording.ID {
		panic("untrusted or unbounded progress input")
	}
	return map[string]recordings.CardPlayback{r.recording.ID: {MediaVersion: "vod:" + r.recording.PackageID + ":5", Progress: &recordings.CardProgress{MediaVersion: "vod:" + r.recording.PackageID + ":5", PositionSeconds: 42, Completed: true}}}, nil
}

func TestMemberBroadcastDurationHTTPContract(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	const pkg = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UTC()
	uploaded, expiry := now.Add(-time.Hour), now.Add(719*time.Hour)
	start, end, duration := 8, 56, 1441.44
	repo := &broadcastSummaryRepository{
		recording: recordings.Recording{ID: id, Broadcast: true, Title: "Broadcast", Status: "published", Version: 1, PackageID: pkg, UploadedAt: &uploaded, ReadyAt: &uploaded, ExpiresAt: &expiry},
		broadcast: recordings.Broadcast{Epoch: 1, MemberState: "vod", RangeApplied: 5, View: recordings.BroadcastView{Announced: true, Boundaries: recordings.BroadcastBoundaries{Revision: 5, StartSequence: &start, EndSequenceExclusive: &end}}, Projection: &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 5, StartSequence: &start, EndSequenceExclusive: &end, State: "ready", DurationSeconds: &duration}},
	}
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: id, State: "ready", ReadyAt: &uploaded, MediaExpiresAt: &expiry, Renditions: []assetclient.RecordingRendition{{Name: "480p", DurationSeconds: 1785.3836}}})
	}))
	defer asset.Close()
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", "")))
	for _, query := range []string{"", "?limit=12", "?limit=12&q=Broadcast"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/member/recordings"+query, nil)
		request = request.WithContext(context.WithValue(request.Context(), principalKey{}, principal{UserID: "11111111-1111-4111-8111-111111111111"}))

		requestContext, observation := diagnostics.WithRequest(request.Context(), "projection-fixture")
		request = request.WithContext(requestContext)
		before := repo.progressCalls
		handler.memberListRecordings(response, request)
		phases, _ := observation.Finish()
		count := uint64(0)
		for _, v := range phases {
			if v.Phase == "projection" {
				count = v.Count
			}
		}
		if (count == 1) != (query != "") {
			t.Fatal("card projection must be measured once for paginated responses", phases)
		}
		if (repo.progressCalls-before == 1) != (query != "") {
			t.Fatal("progress lookup must occur once per paginated request only")
		}
		if strings.Contains(response.Body.String(), "playbackProgress") != (query != "") {
			t.Fatalf("legacy/progress DTO mismatch: %s", response.Body)
		}
		var body struct {
			Data []struct {
				Version  *string                  `json:"playbackMediaVersion"`
				Duration float64                  `json:"durationSeconds"`
				Progress *recordings.CardProgress `json:"playbackProgress"`
			} `json:"data"`
			Meta map[string]any `json:"meta"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Data) != 1 || body.Data[0].Duration != duration {
			t.Fatalf("query=%s status=%d body=%s", query, response.Code, response.Body)
		}
		if query != "" && (body.Data[0].Version == nil || *body.Data[0].Version != "vod:"+pkg+":5") {
			t.Fatalf("lost current version: %s", response.Body)
		}
		if query != "" && (body.Data[0].Progress == nil || !body.Data[0].Progress.Completed || body.Data[0].Progress.PositionSeconds != 42) {
			t.Fatalf("lost card progress: %s", response.Body)
		}
		_, cursor := body.Meta["nextCursor"]
		if cursor != (query != "") || body.Meta["nextCursor"] != nil {
			t.Fatalf("changed pagination contract: %s", response.Body)
		}
	}
}
