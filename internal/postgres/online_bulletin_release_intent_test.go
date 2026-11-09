package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestUploadCapturesExactAutomaticPublicationIntentAndDraftCancelsIt(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "updated", PDFAssetID: "auto-source", PDFFileName: "weekly.pdf", AutoPublish: true, NotifySubscribers: true}
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), event, source.SourceAssetChecksum, now); err != nil {
		t.Fatal(err)
	}
	var enabled, notify bool
	var request string
	if err = db.QueryRow(`SELECT v.auto_publish,v.auto_notify,v.auto_publish_request FROM hhc_web.bulletin_version v WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, issue.ID).Scan(&enabled, &notify, &request); err != nil || !enabled || !notify || request == "" {
		t.Fatal("intent missing", err)
	}
	var jobAuto bool
	if err = db.QueryRow(`SELECT auto_publish FROM hhc_web.online_bulletin_extraction_job WHERE issue_id=$1 AND NOT superseded AND extractor_version=$2`, issue.ID, onlinebulletins.RecognitionExtractorVersion()).Scan(&jobAuto); err != nil || !jobAuto {
		t.Fatal("intent not propagated", err)
	}
	input.AutoPublish, input.NotifySubscribers = false, false
	if _, err = repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, issue.Version, input, postgresBulletinAuditActor, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT auto_publish FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, issue.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("explicit draft retained intent", err)
	}
}

func TestCanonicalEditsCancelEarlierPublisherIntent(t *testing.T) {
	for _, scenario := range []string{"title", "issue", "aba"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			t.Cleanup(func() {
				_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
			})
			var err error
			if scenario == "title" || scenario == "aba" {
				_, err = repo.UpdateVersion(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", 1, "writer's new title", "", postgresBulletinAuditActor, now)
				if err == nil && scenario == "aba" {
					_, err = repo.UpdateVersion(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", 2, source.Canonical.Metadata.Title, source.Canonical.Metadata.Subtitle, postgresBulletinAuditActor, now)
				}
			} else {
				_, err = repo.UpdateIssue(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.UpdateIssueInput{IssueNumber: 987655, IssueDate: "2099-12-31"}, postgresBulletinAuditActor, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			var automatic bool
			if err = db.QueryRow(`SELECT auto_publish FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, source.Canonical.IssueID).Scan(&automatic); err != nil || automatic {
				t.Fatal("write-only metadata change reused prior publication authority", err)
			}
		})
	}
}

func TestOtherEditionUpdateDoesNotInvalidateAuthorizedSource(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	if _, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "en", Title: "English", PDFAssetID: "english-source", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("unrelated edition invalidated exact source/metadata authority", err)
	}
	evidence := verifiedUpdateForTest(t, job.Input)
	for _, a := range evidence.Attempts {
		if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, a, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CompleteOnlineBulletinUpdate(ctx, *job, evidence, now); err != nil {
		t.Fatal(err)
	}
}

func TestPendingRecognitionRenewalKeepsCurrentPublisherAuthority(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	seedOnlineExtraction(t, db, now)
	var issue string
	if err := db.QueryRow(`SELECT id::text FROM hhc_web.bulletin_issue WHERE issue_number=987654`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	repo := New(db).WithAudit(NewAuditOutbox(db)).WithLLMBulletinExtraction(true).WithAutomaticBulletinExtraction(true)
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 1, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64), AutoPublish: true}
	first, err := repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), issue, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	input.OnlineVersion = first.Version
	renewed, err := repo.QueueOnlineBulletinExtraction(auditclient.WithProvenance(ctx, postgresBulletinAuditActor, "renewed-request"), issue, input, postgresBulletinAuditActor, now)
	if err != nil || renewed.Job.ID != first.Job.ID {
		t.Fatal("same pending source was not reused", err)
	}
	var enabled bool
	var request string
	if err = db.QueryRow(`SELECT auto_publish,authorizer_request_id FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, first.Job.ID).Scan(&enabled, &request); err != nil || !enabled || request != "renewed-request" {
		t.Fatal("pending renewal revoked or mismatched authority", enabled, request, err)
	}
}

func TestVerifiedAutomaticUpdatePublishesOnlyExactMeasuredRevision(t *testing.T) {
	for _, scenario := range []string{"published-pdf", "draft-pdf", "cancelled", "authored-after-layout", "unpublish-before-first-online"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			if scenario == "draft-pdf" {
				if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET auto_notify=true WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 2)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			evidence := verifiedUpdateForTest(t, job.Input)
			for _, a := range evidence.Attempts {
				if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, a, now); err != nil {
					t.Fatal(err)
				}
			}
			if err = repo.CompleteOnlineBulletinUpdate(ctx, *job, evidence, now); err != nil {
				t.Fatal(err)
			}
			layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
			if err != nil || layout == nil {
				t.Fatal(err)
			}
			if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, q := range []string{`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1`, `DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, `DELETE FROM hhc_web.online_bulletin_publication WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, `DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, `DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`} {
					if _, e := db.Exec(q, source.Canonical.IssueID); e != nil {
						t.Error(e)
					}
				}
			})
			if scenario != "draft-pdf" {
				if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,now(),1 FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "cancelled" {
				_, err = db.Exec(`UPDATE hhc_web.bulletin_version SET auto_publish=false WHERE issue_id=$1`, source.Canonical.IssueID)
			}
			if scenario == "authored-after-layout" {
				state, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				_, err = repo.SaveOnlineBulletinDraft(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Components: state.Local.Document.Components}, postgresBulletinAuditActor, now)
			}
			if scenario == "unpublish-before-first-online" {
				state, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				_, err = repo.UnpublishOnlineBulletin(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", state.CanonicalVersion, state.Version, postgresBulletinAuditActor, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			progress, err := repo.AutoPublishNextRecognition(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "cancelled" || scenario == "authored-after-layout" || scenario == "unpublish-before-first-online" {
				if progress {
					t.Fatal("cancelled automatic authority revived")
				}
				return
			}
			if !progress {
				t.Fatal("authorized automatic release did not progress")
			}
			state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil || state.Automation == nil || state.Automation.Summary != "核對完成" || state.Automation.AutoPublish != (scenario == "draft-pdf") {
				t.Fatal("missing operator progress", err)
			}
			var publications, workflows int
			if err = db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1),(SELECT count(*) FROM hhc_web.publication_workflow WHERE resource_id=$2)`, source.DocumentID, source.Canonical.IssueID).Scan(&publications, &workflows); err != nil {
				t.Fatal(err)
			}
			if scenario == "published-pdf" && (publications != 1 || workflows != 0) {
				t.Fatal("online release", publications, workflows)
			}
			if scenario == "draft-pdf" && (publications != 0 || workflows != 1) {
				t.Fatal("did not reuse durable PDF publication workflow", publications, workflows)
			}
			if repeated, err := repo.AutoPublishNextRecognition(ctx, now); err != nil || repeated {
				t.Fatal("duplicate automatic publication", repeated, err)
			}
			if scenario == "draft-pdf" {
				var event publication.Event
				if err = db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, source.Canonical.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
					t.Fatal(err)
				}
				event.AggregateID, event.EventType = source.Canonical.IssueID, "bulletin.publish.ensure_asset"
				var intent publication.PublishPayload
				if json.Unmarshal(event.Payload, &intent) != nil || !intent.NotifySubscribers {
					t.Fatal("notification authority lost")
				}
				if err = repo.CompletePublish(ctx, event, "", "", now); err != nil {
					t.Fatal("PDF completion", err)
				}
				if progressed, err := repo.AutoPublishNextRecognition(ctx, now); err != nil || !progressed {
					t.Fatal("PDF completion did not resume exact online release", progressed, err)
				}
				if repeated, err := repo.AutoPublishNextRecognition(ctx, now); err != nil || repeated {
					t.Fatal("resumed release duplicated", repeated, err)
				}
				if err = repo.CompletePublish(ctx, event, "", "", now); err != nil {
					t.Fatal(err)
				}
				var notices int
				if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'`, source.Canonical.IssueID).Scan(&notices); err != nil || notices != 1 {
					t.Fatal("duplicated or lost notification", notices, err)
				}
			}
		})
	}
}
