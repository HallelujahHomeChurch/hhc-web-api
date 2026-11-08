package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

func seedReaderPublication(t *testing.T, db *sql.DB, now time.Time) (*Repository, onlinebulletins.ExtractionJob, int64) {
	t.Helper()
	ctx := context.Background()
	repo, source := seedOnlineLayout(t, db, now)
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: state.Version, Revision: state.Local.Revision, PageCount: len(state.Local.Document.Pages), LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: state.Local.ReviewIssues}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, `INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,now(),1 FROM hhc_web.bulletin_version WHERE issue_id=$1`} {
		if _, err = db.Exec(query, source.Canonical.IssueID); err != nil {
			t.Fatal(err)
		}
	}
	_, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: 1, OnlineVersion: confirmed.Version, Revision: confirmed.Revision}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{`DELETE FROM hhc_web.online_bulletin_reader_receipt WHERE document_id=$1`, `UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, `DELETE FROM hhc_web.online_bulletin_mapping WHERE document_id=$1`, `DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, `DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`} {
			if _, err := db.Exec(query, source.DocumentID); err != nil {
				t.Error(err)
			}
		}
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
			t.Error(err)
		}
	})
	return repo, source, confirmed.Revision
}

func TestReaderReceiptAtomicBindingAndIdempotency(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, revision := seedReaderPublication(t, db, now)
	account := uuid.NewString()
	input := onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString()}
	open := func(actor string, in onlinebulletins.ReaderAccessInput, at time.Time) (onlinebulletins.ReaderResponse, error) {
		return repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", actor, "privacy-v1", in, at)
	}
	first, err := open(account, input, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Access.AccountID != account || first.Access.Revision != revision || first.Access.OfflineValidUntil.Sub(first.Access.ValidatedAt) != 604800*time.Second {
		t.Fatal("binding or lifetime")
	}
	if _, err = bulletinwatermark.NormalizeCode(first.Access.TraceCode); err != nil {
		t.Fatal("trace code", err)
	}
	retry, err := open(account, input, now.Add(time.Hour))
	if err != nil || retry.Access.ReceiptID != first.Access.ReceiptID || !retry.Access.ValidatedAt.Equal(now) {
		t.Fatal("retry renewed receipt", err)
	}
	renew := onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString(), Revision: &revision, ReceiptID: first.Access.ReceiptID}
	next, err := open(account, renew, now.Add(time.Hour))
	if err != nil || next.Access.ReceiptID != first.Access.ReceiptID || next.Access.TraceCode != first.Access.TraceCode || !next.Access.ValidatedAt.Equal(now.Add(time.Hour)) {
		t.Fatal("same revision renewal", err)
	}
	retry, err = open(account, input, now.Add(2*time.Hour))
	if err != nil || !retry.Access.ValidatedAt.Equal(now) {
		t.Fatal("old replay borrowed another validation", err)
	}
	if _, err = open(account, input, now.Add(bulletinwatermark.Retention)); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("expired replay must require a new request", err)
	}
	renew.ClientRequestID = uuid.NewString()
	if _, err = open(uuid.NewString(), renew, now.Add(time.Hour)); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("cross-account receipt", err)
	}
	invalidRevision := revision + 100
	if _, err = open(account, onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString(), Revision: &invalidRevision}, now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("draft or missing revision", err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	next, err = open(account, onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString()}, now)
	if err != nil || next.Document.PDFPublished || next.Document.Download != nil {
		t.Fatal("online depends on PDF publication", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err = open(account, input, now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("replayed unpublished access", err)
	}
}

func TestReaderDiscoveryUnionUsesOneAuthoritativePage(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	repo, source, _ := seedReaderPublication(t, db, time.Now().UTC())
	number := source.Canonical.Metadata.IssueNumber
	page, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant"}, 0, 1, &number)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Items[0].PDFPublished || page.Items[0].OnlineRevision == nil {
		t.Fatal("union duplicates or missing", err)
	}
	page, err = repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant"}, 1, 1, &number)
	if err != nil || page.Total != 1 || len(page.Items) != 0 {
		t.Fatal("offset total", err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	page, err = repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant"}, 0, 1, &number)
	if err != nil || page.Total != 1 || page.Items[0].PDFPublished {
		t.Fatal("online-only hidden", err)
	}
	page, err = repo.DiscoverOnlineBulletins(ctx, "children", []string{"zh-Hant"}, 0, 1, &number)
	if err != nil || page.Total != 0 {
		t.Fatal("series crossing", err)
	}
}

func TestReaderConcurrentReplayAndTraceChannelIsolation(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source, _ := seedReaderPublication(t, db, now)
	account, request := uuid.NewString(), uuid.NewString()
	results := make([]onlinebulletins.ReaderResponse, 8)
	errorsOut := make([]error, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errorsOut[i] = repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", account, "privacy-v1", onlinebulletins.ReaderAccessInput{ClientRequestID: request}, now)
		}()
	}
	wg.Wait()
	for i := range results {
		if errorsOut[i] != nil || results[i].Access.ReceiptID != results[0].Access.ReceiptID {
			t.Fatalf("concurrent replay %d: %v", i, errorsOut[i])
		}
	}
	code, err := bulletinwatermark.NormalizeCode(results[0].Access.TraceCode)
	if err != nil {
		t.Fatal(err)
	}
	pdf := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: code, UserID: account, IssueID: source.Canonical.IssueID, Locale: "zh-Hant", Revision: 1, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), OutputSHA256: stringHash(), RendererVersion: "test", PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention)}
	if err = repo.InsertWatermarkReceipt(ctx, pdf); !errors.Is(err, bulletinwatermark.ErrCodeCollision) {
		t.Fatal("ambiguous cross-channel code", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_reader_receipt WHERE account_id=$1`, account).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate issuance", count, err)
	}
}

func TestReaderDiscoveryPagesIssuesBeforeTheirLanguageEditions(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source, _ := seedReaderPublication(t, db, now)
	baseline, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant", "en"}, 0, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := uuid.NewString()
	if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1::uuid,987653,'2099-12-29',$1::text,'test','test',$2,$2)`, second, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, `DELETE FROM hhc_web.bulletin_version WHERE issue_id=$1`, `DELETE FROM hhc_web.bulletin_issue WHERE id=$1`} {
			if _, err := db.Exec(query, second); err != nil {
				t.Error(err)
			}
		}
	})
	for _, issue := range []string{source.Canonical.IssueID, second} {
		if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,subtitle,pdf_asset_id,pdf_file_name,pdf_sha256,status,created_by,updated_by,created_at,updated_at) VALUES($1,$2,'general','en','English title','subtitle','test-asset','test.pdf',$3,'published','test','test',$4,$4)`, uuid.NewString(), issue, stringHash(), now); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) VALUES($1,'general','en','English title','subtitle','test-asset',$2,1)`, issue, now); err != nil {
			t.Fatal(err)
		}
	}
	first, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant", "en"}, 0, 1, nil)
	if err != nil || first.Total != baseline.Total+1 || len(first.Items) != 2 {
		t.Fatal("edition count inflated issue total", first.Total, len(first.Items), err)
	}
	for _, item := range first.Items {
		if item.IssueID != source.Canonical.IssueID {
			t.Fatal("page split editions")
		}
	}
	next, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant", "en"}, 1, 1, nil)
	if err != nil || next.Total != first.Total || len(next.Items) != 1 || next.Items[0].IssueID != second || next.Items[0].OnlineRevision != nil || !next.Items[0].PDFPublished {
		t.Fatal("English-only PDF issue lost", err)
	}
	number := source.Canonical.Metadata.IssueNumber
	exact, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant", "en"}, 0, 1, &number)
	if err != nil || exact.Total != 1 || len(exact.Items) != 2 {
		t.Fatal("exact issue filter", err)
	}
}

func TestReaderRequestPersistenceFailureRollsBackReceipt(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source, _ := seedReaderPublication(t, db, now)
	account := uuid.NewString()
	// Reject only this test account in the isolated test database, after receipt insertion.
	constraint := "reader_request_test_" + uuid.NewString()[:8]
	if _, err := db.Exec(`ALTER TABLE hhc_web.online_bulletin_reader_request ADD CONSTRAINT ` + constraint + ` CHECK (account_id <> '` + account + `'::uuid)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`ALTER TABLE hhc_web.online_bulletin_reader_request DROP CONSTRAINT ` + constraint); err != nil {
			t.Error(err)
		}
	})
	if _, err := repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", account, "privacy-v1", onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString()}, now); err == nil {
		t.Fatal("failed persistence accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_reader_receipt WHERE account_id=$1`, account).Scan(&count); err != nil || count != 0 {
		t.Fatal("uncommitted receipt survived", count, err)
	}
}

func TestReaderPinsRetainedPublicationAndRejectsDraft(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source, firstRevision := seedReaderPublication(t, db, now)
	account := uuid.NewString()
	open := func(revision *int64) (onlinebulletins.ReaderResponse, error) {
		return repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", account, "privacy-v1", onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString(), Revision: revision}, now)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	components := state.Local.Document.Components
	components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修正。"
	saved, err := repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: components}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = open(&saved.Revision); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("draft disclosed", err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: state.Version, Revision: state.Local.Revision, PageCount: len(state.Local.Document.Pages), LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: state.Local.ReviewIssues}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: 1, OnlineVersion: confirmed.Version, Revision: confirmed.Revision}, "admin", now); err != nil {
		t.Fatal(err)
	}
	old, err := open(&firstRevision)
	if err != nil || old.Document.Revision != firstRevision || old.Access.CurrentRevision != confirmed.Revision {
		t.Fatal("retained publication lost", err)
	}
	current, err := open(nil)
	if err != nil || current.Document.Revision != confirmed.Revision {
		t.Fatal("current publication not selected", err)
	}
	if old.Document.Content.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text == current.Document.Content.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text {
		t.Fatal("pinned content replaced")
	}
}

func TestReaderTraceLookupExportErasureAndRetention(t *testing.T) {
	for _, action := range []string{"erase", "restrict_processing", "expire"} {
		t.Run(action, func(t *testing.T) {
			db := accessDB(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			repo, source, _ := seedReaderPublication(t, db, now)
			account := uuid.NewString()
			input := onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString()}
			value, err := repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", account, "privacy-v1", input, now)
			if err != nil {
				t.Fatal(err)
			}
			code, err := bulletinwatermark.NormalizeCode(value.Access.TraceCode)
			if err != nil {
				t.Fatal(err)
			}
			trace, err := repo.LookupWatermarkReceipt(ctx, code, uuid.NewString(), now)
			if err != nil || trace.Channel != "online_reader" || trace.UserID != account || trace.FingerprintPayload != "" || len(trace.Pages) != 0 {
				t.Fatal("reader trace leaked into PDF evidence", err)
			}
			if action == "expire" {
				_, err = repo.PurgeWatermarkRecords(ctx, now.Add(bulletinwatermark.Retention), 1000)
			} else {
				if action == "erase" {
					_, _, err = repo.CleanupWatermarkAccount(ctx, account, uuid.NewString())
				} else {
					err = seedWatermarkRestriction(ctx, repo.db, account)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			at := now
			if action == "expire" {
				at = now.Add(bulletinwatermark.Retention)
			}
			if _, err = repo.LookupWatermarkReceipt(ctx, code, uuid.NewString(), at); !errors.Is(err, bulletinwatermark.ErrNotFound) {
				t.Fatal("receipt still discoverable", err)
			}
			if action != "expire" {
				if _, err = repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", account, "privacy-v1", input, now); !errors.Is(err, bulletins.ErrNotFound) {
					t.Fatal("subject fence bypassed", err)
				}
			}
			if action != "restrict_processing" {
				var count int
				if err = db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.online_bulletin_reader_receipt WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.online_bulletin_reader_request WHERE account_id=$1)`, account).Scan(&count); err != nil || count != 0 {
					t.Fatal("reader private rows retained", count, err)
				}
			}
		})
	}
}
