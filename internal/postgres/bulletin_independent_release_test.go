package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestScannedUploadSchedulesPDFBeforeRecognitionAndReplayIsIdempotent(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true).WithExplicitOnlinePublication(true)
	ctx := bulletinAuditContext()
	issue, err := repo.PutVersion(ctx, source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "Weekly", Subtitle: "Sunday", PDFAssetID: "independent-source", PDFFileName: "weekly.pdf", AutoPublish: true}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	for range 2 {
		if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, source.SourceAssetChecksum, now); err != nil {
			t.Fatal(err)
		}
	}
	var workflows, epochs int
	if err = db.QueryRow("SELECT (SELECT count(*) FROM hhc_web.publication_workflow WHERE resource_id=$1 AND workflow_type='bulletin_publish'),(SELECT count(*) FROM hhc_web.online_bulletin_publication_intent WHERE issue_id=$1)", issue.ID).Scan(&workflows, &epochs); err != nil || workflows != 1 || epochs != 1 {
		t.Fatal(workflows, epochs, err)
	}
}

// Use the same verified semantic and measured-layout fixture as qualified
// recovery. Both publication paths still execute their normal commit routines.
func TestIndependentReleaseOrderAndNotification(t *testing.T) {
	for _, order := range []string{"pdf-first", "online-first", "concurrent", "cancel-after-claim", "pdf-only-after-online-failure", "retry-pdf-after-online", "online-after-pdf-failure", "manual-online-first", "recovery-first"} {
		for _, notify := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/notify=%t", order, notify), func(t *testing.T) {
				repo, source, state, now := prepareRecoverableBulletin(t)
				issue := state.IssueID
				ctx := auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, "autopilot-test")
				tx, err := repo.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err = tx.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, issue); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`UPDATE hhc_web.bulletin_version SET status='draft',auto_publish=true,auto_notify=$2,verified_pdf_asset_id=pdf_asset_id WHERE issue_id=$1`, issue, notify); err != nil {
					t.Fatal(err)
				}
				if err = repo.changeBulletinIntent(ctx, tx, issue, "general", "zh-Hant", "authorized", "automatic_authorized", postgresBulletinAuditActor, now, ""); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`UPDATE hhc_web.online_bulletin_extraction_job e SET publication_intent_id=v.online_intent_id,publication_intent_epoch=v.online_intent_epoch FROM hhc_web.bulletin_version v WHERE e.id=$1 AND e.issue_id=v.issue_id`, source.ID); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if progressed, err := repo.AutoPublishNextRecognition(ctx, now); err != nil || !progressed {
					t.Fatal("schedule PDF", progressed, err)
				}
				var event publication.Event
				event.EventType, event.AggregateID = "bulletin.publish.ensure_asset", issue
				if err = repo.db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, issue, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
					t.Fatal(err)
				}
				pdf := func() error { return repo.CompletePublish(ctx, event, "", "", now) }
				online := func() error {
					ok, err := repo.AutoPublishNextRecognition(ctx, now)
					if err == nil && !ok {
						return fmt.Errorf("online did not progress")
					}
					return err
				}
				counts := func(pdf, online, g0 int) {
					t.Helper()
					var a, b, c int
					if err := repo.db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.bulletin_published_version WHERE issue_id=$1),(SELECT count(*) FROM hhc_web.online_bulletin_publication p JOIN hhc_web.online_bulletin_document d ON d.id=p.document_id WHERE d.issue_id=$1),(SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit')`, issue).Scan(&a, &b, &c); err != nil || a != pdf || b != online || c != g0 {
						t.Fatalf("got PDF %d online %d g0 %d, want %d/%d/%d: %v", a, b, c, pdf, online, g0, err)
					}
				}
				n := 0
				if notify {
					n = 1
				}
				switch order {
				case "cancel-after-claim":
					tx, err := repo.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					if err = repo.setAutomaticBulletinIntentReason(ctx, tx, issue, "general", "zh-Hant", false, false, postgresBulletinAuditActor, now, "explicit_draft"); err != nil {
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
					if err = pdf(); !errors.Is(err, publication.ErrStalePublication) {
						t.Fatal("revoked claim", err)
					}
					counts(0, 0, 0)
					return
				case "pdf-only-after-online-failure":
					tx, err := repo.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					if err = repo.recordBulletinJobFailure(ctx, tx, source.ID, now); err != nil {
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
					if err = pdf(); err != nil {
						t.Fatal(err)
					}
					counts(1, 0, n)
					return
				case "pdf-first":
					if err = pdf(); err != nil {
						t.Fatal(err)
					}
					counts(1, 0, n)
					if err = online(); err != nil {
						t.Fatal(err)
					}
				case "online-first":
					if err = online(); err != nil {
						t.Fatal(err)
					}
					counts(0, 1, 0)
					if err = pdf(); err != nil {
						t.Fatal(err)
					}
				case "manual-online-first", "recovery-first":
					current, err := repo.OnlineBulletinState(ctx, issue, "general", "zh-Hant")
					if err != nil {
						t.Fatal(err)
					}
					revision := current.Local.Revision
					proof := current.Local.Document.LayoutManifest.LayoutValidationHash
					if order == "recovery-first" {
						_, err = repo.RecoverOnlineBulletinPublication(ctx, issue, onlinebulletins.RecoveryInput{CanonicalVersion: current.CanonicalVersion, OnlineVersion: current.Version, Revision: revision, LayoutValidationHash: proof}, "recover-independent-"+issue, postgresBulletinAuditActor, now)
					} else {
						var confirmed onlinebulletins.ConfirmedRevision
						confirmed, err = repo.ConfirmOnlineBulletin(ctx, issue, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: current.CanonicalVersion, OnlineVersion: current.Version, Revision: revision, PageCount: len(current.Local.Document.Pages), LayoutValidationHash: proof, AcceptedIssues: []onlinebulletins.ReviewIssue{}}, postgresBulletinAuditActor, now)
						if err == nil {
							_, err = repo.PublishOnlineBulletin(ctx, issue, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: current.CanonicalVersion, OnlineVersion: confirmed.Version, Revision: revision}, postgresBulletinAuditActor, now)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					counts(0, 1, 0)
					if err = pdf(); err != nil {
						t.Fatal(err)
					}
				case "online-after-pdf-failure":
					if err = repo.FailPublish(ctx, event, source.SourceAssetID, "", "temporary_failure", now); err != nil {
						t.Fatal(err)
					}
					if err = online(); err != nil {
						t.Fatal(err)
					}
					counts(0, 1, 0)
					return
				case "retry-pdf-after-online":
					if err = online(); err != nil {
						t.Fatal(err)
					}
					if err = repo.FailPublish(ctx, event, source.SourceAssetID, "", "temporary_failure", now); err != nil {
						t.Fatal(err)
					}
					counts(0, 1, 0)
					retryCtx := auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, "retry-independent-pdf")
					repo.WithAutomaticBulletinExtraction(true)
					state, err := repo.BulletinPublicationOutcome(retryCtx, issue)
					if err != nil || state.NextAction != "resume" {
						t.Fatal("retry state", state, err)
					}
					for range 2 {
						if _, err = repo.UpdateBulletinPublication(retryCtx, issue, event.AggregateVersion, bulletins.PublicationUpdateInput{Action: "resume", CommandToken: state.CommandToken}, "retry-pdf", postgresBulletinAuditActor, now); err != nil {
							t.Fatal("retry", err)
						}
					}
					if err = repo.db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' AND status='pending' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, issue, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
						t.Fatal(err)
					}
					if err = pdf(); err != nil {
						t.Fatal(err)
					}
				case "concurrent":
					// SKIP LOCKED may defer a competing claim. Run the next scheduler tick.
					result := make(chan error, 2)
					go func() { result <- pdf() }()
					go func() { _, err := repo.AutoPublishNextRecognition(ctx, now); result <- err }()
					for range 2 {
						if err = <-result; err != nil {
							t.Fatal(err)
						}
					}
					if _, err = repo.AutoPublishNextRecognition(ctx, now); err != nil {
						t.Fatal(err)
					}
				}
				if err = pdf(); err != nil {
					t.Fatal("PDF replay", err)
				}
				counts(1, 1, n)
			})
		}
	}
}
