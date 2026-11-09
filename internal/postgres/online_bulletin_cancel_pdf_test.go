package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestAutomaticDraftChoiceCancelsInFlightPDFPublication(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
	})
	job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	proof := verifiedUpdateForTest(t, job.Input)
	for _, attempt := range proof.Attempts {
		if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, attempt, now); err != nil {
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
	if progressed, err := repo.AutoPublishNextRecognition(ctx, now); err != nil || !progressed {
		t.Fatal(err)
	}
	var event publication.Event
	if err = db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, source.Canonical.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
		t.Fatal(err)
	}
	event.EventType, event.AggregateID = "bulletin.publish.ensure_asset", source.Canonical.IssueID
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum, AutoPublish: false}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompletePublish(ctx, event, "", "", now); !errors.Is(err, publication.ErrStalePublication) {
		t.Fatal("cancelled automatic PDF still published", err)
	}
	var pdfStatus, issueStatus string
	var published int
	if err = db.QueryRow(`SELECT i.status,v.status,(SELECT count(*) FROM hhc_web.bulletin_published_version WHERE issue_id=i.id) FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id WHERE i.id=$1`, source.Canonical.IssueID).Scan(&issueStatus, &pdfStatus, &published); err != nil || pdfStatus != "draft" || issueStatus != "draft" || published != 0 {
		t.Fatal("cancelled workflow retained publication authority", issueStatus, pdfStatus, published, err)
	}
}
