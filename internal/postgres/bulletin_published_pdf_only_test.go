package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

// Complete the real B publication boundary, rather than manufacturing a
// completed workflow beside a published projection.
func prepareEarlyPublishedPDF(t *testing.T) (*Repository, onlinebulletins.AdminState, bulletins.Workflow, publication.Event, time.Time) {
	t.Helper()
	repo, _, state, now := prepareRecoverableBulletin(t)
	if _, err := repo.db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, state.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.bulletin_version SET status='draft',auto_publish=true WHERE issue_id=$1`, state.IssueID); err != nil {
		t.Fatal(err)
	}
	w, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic"}, state.CanonicalVersion, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	var event publication.Event
	if err = repo.db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, state.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
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
	if err = repo.db.QueryRow(`SELECT status,created_at FROM hhc_web.publication_workflow WHERE id=$1`, w.ID).Scan(&w.Status, &w.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return repo, state, w, event, now
}

func TestPublishedPDFOnlyCancelsPlanWithoutRepublishing(t *testing.T) {
	repo, state, original, event, now := prepareEarlyPublishedPDF(t)
	var editionID string
	if err := repo.db.QueryRow(`SELECT id::text FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, state.IssueID).Scan(&editionID); err != nil {
		t.Fatal(err)
	}
	auditCount := func() int {
		t.Helper()
		var n int
		if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='audit.append' AND payload_json->>'action'='cms.bulletin.version.update' AND payload_json->>'resourceId'=$1`, editionID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	auditBefore := auditCount()
	input := bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &state.Version, NotifySubscribers: true}
	var before string
	const physical = `SELECT jsonb_build_object('pdf',(SELECT to_jsonb(p) FROM hhc_web.bulletin_published_version p WHERE p.issue_id=$1),'workflows',(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM hhc_web.publication_workflow w WHERE w.resource_id=$1),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM hhc_web.outbox_event e WHERE e.aggregate_id=$1),'online',(SELECT published_revision FROM hhc_web.online_bulletin_document WHERE issue_id=$1))::text`
	if err := repo.db.QueryRow(physical, state.IssueID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	w, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, input, state.CanonicalVersion, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if w.ID != original.ID || w.Status != "completed" || w.AggregateVersion != original.AggregateVersion || !w.CreatedAt.Equal(original.CreatedAt) {
		t.Fatal("did not reuse exact completed workflow", w, original)
	}
	if w.OnlinePublication == nil || w.OnlinePublication.Outcome != "cancelled" || w.OnlinePublication.Disposition == nil || w.OnlinePublication.Disposition.ReasonCode != "pdf_only" {
		t.Fatal("missing truthful cancellation", w)
	}
	if auditCount() != auditBefore+1 {
		t.Fatal("missing truthful edition plan-change audit")
	}
	var after string
	if err = repo.db.QueryRow(physical, state.IssueID).Scan(&after); err != nil || after != before {
		t.Fatal("plan change mutated completed publication or queued work", err)
	}
	current, err := repo.OnlineBulletinState(context.Background(), state.IssueID, "general", "zh-Hant")
	if err != nil || current.Automation.AutoPublish || current.CanonicalVersion != state.CanonicalVersion+1 || current.Version != state.Version {
		t.Fatal("wrong fences", current, err)
	}
	// Original double-CAS cannot execute the plan change twice.
	snapshot := publicationInterlockSnapshot(t, repo, state.IssueID)
	if _, err = repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, input, state.CanonicalVersion, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale repeat accepted", err)
	}
	if err = repo.CompletePublish(context.Background(), event, "", "", now); err != nil {
		t.Fatal(err)
	}
	if err = repo.Fail(context.Background(), event, "delayed_failure", now); err != nil {
		t.Fatal(err)
	}
	if after := publicationInterlockSnapshot(t, repo, state.IssueID); after != snapshot {
		t.Fatal("stale call or delayed claim changed terminal state")
	}
	// A fresh deliberate no-op reuses the published result without more epochs,
	// canonical bumps, audit, notification, cancellation or extraction commands.
	w, err = repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, input, current.CanonicalVersion, postgresBulletinAuditActor, now)
	if err != nil || w.ID != original.ID || w.Status != "completed" || w.OnlinePublication.Outcome != "terminal" {
		t.Fatal("fresh disabled repeat", w, err)
	}
	if after := publicationInterlockSnapshot(t, repo, state.IssueID); after != snapshot {
		t.Fatal("disabled repeat created effects")
	}
	if auditCount() != auditBefore+1 {
		t.Fatal("disabled repeat created audit")
	}
}

func TestPublishedPDFOnlyRequiresExactProofBeforeCancellation(t *testing.T) {
	for _, scenario := range []string{"missing-workflow", "wrong-asset", "wrong-projection-version", "wrong-metadata", "busy", "stale-online", "stale-canonical"} {
		t.Run(scenario, func(t *testing.T) {
			repo, state, _, _, now := prepareEarlyPublishedPDF(t)
			var q string
			want := bulletins.ErrConflict
			canonical, online := state.CanonicalVersion, state.Version
			switch scenario {
			case "missing-workflow":
				q = `UPDATE hhc_web.publication_workflow SET status='failed' WHERE resource_id=$1`
			case "wrong-asset":
				q = `UPDATE hhc_web.bulletin_published_version SET asset_id='different-asset' WHERE issue_id=$1`
			case "wrong-projection-version":
				q = `UPDATE hhc_web.bulletin_published_version SET version=version+1 WHERE issue_id=$1`
			case "wrong-metadata":
				q = `UPDATE hhc_web.bulletin_published_version SET title='other-title' WHERE issue_id=$1`
			case "busy":
				q = `UPDATE hhc_web.bulletin_issue SET status='publishing' WHERE id=$1`
			case "stale-online":
				online--
				want = bulletins.ErrPrecondition
			case "stale-canonical":
				canonical--
				want = bulletins.ErrPrecondition
			}
			if q != "" {
				if _, err := repo.db.Exec(q, state.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			before := publicationInterlockSnapshot(t, repo, state.IssueID)
			_, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &online}, canonical, postgresBulletinAuditActor, now)
			if !errors.Is(err, want) {
				t.Fatal("unproven plan change accepted", err)
			}
			if after := publicationInterlockSnapshot(t, repo, state.IssueID); after != before {
				t.Fatal("rejected plan change mutated state")
			}
		})
	}
}

func TestPublishedPDFOnlyAndAutomaticOnlineSerialize(t *testing.T) {
	for _, order := range []string{"pdf-only-first", "automatic-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			repo, state, original, _, now := prepareEarlyPublishedPDF(t)
			var cancelErr, autoErr error
			cancel := func() {
				_, cancelErr = repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &state.Version}, state.CanonicalVersion, postgresBulletinAuditActor, now)
			}
			automatic := func() { _, autoErr = repo.AutoPublishNextRecognition(context.Background(), now) }
			switch order {
			case "pdf-only-first":
				cancel()
				automatic()
			case "automatic-first":
				automatic()
				cancel()
			case "concurrent":
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); cancel() }()
				go func() { defer wg.Done(); automatic() }()
				wg.Wait()
			}
			if autoErr != nil {
				t.Fatal(autoErr)
			}
			if cancelErr != nil && !errors.Is(cancelErr, bulletins.ErrPrecondition) {
				t.Fatal(cancelErr)
			}
			current, err := repo.OnlineBulletinState(context.Background(), state.IssueID, "general", "zh-Hant")
			if err != nil || current.Automation.AutoPublish {
				t.Fatal(current, err)
			}
			if cancelErr == nil && current.PublishedRevision != nil {
				t.Fatal("cancelled plan released online")
			}
			if cancelErr != nil && current.PublishedRevision == nil {
				t.Fatal("automatic winner did not publish")
			}
			var count int
			var status string
			if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.publication_workflow WHERE resource_id=$1`, state.IssueID).Scan(&count); err != nil || count != 1 {
				t.Fatal("requeued completed PDF", count, err)
			}
			if err = repo.db.QueryRow(`SELECT status FROM hhc_web.publication_workflow WHERE id=$1`, original.ID).Scan(&status); err != nil || status != "completed" {
				t.Fatal(status, err)
			}
		})
	}
}

func TestPublishedPDFOnlyRetainsAlreadyPublishedOnline(t *testing.T) {
	repo, state, original, _, now := prepareEarlyPublishedPDF(t)
	if changed, err := repo.AutoPublishNextRecognition(context.Background(), now); err != nil || !changed {
		t.Fatal(changed, err)
	}
	current, err := repo.OnlineBulletinState(context.Background(), state.IssueID, "general", "zh-Hant")
	if err != nil || current.PublishedRevision == nil {
		t.Fatal(current, err)
	}
	before := publicationInterlockSnapshot(t, repo, state.IssueID)
	w, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &current.Version, NotifySubscribers: true}, current.CanonicalVersion, postgresBulletinAuditActor, now)
	if err != nil || w.ID != original.ID || w.Status != "completed" || w.OnlinePublication.Outcome != "terminal" {
		t.Fatal("completed plan was republished/cancelled again", w, err)
	}
	if after := publicationInterlockSnapshot(t, repo, state.IssueID); after != before {
		t.Fatal("fresh no-op withdrew Online or altered terminal work")
	}
}

func TestPublishedPDFOnlyAuditFailureRollsBackPlanChange(t *testing.T) {
	repo, state, _, _, now := prepareEarlyPublishedPDF(t)
	if _, err := repo.db.Exec(`CREATE FUNCTION hhc_web.reject_published_plan_audit_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.destination='audit-log' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_published_plan_audit_test BEFORE INSERT ON hhc_web.outbox_event FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_published_plan_audit_test()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.db.Exec(`DROP TRIGGER IF EXISTS reject_published_plan_audit_test ON hhc_web.outbox_event; DROP FUNCTION IF EXISTS hhc_web.reject_published_plan_audit_test()`)
	})
	before := publicationInterlockSnapshot(t, repo, state.IssueID)
	_, err := repo.StartPublishSelection(bulletinAuditContext(), state.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "pdf_only", OnlineVersion: &state.Version}, state.CanonicalVersion, postgresBulletinAuditActor, now)
	if !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("audit failure accepted", err)
	}
	if after := publicationInterlockSnapshot(t, repo, state.IssueID); after != before {
		t.Fatal("audit failure partially cancelled plan")
	}
}
