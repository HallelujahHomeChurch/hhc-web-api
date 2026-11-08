package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestConfirmedBodyKindLearningIsAsyncIndependentAndConflictSafe(t *testing.T) {
	db := accessDB(t)
	ctx, now := bulletinAuditContext(), time.Now().UTC()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_learning_rule_revision; DELETE FROM hhc_web.online_bulletin_learning_rule`)
	})
	var lastIssueID string
	for index, kind := range []string{"sermon", "sermon", "testimony"} {
		jobID := seedOnlineExtraction(t, db, now)
		if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET issue_number=$2,issue_date='2099-12-20'::date+$3::int WHERE id=(SELECT issue_id FROM hhc_web.online_bulletin_extraction_job WHERE id=$1)`, jobID, 987650+index, index); err != nil {
			t.Fatal(err)
		}
		repo := New(db).WithAudit(NewAuditOutbox(db))
		job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
		if err != nil || job == nil {
			t.Fatal(err)
		}
		lastIssueID = job.Canonical.IssueID
		candidate := extractionCandidate(*job)
		candidate.Document.LayoutManifest.RendererVersion = "v8"
		body := candidate.Document.Components[1].BodySection
		body.Kind = "unknown"
		body.Title.Sentences[0].Source = &onlinebulletins.SourceLocation{Page: 2, Box: onlinebulletins.Box{X: .1, Y: .3, Width: .05, Height: .02}}
		body.Contributors[0].Name.Sentences[0].Source = &onlinebulletins.SourceLocation{Page: 2, Box: onlinebulletins.Box{X: .18, Y: .3, Width: .07, Height: .02}}
		candidate.Evidence.PopplerVersion = onlinebulletins.PopplerVersion
		candidate.Evidence.Pages = []onlinebulletins.GeometryPage{{Number: 2, Width: 595.32, Height: 841.92, Texts: []onlinebulletins.GeometryText{
			{Text: "文", FontFamily: "DFXingShu", FontSize: 16, Box: body.Title.Sentences[0].Source.Box},
			{Text: "～", FontFamily: "DFXingShu", FontSize: 16, Box: onlinebulletins.Box{X: .16, Y: .3, Width: .02, Height: .02}},
			{Text: "文", FontFamily: "DFXingShu", FontSize: 16, Box: body.Contributors[0].Name.Sentences[0].Source.Box},
		}, Frames: []onlinebulletins.GeometryFrame{{Element: "bodyHeadingFrame", Box: onlinebulletins.Box{X: .098, Y: .298, Width: .054, Height: .024}}}}}
		if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now); err != nil {
			t.Fatal(err)
		}
		state, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
		if err != nil {
			t.Fatal(err)
		}
		state.Local.Document.Components[1].BodySection.Kind = kind
		state.Local.Document.Components[1].BodySection.Title.Sentences[0].Source = nil
		state.Local.Document.Components[1].BodySection.Contributors[0].Name.Sentences[0].Source = nil
		if _, err = repo.SaveOnlineBulletinDraft(ctx, job.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: state.Local.Document.Components}, postgresBulletinAuditActor, now); err != nil {
			t.Fatal(err)
		}
		layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
		if err != nil || layout == nil {
			t.Fatal(err)
		}
		if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
			t.Fatal(err)
		}
		state, err = repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
		if err != nil {
			t.Fatal(err)
		}
		input := onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: state.Version, Revision: state.Local.Revision, PageCount: 4, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: []onlinebulletins.ReviewIssue{}}
		stale := input
		stale.OnlineVersion--
		if _, err = repo.ConfirmOnlineBulletin(ctx, job.Canonical.IssueID, "general", "zh-Hant", stale, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
			t.Fatal("stale correction became learning", err)
		}
		if _, err = repo.ConfirmOnlineBulletin(ctx, job.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_learning_example WHERE document_id=$1`, job.DocumentID)
			_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, job.DocumentID)
		})
		event := claimLearningEvent(t, db, job.DocumentID, state.Local.Revision, now)
		if index == 0 {
			// Learning storage failure happens after confirmation committed.
			if _, err = db.Exec(`ALTER TABLE hhc_web.online_bulletin_learning_example ADD CONSTRAINT test_learning_failure CHECK(false) NOT VALID`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = db.Exec(`ALTER TABLE hhc_web.online_bulletin_learning_example DROP CONSTRAINT IF EXISTS test_learning_failure`)
			})
			if err = repo.CompleteOnlineBulletinLearning(ctx, event, now); err == nil {
				t.Fatal("learning fault ignored")
			}
			verified, readErr := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
			if readErr != nil || !verified.Confirmed {
				t.Fatal("learning failure rolled back review", readErr)
			}
			if _, err = db.Exec(`ALTER TABLE hhc_web.online_bulletin_learning_example DROP CONSTRAINT test_learning_failure`); err != nil {
				t.Fatal(err)
			}
			forged := event
			forged.AggregateVersion++
			if err = repo.CompleteOnlineBulletinLearning(ctx, forged, now); !errors.Is(err, bulletins.ErrConflict) {
				t.Fatal("event revision replaced", err)
			}
			forged = event
			forged.ClaimedUntil = now
			if err = repo.CompleteOnlineBulletinLearning(ctx, forged, now); !errors.Is(err, publication.ErrStalePublication) {
				t.Fatal("stale lease accepted", err)
			}
		}
		if err = repo.CompleteOnlineBulletinLearning(ctx, event, now); err != nil {
			t.Fatal(err)
		}
		if err = repo.CompleteOnlineBulletinLearning(ctx, event, now); err != nil {
			t.Fatal("duplicate delivery", err)
		}

		var count int
		var status string
		if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_learning_example`).Scan(&count); err != nil || count != index+1 {
			t.Fatal("correction not captured exactly once", count, err)
		}
		if err = db.QueryRow(`SELECT status FROM hhc_web.online_bulletin_learning_rule`).Scan(&status); err != nil || status != []string{"pending", "active", "conflicted"}[index] {
			t.Fatal("independent confirmation or contradiction ignored", status, err)
		}
		if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_learning_example SET kind='reflection'`); err == nil {
			t.Fatal("training evidence mutable")
		}
		if _, err = repo.ConfirmOnlineBulletin(ctx, job.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); err == nil {
			t.Fatal("repeat confirmation accepted")
		}
		if index == 1 {
			page, err := repo.OnlineBulletinLearningRules(ctx, "")
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			rule := page.Items[0]
			if _, err = db.Exec(`ALTER TABLE hhc_web.outbox_event ADD CONSTRAINT test_learning_audit_failure CHECK(payload_json->>'action'<>'cms.site_settings.update') NOT VALID`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT IF EXISTS test_learning_audit_failure`)
			})
			if _, err = repo.SetOnlineBulletinLearningRuleEnabled(ctx, rule.Signature, false, rule.Version, postgresBulletinAuditActor, now); err == nil {
				t.Fatal("audit failure committed a rule change")
			}
			if _, err = db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT test_learning_audit_failure`); err != nil {
				t.Fatal(err)
			}
			detail, err := repo.OnlineBulletinLearningRule(ctx, rule.Signature)
			if err != nil || detail.Rule.Version != 2 || detail.Rule.Status != "active" || len(detail.Revisions) != 2 {
				t.Fatal("failed audit did not roll back", detail, err)
			}
			results := make(chan error, 2)
			for range 2 {
				go func() {
					_, err := repo.SetOnlineBulletinLearningRuleEnabled(ctx, rule.Signature, false, 2, postgresBulletinAuditActor, now)
					results <- err
				}()
			}
			first, second := <-results, <-results
			if !((first == nil && errors.Is(second, bulletins.ErrPrecondition)) || (second == nil && errors.Is(first, bulletins.ErrPrecondition))) {
				t.Fatal("concurrent controls lost their version fence", first, second)
			}
			restored, err := repo.SetOnlineBulletinLearningRuleEnabled(ctx, rule.Signature, true, 3, postgresBulletinAuditActor, now)
			if err != nil || restored.Status != "active" || restored.Version != 4 {
				t.Fatal("verified rule cannot be restored", restored, err)
			}
		}
	}
	var history json.RawMessage
	if err := db.QueryRow(`SELECT jsonb_agg(status ORDER BY version) FROM hhc_web.online_bulletin_learning_rule_revision`).Scan(&history); err != nil {
		t.Fatal("rule history missing", string(history), err)
	}
	var statuses []string
	if json.Unmarshal(history, &statuses) != nil || len(statuses) != 5 || statuses[0] != "pending" || statuses[1] != "active" || statuses[2] != "disabled" || statuses[3] != "active" || statuses[4] != "conflicted" {
		t.Fatal("rule history missing", string(history))
	}
	repo := New(db).WithAudit(NewAuditOutbox(db))
	page, err := repo.OnlineBulletinLearningRules(ctx, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor != "" || !page.Items[0].Conflicted || page.Items[0].ConfirmedIssues != 3 {
		t.Fatal("unlocated or hidden conflict", page, err)
	}
	signature := page.Items[0].Signature
	detail, err := repo.OnlineBulletinLearningRule(ctx, signature)
	if err != nil || len(detail.Revisions) != 5 || len(detail.Examples) != 3 || detail.Examples[0].SourcePage != 2 || detail.Examples[0].Revision != 3 {
		t.Fatal("missing source/revision context", detail, err)
	}
	disabled, err := repo.SetOnlineBulletinLearningRuleEnabled(ctx, signature, false, 5, postgresBulletinAuditActor, now)
	if err != nil || disabled.Status != "disabled" || disabled.Version != 6 || !disabled.Conflicted {
		t.Fatal("disable discarded conflict or history", disabled, err)
	}
	if _, err = repo.SetOnlineBulletinLearningRuleEnabled(ctx, signature, false, 5, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale rule control accepted", err)
	}
	if _, err = repo.SetOnlineBulletinLearningRuleEnabled(ctx, signature, true, 6, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("contradictory evidence reactivated", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_learning_rule_revision SET status='active'`); err == nil {
		t.Fatal("historical administrator decision overwritten")
	}
	// Confirm two more revisions and deliver in reverse order. Returning to the
	// extractor's unknown baseline withdraws this issue's contradictory vote.
	older := confirmLearningKind(t, repo, lastIssueID, "teaching", now)
	newer := confirmLearningKind(t, repo, lastIssueID, "unknown", now)
	if err = repo.CompleteOnlineBulletinLearning(ctx, newer, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLearning(ctx, older, now); err != nil {
		t.Fatal(err)
	}
	detail, err = repo.OnlineBulletinLearningRule(ctx, signature)
	if err != nil || detail.Rule.ConfirmedIssues != 2 || detail.Rule.Conflicted || detail.Rule.Status != "disabled" {
		t.Fatal("withdrawal or manual disable lost", detail.Rule, err)
	}
	restored, err := repo.SetOnlineBulletinLearningRuleEnabled(ctx, signature, true, detail.Rule.Version, postgresBulletinAuditActor, now)
	if err != nil || restored.Status != "active" || restored.Kind != "sermon" {
		t.Fatal("conflict did not recover", restored, err)
	}

	var raw []byte
	if err = db.QueryRow(`SELECT learning_rules_json FROM hhc_web.online_bulletin_extraction_job LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal("job lacks frozen learning input", err)
	}
}

func claimLearningEvent(t *testing.T, db *sql.DB, documentID string, revision int64, now time.Time) publication.Event {
	t.Helper()
	var e publication.Event
	err := db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=attempts+1,claimed_until=$3 WHERE event_type='bulletin.online.learn' AND aggregate_id=$1 AND aggregate_version=$2 RETURNING id::text,event_type,aggregate_id::text,aggregate_version,payload_json,attempts,claimed_until`, documentID, revision, now.Add(time.Minute)).Scan(&e.ID, &e.EventType, &e.AggregateID, &e.AggregateVersion, &e.Payload, &e.Attempts, &e.ClaimedUntil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE id=$1`, e.ID) })
	return e
}

func confirmLearningKind(t *testing.T, repo *Repository, issueID, kind string, now time.Time) publication.Event {
	t.Helper()
	ctx := bulletinAuditContext()
	state, err := repo.OnlineBulletinState(ctx, issueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	state.Local.Document.Components[1].BodySection.Kind = kind
	state.Local.Document.Components[1].BodySection.Title.Sentences[0].Source = nil
	state.Local.Document.Components[1].BodySection.Contributors[0].Name.Sentences[0].Source = nil
	if _, err = repo.SaveOnlineBulletinDraft(ctx, issueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Components: state.Local.Document.Components}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal("save revised kind", err)
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || layout == nil {
		t.Fatal("claim", err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, issueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.ConfirmOnlineBulletin(ctx, issueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, PageCount: len(state.Local.Document.Pages), LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: append([]onlinebulletins.ReviewIssue{}, state.Local.ReviewIssues...)}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	return claimLearningEvent(t, repo.db, result.DocumentID, result.Revision, now)
}
