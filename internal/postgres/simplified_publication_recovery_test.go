package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

type hansRecoveryRepository interface {
	RecoverSimplifiedOnlineBulletinPublication(context.Context, string, onlinebulletins.SimplifiedRecoveryInput, string, string, time.Time) (onlinebulletins.SimplifiedRecoveryResult, error)
	WithSimplifiedPublicationRecovery(bool) *Repository
}

func readyHansRecovery(t *testing.T) (*sql.DB, *Repository, onlinebulletins.SimplifiedRecoveryInput, time.Time) {
	t.Helper()
	db, repo, source, id, now := seedJourney(t)
	completeJourneyProof(t, repo, now)
	hant, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if e != nil {
		t.Fatal(e)
	}
	hans, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	var hash string
	if e = db.QueryRow(`SELECT content_hash FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, *hant.PublishedRevision).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	return db, repo, onlinebulletins.SimplifiedRecoveryInput{TargetDocumentID: hans.DocumentID, CanonicalVersion: hans.CanonicalVersion, OnlineVersion: hans.Version, Source: onlinebulletins.SimplifiedRecoverySource{DocumentID: source.DocumentID, Revision: *hant.PublishedRevision, ContentHash: hash, OnlineVersion: hant.Version}, PublicationOperationID: id, Revision: hans.Local.Revision, LayoutValidationHash: hans.Local.Document.LayoutManifest.LayoutValidationHash}, now
}
func TestSimplifiedRecoverySavedProofAndReplay(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	recoverer, ok := any(repo).(hansRecoveryRepository)
	if !ok {
		t.Fatal("explicit Hans saved-proof recovery not implemented")
	}
	recoverer.WithSimplifiedPublicationRecovery(true)
	var issue string
	if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = enqueueBulletinPublicationNotification(bulletinAuditContext(), tx, issue, "general", sql.NullInt64{Int64: 987654, Valid: true}, postgresBulletinAuditActor, 97, now); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	before := journeyEffects(t, db, issue)
	notificationBefore := journeyNotificationSnapshot(t, db, issue)
	first, e := recoverer.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "saved-hans-proof-"+issue, postgresBulletinAuditActor, now)
	if e != nil {
		t.Fatal(e)
	}
	if first.Replayed || first.Receipt.AuthorizationID == "" || first.Receipt.PublicationOperationID != input.PublicationOperationID || first.Publication.Revision != input.Revision {
		t.Fatalf("invalid receipt %#v", first)
	}
	// Replay survives current source/Local changes and disabled new-operation capability.
	if _, e = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL,local_revision=NULL,version=version+1 WHERE issue_id=$1`, issue); e != nil {
		t.Fatal(e)
	}
	beforeReplay := journeyEffects(t, db, issue)
	recoverer.WithSimplifiedPublicationRecovery(false)
	second, e := recoverer.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "saved-hans-proof-"+issue, postgresBulletinAuditActor, now)
	if e != nil || !second.Replayed || second.Receipt.ID != first.Receipt.ID {
		t.Fatalf("lost response replay: %#v %v", second, e)
	}
	changed := input
	changed.OnlineVersion++
	if _, e = recoverer.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, changed, "saved-hans-proof-"+issue, postgresBulletinAuditActor, now); !errors.Is(e, bulletins.ErrConflict) {
		t.Fatal("changed payload replay", e)
	}
	after := journeyEffects(t, db, issue)
	for _, index := range []int{4, 5, 6} {
		if after[index] != before[index] {
			t.Fatalf("recovery generated table %d: %v -> %v", index, before, after)
		}
	}
	if after != beforeReplay {
		t.Fatal("replay changed state", beforeReplay, after)
	}
	if notificationAfter := journeyNotificationSnapshot(t, db, issue); notificationAfter != notificationBefore {
		t.Fatal("recovery changed existing initial command")
	}

}
func TestSimplifiedRecoveryRejectsUnqualifiedProof(t *testing.T) {
	for _, mode := range []string{"canonical", "target", "target-document", "source-version", "source-hash", "source-revision", "source-document", "local", "hash", "missing-intent", "missing-assessment", "missing-layout", "metadata", "withdrawn", "capability", "traditional-key"} {
		t.Run(mode, func(t *testing.T) {
			db, repo, input, now := readyHansRecovery(t)
			recoverer, ok := any(repo).(hansRecoveryRepository)
			if !ok {
				t.Fatal("explicit Hans saved-proof recovery not implemented")
			}
			recoverer.WithSimplifiedPublicationRecovery(true)
			var issue string
			db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue)
			query := ""
			args := []any{issue}
			switch mode {
			case "target-document":
				input.TargetDocumentID = "11111111-1111-4111-8111-111111111111"
			case "canonical":
				input.CanonicalVersion++
			case "target":
				input.OnlineVersion++
			case "source-version":
				input.Source.OnlineVersion++
			case "source-hash":
				input.Source.ContentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "source-revision":
				input.Source.Revision++
			case "source-document":
				input.Source.DocumentID = "11111111-1111-4111-8111-111111111111"
			case "local":
				input.Revision++
			case "hash":
				input.LayoutValidationHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "missing-intent":
				input.PublicationOperationID = "11111111-1111-4111-8111-111111111111"
			case "missing-assessment":
				query = `UPDATE hhc_web.online_bulletin_conversion_job SET assessment_json='[]' WHERE issue_id=$1`
			case "missing-layout":
				query = `DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND content_locale='zh-Hans')`
			case "metadata":
				query = `UPDATE hhc_web.bulletin_version SET title='Changed target title' WHERE issue_id=$1 AND locale='zh-Hans'`
			case "withdrawn":
				query = `UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1 AND content_locale='zh-Hant'`
			case "capability":
				recoverer.WithSimplifiedPublicationRecovery(false)
			case "traditional-key":
				query = `INSERT INTO hhc_web.online_bulletin_recovery_receipt(id,issue_id,actor_id,operation_key,input_json,result_json,request_id,created_at) VALUES($5,$1::uuid,$2,'negative-proof-'||$1::text,'{}','{}',$3,$4)`
				args = []any{issue, postgresBulletinAuditActor, postgresBulletinAuditRequest, now, input.PublicationOperationID}
			}
			if query != "" {
				if _, e := db.Exec(query, args...); e != nil {
					t.Fatal(e)
				}
			}
			var before int
			db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND content_locale='zh-Hans')`, issue).Scan(&before)
			_, e := recoverer.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "negative-proof-"+issue, postgresBulletinAuditActor, now)
			if e == nil {
				t.Fatal("unqualified proof published")
			}
			var after int
			db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND content_locale='zh-Hans')`, issue).Scan(&after)
			if before != after {
				t.Fatal("rejection changed publication")
			}
		})
	}
}

func TestSimplifiedRecoveryPreservesCancelledHistoryAndFencesOldClaim(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true).WithExplicitOnlinePublication(true)
	var issue string
	if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
		t.Fatal(e)
	}
	old := claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
	if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='publication_withdrawn' WHERE id=$1`, input.PublicationOperationID); e != nil {
		t.Fatal(e)
	}
	result, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "cancelled-human-"+issue, postgresBulletinAuditActor, now)
	if e != nil {
		t.Fatal(e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	if state.PublishedRevision == nil || state.DerivedPublication == nil || state.DerivedPublication.Status != "cancelled" || state.DerivedPublication.ErrorCode != "publication_withdrawn" || state.DerivedPublication.Recovery == nil || state.DerivedPublication.Recovery.AuthorizationID != result.Receipt.AuthorizationID {
		t.Fatalf("recovery rewrote cancelled history %#v", state.DerivedPublication)
	}
	if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now); !errors.Is(e, publication.ErrStalePublication) {
		t.Fatal("old worker published", e)
	}
	if e = repo.RescheduleSimplifiedRelease(context.Background(), old, now, now); e != nil {
		t.Fatal(e)
	}
	var code, status string
	if e = db.QueryRow(`SELECT status,last_error FROM hhc_web.outbox_event WHERE id=$1`, input.PublicationOperationID).Scan(&status, &code); e != nil || status != "failed" || code != "publication_withdrawn" {
		t.Fatal("old worker revived", status, code, e)
	}
	// Shared key namespace rejects Hans->Traditional, preserving the old wire type.
	_, e = repo.RecoverOnlineBulletinPublication(bulletinAuditContext(), issue, onlinebulletins.RecoveryInput{CanonicalVersion: input.CanonicalVersion, OnlineVersion: input.Source.OnlineVersion, Revision: input.Source.Revision, LayoutValidationHash: input.LayoutValidationHash}, "cancelled-human-"+issue, postgresBulletinAuditActor, now)
	if !errors.Is(e, bulletins.ErrConflict) {
		t.Fatal("Hans key reused as Traditional", e)
	}
}
func TestSimplifiedRecoveryConcurrentExactReplay(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue)
	type outcome struct {
		result onlinebulletins.SimplifiedRecoveryResult
		err    error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "concurrent-"+issue, postgresBulletinAuditActor, now)
			results <- outcome{r, e}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.result.Receipt.ID != b.result.Receipt.ID || a.result.Replayed == b.result.Replayed {
		t.Fatal("concurrent operation not replayed exactly", a.err, b.err, a.result, b.result)
	}
}
func TestSimplifiedRecoveryReusesExistingExactConfirmation(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue)
	state, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	confirmed, e := repo.ConfirmOnlineBulletin(bulletinAuditContext(), issue, "general", "zh-Hans", onlinebulletins.ConfirmInput{CanonicalVersion: input.CanonicalVersion, OnlineVersion: input.OnlineVersion, Revision: input.Revision, LayoutValidationHash: input.LayoutValidationHash, PageCount: len(state.Local.Document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{}}, postgresBulletinAuditActor, now)
	if e != nil {
		t.Fatal(e)
	}
	input.OnlineVersion = confirmed.Version
	if _, e = repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "confirmed-"+issue, postgresBulletinAuditActor, now); e != nil {
		t.Fatal("qualified confirmation must be reusable", e)
	}
}

func journeyEffects(t *testing.T, db *sql.DB, issue string) [8]int64 {
	t.Helper()
	var counts [8]int64
	e := db.QueryRow(`SELECT (SELECT COALESCE(sum(version),0) FROM hhc_web.online_bulletin_document WHERE issue_id=$1),
 (SELECT count(*) FROM hhc_web.online_bulletin_publication p JOIN hhc_web.online_bulletin_document d ON d.id=p.document_id WHERE d.issue_id=$1),
 (SELECT count(*) FROM hhc_web.online_bulletin_confirmation c JOIN hhc_web.online_bulletin_document d ON d.id=c.document_id WHERE d.issue_id=$1),
 (SELECT count(*) FROM hhc_web.online_bulletin_recovery_receipt WHERE issue_id=$1),
 (SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'),
 (SELECT count(*) FROM hhc_web.online_bulletin_conversion_job WHERE issue_id=$1),
 (SELECT count(*) FROM hhc_web.online_bulletin_layout_job l JOIN hhc_web.online_bulletin_document d ON d.id=l.document_id WHERE d.issue_id=$1),
 (SELECT count(*) FROM hhc_web.online_bulletin_simplified_authorization a JOIN hhc_web.online_bulletin_recovery_receipt r ON r.id=a.receipt_id WHERE r.issue_id=$1)`, issue).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6], &counts[7])
	if e != nil {
		t.Fatal(e)
	}
	return counts
}
func TestSimplifiedRecoveryRejectsCompletedAutomaticPublication(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue)
	event := claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now); e != nil {
		t.Fatal(e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
	if e != nil || state.PublishedRevision == nil {
		t.Fatal("worker did not publish", e)
	}
	input.OnlineVersion = state.Version
	before := journeyEffects(t, db, issue)
	if _, e = repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "already-published-"+issue, postgresBulletinAuditActor, now); e == nil {
		t.Fatal("completed automatic publication accepted new recovery grant")
	}
	if after := journeyEffects(t, db, issue); after != before {
		t.Fatal("rejected recovery changed state", before, after)
	}
}
func TestSimplifiedHumanRecoveryFencesLiveAutomaticClaim(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue)
	old := claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
	before := journeyEffects(t, db, issue)
	if _, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "live-takeover-"+issue, postgresBulletinAuditActor, now); e != nil {
		t.Fatal(e)
	}
	after := journeyEffects(t, db, issue)
	if after[4] != before[4] || after[5] != before[5] || after[6] != before[6] || after[1] != before[1]+1 {
		t.Fatal("human recovery effects", before, after)
	}
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now); !errors.Is(e, publication.ErrStalePublication) {
		t.Fatal("old live worker accepted", e)
	}
	if e := repo.RescheduleSimplifiedRelease(context.Background(), old, now, now); e != nil {
		t.Fatal(e)
	}
	if final := journeyEffects(t, db, issue); final != after {
		t.Fatal("old worker changed state", after, final)
	}
	state, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
	if e != nil || state.DerivedPublication.Status != "superseded" || state.DerivedPublication.ErrorCode != "publication_authority_superseded" {
		t.Fatal("untruthful authority history", state.DerivedPublication, e)
	}
}
func TestRealTraditionalReceiptConflictsWithSimplifiedRecovery(t *testing.T) {
	var key string
	t.Run("Traditional", func(t *testing.T) {
		traditional, source, state, now := prepareRecoverableBulletin(t)
		key = "actual-global-key-" + source.Canonical.IssueID
		first, e := traditional.RecoverOnlineBulletinPublication(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.RecoveryInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash}, key, postgresBulletinAuditActor, now)
		if e != nil || first.Receipt.ID == "" {
			t.Fatal("actual Traditional receipt", e)
		}
	})

	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue)
	before := journeyEffects(t, db, issue)
	if _, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, key, postgresBulletinAuditActor, now); !errors.Is(e, bulletins.ErrConflict) {
		t.Fatal("real Traditional key accepted as Hans", e)
	}
	if after := journeyEffects(t, db, issue); after != before {
		t.Fatal("collision mutated Hans", before, after)
	}
	anotherActor := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	anotherContext := auditclient.WithProvenance(context.Background(), anotherActor, "another-publisher-request")
	if _, e := repo.RecoverSimplifiedOnlineBulletinPublication(anotherContext, issue, input, key, anotherActor, now); e != nil {
		t.Fatal("distinct actor global namespace incorrectly conflicted", e)
	}
}
func TestSimplifiedRecoveryFencesEveryPriorLiveSourceAuthorization(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue)
	other := platform.NewID()
	if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)SELECT $2::uuid,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json-'jobId',$2::text,'pending',next_attempt_at,created_at,updated_at FROM hhc_web.outbox_event WHERE id=$1`, input.PublicationOperationID, other); e != nil {
		t.Fatal(e)
	}
	stale := claimSimplifiedRelease(t, db, other, now)
	if _, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "all-prior-"+issue, postgresBulletinAuditActor, now); e != nil {
		t.Fatal(e)
	}
	var status string
	if e := db.QueryRow(`SELECT status FROM hhc_web.outbox_event WHERE id=$1`, other).Scan(&status); e != nil || status != "failed" {
		t.Fatal("another old human grant remained live", status, e)
	}
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), stale, now); !errors.Is(e, publication.ErrStalePublication) {
		t.Fatal("another old worker published", e)
	}
}

func completeJourneyProof(t *testing.T, repo *Repository, now time.Time) {
	t.Helper()
	job, e := repo.ClaimOnlineBulletinConversion(context.Background(), now, time.Minute, 3)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	draft := derivedAssessmentDocument(job)
	input, e := onlinebulletins.SimplifiedAssessmentInput(job.Source, job.SourceMetadata, draft, job.SourceMetadata)
	if e != nil {
		t.Fatal(e)
	}
	for _, attempt := range verifiedUpdateForTest(t, input).Attempts {
		if e = repo.SaveOnlineBulletinConversionAssessment(context.Background(), *job, input, attempt, now); e != nil {
			t.Fatal(e)
		}
	}
	if e = repo.CompleteOnlineBulletinConversion(context.Background(), *job, draft, job.SourceMetadata, now); e != nil {
		t.Fatal(e)
	}
	layout, e := repo.ClaimOnlineBulletinLayout(context.Background(), now, time.Minute, 3)
	if e != nil || layout == nil {
		t.Fatal(e)
	}
	if e = repo.CompleteOnlineBulletinLayout(context.Background(), *layout, measuredLayout(t, *layout), now); e != nil {
		t.Fatal(e)
	}
}

func journeyNotificationSnapshot(t *testing.T, db *sql.DB, issue string) string {
	t.Helper()
	var snapshot string
	if e := db.QueryRow(`SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'payload',payload_json,'status',status) ORDER BY id),'[]'::jsonb)::text FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'`, issue).Scan(&snapshot); e != nil {
		t.Fatal(e)
	}
	return snapshot
}
func TestSimplifiedRecoveryRacesAutomaticWorkerExactlyOnce(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
		t.Fatal(e)
	}
	event := claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
	before := journeyEffects(t, db, issue)
	worker := make(chan error, 1)
	human := make(chan error, 1)
	go func() { worker <- repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now) }()
	go func() {
		_, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "human-auto-race-"+issue, postgresBulletinAuditActor, now)
		human <- e
	}()
	workerErr, humanErr := <-worker, <-human
	if workerErr != nil && !errors.Is(workerErr, publication.ErrStalePublication) {
		t.Fatal("unexpected worker outcome", workerErr)
	}
	if workerErr != nil && humanErr != nil {
		t.Fatal("neither actor published", workerErr, humanErr)
	}
	after := journeyEffects(t, db, issue)
	if after[1] != before[1]+1 || after[4] != before[4] || after[5] != before[5] || after[6] != before[6] || after[3] > before[3]+1 || after[7] > before[7]+1 {
		t.Fatal("race created duplicate effects", before, after)
	}
}
func TestSimplifiedUnpublishedTargetCancellationStopsLateWorker(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	var issue string
	if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
		t.Fatal(e)
	}
	old := claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
	if _, e := repo.UnpublishOnlineBulletin(bulletinAuditContext(), issue, "general", "zh-Hans", input.CanonicalVersion, input.OnlineVersion, postgresBulletinAuditActor, now); e != nil {
		t.Fatal(e)
	}
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now); !errors.Is(e, publication.ErrStalePublication) {
		t.Fatal("explicit unpublished-target cancellation left worker authorized", e)
	}
}
func TestSimplifiedRecoveryHonorsAssessedLocalMetadataChoice(t *testing.T) {
	db, repo, source, now := seedPublishedAssessmentSource(t)
	if _, e := db.Exec(`INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,subtitle,pdf_asset_id,pdf_file_name,created_by,updated_by,created_at,updated_at)VALUES($1,$2,'general','zh-Hans','kept target title','kept target subtitle','','','test','test',$3,$3)`, platform.NewID(), source.Canonical.IssueID, now); e != nil {
		t.Fatal(e)
	}
	id := publishAssessmentSource(t, db, repo, source, now)
	event := claimSimplifiedRelease(t, db, id, now)
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now); e != nil {
		t.Fatal(e)
	}
	job, e := repo.ClaimOnlineBulletinConversion(context.Background(), now, time.Minute, 3)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	draft := derivedAssessmentDocument(job)
	assessment, e := onlinebulletins.SimplifiedAssessmentInput(job.Source, job.SourceMetadata, draft, job.SourceMetadata)
	if e != nil {
		t.Fatal(e)
	}
	assessment.LocalMetadata = &job.TargetMetadata
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan := onlinebulletins.UpdatePlan{MetadataChoice: "local", Readiness: "ready", ChangeKind: "layout", Summary: "Synthetic retained metadata proof", Choices: []onlinebulletins.UpdateChoice{}, Mappings: []onlinebulletins.SemanticMappingDecision{}}
		json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": onlinebulletins.RecognitionModelName, "usage": map[string]any{"input_tokens": 100, "output_tokens": 100}, "output": []any{map[string]any{"type": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": string(updateTestJSON(t, plan))}}}}})
	}))
	defer server.Close()
	client, e := onlinebulletins.NewAzureExtractor(server.URL, "test", "test", "", server.Client())
	if e != nil {
		t.Fatal(e)
	}
	proof, e := client.PlanVerifiedUpdate(context.Background(), assessment, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, attempt := range proof.Attempts {
		if e = repo.SaveOnlineBulletinConversionAssessment(context.Background(), *job, assessment, attempt, now); e != nil {
			t.Fatal(e)
		}
	}
	if e = repo.CompleteOnlineBulletinConversion(context.Background(), *job, draft, job.SourceMetadata, now); e != nil {
		t.Fatal(e)
	}
	layout, e := repo.ClaimOnlineBulletinLayout(context.Background(), now, time.Minute, 3)
	if e != nil || layout == nil {
		t.Fatal(e)
	}
	if e = repo.CompleteOnlineBulletinLayout(context.Background(), *layout, measuredLayout(t, *layout), now); e != nil {
		t.Fatal(e)
	}
	hant, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if e != nil {
		t.Fatal(e)
	}
	hans, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	var hash string
	if e = db.QueryRow(`SELECT content_hash FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, *hant.PublishedRevision).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	input := onlinebulletins.SimplifiedRecoveryInput{TargetDocumentID: hans.DocumentID, CanonicalVersion: hans.CanonicalVersion, OnlineVersion: hans.Version, Source: onlinebulletins.SimplifiedRecoverySource{DocumentID: source.DocumentID, Revision: *hant.PublishedRevision, ContentHash: hash, OnlineVersion: hant.Version}, PublicationOperationID: id, Revision: hans.Local.Revision, LayoutValidationHash: hans.Local.Document.LayoutManifest.LayoutValidationHash}
	repo.WithSimplifiedPublicationRecovery(true)
	if _, e = repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), source.Canonical.IssueID, input, "local-choice-"+source.Canonical.IssueID, postgresBulletinAuditActor, now); e != nil {
		t.Fatal("valid assessed local metadata rejected", e)
	}
}
func TestWriteScopeResumeCannotReviveGrantAfterHumanRecovery(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, retry := range []bool{false, true} {
			t.Run(fmt.Sprintf("terminal=%v/retry=%v", terminal, retry), func(t *testing.T) {
				db, repo, input, now := readyHansRecovery(t)
				repo.WithSimplifiedPublicationRecovery(true)
				var issue string
				if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
					t.Fatal(e)
				}
				if terminal {
					if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='simplified_publication_failed' WHERE id=$1`, input.PublicationOperationID); e != nil {
						t.Fatal(e)
					}
				} else {
					_ = claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
				}
				if _, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "no-old-grant-"+issue, postgresBulletinAuditActor, now); e != nil {
					t.Fatal(e)
				}
				hant, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				hans, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
				if e != nil {
					t.Fatal(e)
				}
				before := journeyEffects(t, db, issue)
				notification := journeyNotificationSnapshot(t, db, issue)
				_, e = repo.QueueOnlineBulletinConversion(bulletinAuditContext(), issue, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version, Retry: retry}, postgresBulletinAuditActor, now)
				if !errors.Is(e, bulletins.ErrConflict) {
					t.Fatal("write scope restored superseded original publisher authority", e)
				}
				if after := journeyEffects(t, db, issue); after != before {
					t.Fatal("terminal human receipt resume changed effects", before, after)
				}
				if after := journeyNotificationSnapshot(t, db, issue); after != notification {
					t.Fatal("resume changed notification command")
				}
				var active int
				if e = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type=$2 AND status IN('pending','processing')`, input.Source.DocumentID, simplifiedReleaseEvent).Scan(&active); e != nil || active != 0 {
					t.Fatal("old grant became active", active, e)
				}
			})
		}
	}
}
func TestSourcePublicationReplayPreservesTerminalSimplifiedGrant(t *testing.T) {
	for _, cause := range []string{"human-recovered", "target-withdrawn"} {
		t.Run(cause, func(t *testing.T) {
			db, repo, input, now := readyHansRecovery(t)
			repo.WithSimplifiedPublicationRecovery(true)
			var issue string
			if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
				t.Fatal(e)
			}
			old := claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
			if cause == "human-recovered" {
				if _, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "source-replay-after-human-"+issue, postgresBulletinAuditActor, now); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e := repo.UnpublishOnlineBulletin(bulletinAuditContext(), issue, "general", "zh-Hans", input.CanonicalVersion, input.OnlineVersion, postgresBulletinAuditActor, now); e != nil {
					t.Fatal(e)
				}
			}
			tx, e := db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			if e = enqueueBulletinPublicationNotification(bulletinAuditContext(), tx, issue, "general", sql.NullInt64{Int64: 987654, Valid: true}, postgresBulletinAuditActor, 97, now); e != nil {
				tx.Rollback()
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			snapshot := func() string {
				var raw string
				if e := db.QueryRow(`SELECT jsonb_build_object('payload',e.payload_json,'status',e.status,'reason',e.last_error,'attempts',e.attempts,'createdAt',e.created_at,'updatedAt',e.updated_at,'ledger',to_jsonb(o))::text FROM hhc_web.outbox_event e JOIN hhc_web.online_bulletin_simplified_operation o ON o.operation_id=e.id WHERE e.id=$1`, input.PublicationOperationID).Scan(&raw); e != nil {
					t.Fatal(e)
				}
				return raw
			}
			before := snapshot()
			effects := journeyEffects(t, db, issue)
			notification := journeyNotificationSnapshot(t, db, issue)
			hant, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hant")
			if e != nil {
				t.Fatal(e)
			}
			anotherActor := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
			ctx := auditclient.WithProvenance(context.Background(), anotherActor, "fresh-source-replay-request")
			published, e := repo.PublishOnlineBulletin(ctx, issue, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: hant.CanonicalVersion, OnlineVersion: hant.Version, Revision: *hant.PublishedRevision}, anotherActor, now.Add(time.Second))
			if e != nil {
				t.Fatal(e)
			}
			if published.Version != hant.Version || published.Revision != *hant.PublishedRevision {
				t.Fatal("idempotent source replay changed source publication")
			}
			if after := snapshot(); after != before {
				t.Fatal("source publication replay rewrote terminal original grant or authorizer")
			}
			if after := journeyEffects(t, db, issue); after != effects {
				t.Fatal("source replay added publication or work", effects, after)
			}
			if after := journeyNotificationSnapshot(t, db, issue); after != notification {
				t.Fatal("source replay changed initial notification")
			}
			if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now.Add(time.Second)); !errors.Is(e, publication.ErrStalePublication) {
				t.Fatal("old worker regained authority", e)
			}
		})
	}
}
func TestSimplifiedRecoveryRestoresWithdrawnAutomaticPublication(t *testing.T) {
	db, repo, input, now := readyHansRecovery(t)
	repo.WithSimplifiedPublicationRecovery(true)
	var issue string
	if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
		t.Fatal(e)
	}
	old := claimSimplifiedRelease(t, db, input.PublicationOperationID, now)
	if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now); e != nil {
		t.Fatal(e)
	}
	hans, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.UnpublishOnlineBulletin(bulletinAuditContext(), issue, "general", "zh-Hans", hans.CanonicalVersion, hans.Version, postgresBulletinAuditActor, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	hans, e = repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	if hans.PublishedRevision != nil || hans.DerivedPublication.Status != "completed" {
		t.Fatal("historical completion conflated with current availability")
	}
	var beforeEvent string
	if e = db.QueryRow(`SELECT to_jsonb(e)::text FROM hhc_web.outbox_event e WHERE id=$1`, input.PublicationOperationID).Scan(&beforeEvent); e != nil {
		t.Fatal(e)
	}
	input.CanonicalVersion = hans.CanonicalVersion
	input.OnlineVersion = hans.Version
	before := journeyEffects(t, db, issue)
	notification := journeyNotificationSnapshot(t, db, issue)
	restored, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "restore-auto-"+issue, postgresBulletinAuditActor, now.Add(2*time.Second))
	if e != nil {
		t.Fatal("new human grant could not restore qualified withdrawn edition", e)
	}
	if restored.Receipt.AuthorizationID == "" || restored.Receipt.PublicationOperationID != old.ID {
		t.Fatal("missing distinct human restore grant")
	}
	var afterEvent string
	if e = db.QueryRow(`SELECT to_jsonb(e)::text FROM hhc_web.outbox_event e WHERE id=$1`, input.PublicationOperationID).Scan(&afterEvent); e != nil || beforeEvent != afterEvent {
		t.Fatal("restore rewrote completed automatic operation", e)
	}
	after := journeyEffects(t, db, issue)
	if after[4] != before[4] || after[5] != before[5] || after[6] != before[6] || after[1] != before[1] {
		t.Fatal("restore recomputed proof or notification", before, after)
	}
	if journeyNotificationSnapshot(t, db, issue) != notification {
		t.Fatal("restore changed initial command")
	}
	if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), old, now.Add(3*time.Second)); e != nil {
		t.Fatal(e)
	}
	if final := journeyEffects(t, db, issue); final != after {
		t.Fatal("old completed callback changed restore")
	}
}
func TestSimplifiedRecoverySummaryUsesCausalTargetVersion(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			db, repo, input, now := readyHansRecovery(t)
			repo.WithSimplifiedPublicationRecovery(true)
			originalInput := input
			var issue string
			if e := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.Source.DocumentID).Scan(&issue); e != nil {
				t.Fatal(e)
			}
			first, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "first-human-"+issue, postgresBulletinAuditActor, now)
			if e != nil {
				t.Fatal(e)
			}
			hans, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = repo.UnpublishOnlineBulletin(bulletinAuditContext(), issue, "general", "zh-Hans", hans.CanonicalVersion, hans.Version, postgresBulletinAuditActor, now.Add(time.Second)); e != nil {
				t.Fatal(e)
			}
			hans, e = repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
			if e != nil {
				t.Fatal(e)
			}
			input.CanonicalVersion = hans.CanonicalVersion
			input.OnlineVersion = hans.Version
			secondAt := now
			if reverse {
				secondAt = now.Add(-time.Hour)
			}
			before := journeyEffects(t, db, issue)
			notification := journeyNotificationSnapshot(t, db, issue)
			second, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, input, "second-human-"+issue, postgresBulletinAuditActor, secondAt)
			if e != nil {
				t.Fatal(e)
			}
			// Incomplete or corrupt legacy JSON must neither crash state nor outrank
			// the verified same-target receipt, even with a later recorded clock.
			for _, badVersion := range []string{`"not-an-integer"`, `9999999999999999999`} {
				badReceipt, badAuthorization := platform.NewID(), platform.NewID()
				badKey := "malformed-summary-" + badReceipt
				if _, e = db.Exec(`INSERT INTO hhc_web.online_bulletin_recovery_receipt(id,issue_id,actor_id,operation_key,input_json,result_json,request_id,created_at)
     SELECT $2::uuid,issue_id,actor_id,$4::text,input_json,
     jsonb_set(jsonb_set(jsonb_set(jsonb_set(result_json,'{receipt,id}',to_jsonb($2::text)),'{receipt,authorizationId}',to_jsonb($3::text)),'{receipt,operationKey}',to_jsonb($4::text)),'{publication,version}',$6::jsonb),request_id,$5
     FROM hhc_web.online_bulletin_recovery_receipt WHERE id=$1`, second.Receipt.ID, badReceipt, badAuthorization, badKey, now.Add(2*time.Hour), badVersion); e != nil {
					t.Fatal(e)
				}
				if _, e = db.Exec(`INSERT INTO hhc_web.online_bulletin_simplified_authorization(id,operation_id,receipt_id,actor_id,request_id,created_at)
     SELECT $2::uuid,$3::uuid,id,actor_id,request_id,created_at FROM hhc_web.online_bulletin_recovery_receipt WHERE id=$1`, badReceipt, badAuthorization, input.PublicationOperationID); e != nil {
					t.Fatal(e)
				}
			}
			state, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
			if e != nil {
				t.Fatal(e)
			}
			if state.DerivedPublication.Recovery == nil || state.DerivedPublication.Recovery.AuthorizationID != second.Receipt.AuthorizationID || state.DerivedPublication.Recovery.ReceiptID != second.Receipt.ID {
				t.Fatal("clock or random UUID selected earlier human outcome")
			}
			after := journeyEffects(t, db, issue)
			if after[4] != before[4] || after[5] != before[5] || after[6] != before[6] {
				t.Fatal("second human grant recomputed work", before, after)
			}
			if journeyNotificationSnapshot(t, db, issue) != notification {
				t.Fatal("second human grant changed initial notification")
			}
			replay, e := repo.RecoverSimplifiedOnlineBulletinPublication(bulletinAuditContext(), issue, originalInput, "first-human-"+issue, postgresBulletinAuditActor, now.Add(time.Hour))
			if e != nil || !replay.Replayed || replay.Receipt.ID != first.Receipt.ID || replay.Publication.Version >= second.Publication.Version {
				t.Fatal("old receipt replay changed historical outcome", e)
			}
			if current := journeyEffects(t, db, issue); current != after {
				t.Fatal("historical replay changed current availability", after, current)
			}
		})
	}
}
