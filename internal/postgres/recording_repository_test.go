package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"strings"
	"sync"
	"testing"
	"time"
)

func recordingCleanup(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := db.Exec(`UPDATE hhc_web.member_recording SET primary_asset_version_id=NULL WHERE id=$1`, id); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(`DELETE FROM hhc_web.member_recording_file WHERE recording_id=$1`, id); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(`DELETE FROM hhc_web.member_recording WHERE id=$1`, id); err != nil {
			t.Error(err)
		}
	})
}

func TestRecordingCreateAndAuditCommitTogether(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	now := time.Now().UTC()
	actor, key := platform.NewID(), platform.NewID()
	if _, err := repo.Create(context.Background(), "主日聚會", actor, key, now); !errors.Is(err, recordings.ErrAuditUnavailable) {
		t.Fatalf("missing provenance: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording WHERE idempotency_key=$1`, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("committed without audit: %d %v", count, err)
	}
	ctx := auditclient.WithProvenance(context.Background(), actor, "request-"+key)
	created, err := repo.Create(ctx, "主日聚會", actor, key, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, created.ID)
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'action'='cms.recording.create' AND payload_json->>'resourceId'=$1`, created.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing audit: %d %v", count, err)
	}
}

func TestRecordingDraftQuotaExcludesBroadcasts(t *testing.T) {
	repo, _, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	b, err := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Second scheduled broadcast"}, actor.ActorID, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, b.View.RecordingID)

	key := platform.NewID()
	first, err := repo.Create(ctx, "CLI upload", actor.ActorID, key, now)
	if err != nil {
		t.Fatalf("broadcast drafts blocked ordinary recording: %v", err)
	}
	recordingCleanup(t, repo.db, first.ID)
	second, err := repo.Create(ctx, "Second upload", actor.ActorID, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, second.ID)
	if _, err := repo.Create(ctx, "Third upload", actor.ActorID, platform.NewID(), now); !errors.Is(err, recordings.ErrDraftLimit) || !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("ordinary draft quota not enforced: %v", err)
	}
	if _, err := repo.Create(ctx, "Changed title", actor.ActorID, key, now); !errors.Is(err, recordings.ErrConflict) || errors.Is(err, recordings.ErrDraftLimit) {
		t.Fatalf("idempotency mismatch must remain a generic conflict: %v", err)
	}
	replay, err := repo.Create(ctx, "CLI upload", actor.ActorID, key, now)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("idempotent replay at quota: %+v %v", replay, err)
	}
}

func TestRecordingServiceCreateAndAuditCommitTogether(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actorID, key := platform.NewID(), platform.NewID()
	ctx := auditclient.WithPrincipalProvenance(context.Background(), "service", actorID, "service-"+key)
	value, err := repo.Create(ctx, "Service gathering", actorID, key, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	var kind, id string
	if err := db.QueryRow(`SELECT payload_json->>'actorType',payload_json->>'actorId' FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'action'='cms.recording.create' AND payload_json->>'resourceId'=$1`, value.ID).Scan(&kind, &id); err != nil || kind != "service" || id != actorID {
		t.Fatalf("service audit lost: %s %s %v", kind, id, err)
	}
	// Missing durable audit cannot leave a service-owned business row behind.
	untracked := NewRecordingRepository(db)
	otherKey := platform.NewID()
	if _, err := untracked.Create(ctx, "Unaudited service gathering", actorID, otherKey, time.Now().UTC()); !errors.Is(err, recordings.ErrAuditUnavailable) {
		t.Fatalf("unaudited create: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording WHERE idempotency_key=$1`, otherKey).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unaudited service row committed: %d %v", count, err)
	}
}

func TestRecordingPublishRollsBackWhenNotificationCannotQueue(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	actor, key := platform.NewID(), platform.NewID()
	value, err := repo.Create(ctx, "Rollback recording", actor, key, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	if err := repo.AddPackage(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now); err != nil {
		t.Fatal(err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now)
	if err != nil {
		t.Fatal(err)
	}
	// Generated UUID is safe as a literal; affect only this recording's outbox.
	if _, err := db.Exec(`ALTER TABLE hhc_web.outbox_event ADD CONSTRAINT test_recording_queue_failure CHECK(aggregate_id::text <> '` + value.ID + `') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT test_recording_queue_failure`); err != nil {
			t.Error(err)
		}
	})
	if _, err := repo.Publish(ctx, value.ID, value.Version, actor, now); err == nil {
		t.Fatal("publish succeeded without notification outbox")
	}
	operationCtx := auditclient.WithProvenance(ctx, actor, "publish-"+key)
	if _, err := repo.PublishOperation(operationCtx, value.ID, value.Version, actor, key, now); err == nil {
		t.Fatal("receipt publish succeeded without notification outbox")
	}
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_publish_receipt WHERE recording_id=$1`, value.ID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipt without publication: %d %v", receipts, err)
	}
	stored, err := repo.Get(ctx, value.ID)
	if err != nil || stored.Status != "draft" || stored.Version != value.Version {
		t.Fatalf("partial commit: %+v %v", stored, err)
	}
}

func TestRecordingSinglePackageAndFixedReadyDeadline(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	actor, key := platform.NewID(), platform.NewID()
	value, err := repo.Create(ctx, "主日聚會", actor, key, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	if value.UploadedAt != nil || value.ExpiresAt != nil || value.Status != "draft" || !value.DraftExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("draft=%+v", value)
	}
	replay, err := repo.Create(ctx, "主日聚會", actor, key, now)
	if err != nil || replay.ID != value.ID {
		t.Fatalf("replay: %v", err)
	}
	if _, err := repo.Create(ctx, "different", actor, key, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("key reuse: %v", err)
	}
	if err := repo.AddPackage(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddPackage(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now); err != nil {
		t.Fatalf("attachment replay: %v", err)
	}
	if err := repo.AddPackage(ctx, value.ID, strings.Repeat("b", 32), now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("replacement accepted: %v", err)
	}
	if _, err := repo.Publish(ctx, value.ID, value.Version, actor, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("incomplete published: %v", err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now)
	if err != nil {
		t.Fatal(err)
	}
	if value.Version != 1 || !value.ExpiresAt.Equal(time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("completion: %+v", value)
	}
	if _, err := repo.RecordPackageReady(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now.Add(time.Hour)); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("timestamp changed: %v", err)
	}
	value, err = repo.Publish(ctx, value.ID, value.Version, actor, now)
	if err != nil || !value.Visible(now) {
		t.Fatalf("publish: %+v %v", value, err)
	}
	if _, err := repo.UpdateTitle(ctx, value.ID, value.Version, "changed", actor, now, false, nil); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("write-only published edit: %v", err)
	}
	value, err = repo.UpdateTitle(ctx, value.ID, value.Version, "changed", actor, now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := repo.Create(ctx, "主日聚會", actor, key, now); err != nil || replay.ID != value.ID || replay.Title != "changed" {
		t.Fatalf("create replay after title edit: %+v %v", replay, err)
	}
	if _, err := repo.Unpublish(ctx, value.ID, 1, actor, now); !errors.Is(err, recordings.ErrPrecondition) {
		t.Fatalf("stale version: %v", err)
	}
	value, err = repo.Unpublish(ctx, value.ID, value.Version, actor, now)
	if err != nil || value.Status != "draft" {
		t.Fatalf("unpublish: %v", err)
	}
	value, err = repo.Publish(ctx, value.ID, value.Version, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now)
	if err != nil || !value.ExpiresAt.Equal(now.Add(720*time.Hour)) || value.Title != "changed" {
		t.Fatalf("replay changed metadata: %+v %v", value, err)
	}
	var notificationCount int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='recording.notification.submit'`, value.ID).Scan(&notificationCount); err != nil || notificationCount != 1 {
		t.Fatalf("first publication notification count=%d err=%v", notificationCount, err)
	}
	if err := repo.Expire(ctx, value.ID, *value.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	value, err = repo.Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Publish(ctx, value.ID, value.Version, actor, *value.ExpiresAt); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("published at deadline: %v", err)
	}
	if err := repo.AddPackage(ctx, value.ID, strings.Repeat("c", 32), now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("replacement after deletion: %v", err)
	}
}

func TestRecordingCompletionAndAutosaveDoNotOverwriteEachOther(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	key := platform.NewID()
	value, err := repo.Create(ctx, "before", key, key, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	if err := repo.AddPackage(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, err := repo.RecordPackageReady(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := repo.UpdateTitle(ctx, value.ID, value.Version, "after", key, now, false, nil)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	value, err = repo.Get(ctx, value.ID)
	if err != nil || value.Title != "after" || value.Version != 2 || value.UploadedAt == nil || !value.UploadedAt.Equal(now) {
		t.Fatalf("lost update: %+v %v", value, err)
	}
}

func TestRecordingPublishOperationCannotRepublishAfterUnpublish(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	now := time.Now().UTC().Truncate(time.Microsecond)
	actor, key := platform.NewID(), platform.NewID()
	ctx := auditclient.WithPrincipalProvenance(context.Background(), "service", actor, "publish-"+key)
	value, err := repo.Create(ctx, "Agent recording", actor, key, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	if err := repo.AddPackage(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now); err != nil {
		t.Fatal(err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.PublishOperation(ctx, value.ID, value.Version, actor, key, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != "published" || first.Current.Status != "published" || first.Receipt.PublishedVersion != value.Version+1 {
		t.Fatalf("first: %+v", first)
	}
	concurrent := make(chan error, 2)
	for range 2 {
		go func() {
			result, err := repo.PublishOperation(ctx, value.ID, value.Version, actor, key, now)
			if err == nil && (result.Receipt != first.Receipt || result.Outcome != "published") {
				err = errors.New("concurrent replay changed receipt")
			}
			concurrent <- err
		}()
	}
	for range 2 {
		if err := <-concurrent; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.Unpublish(ctx, value.ID, first.Current.Version, actor, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	replay, err := repo.PublishOperation(ctx, value.ID, value.Version, actor, key, now.Add(2*time.Minute))
	if err != nil || replay.Receipt != first.Receipt || replay.Outcome != "state_changed" || replay.Current.Status != "draft" || replay.Current.Version != first.Current.Version+1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err := repo.PublishOperation(ctx, value.ID, replay.Current.Version, actor, key, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("changed payload: %v", err)
	}
	if _, err := repo.GetPublishOperation(ctx, platform.NewID(), value.Version, actor, key, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("cross-recording key reuse: %v", err)
	}
	if _, err := repo.GetPublishOperation(auditclient.WithProvenance(ctx, actor, "human-replay"), value.ID, value.Version, actor, key, now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("actor type not bound: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='recording.notification.submit'`, value.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("notification: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'action'='cms.recording.publish' AND payload_json->>'resourceId'=$1`, value.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit: %d %v", count, err)
	}
}

func TestRecordingReceiptFailureRollsBackPublicationAuditAndNotification(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	now := time.Now().UTC()
	actor, key := platform.NewID(), platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "publish-"+key)
	value, err := repo.Create(ctx, "Receipt rollback", actor, key, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	if err := repo.AddPackage(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now); err != nil {
		t.Fatal(err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, strings.ReplaceAll(key, "-", ""), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE hhc_web.member_recording_publish_receipt ADD CONSTRAINT test_recording_receipt_failure CHECK(recording_id::text <> '` + value.ID + `') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`ALTER TABLE hhc_web.member_recording_publish_receipt DROP CONSTRAINT test_recording_receipt_failure`); err != nil {
			t.Error(err)
		}
	})
	if _, err := repo.PublishOperation(ctx, value.ID, value.Version, actor, key, now); err == nil {
		t.Fatal("publication succeeded without receipt")
	}
	stored, err := repo.Get(ctx, value.ID)
	if err != nil || stored.Status != "draft" || stored.Version != value.Version {
		t.Fatalf("partial publication: %+v %v", stored, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE (aggregate_id::text=$1 AND event_type='recording.notification.submit') OR (destination='audit-log' AND payload_json->>'action'='cms.recording.publish' AND payload_json->>'resourceId'=$1)`, value.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial outbox: %d %v", count, err)
	}
}
