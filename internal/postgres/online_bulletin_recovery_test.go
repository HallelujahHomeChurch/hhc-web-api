package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestBlockedExtractionRetryPreservesSnapshotAndExistingDraft(t *testing.T) {
	for _, withLocal := range []bool{false, true} {
		for _, upgrade := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "empty", true: "local"}[withLocal], map[bool]string{false: "same", true: "upgrade"}[upgrade]}, "-"), func(t *testing.T) {
				db := accessDB(t)
				ctx := context.Background()
				now := time.Now().UTC()
				repo := New(db)
				var issue string
				if withLocal {
					_, source := seedOnlineLayout(t, db, now)
					issue = source.Canonical.IssueID
					state, err := repo.OnlineBulletinState(ctx, issue, "general", "zh-Hant")
					if err != nil {
						t.Fatal(err)
					}
					if _, err = repo.QueueOnlineBulletinExtraction(ctx, issue, onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: state.Version, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64)}, "admin", now); err != nil {
						t.Fatal(err)
					}
				} else {
					seedOnlineExtraction(t, db, now)
				}
				job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, time.Minute, 3)
				if err != nil || job == nil {
					t.Fatal("claim", err)
				}
				issue = job.Canonical.IssueID
				version := onlinebulletins.ExtractorVersion
				if upgrade {
					version = "previous-parser"
				}
				if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2 WHERE id=$1`, job.ID, version); err != nil {
					t.Fatal(err)
				}
				job.ExtractorVersion = version
				candidate := extractionCandidate(*job)
				candidate.Document = nil
				if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, []onlinebulletins.ReviewIssue{{Code: "unsupported_source_structure", Blocking: true}}, now); err != nil {
					t.Fatal(err)
				}
				before, err := repo.OnlineBulletinState(ctx, issue, "general", "zh-Hant")
				if err != nil {
					t.Fatal(err)
				}
				if before.Extraction == nil || before.Extraction.ResultRevision != nil || withLocal != (before.Local != nil) {
					t.Fatal("job result mistaken for previous draft")
				}
				var snapshot string
				if err = db.QueryRow(`SELECT snapshot_json::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&snapshot); err != nil {
					t.Fatal(err)
				}
				input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: before.Version, SourceAssetID: "test-asset", SourceChecksum: job.SourceAssetChecksum, Retry: true}
				queued, err := repo.QueueOnlineBulletinExtraction(ctx, issue, input, "admin", now)
				if err != nil || queued.Job.ID == job.ID {
					t.Fatal("blocked retry", err)
				}
				after, err := repo.OnlineBulletinState(ctx, issue, "general", "zh-Hant")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before.Local, after.Local) || !reflect.DeepEqual(before.BaseRevision, after.BaseRevision) || !reflect.DeepEqual(before.IncomingRevision, after.IncomingRevision) || !reflect.DeepEqual(before.PublishedRevision, after.PublishedRevision) {
					t.Fatal("retry overwrote pointers")
				}
				var retained string
				var superseded bool
				if err = db.QueryRow(`SELECT snapshot_json::text,superseded FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&retained, &superseded); err != nil || retained != snapshot || !superseded {
					t.Fatal("immutable evidence changed", err)
				}
				input.OnlineVersion = queued.Version
				input.Retry = false
				again, err := repo.QueueOnlineBulletinExtraction(ctx, issue, input, "admin", now)
				if err != nil || again.Job.ID != queued.Job.ID {
					t.Fatal("non-idempotent queue", err)
				}
				input.Retry = true
				if _, err = repo.QueueOnlineBulletinExtraction(ctx, issue, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
					t.Fatal("active retry allowed", err)
				}
			})
		}
	}
}

func TestParserUpgradeDoesNotCancelActiveExtraction(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo := New(db)
	seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: state.Version, SourceAssetID: job.SourceAssetID, SourceChecksum: job.SourceAssetChecksum}
	if _, err = repo.QueueOnlineBulletinExtraction(ctx, job.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("upgrade cancelled active job", err)
	}
	if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, extractionCandidate(*job), nil, now); err != nil {
		t.Fatal("lease lost", err)
	}
}

func TestLayoutFailureCandidateIsSavedAndCannotBeConfirmed(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	issue := onlinebulletins.ReviewIssue{Code: "layout_validation_failed", FixedElement: "visionMission", SourcePage: 1, Blocking: true}
	repo, job := seedOnlineLayoutIssues(t, db, now, []onlinebulletins.ReviewIssue{issue})
	state, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if state.Local == nil || state.Extraction.ResultRevision == nil || *state.Extraction.ResultRevision != state.Local.Revision || state.Confirmed || len(state.Local.ReviewIssues) != 1 || state.Local.ReviewIssues[0] != issue {
		t.Fatal("failure candidate lost or confirmed")
	}
	if err = onlinebulletins.ValidateReview(state.Local.ReviewIssues, state.Local.ReviewIssues); err == nil {
		t.Fatal("failure waived")
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(2*time.Second), time.Minute, 3)
	if err != nil || layout == nil {
		t.Fatal("saved candidate not queued", err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Layout.Status != "ready" || state.Confirmed {
		t.Fatal("layout recovery", err)
	}
}

func TestLearningRuleCapacityFallsBackWithoutAbortingTransaction(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	rules := make([]onlinebulletins.LearningRuleInput, 4096)
	for i := range rules {
		rules[i] = onlinebulletins.LearningRuleInput{Signature: fmt.Sprintf("%064x", i+1), Version: 1, Kind: "sermon"}
	}
	raw, err := learningRulesSnapshot(ctx, rules)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := onlinebulletins.DecodeLearningRules(raw)
	if err != nil || len(decoded) != 4096 {
		t.Fatal("valid capacity", err)
	}
	rules = append(rules, onlinebulletins.LearningRuleInput{Signature: fmt.Sprintf("%064x", 4097), Version: 1, Kind: "sermon"})
	raw, err = learningRulesSnapshot(ctx, rules)
	if err != nil || string(raw) != "[]" {
		t.Fatal("capacity blocked conversion", err)
	}
	rules[0].Version = 9007199254740992
	if _, err = learningRulesSnapshot(ctx, rules); err == nil {
		t.Fatal("capacity swallowed corrupted rule")
	}

	var alive int
	if err = tx.QueryRow(`SELECT 1`).Scan(&alive); err != nil {
		t.Fatal("fallback aborted transaction", err)
	}
	if _, err = tx.Exec(`SELECT 1/0`); err == nil {
		t.Fatal("fault injection failed")
	}
	if _, err = loadActiveLearningRules(ctx, tx); err == nil {
		t.Fatal("SQL failure swallowed")
	}
}

func TestExtractionReplayReportsItsOwnCandidateRevision(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := context.Background()
	repo, job := seedOnlineLayout(t, db, now)
	// Simulate an already completed job from this parser identity.
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2 WHERE id=$1`, job.ID, onlinebulletins.ExtractorVersion); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.QueueOnlineBulletinExtraction(ctx, job.Canonical.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: state.Version, SourceAssetID: job.SourceAssetID, SourceChecksum: job.SourceAssetChecksum}, "admin", now)
	if err != nil || result.Job.ID != job.ID || result.Job.ResultRevision == nil || *result.Job.ResultRevision != state.Local.Revision {
		t.Fatal("idempotent response lost job result", result.Job, err)
	}
}
