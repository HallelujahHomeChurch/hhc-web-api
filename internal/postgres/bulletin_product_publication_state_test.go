package postgres

import (
	"errors"
	"reflect"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func TestExistingPublicationDoesNotBecomePublishableFromFreshnessOrHistory(t *testing.T) {
	for _, current := range []bool{false, true} {
		for _, failure := range []string{"", "source_publication_changed", "publication_authority_superseded"} {
			p := bulletinProductState{number: 1747, publishable: true, asset: "source", derivedStatus: "failed", derivedError: failure}
			p.outcome.Availability = bulletins.PublicationChannels{PDF: true, Traditional: true, Simplified: true}
			p.outcome.Current = bulletins.PublicationChannels{PDF: true, Traditional: current}
			if failure == "" {
				p.derivedStatus = ""
			}
			p.classify(true)
			if p.outcome.Status != "published" || p.outcome.NextAction != "" {
				t.Fatalf("current=%v history=%q: %+v", current, failure, p.outcome)
			}
			if p.outcome.Current.Traditional != current {
				t.Fatal("publication status fabricated source freshness")
			}
		}
	}
}

func TestPublicationEligibilityRequiresANewDraftOrNoPublicEdition(t *testing.T) {
	for _, available := range []bool{false, true} {
		p := bulletinProductState{number: 1747, publishable: true, sourceDraft: true, asset: "new-source"}
		p.outcome.Availability = bulletins.PublicationChannels{PDF: available, Traditional: available, Simplified: available}
		p.classify(true)
		if p.outcome.Status != "draft" || p.outcome.NextAction != "publish" {
			t.Fatalf("available=%v: %+v", available, p.outcome)
		}
	}
	p := bulletinProductState{number: 1747, publishable: true, withdrawn: true, asset: "withdrawn-source"}
	p.outcome.Availability = bulletins.PublicationChannels{PDF: true, Traditional: true}
	p.classify(true)
	if p.outcome.Status != "withdrawn" || p.outcome.NextAction != "publish" {
		t.Fatalf("withdrawn=%+v", p.outcome)
	}
}

func TestExistingPublishedIssueRejectsPublishDespiteHistoricalDerivedFailure(t *testing.T) {
	db, repo, source, now := seedPublishedAssessmentSource(t)
	event := publishAssessmentSource(t, db, repo, source, now)
	repo.WithAutomaticBulletinExtraction(true)
	issue := source.Canonical.IssueID
	// A legacy published source can lack modern checksum evidence. Its public
	// projections and explicit source lifecycle still determine write eligibility.
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET auto_publish=false,pdf_sha256='' WHERE issue_id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='source_publication_changed' WHERE id=$1`, event); err != nil {
		t.Fatal(err)
	}
	ctx := bulletinAuditContext()
	before, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	o := before.PublicationOutcome
	if o.Status != "published" || o.NextAction != "" || !o.Availability.PDF || !o.Availability.Traditional || o.Current.Traditional {
		t.Fatalf("incorrect public/authority projection: %+v", o)
	}
	var eventsBefore int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateBulletinPublication(ctx, issue, before.Version, bulletins.PublicationUpdateInput{Action: "publish", CommandToken: o.CommandToken}, "reject-spurious-publish", postgresBulletinAuditActor, now)
	if !errors.Is(err, bulletins.ErrConflict) {
		t.Fatalf("already public accepted publish: %v", err)
	}
	after, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
	if err != nil || after.Version != before.Version || !reflect.DeepEqual(after.PublicationOutcome, o) {
		t.Fatalf("rejected command changed state: %+v %v", after, err)
	}
	var eventsAfter int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event`).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
		t.Fatalf("rejected command queued work: %d -> %d (%v)", eventsBefore, eventsAfter, err)
	}
	// An actual saved replacement is different from a stale classifier result.
	if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET status='draft',pdf_asset_id='new-explicit-source' WHERE issue_id=$1`, issue); err != nil {
		t.Fatal(err)
	}
	replacement, err := repo.GetIssueWithPublicationOutcome(ctx, issue)
	if err != nil || replacement.PublicationOutcome.NextAction != "publish" {
		t.Fatalf("new source lost explicit update: %+v %v", replacement, err)
	}
	result, err := repo.UpdateBulletinPublication(ctx, issue, replacement.Version, bulletins.PublicationUpdateInput{Action: "publish", CommandToken: replacement.PublicationOutcome.CommandToken}, "publish-real-replacement", postgresBulletinAuditActor, now)
	if err != nil || result.Outcome.Status != "preparing" || !result.Outcome.Availability.Traditional {
		t.Fatalf("replacement failed or hid public content: %+v %v", result, err)
	}
}
