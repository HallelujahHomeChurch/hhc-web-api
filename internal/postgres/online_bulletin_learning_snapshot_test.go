package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestLearningSnapshotIsFrozenAcrossDisableReplayAndRetry(t *testing.T) {
	db := accessDB(t)
	ctx, now := bulletinAuditContext(), time.Now().UTC()
	signature := strings.Repeat("e", 64)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_learning_rule WHERE signature=$1`, signature)
	})
	repo, source := seedOnlineLayout(t, db, now)
	repo = repo.WithAudit(NewAuditOutbox(db))
	// Fixture represents a prior verified rule; real activation requires independent confirmations.
	if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_learning_rule(signature,version,status,kind,title,speaker) VALUES($1,2,'active','sermon','信息','講員')`, signature); err != nil {
		t.Fatal(err)
	}

	// Active status alone is insufficient: the queued snapshot also requires two
	// current, independent confirmations, even if an evaluator is delayed.
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET issue_number=987653,issue_date='2099-12-29' WHERE id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	_, other := seedOnlineLayout(t, db, now)
	for _, proof := range []onlinebulletins.ExtractionJob{source, other} {
		if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_confirmation(document_id,revision,canonical_metadata,layout_validation_hash,page_count,accepted_issues,created_by,created_at) VALUES($1,1,$2,$3,4,'[]','test',$4)`, proof.DocumentID, mustJSON(proof.Canonical.Metadata), strings.Repeat("d", 64), now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_learning_example(signature,issue_id,document_id,revision,extracted_revision,extraction_job_id,component_id,source_page,source_checksum,layout_validation_hash,kind,created_by,created_at) VALUES($1,$2,$3,1,1,$4,'body',2,$5,$6,'sermon','test',$7)`, signature, proof.Canonical.IssueID, proof.DocumentID, proof.ID, proof.SourceAssetChecksum, strings.Repeat("d", 64), now); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_learning_example WHERE document_id=$1`, proof.DocumentID)
			_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, proof.DocumentID)
		})
	}
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 2, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum}
	queued, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_learning_rule SET status='disabled',version=3 WHERE signature=$1`, signature); err != nil {
		t.Fatal(err)
	}
	input.OnlineVersion = queued.Version
	replay, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now)
	if err != nil || replay.Job.ID != queued.Job.ID {
		t.Fatal("replay changed frozen job", err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, time.Minute, 3)
	expected := []onlinebulletins.LearningRuleInput{{Signature: signature, Version: 2, Kind: "sermon"}}
	if err != nil || job == nil || !reflect.DeepEqual(job.LearningRules, expected) {
		t.Fatal("queued rule followed mutable state", job, err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET learning_rules_json='[]' WHERE id=$1`, job.ID); err == nil {
		t.Fatal("queued learning input was mutable")
	}
	forged := *job
	forged.LearningRules = nil
	if err = repo.CompleteOnlineBulletinExtraction(ctx, forged, extractionCandidate(forged), nil, now); !errors.Is(err, onlinebulletins.ErrExtractionJob) {
		t.Fatal("worker replaced frozen rules", err)
	}
	if err = repo.FailOnlineBulletinExtraction(ctx, *job, "test_failure", now, 1); err != nil {
		t.Fatal(err)
	}
	input.Retry = true
	if _, err = repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ClaimOnlineBulletinExtraction(ctx, now, time.Minute, 3)
	if err != nil || job == nil || !reflect.DeepEqual(job.LearningRules, expected) {
		t.Fatal("retry recaptured mutable rule state", job, err)
	}
}
