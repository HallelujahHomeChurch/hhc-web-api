package postgres

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
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func liveFixture(t *testing.T) (*RecordingRepository, recordings.Capture, *time.Time, *recordings.Service, *assetclient.RecordingCaptureStatus, *int, *assetclient.Client) {
	t.Helper()
	repo, c, start := captureFixture(t)
	now := start.Add(100 * time.Second)
	var err error
	c, err = repo.PrepareCapture(context.Background(), c, start)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(context.Background(), c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.ReplaceAll(platform.NewID(), "-", "")
		c.Status.State = "uploading"
		c.Operations = map[string]recordings.CaptureOperation{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status := c.Status.RecordingCaptureStatus
	status.Progress = assetclient.RecordingLiveProgress{Revision: 3, LastSequence: 2, MediaEndSeconds: 90, LastAdvancedAt: &now}
	grants := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/progress"):
			if r.Header.Get("X-HHC-Actor-ID") != c.ActorID {
				t.Error("progress forged uploader")
			}
			json.NewEncoder(w).Encode(status.Progress)
		case strings.HasSuffix(r.URL.Path, "/grant"):
			var input struct {
				RecordingID, UserID, PlaybackScopeID string
				ExpiresAt                            time.Time
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.RecordingID != c.RecordingID || input.UserID == "" || input.PlaybackScopeID == "" || input.ExpiresAt.After(now.Add(5*time.Minute)) {
				t.Error("bad grant binding")
			}
			grants++
			json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "credential", ExpiresAt: input.ExpiresAt})
		case strings.Contains(r.URL.Path, "recording-captures/"):
			json.NewEncoder(w).Encode(status)
		default:
			json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 30, Revision: 1}})
		}
	}))
	t.Cleanup(server.Close)
	client := assetclient.New(server.URL, "hhc-web-api", "")
	service := recordings.NewService(repo, "https://media.alive.org.tw", []byte(strings.Repeat("test", 8)), func() time.Time { return now }).WithPackages(client).WithWatermarkTrace(true)
	t.Cleanup(func() {
		repo.db.Exec(`DELETE FROM hhc_web.recording_watermark_receipt WHERE recording_id=$1`, c.RecordingID)
	})
	return repo, c, &now, service, &status, &grants, client
}
func TestExpiredLiveGrantCanResumeRegisteredScopeAfterRestartAndStop(t *testing.T) {
	repo, c, now, service, status, grants, client := liveFixture(t)
	ctx := context.Background()
	user, scope := platform.NewID(), platform.NewID()
	first, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user)
	if err != nil {
		t.Fatal(err)
	}
	if first.WatermarkCode == "" || !strings.Contains(first.MediaURL, "/captures/"+c.Status.CaptureID+"/sessions/"+scope) {
		t.Fatal("missing live trace/path")
	}
	stop := *now
	_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Status.StopAcceptedAt = &stop; return nil })
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Minute)
	// A fresh service instance reloads the durable registry, even after grant expiry.
	service = recordings.NewService(NewRecordingRepository(repo.db), "https://media.alive.org.tw", []byte(strings.Repeat("test", 8)), func() time.Time { return *now }).WithPackages(client).WithWatermarkTrace(true)
	resumed, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user)
	if err != nil || resumed.LiveState != "ending" || resumed.WatermarkCode != first.WatermarkCode {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	for _, tc := range []struct{ capture, scope, user string }{{c.Status.CaptureID, platform.NewID(), user}, {c.Status.CaptureID, scope, platform.NewID()}, {strings.Repeat("0", 32), scope, user}} {
		if _, err = service.LivePlayback(ctx, c.RecordingID, tc.capture, tc.scope, tc.user); !errors.Is(err, recordings.ErrNotFound) {
			t.Fatalf("spoof or new scope %v", err)
		}
	}
	ended := *now
	status.Progress.Ended = true
	status.Progress.EndedAt = &ended
	status.Progress.MediaEndSeconds = 900
	status.Progress.Revision++
	_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.NormalEnd = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user)
	if err != nil || result.LiveState != "ended" || result.ReplayUntil == nil {
		t.Fatalf("ended %+v %v", result, err)
	}
	*now = result.ReplayUntil.Add(-time.Minute)
	last, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user)
	if err != nil || last.ExpiresAt.After(*result.ReplayUntil) {
		t.Fatalf("last grant %+v %v", last, err)
	}
	*now = *result.ReplayUntil
	if _, err = service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("replay deadline %v", err)
	}
	if *grants != 4 {
		t.Fatalf("denied request issued grant %d", *grants)
	}
}
func TestLiveGrantAndStopOrCloseShareParentLockInBothOrders(t *testing.T) {
	for _, operation := range []string{"stop", "close"} {
		for _, closeFirst := range []bool{false, true} {
			t.Run(operation+"/"+map[bool]string{false: "grant first", true: "control first"}[closeFirst], func(t *testing.T) {
				repo, c, now, _, _, _, _ := liveFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				closeCapture := func() error {
					_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
						if operation == "stop" {
							c.Status.StopAcceptedAt = now
						} else {
							c.Status.LiveEnabled = false
						}
						return nil
					})
					return err
				}
				if closeFirst {
					if err := closeCapture(); err != nil {
						t.Fatal(err)
					}
				}
				entered, release := make(chan struct{}), make(chan struct{})
				issued := make(chan error, 1)
				go func() {
					issued <- repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID(), *now, true, func(recordings.Capture, string) error { close(entered); <-release; return nil })
				}()
				if closeFirst {
					if err := <-issued; !errors.Is(err, recordings.ErrNotFound) {
						t.Fatalf("closed issued %v", err)
					}
					return
				}
				<-entered
				closing := make(chan error, 1)
				go func() { closing <- closeCapture() }()
				select {
				case err := <-closing:
					t.Fatalf("close overtook grant call %v", err)
				case <-time.After(80 * time.Millisecond):
				}
				close(release)
				if err := <-issued; err != nil {
					t.Fatal(err)
				}
				if err := <-closing; err != nil {
					t.Fatal(err)
				}
				if err := repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID(), *now, false, func(recordings.Capture, string) error { t.Fatal("closed grant callback"); return nil }); !errors.Is(err, recordings.ErrNotFound) {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestLiveScopeAccountCleanup(t *testing.T) {
	repo, c, _, service, _, grants, _ := liveFixture(t)
	ctx := context.Background()
	user, scope := platform.NewID(), platform.NewID()
	if _, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user); err != nil {
		t.Fatal(err)
	}
	cleanup := New(repo.db)
	count, remaining, err := cleanup.CleanupWatermarkAccount(ctx, user, platform.NewID())
	if err != nil || count != 2 || remaining != 0 {
		t.Fatalf("erase count=%d remaining=%d err=%v", count, remaining, err)
	}
	if _, err = service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("erased subject could issue %v", err)
	}
	if *grants != 1 {
		t.Fatalf("erased subject received credential %d", *grants)
	}
}

func TestExpiredCaptureCannotRenewWithoutReconcilerAndNormalVODSurvives(t *testing.T) {
	for _, normal := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash", true: "normal VOD"}[normal], func(t *testing.T) {
			repo, c, now, service, status, grants, _ := liveFixture(t)
			ctx := context.Background()
			user, scope := platform.NewID(), platform.NewID()
			if _, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user); err != nil {
				t.Fatal(err)
			}
			if normal {
				stop, ended := *now, *now
				status.State = "ready"
				status.PackageID = &c.Status.CaptureID
				status.Progress.Ended = true
				status.Progress.EndedAt = &ended
				status.Progress.Revision++
				_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
					c.NormalEnd = true
					c.Status.StopAcceptedAt = &stop
					c.Status.RecordingCaptureStatus = *status
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET status='published',uploaded_at=$2,ready_at=$2,expires_at=$3 WHERE id=$1`, c.RecordingID, ended, ended.Add(30*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			*now = c.Status.ExpiresAt
			if _, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user); !errors.Is(err, recordings.ErrNotFound) {
				t.Fatalf("deadline without reconciler %v", err)
			}
			saved, err := repo.GetCapture(ctx, c.IntentID, c.RecordingID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status.LiveEnabled {
				t.Fatal("deadline did not disable live")
			}
			if normal {
				value, err := repo.Get(ctx, c.RecordingID)
				if err != nil || !value.Visible(*now) || saved.Status.AutoPublish != "pending" || saved.Status.State != "ready" {
					t.Fatalf("live deadline damaged normal VOD %+v %+v %v", value, saved.Status, err)
				}
			} else if saved.Status.State != "expired" || saved.Status.AutoPublish != "cancelled" {
				t.Fatalf("crash not durably terminal %+v", saved.Status)
			}
			if *grants != 1 {
				t.Fatal("expired issued grant")
			}
		})
	}
}
func TestPermanentLiveTailFailureCancelsAutoPublish(t *testing.T) {
	repo, c, now, service, status, grants, _ := liveFixture(t)
	ctx := context.Background()
	user, scope := platform.NewID(), platform.NewID()
	if _, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user); err != nil {
		t.Fatal(err)
	}
	status.State = "failed"
	reason := "capture_incomplete"
	status.TerminalReason = &reason
	status.TerminalAt = now
	if _, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("failed tail issued %v", err)
	}
	saved, err := repo.GetCapture(ctx, c.IntentID, c.RecordingID)
	if err != nil || saved.Status.AutoPublish != "cancelled" || saved.Status.LiveEnabled || saved.NormalEnd || !saved.Terminal() {
		t.Fatalf("tail failure %+v %v", saved, err)
	}
	if *grants != 1 {
		t.Fatal("failed tail issued grant")
	}
}
func TestLiveAndAutoPublishIndependentWithoutDraftVOD(t *testing.T) {
	repo, c, _, service, _, _, _ := liveFixture(t)
	ctx := context.Background()
	_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Status.AutoPublish = "cancelled"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListLive(ctx, "")
	if err != nil || len(listed) == 0 {
		t.Fatalf("auto=false blocked live %v", err)
	}

	for _, q := range []string{listed[0].Title, "does-not-match-the-live-title"} {
		matched, e := service.ListLive(ctx, q)
		if e != nil || (len(matched) > 0) != (q == listed[0].Title) {
			t.Fatalf("live search=%q results=%+v err=%v", q, matched, e)
		}
	}
	if _, err = service.Playback(ctx, c.RecordingID, platform.NewID(), "", platform.NewID()); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("draft VOD exposed: %v", err)
	}
	_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.AutoPublish = "pending"
		c.Status.LiveEnabled = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err = service.ListLive(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range listed {
		if v.ID == c.RecordingID {
			t.Fatal("auto=true opened live")
		}
	}
}
func TestLiveScopeRetention(t *testing.T) {
	repo, c, now, _, _, _, _ := liveFixture(t)
	ctx := context.Background()
	user := platform.NewID()
	for i := 0; i < 3; i++ {
		if err := repo.RegisterCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, platform.NewID(), *now); err != nil {
			t.Fatal(err)
		}
	}
	cleanup := New(repo.db)
	if _, err := cleanup.PurgeWatermarkRecords(ctx, c.Status.ExpiresAt, 1000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_capture_scope WHERE user_id=$1`, user).Scan(&count); err != nil || count != 0 {
		t.Fatalf("TTL %d %v", count, err)
	}
}

func TestLivePlaybackRejectsMissingMediaConfiguration(t *testing.T) {
	repo, c, now, _, _, grants, client := liveFixture(t)
	service := recordings.NewService(repo, "", nil, func() time.Time { return *now }).WithPackages(client)
	if _, err := service.LivePlayback(context.Background(), c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID()); !errors.Is(err, recordings.ErrInvalid) {
		t.Fatalf("missing config granted %v", err)
	}
	if *grants != 0 {
		t.Fatal("invalid configuration reached Asset grant")
	}
}

func TestLiveGrantHonorsEarlierRetentionBeforeNormalEnd(t *testing.T) {
	repo, c, now, service, _, _, _ := liveFixture(t)
	until := now.Add(2 * time.Minute)
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET expires_at=$2,uploaded_at=$3,package_id=$4,ready_at=$3 WHERE id=$1`, c.RecordingID, until, *now, c.Status.CaptureID); err != nil {
		t.Fatal(err)
	}
	grant, err := service.LivePlayback(context.Background(), c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID())
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExpiresAt.After(until) {
		t.Fatalf("retention exceeded %v > %v", grant.ExpiresAt, until)
	}
}

func TestLiveAccountCleanupWaitsForGrantAndBlocksNextIssuance(t *testing.T) {
	repo, c, now, _, _, _, _ := liveFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, scope := platform.NewID(), platform.NewID()
	entered, release := make(chan struct{}), make(chan struct{})
	issued := make(chan error, 1)
	go func() {
		issued <- repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, scope, *now, true, func(recordings.Capture, string) error { close(entered); <-release; return nil })
	}()
	<-entered
	restricted := make(chan error, 1)
	go func() {
		_, _, err := New(repo.db).CleanupWatermarkAccount(ctx, user, platform.NewID())
		restricted <- err
	}()
	select {
	case err := <-restricted:
		t.Fatalf("restriction overtook in-flight grant %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	if err := <-issued; err != nil {
		t.Fatal(err)
	}
	if err := <-restricted; err != nil {
		t.Fatal(err)
	}
	if err := repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, scope, *now, true, func(recordings.Capture, string) error { t.Fatal("restricted callback"); return nil }); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("restricted grant %v", err)
	}
}
func TestLiveTracePersistenceFailureWithholdsCredentialAndScope(t *testing.T) {
	repo, c, now, service, _, grants, _ := liveFixture(t)
	user, scope := platform.NewID(), platform.NewID()
	// A pre-existing receipt bound to different immutable bytes must fail closed.
	if _, err := repo.db.Exec(`INSERT INTO hhc_web.recording_watermark_receipt(id,trace_code,user_id,recording_id,package_id,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, platform.NewID(), "01234ABCDE", user, c.RecordingID, strings.Repeat("b", 32), *now, now.Add(recordings.WatermarkRetention)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LivePlayback(context.Background(), c.RecordingID, c.Status.CaptureID, scope, user); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("trace mismatch %v", err)
	}
	if *grants != 0 {
		t.Fatal("credential issued without valid trace receipt")
	}
	var count int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_capture_scope WHERE user_id=$1`, user).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed scope committed %d %v", count, err)
	}
}

func TestAdminCaptureSummaryAndOtherStaffCloseThroughRealHTTP(t *testing.T) {
	repo, c, now, service, _, _, _ := liveFixture(t)
	ctx := context.Background()
	staff := platform.NewID()
	_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.Objects = []assetclient.RecordingCaptureObject{{RecordingPackageObject: assetclient.RecordingPackageObject{Path: "480p/seg-000000.m4s"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(service).WithAuditOutbox(NewAuditOutbox(repo.db), func() time.Time { return *now }).Routes()
	request := func(method, path, scopes, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Dapr-Caller-App-Id", "api-gateway")
		req.Header.Set("X-HHC-Auth-Provider", "account-api")
		req.Header.Set("X-HHC-User-ID", staff)
		req.Header.Set("X-HHC-Request-ID", "staff-live-control")
		req.Header.Set("X-HHC-Scopes", scopes)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	summary := request("GET", "/api/admin/recordings/"+c.RecordingID, "cms:recordings:read", "")
	if summary.Code != 200 || !strings.Contains(summary.Body.String(), `"capture":{"captureId":"`+c.Status.CaptureID) || strings.Contains(summary.Body.String(), "objects") || strings.Contains(summary.Body.String(), "seg-000000") {
		t.Fatalf("unsafe summary %d %s", summary.Code, summary.Body)
	}
	for _, kind := range []string{"live", "auto-publish"} {
		path := "/api/admin/recordings/" + c.RecordingID + "/captures/" + c.Status.CaptureID + "/" + kind
		denied := request("DELETE", path, "cms:recordings:read", `{"operationKey":"staff-`+kind+`"}`)
		if denied.Code != 403 {
			t.Fatalf("read scope mutated %d", denied.Code)
		}
		closed := request("DELETE", path, "cms:recordings:publish", `{"operationKey":"staff-`+kind+`"}`)
		if closed.Code != 200 || strings.Contains(closed.Body.String(), "seg-000000") || !strings.Contains(closed.Body.String(), `"objects":[]`) {
			t.Fatalf("staff control %d %s", closed.Code, closed.Body)
		}
	}
	upload := request("POST", "/api/admin/recordings/"+c.RecordingID+"/captures/"+c.Status.CaptureID+"/stop", "cms:recordings:write", `{"operationKey":"staff-stop"}`)
	if upload.Code != 404 {
		t.Fatalf("staff controlled uploader-only stop %d", upload.Code)
	}
}

func TestLiveReconcilerClosesCompletedReplayWithoutCancellingNormalPublication(t *testing.T) {
	repo, c, now, service, status, _, _ := liveFixture(t)
	ctx := context.Background()
	ended := *now
	status.State = "ready"
	status.PackageID = &c.Status.CaptureID
	status.Progress.Ended = true
	status.Progress.EndedAt = &ended
	_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.NormalEnd = true
		c.Status.StopAcceptedAt = &ended
		c.Status.RecordingCaptureStatus = *status
		until := ended.Add(31 * time.Minute)
		c.Status.ReplayUntil = &until
		c.Operations = map[string]recordings.CaptureOperation{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	*now = ended.Add(32 * time.Minute)
	if err = service.ReconcileCaptures(ctx, 100); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetCapture(ctx, c.IntentID, c.RecordingID)
	if err != nil || saved.Status.LiveEnabled || saved.Status.AutoPublish != "pending" || saved.Status.State != "ready" {
		t.Fatalf("completed replay not closed independently %+v %v", saved.Status, err)
	}
}
