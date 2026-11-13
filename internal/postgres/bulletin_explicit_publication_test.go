package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"strings"
	"sync"
	"testing"
	"time"
)

func prepareRecoverableBulletin(t *testing.T) (*Repository, onlinebulletins.ExtractionJob, onlinebulletins.AdminState, time.Time) {
	t.Helper()
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	repo.WithExplicitOnlinePublication(true)
	job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	proof := verifiedUpdateForTest(t, job.Input)
	for _, a := range proof.Attempts {
		if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, a, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CompleteOnlineBulletinUpdate(ctx, *job, proof, now); err != nil {
		t.Fatal(err)
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || layout == nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE hhc_web.bulletin_version SET status='published',auto_publish=false,auto_notify=true WHERE issue_id=$1`, `INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,now(),1 FROM hhc_web.bulletin_version WHERE issue_id=$1`} {
		if _, err = db.Exec(q, source.Canonical.IssueID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1`, `DELETE FROM hhc_web.online_bulletin_publication WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, `DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, `DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, `DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`} {
			_, _ = db.Exec(q, source.Canonical.IssueID)
		}
	})
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	return repo, source, state, now
}
func TestExplicitPDFOnlyCancelsBeforeIndependentWorkflow(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	repo.WithExplicitOnlinePublication(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
	})
	state, err := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	service := bulletins.NewService(repo, func() time.Time { return now })
	workflow, err := service.PublishSelection(bulletinAuditContext(), source.Canonical.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &state.Version}, state.CanonicalVersion, postgresBulletinAuditActor)
	if err != nil {
		t.Fatal("explicit PDF-only unavailable", err)
	}
	var enabled bool
	var status string
	if err = db.QueryRow(`SELECT v.auto_publish,w.status FROM hhc_web.bulletin_version v JOIN hhc_web.publication_workflow w ON w.id=$2 WHERE v.issue_id=$1`, source.Canonical.IssueID, workflow.ID).Scan(&enabled, &status); err != nil || enabled || status != "waiting_asset_scan" {
		t.Fatal("new independent workflow cancelled or online authority retained", enabled, status, err)
	}
	if workflow.OnlinePublication == nil || workflow.OnlinePublication.Outcome != "cancelled" {
		t.Fatal("missing cancellation outcome", workflow)
	}
}
func TestQualifiedRecoveryPublishesOnceWithoutReprocessingOrNotification(t *testing.T) {
	repo, source, state, now := prepareRecoverableBulletin(t)
	ctx := bulletinAuditContext()
	db := repo.db
	input := onlinebulletins.RecoveryInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash}
	service := bulletins.NewService(repo, func() time.Time { return now })
	counts := func() (int, int, int) {
		t.Helper()
		var e, l, n int
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.online_bulletin_extraction_job WHERE document_id=$1),(SELECT count(*) FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1),(SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$2 AND event_type='bulletin.notification.submit')`, state.DocumentID, source.Canonical.IssueID).Scan(&e, &l, &n); err != nil {
			t.Fatal(err)
		}
		return e, l, n
	}
	e, l, n := counts()
	result, err := service.RecoverOnlinePublication(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, "recover-"+source.Canonical.IssueID, postgresBulletinAuditActor)
	if err != nil {
		t.Fatal("qualified existing draft rejected", err)
	}
	if result.Replayed || result.Receipt.ID == "" || result.Publication.Revision != input.Revision {
		t.Fatal("invalid receipt", result)
	}
	if e2, l2, n2 := counts(); e2 != e || l2 != l || n2 != n {
		t.Fatal("recovery created work or notification", e2, l2, n2)
	}
	after, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UnpublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", after.CanonicalVersion, after.Version, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	replay, err := service.RecoverOnlinePublication(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, "recover-"+source.Canonical.IssueID, postgresBulletinAuditActor)
	if err != nil || !replay.Replayed || replay.Receipt.ID != result.Receipt.ID || replay.Publication != result.Publication {
		t.Fatal("immutable receipt replay failed", replay, err)
	}
	after, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || after.PublishedRevision != nil {
		t.Fatal("receipt replay republished withdrawn draft", err)
	}
	changed := input
	changed.LayoutValidationHash = strings.Repeat("b", 64)
	if _, err = service.RecoverOnlinePublication(ctx, source.Canonical.IssueID, "general", "zh-Hant", changed, "recover-"+source.Canonical.IssueID, postgresBulletinAuditActor); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("changed key payload accepted", err)
	}
	var actor, request string
	if err = db.QueryRow(`SELECT authorizer_id::text,authorizer_request_id FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, source.ID).Scan(&actor, &request); err != nil || actor != postgresBulletinAuditActor || request != "autopilot-test" {
		t.Fatal("original authorizer overwritten", actor, request, err)
	}
}
func TestRecoveryRejectsUnqualifiedOrStaleDraft(t *testing.T) {
	for _, scenario := range []string{"hash", "source", "metadata", "review", "stale", "snapshot-hash", "snapshot-source"} {
		t.Run(scenario, func(t *testing.T) {
			repo, source, state, now := prepareRecoverableBulletin(t)
			input := onlinebulletins.RecoveryInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash}
			var q string
			switch scenario {
			case "snapshot-hash", "snapshot-source":
				var saved onlineExtractionSnapshot
				var raw []byte
				var savedHash string
				if err := repo.db.QueryRow(`SELECT snapshot_json,snapshot_sha256 FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, source.ID).Scan(&raw, &savedHash); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &saved); err != nil {
					t.Fatal(err)
				}
				if scenario == "snapshot-source" {
					saved.Candidate.Evidence.SourceSHA256 = strings.Repeat("b", 64)
					raw = mustJSON(saved)
					sum := sha256.Sum256(raw)
					savedHash = hex.EncodeToString(sum[:])
				} else {
					savedHash = strings.Repeat("b", 64)
				}
				// Seed a distinct historical row rather than disabling immutable evidence.
				// This models malformed stored/imported proof at its creation boundary.
				clone := platform.NewID()
				if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET superseded=true WHERE id=$1`, source.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.db.Exec(`INSERT INTO hhc_web.online_bulletin_extraction_job SELECT (jsonb_populate_record(NULL::hhc_web.online_bulletin_extraction_job,to_jsonb(e)||jsonb_build_object('id',$2::text,'snapshot_json',$3::jsonb,'snapshot_sha256',$4::text,'superseded',false))).* FROM hhc_web.online_bulletin_extraction_job e WHERE id=$1`, source.ID, clone, raw, savedHash); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_update_job SET extraction_job_id=$2 WHERE extraction_job_id=$1`, source.ID, clone); err != nil {
					t.Fatal(err)
				}

			case "hash":
				input.LayoutValidationHash = strings.Repeat("b", 64)
			case "source":
				q = `UPDATE hhc_web.bulletin_version SET pdf_sha256=repeat('b',64) WHERE issue_id=$1`
			case "metadata":
				q = `UPDATE hhc_web.bulletin_version SET title='new' WHERE issue_id=$1`
			case "review":
				q = `UPDATE hhc_web.online_bulletin_document SET incoming_revision=local_revision WHERE issue_id=$1`
			case "stale":
				input.OnlineVersion++
			}
			if q != "" {
				if _, err := repo.db.Exec(q, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			_, err := bulletins.NewService(repo, func() time.Time { return now }).RecoverOnlinePublication(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", input, "blocked-"+source.Canonical.IssueID, postgresBulletinAuditActor)
			var blocked *onlinebulletins.RecoveryBlockedError
			if !errors.As(err, &blocked) && !errors.Is(err, bulletins.ErrPrecondition) {
				t.Fatal("missing structured blocker/CAS failure", err)
			}
		})
	}
}

func TestPDFOnlyCancelsLeasedAutomaticPDFBeforeNewWorkflow(t *testing.T) {
	repo, source, state, now := prepareRecoverableBulletin(t)
	db := repo.db
	ctx := bulletinAuditContext()
	if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET status='draft',auto_publish=true WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if changed, err := repo.AutoPublishNextRecognition(context.Background(), now); err != nil || !changed {
		t.Fatal(changed, err)
	}
	var old publication.Event
	var oldWorkflow string
	if err := db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until,payload_json->>'workflowId'`, source.Canonical.IssueID, now.Add(time.Minute)).Scan(&old.ID, &old.Payload, &old.AggregateVersion, &old.Attempts, &old.ClaimedUntil, &oldWorkflow); err != nil {
		t.Fatal(err)
	}
	old.EventType, old.AggregateID = "bulletin.publish.ensure_asset", source.Canonical.IssueID
	current, err := repo.OnlineBulletinState(ctx, state.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	next, err := repo.StartPublishSelection(ctx, state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &current.Version}, current.CanonicalVersion, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CompletePublish(context.Background(), old, "", "", now); !errors.Is(err, publication.ErrStalePublication) {
		t.Fatal("old automatic publication survived cancellation", err)
	}
	if err = repo.FailPublish(context.Background(), old, source.SourceAssetID, "", "late", now); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = db.QueryRow(`SELECT (SELECT status FROM hhc_web.publication_workflow WHERE id=$1),(SELECT status FROM hhc_web.publication_workflow WHERE id=$2)`, oldWorkflow, next.ID).Scan(&before, &after); err != nil || before != "cancelled" || after != "waiting_asset_scan" {
		t.Fatal("new independent workflow affected", before, after, err)
	}
	var authorized int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_intent_transition t JOIN hhc_web.online_bulletin_publication_intent h ON h.id=t.intent_id WHERE h.issue_id=$1 AND t.status='authorized'`, state.IssueID).Scan(&authorized); err != nil || authorized != 0 {
		t.Fatal("legacy cancellation invented authorization", authorized, err)
	}
}

func TestDeletingBeforeExtractionClosesKnownIntent(t *testing.T) {
	for _, wholeIssue := range []bool{false, true} {
		t.Run(map[bool]string{false: "edition", true: "issue"}[wholeIssue], func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			ctx := bulletinAuditContext()
			repo := New(db).WithAudit(NewAuditOutbox(db)).WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true).WithExplicitOnlinePublication(true)
			issue, err := repo.CreateIssue(ctx, 1234567, "2099-12-30", postgresBulletinAuditActor, platform.NewID(), now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID) })
			issue, err = repo.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "Synthetic", PDFAssetID: "synthetic", PDFFileName: "weekly.pdf", AutoPublish: true}, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			if wholeIssue {
				err = repo.DeleteIssue(ctx, issue.ID, issue.Version, postgresBulletinAuditActor, now)
			} else {
				issue, err = repo.DeleteVersion(ctx, issue.ID, "general", "zh-Hant", issue.Version, postgresBulletinAuditActor, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			var status string
			if err = db.QueryRow(`SELECT t.status FROM hhc_web.online_bulletin_publication_intent h JOIN hhc_web.online_bulletin_intent_transition t ON t.intent_id=h.id WHERE h.issue_id=$1 ORDER BY h.epoch DESC,t.sequence DESC LIMIT 1`, issue.ID).Scan(&status); err != nil || status != "cancelled" {
				t.Fatal("deleted source retained active grant", status, err)
			}
			if !wholeIssue {
				issue, err = repo.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "Replacement", PDFAssetID: "new-synthetic", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
				if err != nil {
					t.Fatal(err)
				}
				state, err := repo.OnlineBulletinState(ctx, issue.ID, "general", "zh-Hant")
				if err != nil || state.Automation.AutoPublish || state.Automation.Disposition == nil || state.Automation.Disposition.Status != "cancelled" {
					t.Fatal("disabled recreation revived old intent", state.Automation, err)
				}
			}
		})
	}
}

func TestFailedIntentIsTerminalEvenWhenLegacyFlagRemainsTrue(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	repo.WithAutomaticBulletinExtraction(true).WithExplicitOnlinePublication(true)
	ctx := auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, "failed-intent")
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
	})
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.QueueOnlineBulletinExtraction(ctx, state.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum, AutoPublish: true}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = repo.FailOnlineBulletinExtraction(context.Background(), *job, "invalid_source", now, 1); err != nil {
		t.Fatal(err)
	}
	before := publicationInterlockSnapshot(t, repo, state.IssueID)
	if _, err = repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic"}, state.CanonicalVersion, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("failed plan incorrectly accepted as healthy early PDF", err)
	}
	if _, err = repo.StartPublish(bulletinAuditContext(), state.IssueID, "general", "zh-Hant", state.CanonicalVersion, false, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAutomaticPublicationActive) {
		t.Fatal("ordinary request bypassed retained authority", err)
	}
	if after := publicationInterlockSnapshot(t, repo, state.IssueID); after != before {
		t.Fatal("blocked requests altered failed intent")
	}
	current, err := repo.OnlineBulletinState(ctx, state.IssueID, "general", "zh-Hant")
	if err != nil || current.Automation.Disposition == nil || current.Automation.Disposition.Status != "failed" || !current.Automation.AutoPublish {
		t.Fatal("failed disposition or durable authority changed", err)
	}
}

func TestExplicitPDFOnlyNoDocumentCASAndUnknownHistory(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := bulletinAuditContext()
	repo := New(db).WithAudit(NewAuditOutbox(db)).WithExplicitOnlinePublication(true)
	issue, err := repo.CreateIssue(ctx, 1234568, "2099-12-30", postgresBulletinAuditActor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, issue.ID)
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID)
	})
	issue, err = repo.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "Synthetic", PDFAssetID: "synthetic", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	stale := int64(1)
	input := bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &stale}
	if _, err = repo.StartPublishSelection(ctx, issue.ID, input, issue.Version, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("missing document accepted onlineVersion1", err)
	}
	zero := int64(0)
	input.OnlineVersion = &zero
	workflow, err := repo.StartPublishSelection(ctx, issue.ID, input, issue.Version, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.OnlinePublication == nil || workflow.OnlinePublication.Outcome != "absent" || workflow.OnlinePublication.Disposition != nil {
		t.Fatal("legacyfalse invented cancellation", workflow.OnlinePublication)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication_intent WHERE issue_id=$1`, issue.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("unknown history synthesized", count, err)
	}
}

func TestNewEpochNeverRewritesOriginalAuthorizer(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	seedOnlineExtraction(t, db, now)
	var issue string
	if err := db.QueryRow(`SELECT id::text FROM hhc_web.bulletin_issue WHERE issue_number=987654`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	repo := New(db).WithAudit(NewAuditOutbox(db)).WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true).WithExplicitOnlinePublication(true)
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 1, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64), AutoPublish: true}
	firstCtx := auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, "epoch-first")
	first, err := repo.QueueOnlineBulletinExtraction(firstCtx, issue, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || claimed == nil || claimed.ID != first.Job.ID {
		t.Fatal(err)
	}
	input.OnlineVersion = first.Version
	second, err := repo.QueueOnlineBulletinExtraction(auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, "epoch-second"), issue, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if second.Job.ID == first.Job.ID {
		t.Fatal("new authority reused old leased identity")
	}
	var request string
	var superseded bool
	var epoch int64
	if err = db.QueryRow(`SELECT authorizer_request_id,superseded FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, first.Job.ID).Scan(&request, &superseded); err != nil || request != "epoch-first" || !superseded {
		t.Fatal("original authorizer/lease overwritten", request, superseded, err)
	}
	if err = db.QueryRow(`SELECT publication_intent_epoch FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, second.Job.ID).Scan(&epoch); err != nil || epoch != 2 {
		t.Fatal("epoch failed to advance", epoch, err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_intent_transition SET reason_code='manual_state_change' WHERE intent_id=(SELECT online_intent_id FROM hhc_web.bulletin_version WHERE issue_id=$1)`, issue); err == nil {
		t.Fatal("disposition history mutable")
	}
}

func TestRecoveryConcurrentSameKeyCommitsOneReceipt(t *testing.T) {
	repo, source, state, now := prepareRecoverableBulletin(t)
	input := onlinebulletins.RecoveryInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash}
	key := "race-" + source.Canonical.IssueID
	results := make(chan onlinebulletins.RecoveryResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := repo.RecoverOnlineBulletinPublication(bulletinAuditContext(), state.IssueID, input, key, postgresBulletinAuditActor, now)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	replayed := 0
	for result := range results {
		if id != "" && id != result.Receipt.ID {
			t.Fatal("two recovery receipts")
		}
		id = result.Receipt.ID
		if result.Replayed {
			replayed++
		}
	}
	if replayed != 1 {
		t.Fatal("concurrent key did not replay", replayed)
	}
	var count int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_recovery_receipt WHERE issue_id=$1`, state.IssueID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate committed recovery", count, err)
	}
}

func TestReplacingPDFClearsOnlyDifferentObjectsChecksum(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := bulletinAuditContext()
	repo := New(db).WithAudit(NewAuditOutbox(db))
	issue, err := repo.CreateIssue(ctx, 1234569, "2099-12-30", postgresBulletinAuditActor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID) })
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "Synthetic", PDFAssetID: "same-object", PDFFileName: "weekly.pdf"}
	issue, err = repo.PutVersion(ctx, issue.ID, issue.Version, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_sha256=repeat('a',64) WHERE issue_id=$1`, issue.ID); err != nil {
		t.Fatal(err)
	}
	issue, err = repo.PutVersion(ctx, issue.ID, issue.Version, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err = db.QueryRow(`SELECT pdf_sha256 FROM hhc_web.bulletin_version WHERE issue_id=$1`, issue.ID).Scan(&checksum); err != nil || checksum != strings.Repeat("a", 64) {
		t.Fatal("same object lost known checksum", checksum, err)
	}
	input.PDFAssetID = "replacement-object"
	if _, err = repo.PutVersion(ctx, issue.ID, issue.Version, input, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT pdf_sha256 FROM hhc_web.bulletin_version WHERE issue_id=$1`, issue.ID).Scan(&checksum); err != nil || checksum != "" {
		t.Fatal("replacement inherited old object checksum", checksum, err)
	}
}

func TestLegacySuccessfulPublicationRecordsCompletionWithoutInventedAuthorization(t *testing.T) {
	for _, mode := range []string{"automatic", "manual"} {
		t.Run(mode, func(t *testing.T) {
			repo, source, state, now := prepareRecoverableBulletin(t)
			ctx := bulletinAuditContext()
			if _, err := repo.db.Exec(`UPDATE hhc_web.bulletin_version SET auto_publish=true WHERE issue_id=$1`, state.IssueID); err != nil {
				t.Fatal(err)
			}
			if mode == "automatic" {
				if changed, err := repo.AutoPublishNextRecognition(context.Background(), now); err != nil || !changed {
					t.Fatal(changed, err)
				}
			} else {
				confirmed, err := repo.ConfirmOnlineBulletin(ctx, state.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, PageCount: len(state.Local.Document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{}}, postgresBulletinAuditActor, now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.PublishOnlineBulletin(ctx, state.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: confirmed.Version, Revision: confirmed.Revision}, postgresBulletinAuditActor, now); err != nil {
					t.Fatal(err)
				}
			}
			current, err := repo.OnlineBulletinState(ctx, state.IssueID, "general", "zh-Hant")
			if err != nil || current.Automation.Disposition == nil || current.Automation.Disposition.Status != "completed" {
				t.Fatal("successful publication labelled cancelled", current.Automation, err)
			}
			var authorized int
			if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_intent_transition t JOIN hhc_web.online_bulletin_publication_intent h ON h.id=t.intent_id WHERE h.issue_id=$1 AND t.status='authorized'`, source.Canonical.IssueID).Scan(&authorized); err != nil || authorized != 0 {
				t.Fatal("completion invented historical authorization", authorized, err)
			}
		})
	}
}
