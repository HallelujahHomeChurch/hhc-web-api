package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

// One MVCC snapshot covers the authority, leases, versions, publication,
// immutable history and outbox that a rejected manual request must leave alone.
func publicationInterlockSnapshot(t *testing.T, repo *Repository, issue string) [32]byte {
	t.Helper()
	var raw []byte
	err := repo.db.QueryRow(`SELECT jsonb_build_object('issue',to_jsonb(i),'edition',to_jsonb(v),'document',to_jsonb(d),
 'extractions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM hhc_web.online_bulletin_extraction_job x WHERE x.issue_id=i.id),
 'updates',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM hhc_web.online_bulletin_update_job x WHERE x.document_id=d.id),
 'layouts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM hhc_web.online_bulletin_layout_job x WHERE x.document_id=d.id),
 'workflows',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM hhc_web.publication_workflow x WHERE x.resource_id=i.id),
 'outbox',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM hhc_web.outbox_event x WHERE x.aggregate_id=i.id OR x.aggregate_id=d.id),
 'publications',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.revision) FROM hhc_web.online_bulletin_publication x WHERE x.document_id=d.id),
 'confirmations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.revision) FROM hhc_web.online_bulletin_confirmation x WHERE x.document_id=d.id),
 'intents',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.epoch) FROM hhc_web.online_bulletin_publication_intent x WHERE x.issue_id=i.id),
 'transitions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.intent_id,x.sequence) FROM hhc_web.online_bulletin_intent_transition x JOIN hhc_web.online_bulletin_publication_intent h ON h.id=x.intent_id WHERE h.issue_id=i.id))
 FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant' LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series=v.series AND d.content_locale=v.locale WHERE i.id=$1`, issue).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func TestOrdinaryPDFRejectsActiveAuthorityWithoutSideEffects(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
	})
	// Use exactly the original authorizer/request: actor identity is not a worker discriminator.
	human := auditclient.WithProvenance(ctx, postgresBulletinAuditActor, "autopilot-test")
	for _, capability := range []bool{false, true} {
		for _, mode := range []string{""} {
			repo.WithExplicitOnlinePublication(capability)
			before := publicationInterlockSnapshot(t, repo, source.Canonical.IssueID)
			_, err := repo.StartPublishSelection(human, source.Canonical.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: mode, NotifySubscribers: true}, 1, postgresBulletinAuditActor, now)
			if !errors.Is(err, bulletins.ErrAutomaticPublicationActive) {
				t.Fatalf("capability=%v mode=%q manual competition accepted: %v", capability, mode, err)
			}
			if after := publicationInterlockSnapshot(t, repo, source.Canonical.IssueID); after != before {
				t.Fatal("rejected PDF request mutated protected state")
			}
		}
	}
}

// This existing-contract check is independent of a UI interlock policy:
// a qualified, explicitly authorized recovery either wins atomically or sees
// the automatic publication's new Online version and refuses stale input.
func TestQualifiedRecoveryAndAutomaticPublicationSerialize(t *testing.T) {
	for _, order := range []string{"recovery-first", "automatic-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			repo, source, state, now := prepareRecoverableBulletin(t)
			if _, err := repo.db.Exec(`UPDATE hhc_web.bulletin_version SET auto_publish=true WHERE issue_id=$1`, state.IssueID); err != nil {
				t.Fatal(err)
			}
			input := onlinebulletins.RecoveryInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash}
			var result onlinebulletins.RecoveryResult
			var recoveryErr, autoErr error
			var automatic bool
			recover := func() {
				result, recoveryErr = repo.RecoverOnlineBulletinPublication(bulletinAuditContext(), state.IssueID, input, "takeover-"+state.IssueID, postgresBulletinAuditActor, now)
			}
			release := func() { automatic, autoErr = repo.AutoPublishNextRecognition(context.Background(), now) }
			switch order {
			case "recovery-first":
				recover()
				release()
			case "automatic-first":
				release()
				recover()
			case "concurrent":
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); recover() }()
				go func() { defer wg.Done(); release() }()
				wg.Wait()
			}
			if autoErr != nil {
				t.Fatal(autoErr)
			}
			if recoveryErr != nil && !errors.Is(recoveryErr, bulletins.ErrPrecondition) {
				t.Fatal(recoveryErr)
			}
			if automatic && (recoveryErr == nil || result.Receipt.ID != "") {
				t.Fatal("automatic completion and stale recovery both won")
			}
			if !automatic && (recoveryErr != nil || result.Receipt.ID == "") {
				t.Fatal("neither publication completed", recoveryErr)
			}
			current, err := repo.OnlineBulletinState(context.Background(), state.IssueID, "general", "zh-Hant")
			if err != nil || current.PublishedRevision == nil || *current.PublishedRevision != input.Revision || current.Automation.AutoPublish {
				t.Fatal("publication or authority state diverged", err)
			}
			var publications, receipts, notifications int
			if err = repo.db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1),(SELECT count(*) FROM hhc_web.online_bulletin_recovery_receipt WHERE issue_id=$2),(SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$2 AND event_type='bulletin.notification.submit')`, state.DocumentID, state.IssueID).Scan(&publications, &receipts, &notifications); err != nil {
				t.Fatal(err)
			}
			wantReceipts, wantNotifications := 1, 0
			if automatic {
				wantReceipts, wantNotifications = 0, 1
			}
			if publications != 1 || receipts != wantReceipts || notifications != wantNotifications {
				t.Fatal("duplicate publication or wrong notification boundary", publications, receipts, notifications)
			}
			var request string
			if err = repo.db.QueryRow(`SELECT authorizer_request_id FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, source.ID).Scan(&request); err != nil || request != "autopilot-test" {
				t.Fatal("original authorizer mutated", request, err)
			}
		})
	}
}

func TestEarlyPDFRejectsUnprovableCurrentWorkWithoutEffects(t *testing.T) {
	for _, scenario := range []string{"request-mismatch", "metadata-mismatch", "stale-local", "failed-current-update", "already-published"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			repo.WithExplicitOnlinePublication(true)
			t.Cleanup(func() {
				_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
			})
			var q string
			switch scenario {
			case "request-mismatch":
				q = `UPDATE hhc_web.online_bulletin_extraction_job SET authorizer_request_id='older-request' WHERE issue_id=$1`
			case "metadata-mismatch":
				q = `UPDATE hhc_web.bulletin_version SET title='Other metadata' WHERE issue_id=$1`
			case "stale-local":
				q = `UPDATE hhc_web.online_bulletin_document SET version=version+1 WHERE issue_id=$1`
			case "failed-current-update":
				q = `UPDATE hhc_web.online_bulletin_update_job SET status='failed' WHERE document_id=(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`
			case "already-published":
				q = `UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`
			}
			if _, err := db.Exec(q, source.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			before := publicationInterlockSnapshot(t, repo, source.Canonical.IssueID)
			_, err := repo.StartPublishSelection(bulletinAuditContext(), source.Canonical.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic"}, 1, postgresBulletinAuditActor, now)
			if !errors.Is(err, bulletins.ErrConflict) {
				t.Fatal("unproven early PDF accepted", scenario, err)
			}
			if after := publicationInterlockSnapshot(t, repo, source.Canonical.IssueID); after != before {
				t.Fatal("unproven request mutated protected state")
			}
		})
	}
}

func prepareFailedOwnedPDF(t *testing.T) (*Repository, onlinebulletins.ExtractionJob, onlinebulletins.AdminState, time.Time) {
	t.Helper()
	repo, source, state, now := prepareRecoverableBulletin(t)
	if _, err := repo.db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, state.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.bulletin_version SET status='draft',auto_publish=true WHERE issue_id=$1`, state.IssueID); err != nil {
		t.Fatal(err)
	}
	if changed, err := repo.AutoPublishNextRecognition(context.Background(), now); err != nil || !changed {
		t.Fatal(changed, err)
	}
	var event publication.Event
	if err := repo.db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, state.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
		t.Fatal(err)
	}
	event.EventType, event.AggregateID = "bulletin.publish.ensure_asset", state.IssueID
	if err := repo.Fail(context.Background(), event, "synthetic_asset_failure", now); err != nil {
		t.Fatal(err)
	}
	current, err := repo.OnlineBulletinState(context.Background(), state.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	return repo, source, current, now
}

func TestEarlyPDFRejectsItsFailedOwnedLinkEvenAfterOtherManualSuccess(t *testing.T) {
	for _, manualSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned-failed", true: "owned-failed-other-manual-completed"}[manualSuccess], func(t *testing.T) {
			repo, source, state, now := prepareFailedOwnedPDF(t)
			if manualSuccess {
				// Retained history produced by the prior preserve-only public behavior.
				// This is fixture construction, not an alternate public route or a repair.
				tx, err := repo.db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.startBulletinPublishTx(bulletinAuditContext(), tx, state.IssueID, "general", "zh-Hant", state.CanonicalVersion, false, postgresBulletinAuditActor, now); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				var event publication.Event
				if err = repo.db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' AND status='pending' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, state.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
					t.Fatal(err)
				}
				event.EventType, event.AggregateID = "bulletin.publish.ensure_asset", state.IssueID
				if err = repo.CompletePublish(context.Background(), event, "", "", now); err != nil {
					t.Fatal(err)
				}
				state, err = repo.OnlineBulletinState(context.Background(), state.IssueID, "general", "zh-Hant")
				if err != nil {
					t.Fatal(err)
				}
			}
			before := publicationInterlockSnapshot(t, repo, state.IssueID)
			if _, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic"}, state.CanonicalVersion, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrConflict) {
				t.Fatal("failed owned link accepted for early PDF", err)
			}
			if after := publicationInterlockSnapshot(t, repo, state.IssueID); after != before {
				t.Fatal("preflight repaired or mutated failed ownership")
			}
			var status string
			if err := repo.db.QueryRow(`SELECT w.status FROM hhc_web.online_bulletin_update_job u JOIN hhc_web.publication_workflow w ON w.id=u.pdf_workflow_id WHERE u.extraction_job_id=$1`, source.ID).Scan(&status); err != nil || status != "failed" {
				t.Fatal("owned link was replaced", status, err)
			}
		})
	}
}

func TestEarlyPDFAcceptsNewHealthyRequestDespiteOlderFailedWorkflow(t *testing.T) {
	repo, source, state, now := prepareFailedOwnedPDF(t)
	repo.WithAutomaticBulletinExtraction(true)
	ctx := auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, "renewed-healthy-automatic-request")
	queued, err := repo.QueueOnlineBulletinExtraction(ctx, state.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum, AutoPublish: true, NotifySubscribers: true}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Job.ID == source.ID {
		t.Fatal("new request borrowed failed source job")
	}
	workflow, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic"}, state.CanonicalVersion, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal("unrelated historical failure blocked current healthy work", err)
	}
	if workflow.OnlinePublication == nil || workflow.OnlinePublication.Outcome != "preserved" {
		t.Fatal("current authorization not preserved", workflow.OnlinePublication)
	}
	var enabled, notify bool
	var request string
	if err = repo.db.QueryRow(`SELECT auto_publish,auto_notify,auto_publish_request FROM hhc_web.bulletin_version WHERE issue_id=$1`, state.IssueID).Scan(&enabled, &notify, &request); err != nil || !enabled || !notify || request != "renewed-healthy-automatic-request" {
		t.Fatal("early PDF changed current automatic plan", enabled, notify, request, err)
	}
}

func TestInactiveContinuePreservesOnlyAndDoesNotCreateAuthority(t *testing.T) {
	repo, source, state, now := prepareRecoverableBulletin(t)
	workflow, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic"}, state.CanonicalVersion, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.OnlinePublication == nil || workflow.OnlinePublication.Outcome != "absent" || workflow.OnlinePublication.Disposition != nil {
		t.Fatal("inactive preserve option created authority", workflow.OnlinePublication)
	}
	var enabled bool
	var count int
	if err = repo.db.QueryRow(`SELECT auto_publish,(SELECT count(*) FROM hhc_web.online_bulletin_publication_intent WHERE issue_id=$1) FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID).Scan(&enabled, &count); err != nil || enabled || count != 0 {
		t.Fatal("disabled plan was renewed", enabled, count, err)
	}
}
