package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type cardWireEntitlements struct{ first, second string }

func (e cardWireEntitlements) CheckEntitlements(_ context.Context, user string, codes []string) ([]operationsclient.EntitlementDecision, error) {
	decision := "deny"
	if user == e.first || user == e.second {
		decision = "allow"
	}
	return []operationsclient.EntitlementDecision{{EntitlementCode: codes[0], Decision: decision}}, nil
}

// Real HTTP admission, domain and PostgreSQL; only external Asset/entitlement responses are synthetic.
func TestCardAuthorityHTTPPostgresReadOnlyAndAccountFence(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Microsecond)
	id, first, second := platform.NewID(), platform.NewID(), platform.NewID()
	const pkg = "dddddddddddddddddddddddddddddddd"
	expiry := now.Add(time.Hour)
	if _, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at)
 VALUES($1::uuid,$1::text,$1::text,$2,$3,$2,'published',$4,$2,$5,'test','test',$2,$2)`, id, now, expiry, pkg, platform.NewID()); err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, id)
	status := func(recording, packageID string) assetclient.RecordingPackageStatus {
		return assetclient.RecordingPackageStatus{RecordingID: recording, PackageID: packageID, SessionID: packageID, State: "ready", UploadedAt: &now, ReadyAt: &now, MediaExpiresAt: &expiry, RetentionRevision: 1, Renditions: []assetclient.RecordingRendition{{Name: "480p", DurationSeconds: 100}}}
	}
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/priv/recordings/lifecycle" {
			var input struct {
				Items []assetclient.RecordingLifecycleBinding
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			out := assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}}
			for _, binding := range input.Items {
				out.Items = append(out.Items, status(binding.RecordingID, binding.PackageID))
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/ready") {
			t.Errorf("unexpected Asset operation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(status(r.URL.Query().Get("recordingId"), strings.Split(r.URL.Path, "/")[3]))
	}))
	defer asset.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", ""))
	server := httptest.NewServer(httpapi.NewWithContent(nil, nil, db, nil, "api-gateway", "local-card-wire", false).
		WithRecordings(service).WithBulletinEntitlements(cardWireEntitlements{first, second}).Routes())
	defer server.Close()
	read := func(user string, want int, position *float64) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, server.URL+"/api/member/recordings?limit=12&q="+url.QueryEscape(id), nil)
		if err != nil {
			t.Fatal(err)
		}
		if user != "" {
			request.Header.Set("Dapr-Caller-App-Id", "api-gateway")
			request.Header.Set("dapr-api-token", "local-card-wire")
			request.Header.Set("X-HHC-Auth-Provider", "account-api")
			request.Header.Set("X-HHC-User-ID", user)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("status/cache: %d %q", response.StatusCode, response.Header.Get("Cache-Control"))
		}
		if want != http.StatusOK {
			return
		}
		var body struct {
			Data []struct {
				ID       string                   `json:"id"`
				Version  string                   `json:"playbackMediaVersion"`
				Progress *recordings.CardProgress `json:"playbackProgress"`
			}
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil || len(body.Data) != 1 || body.Data[0].ID != id || body.Data[0].Version != "vod:"+pkg {
			t.Fatalf("card authority: %+v %v", body, err)
		}
		progress := body.Data[0].Progress
		if position == nil && progress != nil || position != nil && (progress == nil || progress.PositionSeconds != *position) {
			t.Fatalf("account history: %+v", progress)
		}
	}
	read("", http.StatusUnauthorized, nil)
	read(platform.NewID(), http.StatusNotFound, nil)
	read(second, http.StatusOK, nil)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_progress WHERE recording_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("list created owner: %d %v", count, err)
	}
	claim := recordings.ProgressClaim{SessionID: platform.NewID(), ClaimID: platform.NewID(), MediaVersion: "vod:" + pkg, StartKind: "resume"}
	owner, err := repo.ClaimPlaybackProgress(ctx, id, first, claim, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	position := 42.0
	if _, err := repo.UpdatePlaybackProgress(ctx, id, first, recordings.ProgressUpdate{SessionID: claim.SessionID, Generation: owner.Generation, MediaVersion: claim.MediaVersion, Sequence: 1, PositionSeconds: position, State: "paused"}, now); err != nil {
		t.Fatal(err)
	}
	read(first, http.StatusOK, &position)
	read(second, http.StatusOK, nil)
	after, err := repo.GetPlaybackProgress(ctx, id, first, now)
	if err != nil || after.Progress == nil || after.Progress.Generation != owner.Generation || after.Progress.PositionSeconds != position {
		t.Fatalf("list replaced owner/history: %+v %v", after, err)
	}
}
