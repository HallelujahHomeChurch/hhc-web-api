package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestPDFUploadDurablyRequestsExtractionFrom1739(t *testing.T) {
	for _, tc := range []struct {
		name           string
		number         int
		series, locale string
		want           int
	}{
		{"before-launch", 1738, "general", "zh-Hant", 0},
		{"first-supported", 1739, "general", "zh-Hant", 1},
		{"current", 1741, "general", "zh-Hant", 1},
		{"simplified-pdf", 1741, "general", "zh-Hans", 0},
		{"english-pdf", 1741, "general", "en", 0},
		{"children-pdf", 1741, "children", "zh-Hant", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			repo, source := seedOnlineLayout(t, db, now)
			repo.WithAutomaticBulletinExtraction(true)
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET issue_number=$2 WHERE id=$1`, source.Canonical.IssueID, tc.number); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
			})
			_, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: tc.series, Locale: tc.locale, Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "replacement.pdf"}, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction'`, source.Canonical.IssueID).Scan(&count); err != nil || count != tc.want {
				t.Fatalf("requests=%d want=%d err=%v", count, tc.want, err)
			}
		})
	}
}

func claimUploadRequest(t *testing.T, repo *Repository, issueID string, now time.Time) publication.Event {
	t.Helper()
	var event publication.Event
	err := repo.db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=attempts+1,claimed_until=$2 WHERE id=(SELECT id FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' AND status='pending' ORDER BY created_at DESC,id DESC LIMIT 1) RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, issueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil)
	if err != nil {
		t.Fatal(err)
	}
	event.EventType, event.AggregateID = "bulletin.online.ensure_extraction", issueID
	return event
}

func TestUploadExtractionRebindsIdenticalPendingBytesAndFencesOldParser(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	ctx := bulletinAuditContext()
	version := int64(1)
	checksum := strings.Repeat("b", 64)
	var first *onlinebulletins.ExtractionJob
	for index, asset := range []string{"first-upload", "identical-reupload", "first-upload"} {
		issue, err := repo.PutVersion(ctx, source.Canonical.IssueID, version, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", PDFAssetID: asset, PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		version = issue.Version
		event := claimUploadRequest(t, repo, source.Canonical.IssueID, now)
		if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, checksum, now); err != nil {
			t.Fatal("identical verified bytes should remain convertible", err)
		}
		job, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(time.Duration(index)*time.Second), time.Minute, 3)
		if err != nil || job == nil || job.SourceAssetID != asset {
			t.Fatal("current source not queued", job, err)
		}
		if index == 0 {
			first = job
		} else if err = repo.CompleteOnlineBulletinExtraction(ctx, *first, extractionCandidate(*first), nil, now.Add(2*time.Second)); err == nil {
			t.Fatal("old parser retained authority after source rebind")
		}
	}
}

func TestUploadExtractionSkipsStaleSourceAndRejectsReclaimedLease(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	ctx := bulletinAuditContext()
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", PDFAssetID: "old-upload", PDFFileName: "weekly.pdf"}
	issue, err := repo.PutVersion(ctx, source.Canonical.IssueID, 1, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	old := claimUploadRequest(t, repo, issue.ID, now)
	input.PDFAssetID = "new-upload"
	if _, err = repo.PutVersion(ctx, issue.ID, issue.Version, input, postgresBulletinAuditActor, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), old, strings.Repeat("b", 64), now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_extraction_job WHERE document_id=$1 AND source_asset_id='old-upload'`, source.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale source queued", count, err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET attempts=attempts+1,claimed_until=$2 WHERE id=$1`, event.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("b", 64), now); !errors.Is(err, publication.ErrStalePublication) {
		t.Fatal("reclaimed lease accepted", err)
	}
}

func TestUploadExtractionRejectsExpiredLeaseAndForgedPayload(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	forged := event
	var payload publication.BulletinExtractionPayload
	_ = json.Unmarshal(event.Payload, &payload)
	payload.RequestID = "another-request"
	forged.Payload, _ = json.Marshal(payload)
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), forged, strings.Repeat("b", 64), now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("forged command accepted", err)
	}
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("b", 64), now.Add(time.Minute)); !errors.Is(err, publication.ErrStalePublication) {
		t.Fatal("expired lease accepted", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_extraction_job WHERE source_asset_id='replacement'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected request changed queue", count, err)
	}
}

func TestUploadExtractionRetainsImmutableEvidenceForIdenticalReupload(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	version := int64(1)
	var originalID string
	var originalSnapshot []byte
	for index, asset := range []string{"first-upload", "identical-reupload", "first-upload"} {
		issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, version, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: asset, PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		version = issue.Version
		event := claimUploadRequest(t, repo, issue.ID, now)
		if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("b", 64), now); err != nil {
			t.Fatal(err)
		}
		job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
		if err != nil || job == nil || job.SourceAssetID != asset {
			t.Fatal("new upload lost its own evidence", job, err)
		}
		if err = repo.CompleteOnlineBulletinExtraction(context.Background(), *job, extractionCandidate(*job), nil, now); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			originalID = job.ID
			if err = db.QueryRow(`SELECT snapshot_json FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&originalSnapshot); err != nil {
				t.Fatal(err)
			}
		} else {
			var after []byte
			if err = db.QueryRow(`SELECT snapshot_json FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, originalID).Scan(&after); err != nil || string(after) != string(originalSnapshot) || originalID == job.ID {
				t.Fatal("immutable source evidence changed", err)
			}
		}
	}
}

func TestUploadExtractionAuditFailureRollsBackQueueAndDelivery(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	repo.WithAudit(NewAuditOutbox(db))
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	_, err = db.Exec(`CREATE FUNCTION hhc_web.reject_test_extraction_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.destination='audit-log' AND NEW.payload_json->>'action'='cms.bulletin.online.extract' THEN RAISE EXCEPTION 'test extraction audit unavailable'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_test_extraction_audit BEFORE INSERT ON hhc_web.outbox_event FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_test_extraction_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER reject_test_extraction_audit ON hhc_web.outbox_event; DROP FUNCTION hhc_web.reject_test_extraction_audit()`)
	})
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("b", 64), now); err == nil {
		t.Fatal("audit failure ignored")
	}
	var count int
	var status string
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_extraction_job WHERE source_asset_id='replacement'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unaudited queue committed", count, err)
	}
	if err = db.QueryRow(`SELECT status FROM hhc_web.outbox_event WHERE id=$1`, event.ID).Scan(&status); err != nil || status != "processing" {
		t.Fatal("failed queue acknowledged", status, err)
	}
}

func TestUploadExtractionFirstDraftAndAuditRollback(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo := New(db).WithAudit(NewAuditOutbox(db)).WithAutomaticBulletinExtraction(true)
	ctx := bulletinAuditContext()
	issue, err := repo.CreateIssue(ctx, 1741, "2026-10-04", postgresBulletinAuditActor, postgresBulletinAuditRequest, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_extraction_job WHERE issue_id=$1`, issue.ID)
		_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_document WHERE issue_id=$1`, issue.ID)
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, issue.ID)
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_version WHERE issue_id=$1`, issue.ID)
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID)
	})
	input := bulletinVersionInput()
	input.Locale = "zh-Hant"
	if _, err = repo.PutVersion(context.Background(), issue.ID, issue.Version, input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("untrusted upload should roll back", err)
	}
	issue, err = repo.PutVersion(ctx, issue.ID, issue.Version, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("c", 64), now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, issue.ID, "general", "zh-Hant")
	if err != nil || state.Extraction == nil || state.Extraction.Status != "queued" || state.Local != nil || state.PublishedRevision != nil {
		t.Fatal("first conversion was lost or auto published", state, err)
	}
	var audits int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'action'='cms.bulletin.online.extract'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("original actor extraction audit not persisted", audits, err)
	}
}

func TestUploadRequestWaitingAndFailureAreVisibleBeforeParser(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pending", "failed"} {
		if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET status=$2,last_error='upload_source_unavailable' WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction'`, issue.ID, status); err != nil {
			t.Fatal(err)
		}
		state, err := repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
		want := "queued"
		if status == "failed" {
			want = "failed"
		}
		if err != nil || state.Extraction == nil || state.Extraction.Status != want || state.Extraction.ErrorCode != "upload_source_unavailable" || state.Local == nil || state.Local.Revision != 1 {
			t.Fatal("background source status invisible", state.Extraction, err)
		}
	}
}

func TestPDFUploadCompatibilityReleaseDoesNotProduceRequests(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	_, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction'`, source.Canonical.IssueID).Scan(&count); err != nil || count != 0 {
		t.Fatal("compatibility release produced unsupported rollback work", count, err)
	}
}

func TestUploadPreParserFailureCanBeRecoveredManually(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='upload_source_unavailable' WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction'`, issue.ID); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), issue.ID, onlinebulletins.QueueExtractionInput{CanonicalVersion: issue.Version, OnlineVersion: state.Version, SourceAssetID: input.PDFAssetID, SourceChecksum: strings.Repeat("b", 64), Retry: true}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal("pre-parser failure could not recover", err)
	}
	state, err = repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil || state.Extraction == nil || state.Extraction.Status != "queued" || state.Extraction.ErrorCode != "" {
		t.Fatal("recovered job shadowed by old failure", state.Extraction, err)
	}
}

func TestUploadStateIgnoresOlderPendingIntentAfterLatestDelivery(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	version := int64(1)
	for index := range 2 {
		issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, version, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		version = issue.Version
	}
	event := claimUploadRequest(t, repo, source.Canonical.IssueID, now)
	if err := repo.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("b", 64), now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Extraction == nil || state.Extraction.ID == event.ID || state.Extraction.ErrorCode != "" {
		t.Fatal("older intent shadowed latest parser", state.Extraction, err)
	}
}

func TestUploadFailedIntentDoesNotShadowRetriedParser(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	checksum := strings.Repeat("b", 64)
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, checksum, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if err = repo.FailOnlineBulletinExtraction(context.Background(), *job, "source_unavailable", now, 1); err != nil {
		t.Fatal(err)
	}
	issue, err = repo.PutVersion(bulletinAuditContext(), issue.ID, issue.Version, input, postgresBulletinAuditActor, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='upload_request_failed' WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' AND status='pending'`, issue.ID); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), issue.ID, onlinebulletins.QueueExtractionInput{CanonicalVersion: issue.Version, OnlineVersion: state.Version, SourceAssetID: input.PDFAssetID, SourceChecksum: checksum, Retry: true}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil || state.Extraction == nil || state.Extraction.Status != "queued" || state.Extraction.ErrorCode != "" {
		t.Fatal("failed intent shadows retried parser", state.Extraction, err)
	}
	job, err = repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if err = repo.CompleteOnlineBulletinExtraction(context.Background(), *job, extractionCandidate(*job), nil, now); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil || state.Extraction == nil || state.Extraction.Status != "review_ready" {
		t.Fatal("failed intent shadows ready parser", state.Extraction, err)
	}
}

func TestUploadFailedIntentCanReuseCompletedParserWithoutChangingSnapshot(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	checksum := strings.Repeat("b", 64)
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, checksum, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if err = repo.CompleteOnlineBulletinExtraction(context.Background(), *job, extractionCandidate(*job), nil, now); err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	if err = db.QueryRow(`SELECT snapshot_json FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	issue, err = repo.PutVersion(bulletinAuditContext(), issue.ID, issue.Version, input, postgresBulletinAuditActor, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='upload_request_failed' WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' AND status='pending'`, issue.ID); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), issue.ID, onlinebulletins.QueueExtractionInput{CanonicalVersion: issue.Version, OnlineVersion: state.Version, SourceAssetID: input.PDFAssetID, SourceChecksum: checksum, Retry: true}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal("failed intent cannot recover cached parser", err)
	}
	state, err = repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil || state.Extraction == nil || state.Extraction.Status != "review_ready" || state.Extraction.ID != job.ID {
		t.Fatal("cached result not recovered", state.Extraction, err)
	}
	var after []byte
	if err = db.QueryRow(`SELECT snapshot_json FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&after); err != nil || string(after) != string(snapshot) {
		t.Fatal("cached snapshot mutated", err)
	}
}

func TestUploadRequestRescheduleFencesABAAndDeliveredCommands(t *testing.T) {
	db := accessDB(t)
	now := time.Unix(1791590400, 123456789).UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", PDFAssetID: "replacement", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	store, ok := any(repo).(interface {
		RescheduleOnlineExtractionRequest(context.Context, publication.Event, string, time.Time, time.Time, bool) error
	})
	if !ok {
		t.Fatal("upload errors have no lease-fenced rescheduler")
	}
	old := claimUploadRequest(t, repo, issue.ID, now)
	// PostgreSQL timestamps retain microseconds, including on nanosecond clocks.
	currentLease := now.Add(2 * time.Minute).Truncate(time.Microsecond)
	if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET claimed_until=$2 WHERE id=$1`, old.ID, currentLease); err != nil {
		t.Fatal(err)
	}
	for _, next := range []time.Time{time.Time{}, now.Add(time.Second)} {
		if err = store.RescheduleOnlineExtractionRequest(context.Background(), old, "upload_request_failed", next, now, true); err != nil {
			t.Fatal(err)
		}
		var status string
		var lease time.Time
		if err = db.QueryRow(`SELECT status,claimed_until FROM hhc_web.outbox_event WHERE id=$1`, old.ID).Scan(&status, &lease); err != nil || status != "processing" || !lease.Equal(currentLease) {
			t.Fatal("old claimant canceled new lease", status, err)
		}
	}
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), old, strings.Repeat("b", 64), now); !errors.Is(err, publication.ErrStalePublication) {
		t.Fatal("ABA lease accepted", err)
	}
	current := old
	current.ClaimedUntil = currentLease
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), current, strings.Repeat("b", 64), now); err != nil {
		t.Fatal(err)
	}
	if err = store.RescheduleOnlineExtractionRequest(context.Background(), current, "upload_request_failed", time.Time{}, now, false); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.QueryRow(`SELECT status FROM hhc_web.outbox_event WHERE id=$1`, current.ID).Scan(&status); err != nil || status != "delivered" {
		t.Fatal("delivered event reopened", status, err)
	}
}

func TestUploadExtractionRequestPreservesLocalAndHandlesReplacement(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	ctx := bulletinAuditContext()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	var before []byte
	if err := db.QueryRow(`SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=1`, source.DocumentID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "replacement.pdf"}
	issue, err := repo.PutVersion(ctx, source.Canonical.IssueID, 1, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	var event publication.Event
	err = db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, source.Canonical.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil)
	if err != nil {
		t.Fatal(err)
	}
	event.EventType, event.AggregateID = "bulletin.online.ensure_extraction", source.Canonical.IssueID
	consumer, ok := any(repo).(interface {
		CompleteOnlineExtractionRequest(context.Context, publication.Event, string, time.Time) error
	})
	if !ok {
		t.Fatal("durable upload request has no consumer")
	}
	for range 2 {
		if err = consumer.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("b", 64), now); err != nil {
			t.Fatal(err)
		}
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local == nil || state.Local.Revision != 1 || state.IncomingRevision != nil || state.PublishedRevision != nil {
		t.Fatal("upload overwrote content", err)
	}
	var after []byte
	if err = db.QueryRow(`SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=1`, source.DocumentID).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatal("authored revision changed", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_extraction_job WHERE document_id=$1 AND source_asset_checksum=$2 AND NOT superseded`, source.DocumentID, strings.Repeat("b", 64)).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate extraction", count, err)
	}
	input.PDFAssetID = "newer-source"
	if _, err = repo.PutVersion(ctx, source.Canonical.IssueID, issue.Version, input, postgresBulletinAuditActor, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// A retry of the old source must not queue it over the newer upload.
	var payload publication.BulletinExtractionPayload
	_ = json.Unmarshal(event.Payload, &payload)
	if payload.AssetID != "replacement" {
		t.Fatal("wrong immutable source")
	}
	if err = consumer.CompleteOnlineExtractionRequest(context.Background(), event, strings.Repeat("b", 64), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
}
