package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestPublicationProductResumesUploadAndReplaysOriginalAcceptance(t *testing.T) {
	db := accessDB(t)
	repo := New(db).WithAudit(NewAuditOutbox(db)).WithLLMBulletinExtraction(true).WithAutomaticBulletinExtraction(true)
	now := time.Now().UTC()
	actor := postgresBulletinAuditActor
	ctx := bulletinAuditContext()
	if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE issue_number=901201`); err != nil {
		t.Fatal(err)
	}
	issue, err := repo.CreateIssue(ctx, 901201, "2099-11-16", actor, "product-resume-issue", now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID)
	})
	issue, err = repo.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "Weekly", PDFAssetID: "product-draft-asset", PDFFileName: "weekly.pdf", AutoPublish: true}, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	event := claimUploadRequest(t, repo, issue.ID, now)
	if err = repo.RescheduleOnlineExtractionRequest(ctx, event, "upload_request_failed", time.Time{}, now, false); err != nil {
		t.Fatal(err)
	}
	before, err := repo.BulletinPublicationOutcome(ctx, issue.ID)
	if err != nil || before == nil || before.Status != "needs_attention" || before.NextAction != "resume" {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	resumeCtx := auditclient.WithProvenance(context.Background(), actor, "product-resume-request")
	input := bulletins.PublicationUpdateInput{}
	result, err := repo.UpdateBulletinPublication(resumeCtx, issue.ID, issue.Version, input, "resume-key", actor, now.Add(time.Second))
	if err != nil || result.Outcome.Status != "preparing" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	next := claimUploadRequest(t, repo, issue.ID, now.Add(time.Second))
	if next.ID == event.ID {
		t.Fatal("resume overwrote original request")
	}
	if err = repo.RescheduleOnlineExtractionRequest(ctx, next, "upload_source_invalid", time.Time{}, now.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	replay, err := repo.UpdateBulletinPublication(ctx, issue.ID, issue.Version, input, "resume-key", actor, now.Add(2*time.Second))
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	_, err = repo.UpdateBulletinPublication(ctx, issue.ID, issue.Version, bulletins.PublicationUpdateInput{Metadata: &bulletins.PublicationMetadata{IssueNumber: 901202, IssueDate: "2099-11-16", Title: "New title"}}, "resume-key", actor, now)
	if !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("changed body reused receipt", err)
	}
}

func TestDeleteUploadedDraftFencesExtractionAndQueuesAssetCleanup(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := context.Background()
	repo := New(db)
	jobID := seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, time.Minute, 3)
	if err != nil || job == nil || job.ID != jobID {
		t.Fatal("claim", err)
	}
	issue := job.Canonical.IssueID
	if err = repo.DeleteIssue(ctx, issue, 1, "admin", now); err != nil {
		t.Fatal("delete draft", err)
	}
	if _, err = repo.GetIssue(ctx, issue); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("draft still visible", err)
	}
	if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, extractionCandidate(*job), nil, now.Add(time.Second)); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatal("late worker revived removed draft")
	}
	var cleanup int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='asset.owner.delete' AND payload_json->>'assetId'=$2 AND status='pending'`, issue, job.SourceAssetID).Scan(&cleanup); err != nil || cleanup != 1 {
		t.Fatal("asset cleanup", cleanup, err)
	}
}

func TestPublishedMetadataIsStagedAndOldIssueNumberResolvesAfterCommit(t *testing.T) {
	db := accessDB(t)
	ctx, now := bulletinAuditContext(), time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAudit(NewAuditOutbox(db)).WithLLMBulletinExtraction(true).WithAutomaticBulletinExtraction(true)
	issue := source.Canonical.IssueID
	t.Cleanup(func() {
		for _, q := range []string{
			`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1`,
			`DELETE FROM hhc_web.online_bulletin_mapping WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
			`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
			`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
			`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`,
		} {
			if _, e := db.Exec(q, issue); e != nil {
				t.Error(e)
			}
		}
	})
	_, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || layout == nil {
		t.Fatal("claim", err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, issue, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := repo.ConfirmOnlineBulletin(ctx, issue, "general", "zh-Hant", onlinebulletins.ConfirmInput{
		CanonicalVersion: 1, OnlineVersion: state.Version, Revision: state.Local.Revision,
		LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, PageCount: len(state.Local.Document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{},
	}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version,issue_number,issue_date)
 SELECT v.issue_id,v.series,v.locale,v.title,v.subtitle,v.pdf_asset_id,$2,1,i.issue_number,i.issue_date
 FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id WHERE i.id=$1`, issue, now); err != nil {
		t.Fatal(err)
	}
	_, err = repo.PublishOnlineBulletin(ctx, issue, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: 1, OnlineVersion: confirmed.Version, Revision: state.Local.Revision}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	oldNumber := source.Canonical.Metadata.IssueNumber
	input := bulletins.PublicationUpdateInput{Metadata: &bulletins.PublicationMetadata{IssueNumber: oldNumber + 17, IssueDate: "2099-12-31", Title: "Corrected issue title", Subtitle: "Subtitle"}}
	current, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	input.CommandToken = current.PublicationOutcome.CommandToken
	correction, err := repo.UpdateBulletinPublication(ctx, issue, 1, input, "metadata-correction", postgresBulletinAuditActor, now.Add(time.Second))
	if err != nil || correction.Outcome.Status != "preparing" || correction.Outcome.PendingMetadata == nil || *correction.Issue.IssueNumber != oldNumber {
		t.Fatalf("stage=%+v err=%v", correction, err)
	}
	pdf, err := repo.GetProtectedVersion(ctx, issue, "general", "zh-Hant")
	if err != nil || pdf.Title != "test title" || pdf.IssueNumber == nil || *pdf.IssueNumber != oldNumber {
		t.Fatal("old PDF stopped serving", pdf, err)
	}
	if _, err = repo.CreateIssue(ctx, input.Metadata.IssueNumber, "2099-12-31", postgresBulletinAuditActor, "pending-collision", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("staged issue number could be taken", err)
	}
	if _, err = repo.UpdateBulletinPublication(ctx, issue, 1, input, "competing-metadata", postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("competing stale metadata command accepted", err)
	}
	corrected, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(time.Second), time.Minute, 3)
	if err != nil || corrected == nil || corrected.Canonical.Title != input.Metadata.Title {
		t.Fatal("stage proof not claimed", err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *corrected, measuredLayout(t, *corrected), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	oldLink, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant"}, 0, 5, &oldNumber)
	if err != nil || len(oldLink.Items) != 1 || oldLink.Items[0].IssueID != issue || oldLink.Items[0].CanonicalMetadata.Title != input.Metadata.Title {
		t.Fatal("old reader link lost", oldLink, err)
	}
	pdf, err = repo.GetProtectedVersion(ctx, issue, "general", "zh-Hant")
	if err != nil || pdf.Title != input.Metadata.Title || *pdf.IssueNumber != input.Metadata.IssueNumber || pdf.ProtectedAssetID != source.SourceAssetID {
		t.Fatal("PDF metadata not committed with online publication", pdf, err)
	}
	_, err = repo.CreateIssue(ctx, input.Metadata.IssueNumber, "2099-12-31", postgresBulletinAuditActor, "canonical-collision", now)
	if !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("corrected number became available to another issue", err)
	}
	_, err = repo.CreateIssue(ctx, oldNumber, "2099-12-31", postgresBulletinAuditActor, "alias-collision", now)
	if !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("historic issue number reassigned", err)
	}
	var notifications int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'`, issue).Scan(&notifications); err != nil || notifications != 0 {
		t.Fatal("correction notified subscribers", notifications, err)
	}
	final, err := repo.OnlineBulletinState(ctx, issue, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	preWithdraw, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UnpublishOnlineBulletin(ctx, issue, "general", "zh-Hant", final.CanonicalVersion, final.Version, postgresBulletinAuditActor, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, late := range []bulletins.PublicationUpdateInput{
		{Action: "publish", CommandToken: preWithdraw.PublicationOutcome.CommandToken},
		{Metadata: input.Metadata, CommandToken: preWithdraw.PublicationOutcome.CommandToken},
	} {
		if _, err = repo.UpdateBulletinPublication(ctx, issue, preWithdraw.Version, late, "late-after-online-withdrawal", postgresBulletinAuditActor, now.Add(3*time.Second)); !errors.Is(err, bulletins.ErrPrecondition) {
			t.Fatal("old command acquired authority after online-only withdrawal", err)
		}
	}
	if _, err = repo.Unpublish(ctx, issue, "general", "zh-Hant", final.CanonicalVersion, postgresBulletinAuditActor, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	hidden, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant"}, 0, 5, &oldNumber)
	if err != nil || len(hidden.Items) != 0 {
		t.Fatal("old alias exposed withdrawn publication", hidden, err)
	}
	withdrawn, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
	if err != nil || withdrawn.PublicationOutcome.Status != "withdrawn" || withdrawn.PublicationOutcome.Availability.PDF || withdrawn.PublicationOutcome.Availability.Traditional {
		t.Fatal("withdrawn status", withdrawn, err)
	}
	if _, err = repo.UpdateBulletinPublication(ctx, issue, withdrawn.Version, bulletins.PublicationUpdateInput{}, "withdrawn-resume", postgresBulletinAuditActor, now.Add(4*time.Second)); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("resume resurrected withdrawn authority", err)
	}
}

func TestPublicationProductResumesAssessmentWithCompletedPass(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	base := context.Background()
	repo, source := seedAutomaticUpdate(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	issue := source.Canonical.IssueID
	first, err := repo.ClaimOnlineBulletinUpdate(base, now, time.Minute, 1)
	if err != nil || first == nil {
		t.Fatal("claim assessment", err)
	}
	evidence := verifiedUpdateForTest(t, first.Input)
	if err = repo.SaveOnlineBulletinUpdateAttempt(base, *first, evidence.Attempts[0], now); err != nil {
		t.Fatal(err)
	}
	if err = repo.FailOnlineBulletinUpdate(base, *first, "provider_unavailable", now, 1); err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetIssueWithPublicationOutcome(base, issue)
	if err != nil || before.PublicationOutcome.NextAction != "resume" {
		t.Fatal(before, err)
	}
	resumeCtx := auditclient.WithProvenance(base, postgresBulletinAuditActor, "resume-checkpoint-1")
	result, err := repo.UpdateBulletinPublication(resumeCtx, issue, before.Version, bulletins.PublicationUpdateInput{}, "resume-assessment", postgresBulletinAuditActor, now.Add(time.Second))
	if err != nil || result.Outcome.Status != "preparing" {
		t.Fatal("resume", result, err)
	}
	job, err := repo.ClaimOnlineBulletinUpdate(base, now.Add(time.Second), time.Minute, 3)
	if err != nil || job == nil || job.ID != first.ID || job.ExtractionJobID != source.ID || len(job.SavedAttempts) != 1 {
		t.Fatal("completed assessment repeated", job, err)
	}
	if err = repo.SaveOnlineBulletinUpdateAttempt(base, *first, evidence.Attempts[1], now.Add(time.Second)); err == nil {
		t.Fatal("old lease advanced renewed assessment")
	}
	if err = repo.SaveOnlineBulletinUpdateAttempt(base, *job, evidence.Attempts[1], now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinUpdate(base, *job, evidence, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestKeyedUploadCompletionDoesNotSupersedeCurrentSourceOnReplay(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := bulletinAuditContext()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true)
	issue := source.Canonical.IssueID
	input := bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "Updated", PDFAssetID: "uploaded-production-pdf", PDFFileName: "weekly.pdf", AutoPublish: true, CompletionCommand: bulletins.UploadCompletionCommand{Key: "completion-replay", InputHash: [32]byte{1}}}
	first, err := repo.PutVersion(ctx, issue, 1, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	replacement := input
	replacement.PDFAssetID, replacement.AutoPublish, replacement.CompletionCommand = "later-draft", false, bulletins.UploadCompletionCommand{}
	second, err := repo.PutVersion(ctx, issue, first.Version, replacement, postgresBulletinAuditActor, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replay, exists, err := repo.ReplayBulletinUploadCompletion(ctx, issue, postgresBulletinAuditActor, input.CompletionCommand)
	if err != nil || !exists || replay.Version != first.Version || replay.Versions[0].PDFAssetID != input.PDFAssetID {
		t.Fatal("response lost attachment", replay, err)
	}
	replay, err = repo.PutVersion(ctx, issue, 1, input, postgresBulletinAuditActor, now.Add(2*time.Second))
	if err != nil || replay.Version != first.Version {
		t.Fatal("receipt was not read before canonical CAS", replay, err)
	}
	current, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
	if err != nil || current.Version != second.Version || current.Versions[0].PDFAssetID != replacement.PDFAssetID || current.PublicationOutcome.Status != "draft" {
		t.Fatal("replay revived prior source", current, err)
	}
}

func TestDraftDeletionCollectsPendingUploadSessionsBeforeAndAfterDelete(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := bulletinAuditContext()
	repo := New(db).WithAudit(NewAuditOutbox(db))
	actor := postgresBulletinAuditActor
	issue, err := repo.CreateIssue(ctx, 901205, "2099-11-17", actor, "delete-pending-upload", now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID) })
	edition, _ := bulletins.NormalizeBulletinEdition("general", "zh-Hant")
	if err = repo.TrackBulletinUploadAsset(ctx, issue.ID, edition, "pending-pdf-before-delete", actor, now); err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListIssues(ctx, 1, 10, "", "901205", "issueNumber", "desc")
	if err != nil || len(list.Items) != 1 || list.Items[0].Deletion == nil || !list.Items[0].Deletion.Allowed {
		t.Fatal("draft list deletion inaccurate", list, err)
	}
	if err = repo.DeleteIssue(ctx, issue.ID, issue.Version, actor, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.TrackBulletinUploadAsset(ctx, issue.ID, edition, "pending-pdf-after-delete", actor, now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("deleted issue accepted another asset", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='asset.owner.delete' AND status='pending' AND payload_json->>'assetId' IN ('pending-pdf-before-delete','pending-pdf-after-delete')`, issue.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("lost asset cleanup", count, err)
	}
}

func TestProductOutcomeUsesAgreedSourceAssessmentForMetadataCorrection(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := bulletinAuditContext()
	repo, source := seedAutomaticUpdate(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	evidence := verifiedUpdateForTest(t, job.Input)
	for _, attempt := range evidence.Attempts {
		attempt.Plan.Readiness = "wrong_issue"
		if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, attempt, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.FailOnlineBulletinUpdate(ctx, *job, "source_not_ready", now, 3); err != nil {
		t.Fatal(err)
	}
	outcome, err := repo.BulletinPublicationOutcome(ctx, source.Canonical.IssueID)
	if err != nil || outcome.Reason != "metadata_mismatch" || outcome.NextAction != "update_metadata" {
		t.Fatal("assessment did not route to metadata correction", outcome, err)
	}
}

func TestProductCommandPublishesExistingExplicitDraftWithoutReupload(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		name := "draft"
		if withdrawn {
			name = "withdrawn"
		}
		t.Run(name, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			ctx := bulletinAuditContext()
			actor := postgresBulletinAuditActor
			repo, source := seedOnlineLayout(t, db, now)
			repo.WithAudit(NewAuditOutbox(db)).WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true)
			issue := source.Canonical.IssueID
			if withdrawn {
				if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET status='unpublished' WHERE issue_id=$1`, issue); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET status='unpublished' WHERE id=$1`, issue); err != nil {
					t.Fatal(err)
				}
			}
			before, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
			if err != nil || before.PublicationOutcome.Status != name || before.PublicationOutcome.NextAction != "publish" {
				t.Fatal("existing source offers no publish action", before, err)
			}
			if _, err = repo.UpdateBulletinPublication(ctx, issue, before.Version, bulletins.PublicationUpdateInput{}, "old-resume-key", actor, now); !errors.Is(err, bulletins.ErrConflict) {
				t.Fatal("resume started a new publication", err)
			}
			command := bulletins.PublicationUpdateInput{Action: "publish", CommandToken: before.PublicationOutcome.CommandToken}
			outcome, err := repo.UpdateBulletinPublication(ctx, issue, before.Version, command, "publish-existing-source", actor, now)
			if err != nil || outcome.Outcome.Status != "preparing" || outcome.Issue.Versions[0].PDFAssetID != source.SourceAssetID {
				t.Fatal("explicit publish lost original source", outcome, err)
			}
			event := claimUploadRequest(t, repo, issue, now)
			if err = repo.CompleteOnlineExtractionRequest(ctx, event, source.SourceAssetChecksum, now); err != nil {
				t.Fatal(err)
			}
			replay, err := repo.UpdateBulletinPublication(ctx, issue, before.Version, command, "publish-existing-source", actor, now.Add(time.Second))
			if err != nil || !reflect.DeepEqual(outcome, replay) {
				t.Fatal("publish command was not idempotent", replay, err)
			}
			var sourceCount int
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' AND payload_json->>'assetId'=$2`, issue, source.SourceAssetID).Scan(&sourceCount); err != nil || sourceCount != 1 {
				t.Fatal("publish duplicated source work", sourceCount, err)
			}
		})
	}
}
