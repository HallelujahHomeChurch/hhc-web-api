package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func seedJourney(t *testing.T) (*sql.DB, *Repository, onlinebulletins.ExtractionJob, string, time.Time) {
	t.Helper()
	db, repo, source, now := seedPublishedAssessmentSource(t)
	id := publishAssessmentSource(t, db, repo, source, now)
	repo.WithLLMBulletinExtraction(false)
	event := claimSimplifiedRelease(t, db, id, now)
	if err := repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now); err != nil {
		t.Fatal(err)
	}
	return db, repo, source, id, now
}
func TestSimplifiedResumeRejectsStaleTargetBeforeReuse(t *testing.T) {
	_, repo, source, _, now := seedJourney(t)
	hant, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if e != nil {
		t.Fatal(e)
	}
	hans, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	_, e = repo.QueueOnlineBulletinConversion(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version - 1, Retry: true}, postgresBulletinAuditActor, now)
	if !errors.Is(e, bulletins.ErrPrecondition) {
		t.Fatalf("stale mutating resume accepted: %v", e)
	}
}
func TestSimplifiedConversionProjectionKeepsConversionIdentity(t *testing.T) {
	db, repo, source, id, _ := seedJourney(t)
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	var job string
	if e = db.QueryRow(`SELECT id::text FROM hhc_web.online_bulletin_conversion_job WHERE publication_event_id=$1`, id).Scan(&job); e != nil {
		t.Fatal(e)
	}
	if state.Conversion == nil || state.Conversion.ID != job {
		t.Fatalf("conversion replaced with release identity: %#v", state.Conversion)
	}
}
func TestSimplifiedWaitingClaimCannotABA(t *testing.T) {
	db, repo, _, id, now := seedJourney(t)
	old := claimSimplifiedRelease(t, db, id, now)
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now); e != nil {
		t.Fatal(e)
	}
	fresh := claimSimplifiedRelease(t, db, id, now)
	if fresh.Attempts != old.Attempts || !fresh.ClaimedUntil.Equal(old.ClaimedUntil) {
		t.Fatal("fixture did not recreate ABA tuple")
	}
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now); !errors.Is(e, publication.ErrStalePublication) {
		t.Fatalf("old claim accepted after equal tuple reclaimed: %v", e)
	}
}
func TestSimplifiedFailureRetainedAfterResume(t *testing.T) {
	db, repo, source, id, now := seedJourney(t)
	event := claimSimplifiedRelease(t, db, id, now)
	if e := repo.RescheduleSimplifiedRelease(context.Background(), event, time.Time{}, now); e != nil {
		t.Fatal(e)
	}
	hant, _ := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	hans, _ := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	_, e := repo.QueueOnlineBulletinConversion(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version, Retry: true}, postgresBulletinAuditActor, now)
	if e != nil {
		t.Fatal(e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(state)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	if len(fields["derivedPublication"]) == 0 {
		t.Fatal("missing source-bound publication/history")
	}
	var release struct {
		LastFailure *struct {
			Code string `json:"code"`
		} `json:"lastFailure"`
	}
	json.Unmarshal(fields["derivedPublication"], &release)
	if release.LastFailure == nil || release.LastFailure.Code != "simplified_publication_failed" {
		t.Fatalf("failure lost after reschedule: %s", fields["derivedPublication"])
	}
}
