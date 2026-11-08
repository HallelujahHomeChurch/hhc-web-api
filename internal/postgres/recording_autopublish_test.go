package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func autoPublishFixture(t *testing.T) (*RecordingRepository, recordings.Capture, time.Time) {
	t.Helper()
	repo, c, now := captureFixture(t)
	c, err := repo.PrepareCapture(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(context.Background(), c.IntentID, func(c *recordings.Capture) error {
		// Existing publication tests exercise pre-rollout captures; new cover inheritance has separate integration tests.
		c.LiveCover = nil
		pkg := strings.Repeat("a", 32)
		c.Status.CaptureID = pkg
		c.Status.PackageID = &pkg
		c.Status.State = "ready"
		c.VerifiedReadyAt = &now
		c.Status.StopAcceptedAt = &now
		c.NormalEnd = true
		c.Operations = map[string]recordings.CaptureOperation{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RecordPackageReady(context.Background(), c.RecordingID, c.Status.CaptureID, now); err != nil {
		t.Fatal(err)
	}
	repo.WithAudit(NewAuditOutbox(repo.db))
	return repo, c, now
}
func TestAutoPublishDurableIdempotentAndManualUnpublishCannotRepublish(t *testing.T) {
	repo, c, now := autoPublishFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		repo = NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db))
		if err := repo.PublishCapture(ctx, c.IntentID, now, true, now); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repo.Get(ctx, c.RecordingID)
	if err != nil || r.Status != "published" {
		t.Fatalf("%+v %v", r, err)
	}
	var generation int64
	if err := repo.db.QueryRow(`SELECT human_edit_generation FROM hhc_web.member_recording WHERE id::text=$1`, r.ID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != c.Generation {
		t.Fatal("internal publish changed human generation")
	}
	var n int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'action'='cms.recording.capture.publish' AND payload_json->>'actorType'='service' AND payload_json->>'actorId'='hhc-web-api' AND payload_json->'metadata'->>'authorizerId'=$1 AND payload_json->'metadata'->>'captureId'=$2 AND payload_json->'metadata'->>'intentId'=$3`, c.ActorID, c.Status.CaptureID, c.IntentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("audit count %d", n)
	}
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='recording.notification.submit' AND aggregate_id=$1 AND payload_json->>'actorId'=$2`, r.ID, c.ActorID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("notification count %d", n)
	}
	human := auditclient.WithProvenance(ctx, c.ActorID, "human-unpublish")
	if _, err = repo.Unpublish(human, r.ID, r.Version, c.ActorID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = repo.PublishCapture(ctx, c.IntentID, now.Add(time.Second), true, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	r, _ = repo.Get(ctx, r.ID)
	if r.Status != "draft" {
		t.Fatal("republished")
	}
}
func TestAutoPublishChecksAllTransactionalFences(t *testing.T) {
	for _, name := range []string{"normal_end", "stop", "verified_ready", "package", "generation", "denied", "cancelled", "audit"} {
		t.Run(name, func(t *testing.T) {
			repo, c, now := autoPublishFixture(t)
			ctx := context.Background()
			allowed := true
			switch name {
			case "normal_end", "stop", "verified_ready", "package", "cancelled":
				_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
					switch name {
					case "normal_end":
						c.NormalEnd = false
					case "stop":
						c.Status.StopAcceptedAt = nil
					case "verified_ready":
						c.VerifiedReadyAt = nil
					case "package":
						p := strings.Repeat("b", 32)
						c.Status.PackageID = &p
						c.NormalEnd = false
					case "cancelled":
						c.Status.AutoPublish = "cancelled"
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "generation":
				if _, err := repo.UpdateTitle(auditclient.WithProvenance(ctx, c.ActorID, "edit"), c.RecordingID, 1, "human edited", c.ActorID, now, true, nil); err != nil {
					t.Fatal(err)
				}
			case "denied":
				allowed = false
			case "audit":
				repo.auditOutbox = nil
			}
			err := repo.PublishCapture(ctx, c.IntentID, now, allowed, now)
			if name == "audit" && !errors.Is(err, recordings.ErrAuditUnavailable) {
				t.Fatalf("audit fail-open %v", err)
			}
			r, _ := repo.Get(ctx, c.RecordingID)
			if r.Status != "draft" {
				t.Fatalf("published past %s", name)
			}
			saved, _ := repo.GetCapture(ctx, c.IntentID, "")
			if (name == "generation" || name == "denied") && saved.Status.AutoPublish != "blocked" {
				t.Fatalf("not blocked: %+v %v", saved, err)
			}
		})
	}
}

func TestAutoPublishServiceRechecksPermissionAfterTransientAndRestart(t *testing.T) {
	repo, c, now := autoPublishFixture(t)
	permissionCalls := 0
	ready := now
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		permissionCalls++
		if permissionCalls == 1 {
			w.WriteHeader(503)
			return
		}
		if permissionCalls == 2 {
			w.Write([]byte(`{"allowed":null}`))
			return
		}
		w.Write([]byte(`{"allowed":true}`))
	}))
	defer account.Close()
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/covers") {
			json.NewEncoder(w).Encode(struct {
				Items []assetclient.RecordingCover `json:"items"`
			}{[]assetclient.RecordingCover{{ID: "default-cover", UploadID: "default-cover", Kind: "auto", State: "ready"}}})
			return
		}
		if r.URL.Path != "/priv/recording-packages/"+c.Status.CaptureID+"/ready" || r.URL.Query().Get("recordingId") != c.RecordingID || r.Header.Get("X-HHC-Actor-ID") != c.ActorID || r.Header.Get("X-HHC-Request-ID") != c.RequestID {
			t.Error("restart readiness lost original capture owner binding")
		}
		expiry := ready.Add(720 * time.Hour)
		json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: c.Status.CaptureID, SessionID: c.Status.CaptureID, RecordingID: c.RecordingID, State: "ready", ReadyAt: &ready, MediaExpiresAt: &expiry})
	}))
	defer asset.Close()
	service := func() *recordings.Service {
		return recordings.NewService(NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db)), "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", "")).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	}
	if err := service().ReconcileAutoPublication(context.Background(), 100); err == nil {
		t.Fatal("transient error hidden")
	}
	saved, _ := repo.GetCapture(context.Background(), c.IntentID, "")
	if saved.Status.AutoPublish != "pending" || saved.AutoNextAttemptAt == nil {
		t.Fatalf("lost pending retry %+v", saved)
	}
	if err := service().ReconcileAutoPublication(context.Background(), 100); err != nil || permissionCalls != 1 {
		t.Fatalf("ignored backoff %d %v", permissionCalls, err)
	}
	now = now.Add(10 * time.Minute)
	if err := service().ReconcileAutoPublication(context.Background(), 100); err == nil {
		t.Fatal("invalid permission response hidden")
	}
	current, _ := repo.Get(context.Background(), c.RecordingID)
	saved, _ = repo.GetCapture(context.Background(), c.IntentID, "")
	if current.Status != "draft" || saved.Status.AutoPublish != "pending" || saved.PermissionCheckedAt != nil || saved.AutoNextAttemptAt == nil {
		t.Fatalf("invalid permission response lost retry or published: %+v %+v", current, saved)
	}
	now = now.Add(10 * time.Minute)
	if err := service().ReconcileAutoPublication(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	current, _ = repo.Get(context.Background(), c.RecordingID)
	if current.Status != "published" || permissionCalls != 3 {
		t.Fatalf("restart did not publish %+v probes %d", current, permissionCalls)
	}
	saved, _ = repo.GetCapture(context.Background(), c.IntentID, "")
	if saved.PermissionCheckedAt == nil || !saved.PermissionCheckedAt.Equal(now) {
		t.Fatal("missing latest permission timestamp")
	}
}

func TestAutoPublishManualControlsWinInBothOrders(t *testing.T) {
	for _, operation := range []string{"title", "cover", "publish", "unpublish", "delete", "cancel", "close_live"} {
		for _, workerFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/worker_first_%v", operation, workerFirst), func(t *testing.T) {
				repo, c, now := autoPublishFixture(t)
				ctx := context.Background()
				human := auditclient.WithProvenance(ctx, c.ActorID, "manual")
				if workerFirst {
					if err := repo.PublishCapture(ctx, c.IntentID, now, true, now); err != nil {
						t.Fatal(err)
					}
				}
				current, _ := repo.Get(ctx, c.RecordingID)
				var err error
				switch operation {
				case "title":
					_, err = repo.UpdateTitle(human, c.RecordingID, current.Version, "manual title", c.ActorID, now, true, nil)
				case "cover":
					cover := strings.Repeat("b", 32)
					op := recordings.CoverOperation{ID: platform.NewID(), RecordingID: c.RecordingID, PackageID: c.Status.CaptureID, CoverID: cover, Expected: current.Version, Selection: recordings.CoverSelection{Mode: "auto", CandidateID: cover}, Actor: c.ActorID, Key: platform.NewID()}
					op, err = repo.PrepareCoverOperation(human, op, now)
					if err == nil {
						_, err = repo.CommitCoverOperation(human, op, true, now, func(context.Context) error { return nil })
					}
				case "publish":
					if !workerFirst {
						_, err = repo.Publish(human, c.RecordingID, current.Version, c.ActorID, now)
					}
				case "unpublish":
					_, err = repo.Unpublish(human, c.RecordingID, current.Version, c.ActorID, now)
				case "delete":
					err = repo.Delete(human, c.RecordingID, current.Version, c.ActorID, now, true)
				case "cancel", "close_live":
					service := recordings.NewService(repo, "", nil, func() time.Time { return now })
					if operation == "cancel" {
						_, err = service.CancelCaptureAutoPublish(human, c.RecordingID, c.Status.CaptureID, c.ActorID, "cancel")
					} else {
						_, err = service.CloseCaptureLive(human, c.RecordingID, c.Status.CaptureID, c.ActorID, "close")
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = repo.PublishCapture(ctx, c.IntentID, now, true, now); err != nil {
					t.Fatal(err)
				}
				saved, _ := repo.GetCapture(ctx, c.IntentID, "")
				want := "blocked"
				if workerFirst || operation == "close_live" {
					want = "published"
				} else if operation == "cancel" || operation == "unpublish" || operation == "delete" {
					want = "cancelled"
				}
				if saved.Status.AutoPublish != want {
					t.Fatalf("want %s, got %s", want, saved.Status.AutoPublish)
				}
				if operation == "unpublish" {
					r, _ := repo.Get(ctx, c.RecordingID)
					if r.Status != "draft" {
						t.Fatal("manual unpublish overwritten")
					}
				}
				if operation == "title" {
					r, _ := repo.Get(ctx, c.RecordingID)
					if r.Title != "manual title" {
						t.Fatal("manual title overwritten")
					}
				}
			})
		}
	}
}

func TestAutoPublishCancelWinsWhilePermissionProbeIsInFlight(t *testing.T) {
	repo, c, now := autoPublishFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(`{"allowed":true}`))
	}))
	defer account.Close()
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/covers") {
			json.NewEncoder(w).Encode(struct {
				Items []assetclient.RecordingCover `json:"items"`
			}{[]assetclient.RecordingCover{{ID: "default-cover", UploadID: "default-cover", Kind: "auto", State: "ready"}}})
			return
		}
		expiry := now.Add(720 * time.Hour)
		json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: c.Status.CaptureID, SessionID: c.Status.CaptureID, RecordingID: c.RecordingID, State: "ready", ReadyAt: &now, MediaExpiresAt: &expiry})
	}))
	defer asset.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", "")).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	result := make(chan error, 1)
	go func() { result <- service.ReconcileAutoPublication(context.Background(), 100) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("probe not entered")
	}
	_, err := service.CancelCaptureAutoPublish(context.Background(), c.RecordingID, c.Status.CaptureID, c.ActorID, "cancel-race")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.Get(context.Background(), c.RecordingID)
	if stored.Status != "draft" {
		t.Fatal("late permission response published cancelled intent")
	}
	var n int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id::text=$1 OR payload_json->>'resourceId'=$1`, c.RecordingID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("cancelled intent emitted %d effects", n)
	}
}
func TestAutoPublishConcurrentWorkersCommitExactlyOnce(t *testing.T) {
	repo, c, now := autoPublishFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- repo.PublishCapture(context.Background(), c.IntentID, now, true, now) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id::text=$1 OR payload_json->>'resourceId'=$1`, c.RecordingID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want one audit and one notification, got %d", n)
	}
}
func TestAutoPublishNotificationFailureRollsBackIntentAndAudit(t *testing.T) {
	repo, c, now := autoPublishFixture(t)
	_, err := repo.db.Exec(`ALTER TABLE hhc_web.outbox_event ADD CONSTRAINT test_capture_notification_failure CHECK (aggregate_id::text<>'` + c.RecordingID + `' OR event_type<>'recording.notification.submit') NOT VALID`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := repo.db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT test_capture_notification_failure`); err != nil {
			t.Error(err)
		}
	})
	if err = repo.PublishCapture(context.Background(), c.IntentID, now, true, now); err == nil {
		t.Fatal("publication ignored notification failure")
	}
	current, _ := repo.Get(context.Background(), c.RecordingID)
	saved, _ := repo.GetCapture(context.Background(), c.IntentID, "")
	var n int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id::text=$1 OR payload_json->>'resourceId'=$1`, c.RecordingID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if current.Status != "draft" || saved.Status.AutoPublish != "pending" || saved.PermissionCheckedAt != nil || n != 0 {
		t.Fatalf("partial publication %+v %+v effects=%d", current, saved, n)
	}
}

func TestAutoPublishRevokedOrDisabledAuthorizerIsDurablyBlocked(t *testing.T) {
	repo, c, now := autoPublishFixture(t)
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":false}`)) }))
	defer account.Close()
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/covers") {
			json.NewEncoder(w).Encode(struct {
				Items []assetclient.RecordingCover `json:"items"`
			}{[]assetclient.RecordingCover{{ID: "default-cover", UploadID: "default-cover", Kind: "auto", State: "ready"}}})
			return
		}
		expiry := now.Add(720 * time.Hour)
		json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: c.Status.CaptureID, SessionID: c.Status.CaptureID, RecordingID: c.RecordingID, State: "ready", ReadyAt: &now, MediaExpiresAt: &expiry})
	}))
	defer asset.Close()
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", "")).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	for i := 0; i < 2; i++ {
		if err := service.ReconcileAutoPublication(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := repo.GetCapture(context.Background(), c.IntentID, "")
	if err != nil || saved.Status.AutoPublish != "blocked" || saved.PermissionCheckedAt == nil {
		t.Fatalf("not durably blocked: %+v %v", saved, err)
	}
	current, err := repo.Get(context.Background(), c.RecordingID)
	if err != nil || current.Status != "draft" {
		t.Fatalf("denied permission published recording: %+v %v", current, err)
	}
	var n int
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE payload_json->>'action'='cms.recording.capture.block' AND payload_json->>'actorId'='hhc-web-api' AND payload_json->'metadata'->>'authorizerId'=$1`, c.ActorID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("block audit %d %v", n, err)
	}
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='recording.notification.submit' AND aggregate_id=$1`, c.RecordingID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("denied permission emitted publication notification: %d %v", n, err)
	}
}

func TestAutoPublishWaitsForDefaultOrSelectedCoverThenPublishesOnce(t *testing.T) {
	for _, selected := range []bool{false, true} {
		for _, initial := range []string{"missing", "pending", "failed"} {
			t.Run(fmt.Sprintf("selected_%v/%s", selected, initial), func(t *testing.T) {
				repo, c, now := autoPublishFixture(t)
				ready := now
				coverState := initial
				checks := 0
				if selected {
					if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET selected_cover_id='selected-cover' WHERE id::text=$1`, c.RecordingID); err != nil {
						t.Fatal(err)
					}
				}
				account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var input struct {
						UserID     string `json:"userId"`
						Permission string `json:"permission"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.UserID != c.ActorID || input.Permission != "cms:recordings:publish" {
						t.Error("lost original publication authorizer")
					}
					checks++
					w.Write([]byte(`{"allowed":true}`))
				}))
				defer account.Close()
				asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-HHC-Actor-ID") != c.ActorID || r.URL.Query().Get("recordingId") != c.RecordingID {
						t.Error("lost cover/package ownership binding")
					}
					if strings.HasSuffix(r.URL.Path, "/covers") {
						items := []assetclient.RecordingCover{}
						if selected {
							items = append(items, assetclient.RecordingCover{ID: "default-cover", UploadID: "default-cover", Kind: "auto", State: "ready"})
						}
						if coverState != "missing" {
							id, kind := "default-cover", "auto"
							if selected {
								id, kind = "selected-cover", "custom"
							}
							items = append(items, assetclient.RecordingCover{ID: id, UploadID: id, Kind: kind, State: coverState})
						}
						json.NewEncoder(w).Encode(struct {
							Items []assetclient.RecordingCover `json:"items"`
						}{items})
						return
					}
					expiry := ready.Add(720 * time.Hour)
					json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: c.Status.CaptureID, SessionID: c.Status.CaptureID, RecordingID: c.RecordingID, State: "ready", ReadyAt: &ready, MediaExpiresAt: &expiry})
				}))
				defer asset.Close()
				service := func() *recordings.Service {
					return recordings.NewService(NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db)), "", nil, func() time.Time { return now }).WithPackages(assetclient.New(asset.URL, "hhc-web-api", "")).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
				}
				if err := service().ReconcileAutoPublication(context.Background(), 100); err == nil {
					t.Fatal("unready cover was published")
				}
				saved, err := repo.GetCapture(context.Background(), c.IntentID, "")
				if err != nil || saved.Status.AutoPublish != "pending" || saved.AutoNextAttemptAt == nil || checks != 0 {
					t.Fatalf("cover wait lost pending intent: %+v checks=%d err=%v", saved, checks, err)
				}
				var n int
				if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id::text=$1 OR payload_json->>'resourceId'=$1`, c.RecordingID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("unready cover emitted %d effects: %v", n, err)
				}
				coverState = "ready"
				now = now.Add(10 * time.Minute)
				for i := 0; i < 2; i++ {
					if err = service().ReconcileAutoPublication(context.Background(), 100); err != nil {
						t.Fatal(err)
					}
				}
				saved, err = repo.GetCapture(context.Background(), c.IntentID, "")
				if err != nil || saved.Status.AutoPublish != "published" || saved.PermissionCheckedAt == nil || !saved.PermissionCheckedAt.Equal(now) || checks != 1 {
					t.Fatalf("ready cover did not publish with fresh permission: %+v checks=%d err=%v", saved, checks, err)
				}
				if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id::text=$1 OR payload_json->>'resourceId'=$1`, c.RecordingID).Scan(&n); err != nil || n != 2 {
					t.Fatalf("want one publication audit and notification, got %d: %v", n, err)
				}
			})
		}
	}
}
