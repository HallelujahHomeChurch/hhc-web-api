package postgres

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInboxPublicationFirstRecordingIsIndependent(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo := NewRecordingRepository(db).WithInboxPublications(now.Add(-time.Minute))
	ctx := context.Background()
	actor, key := platform.NewID(), platform.NewID()
	v, err := repo.Create(ctx, "Meeting", actor, key, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, v.ID)
	if err = repo.AddPackage(ctx, v.ID, strings.ReplaceAll(key, "-", ""), now); err != nil {
		t.Fatal(err)
	}
	v, err = repo.RecordPackageReady(ctx, v.ID, strings.ReplaceAll(key, "-", ""), now)
	if err != nil {
		t.Fatal(err)
	}
	v, err = repo.Publish(ctx, v.ID, v.Version, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='inbox.publication'`, v.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("inbox intents=%d err=%v", count, err)
	}
}

func TestInboxPublicationDisabledFirstVisibilityAndQueueIsolation(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := platform.NewID()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordInboxPublication(ctx, tx, time.Time{}, "recording.published", id, "", "", 1, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordInboxPublication(ctx, tx, now.Add(-time.Hour), "recording.published", id, "", "", 2, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND destination='engagement-inbox'`, id).Scan(&n); err != nil || n != 0 {
		t.Fatal("disabled publication backfilled", n, err)
	}
	id2 := platform.NewID()
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordInboxPublication(ctx, tx, now.Add(-time.Hour), "bulletin.published", id2, "children", "en", 3, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	if _, ok, err := repo.Claim(ctx, now.Add(time.Second), time.Minute); err != nil || ok {
		t.Fatal("legacy dispatcher stole inbox event", ok, err)
	}
	event, ok, err := repo.ClaimInboxPublication(ctx, now.Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatal("inbox dispatcher did not claim", ok, err)
	}
	var wire publication.InboxPublication
	if err = json.Unmarshal(event.Payload, &wire); err != nil || wire.EventID != event.ID || wire.ResourceID != id2 || wire.Series != "children" || wire.Locale != "en" || wire.SourceVersion != 3 || !wire.OccurredAt.Equal(now) {
		t.Fatal("wire changed", wire, err)
	}
	if err = repo.FinishInboxPublication(ctx, event, true, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE id=$1`, event.ID); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordInboxPublication(ctx, tx, now.Add(-time.Hour), "bulletin.published", id2, "children", "en", 99, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = repo.ClaimInboxPublication(ctx, now.Add(2*time.Minute), time.Minute); err != nil || ok {
		t.Fatal("outbox purge lost first-publication evidence", ok, err)
	}
	legacyID := platform.NewID()
	if _, err = db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'engagement-api','recording.notification.submit','recording',$2,1,'{}',$3,'pending',$4,$4,$4)`, legacyID, id, "legacy-"+legacyID, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = repo.ClaimInboxPublication(ctx, now.Add(time.Minute), time.Minute); err != nil || ok {
		t.Fatal("inbox dispatcher stole external event", ok, err)
	}
	claimed, ok, err := repo.Claim(ctx, now.Add(time.Minute), time.Minute)
	if err != nil || !ok || claimed.ID != legacyID {
		t.Fatal("external queue unavailable", ok, err)
	}
}

func TestInboxPDFCompletionIndependentOfExternalNotifyAndRepublish(t *testing.T) {
	db := accessDB(t)
	clearBulletinAuditDB(t, db)
	ctx := context.Background()
	now := time.Now().UTC()
	repo := New(db).WithInboxPublications(now.Add(-time.Hour))
	issue := seedBulletinAuditIssue(t, db, now, true)
	publish := func() {
		t.Helper()
		current, err := repo.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.StartPublish(ctx, issue.ID, "general", "en", current.Version, false, postgresBulletinAuditActor, now); err != nil {
			t.Fatal(err)
		}
		event, ok, err := repo.Claim(ctx, now, time.Minute)
		if err != nil || !ok {
			t.Fatal("claim", ok, err)
		}
		if err = repo.CompletePublish(ctx, event, "", "", now); err != nil {
			t.Fatal(err)
		}
		if err = repo.CompletePublish(ctx, event, "", "", now); err != nil {
			t.Fatal("replay", err)
		}
	}
	publish()
	var inbox, external int
	count := func() {
		t.Helper()
		if err := db.QueryRow(`SELECT count(*) FILTER(WHERE event_type='inbox.publication'),count(*) FILTER(WHERE event_type='bulletin.notification.submit') FROM hhc_web.outbox_event WHERE aggregate_id=$1`, issue.ID).Scan(&inbox, &external); err != nil {
			t.Fatal(err)
		}
	}
	count()
	if inbox != 1 || external != 0 {
		t.Fatal("independent notifications", inbox, external)
	}
	current, err := repo.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Unpublish(ctx, issue.ID, "general", "en", current.Version, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	event, ok, err := repo.Claim(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatal("unpublish claim", ok, err)
	}
	if err = repo.CompleteUnpublish(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	// The inbox queue is isolated while normal retire/unpublish work remains on its original destination.
	if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET status='delivered' WHERE destination<>'engagement-inbox'`); err != nil {
		t.Fatal(err)
	}
	publish()
	count()
	if inbox != 1 || external != 0 {
		t.Fatal("republish emitted another inbox event", inbox, external)
	}
}

func TestInboxLegacyLiveFirstPlayableOnce(t *testing.T) {
	repo, c, start := captureFixture(t)
	ctx := context.Background()
	repo.WithInboxPublications(start.Add(-time.Hour))
	c, err := repo.PrepareCapture(ctx, c, start)
	if err != nil {
		t.Fatal(err)
	}
	advance := func(last int, liveAge time.Duration) {
		t.Helper()
		_, err = repo.UpdateCapture(ctx, c.IntentID, func(v *recordings.Capture) error {
			now := time.Now().UTC()
			v.Status.State = "uploading"
			v.Status.CaptureID = strings.ReplaceAll(platform.NewID(), "-", "")
			if c.Status.CaptureID != "" {
				v.Status.CaptureID = c.Status.CaptureID
			}
			v.Status.CreatedAt = now.Add(-100 * time.Second)
			advanced := now.Add(-liveAge)
			v.Status.Progress = assetclient.RecordingLiveProgress{Revision: 3, LastSequence: last, MediaEndSeconds: 90, LastAdvancedAt: &advanced}
			v.DeriveLiveState(now)
			c = *v
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='inbox.publication' AND aggregate_id=$1`, c.RecordingID).Scan(&n); err != nil || n != want {
			t.Fatalf("intents=%d want=%d err=%v", n, want, err)
		}
	}
	advance(1, 0)
	count(0)
	advance(2, 2*time.Minute)
	count(0)
	advance(2, 0)
	count(1)
	advance(4, 0)
	count(1)
}

func TestInboxOnlineCompletionSharesPDFEditionMarker(t *testing.T) {
	db := accessDB(t)
	clearBulletinAuditDB(t, db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithInboxPublications(now.Add(-time.Hour))
	t.Cleanup(func() {
		db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, source.DocumentID)
		db.Exec(`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, source.DocumentID)
		db.Exec(`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, source.DocumentID)
		db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: state.Version, Revision: state.Local.Revision, PageCount: len(state.Local.Document.Pages), LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: state.Local.ReviewIssues}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	// Existing Traditional Online publication requires its PDF projection; emulate
	// pre-feature PDF visibility and its same-edition marker without an inbox event.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordInboxPublication(ctx, tx, time.Time{}, "bulletin.published", source.Canonical.IssueID, "general", "zh-Hant", 1, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, `INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,now(),1 FROM hhc_web.bulletin_version WHERE issue_id=$1`} {
		if _, err = db.Exec(query, source.Canonical.IssueID); err != nil {
			t.Fatal(err)
		}
	}
	in := onlinebulletins.PublishInput{CanonicalVersion: 1, OnlineVersion: confirmed.Version, Revision: confirmed.Revision}
	if _, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", in, "admin", now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='inbox.publication' AND aggregate_id=$1`, source.Canonical.IssueID).Scan(&n); err != nil || n != 0 {
		t.Fatal("PDF-to-Online backfilled", n, err)
	}
	// Reuse the confirmed layout under a supported late English edition; its first
	// actual Online visibility gets an independent edition event and exact locale.
	if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,subtitle,pdf_asset_id,pdf_file_name,status,version,created_by,updated_by,created_at,updated_at) SELECT $2,issue_id,series,'en','English',subtitle,'','',status,version,created_by,updated_by,created_at,updated_at FROM hhc_web.bulletin_version WHERE issue_id=$1 AND locale='zh-Hant'`, source.Canonical.IssueID, platform.NewID()); err != nil {
		t.Fatal(err)
	}
	englishDoc := platform.NewID()
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		`INSERT INTO hhc_web.online_bulletin_document SELECT (jsonb_populate_record(NULL::hhc_web.online_bulletin_document,to_jsonb(d)||jsonb_build_object('id',$2::text,'content_locale','en','version',$3::bigint,'base_revision',NULL,'local_revision',NULL,'incoming_revision',NULL,'published_revision',NULL))).* FROM hhc_web.online_bulletin_document d WHERE id=$1`,
		`INSERT INTO hhc_web.online_bulletin_revision SELECT (jsonb_populate_record(NULL::hhc_web.online_bulletin_revision,to_jsonb(v)||jsonb_build_object('document_id',$2::text,'content_locale','en'))).* FROM hhc_web.online_bulletin_revision v WHERE document_id=$1 AND $3::bigint>0`,
		`INSERT INTO hhc_web.online_bulletin_confirmation SELECT (jsonb_populate_record(NULL::hhc_web.online_bulletin_confirmation,to_jsonb(v)||jsonb_build_object('document_id',$2::text))).* FROM hhc_web.online_bulletin_confirmation v WHERE document_id=$1 AND $3::bigint>0`,
		`UPDATE hhc_web.online_bulletin_document SET local_revision=(SELECT local_revision FROM hhc_web.online_bulletin_document WHERE id=$1) WHERE id=$2 AND $3::bigint>0`,
	} {
		if _, err = tx.Exec(query, source.DocumentID, englishDoc, confirmed.Version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO hhc_web.online_bulletin_layout_job(id,document_id,source_revision,canonical_metadata,content_hash,renderer_artifact_sha256,status,attempts,available_at,lease_until,lease_token,last_error_code,result_revision,measurement_json,created_at,updated_at) SELECT $2,$3,source_revision,canonical_metadata,content_hash,renderer_artifact_sha256,status,attempts,available_at,lease_until,lease_token,last_error_code,result_revision,measurement_json,created_at,updated_at FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1 AND status='ready' ORDER BY result_revision DESC LIMIT 1`, source.DocumentID, platform.NewID(), englishDoc); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE hhc_web.bulletin_version SET title=$2 WHERE issue_id=$1 AND locale='en'`, source.Canonical.IssueID, source.Canonical.Metadata.Title); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []string{englishDoc} {
			for _, query := range []string{`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL,local_revision=NULL,incoming_revision=NULL,base_revision=NULL WHERE id=$1`, `DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, `DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, `DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1`, `DELETE FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, `DELETE FROM hhc_web.online_bulletin_document WHERE id=$1`} {
				if _, e := db.Exec(query, id); e != nil {
					t.Error(e)
				}
			}
		}
		db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	if _, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "en", in, "admin", now); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = db.QueryRow(`SELECT payload_json FROM hhc_web.outbox_event WHERE event_type='inbox.publication' AND aggregate_id=$1`, source.Canonical.IssueID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var event publication.InboxPublication
	if err = json.Unmarshal(raw, &event); err != nil || event.Series != "general" || event.Locale != "en" {
		t.Fatal("online edition wire", event, err)
	}
}

func TestInboxMigrationSuppressesAlreadyLiveLegacyCapture(t *testing.T) {
	repo, c, start := captureFixture(t)
	ctx := context.Background()
	c, err := repo.PrepareCapture(ctx, c, start)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	c.Status.CaptureID = strings.ReplaceAll(platform.NewID(), "-", "")
	c.Status.State = "uploading"
	c.Status.CreatedAt = now.Add(-100 * time.Second)
	c.Status.Progress = assetclient.RecordingLiveProgress{Revision: 3, LastSequence: 2, MediaEndSeconds: 90, LastAdvancedAt: &now}
	c.DeriveLiveState(now)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_capture SET capture_id=$2,payload_json=$3 WHERE id=$1`, c.IntentID, c.Status.CaptureID, raw); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../migrations/sql/110_inbox_publication_sources.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(migration)
	body = body[strings.Index(body, "-- Legacy C1"):strings.Index(body, "CREATE INDEX")]
	if _, err = repo.db.Exec(body); err != nil {
		t.Fatal(err)
	}
	var marker time.Time
	if err = repo.db.QueryRow(`SELECT first_visible_at FROM hhc_web.inbox_publication_source WHERE kind='recording.published' AND resource_id=$1`, c.RecordingID).Scan(&marker); err != nil || !marker.Equal(*c.Status.Progress.LastAdvancedAt) {
		t.Fatal("historical evidence", marker, err)
	}
	repo.WithInboxPublications(start.Add(-time.Hour))
	if _, err = repo.UpdateCapture(ctx, c.IntentID, func(*recordings.Capture) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='engagement-inbox' AND aggregate_id=$1`, c.RecordingID).Scan(&n); err != nil || n != 0 {
		t.Fatal("historical live backfilled", n, err)
	}
}

func TestInboxBroadcastFirstReadyArchiveAfterAuthorityApplied(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	repo.WithInboxPublications(now.Add(-time.Hour))
	start, end := 0, 8
	duration := 80.0
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET status='published',uploaded_at=$2,ready_at=$2,expires_at=$3,package_id=$4 WHERE id=$1`, b.View.RecordingID, now, now.Add(time.Hour), strings.Repeat("f", 32)); err != nil {
		t.Fatal(err)
	}
	update := func(pending bool) {
		t.Helper()
		_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(_ context.Context, v *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			v.Epoch = 1
			v.MemberState = "vod"
			v.RangePending = pending
			v.RangeApplied = 3
			v.View.Announced = true
			v.View.Phase = "archived"
			v.View.Boundaries = recordings.BroadcastBoundaries{Revision: 3, StartSequence: &start, EndSequenceExclusive: &end}
			v.Projection = &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 3, State: "ready", StartSequence: &start, EndSequenceExclusive: &end, DurationSeconds: &duration}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='engagement-inbox' AND aggregate_id=$1`, b.View.RecordingID).Scan(&n); err != nil || n != want {
			t.Fatalf("inbox intents=%d want=%d err=%v", n, want, err)
		}
	}
	update(true)
	count(0)
	update(false)
	count(1)
	update(false)
	count(1)
}

func TestInboxMigrationDoesNotSuppressNeverPlayableLegacyCapture(t *testing.T) {
	migration, err := os.ReadFile("../migrations/sql/110_inbox_publication_sources.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(migration)
	body = body[strings.Index(body, "-- Legacy C1"):strings.Index(body, "CREATE INDEX")]
	for _, state := range []string{"recovering", "interrupted", "ending", "ended", "mislabeled_live"} {
		for _, publication := range []string{"live", "vod"} {
			t.Run(state+"/"+publication, func(t *testing.T) {
				repo, c, start := captureFixture(t)
				ctx := context.Background()
				c, err := repo.PrepareCapture(ctx, c, start)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC().Truncate(time.Microsecond)
				advanced := now
				if state == "interrupted" {
					advanced = now.Add(-2 * time.Minute)
				}
				c.Status.CaptureID = strings.ReplaceAll(platform.NewID(), "-", "")
				c.Status.State = "uploading"
				c.Status.CreatedAt = now.Add(-1000 * time.Second)
				c.Status.Progress = assetclient.RecordingLiveProgress{Revision: 3, LastSequence: 2, MediaEndSeconds: 90, LastAdvancedAt: &advanced}
				if state == "ending" || state == "ended" {
					c.Status.StopAcceptedAt = &now
				}
				if state == "ended" {
					c.NormalEnd = true
					c.Status.Progress.Ended = true
					c.Status.Progress.EndedAt = &now
				}
				c.DeriveLiveState(now)
				if c.InboxLiveReady(now) {
					t.Fatal("fixture was ever currently playable")
				}
				if state == "mislabeled_live" {
					c.Status.LiveState = "live"
				} else if c.Status.LiveState != state {
					t.Fatalf("fixture state %s want %s", c.Status.LiveState, state)
				}
				raw, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_capture SET capture_id=$2,payload_json=$3 WHERE id=$1`, c.IntentID, c.Status.CaptureID, raw); err != nil {
					t.Fatal(err)
				}
				if _, err = repo.db.Exec(body); err != nil {
					t.Fatal(err)
				}
				var n int
				if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.inbox_publication_source WHERE kind='recording.published' AND resource_id=$1`, c.RecordingID).Scan(&n); err != nil || n != 0 {
					t.Fatalf("ambiguous history permanently suppressed first visibility: markers=%d err=%v", n, err)
				}
				repo.WithInboxPublications(start.Add(-time.Hour))
				if publication == "live" {
					_, err = repo.UpdateCapture(ctx, c.IntentID, func(v *recordings.Capture) error {
						at := time.Now().UTC()
						v.Status.CreatedAt = at.Add(-100 * time.Second)
						v.Status.StopAcceptedAt = nil
						v.Status.ReplayUntil = nil
						v.NormalEnd = false
						v.Status.Progress = assetclient.RecordingLiveProgress{Revision: 4, LastSequence: 3, MediaEndSeconds: 100, LastAdvancedAt: &at}
						v.DeriveLiveState(at)
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = repo.UpdateCapture(ctx, c.IntentID, func(*recordings.Capture) error { return nil }); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET uploaded_at=$2,ready_at=$2,expires_at=$3,package_id=$4 WHERE id=$1`, c.RecordingID, now, now.Add(time.Hour), strings.Repeat("f", 32)); err != nil {
						t.Fatal(err)
					}
					v, err := repo.Publish(ctx, c.RecordingID, 1, c.ActorID, now)
					if err != nil {
						t.Fatal(err)
					}
					v, err = repo.Unpublish(ctx, c.RecordingID, v.Version, c.ActorID, now)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = repo.Publish(ctx, c.RecordingID, v.Version, c.ActorID, now); err != nil {
						t.Fatal(err)
					}
				}
				if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='engagement-inbox' AND aggregate_id=$1`, c.RecordingID).Scan(&n); err != nil || n != 1 {
					t.Fatalf("first ready publication count=%d err=%v", n, err)
				}
			})
		}
	}
}
