package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

func TestOnlineBefore1739RetainsPDFAndPrivateHistory(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source, _ := seedReaderPublication(t, db, now)
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET issue_number=1738 WHERE id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	before, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	number := 1738
	page, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant"}, 0, 10, &number)
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if item := page.Items[0]; !item.PDFPublished || item.Download == nil || item.DocumentID != "" || item.OnlineRevision != nil {
		t.Errorf("old online version remained discoverable or PDF disappeared: %+v", item)
	}
	if _, err = repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", uuid.NewString(), "privacy-v1", onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString()}, now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Errorf("old reader access: %v", err)
	}
	if _, err = repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: before.CanonicalVersion, OnlineVersion: before.Version, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum}, "admin", now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Errorf("old extraction: %v", err)
	}
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: before.CanonicalVersion, SourceOnlineVersion: before.Version, SourceRevision: before.Local.Revision}, "admin", now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Errorf("old Simplified conversion: %v", err)
	}
	if _, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: before.CanonicalVersion, OnlineVersion: before.Version, Revision: before.Local.Revision}, "admin", now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Errorf("old publication: %v", err)
	}
	after, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Errorf("retirement mutated private revisions/state: %v", err)
	}
}

func TestOnline1739BoundaryStillReadable(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source, _ := seedReaderPublication(t, db, now)
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET issue_number=1739 WHERE id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	number := 1739
	page, err := repo.DiscoverOnlineBulletins(ctx, "general", []string{"zh-Hant"}, 0, 10, &number)
	if err != nil || len(page.Items) != 1 || page.Items[0].DocumentID != source.DocumentID || page.Items[0].Download == nil {
		t.Fatal(page, err)
	}
	if _, err = repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", uuid.NewString(), "privacy-v1", onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString()}, now); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineReaderWaitsForIssueWithoutLockingDocument(t *testing.T) {
	db := accessDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	repo, source, _ := seedReaderPublication(t, db, now)
	if _, err := db.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET issue_number=1739 WHERE id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var blocker int
	if err = tx.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, source.Canonical.IssueID).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := repo.OpenOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", uuid.NewString(), "privacy-v1", onlinebulletins.ReaderAccessInput{ClientRequestID: uuid.NewString()}, now)
		done <- err
	}()
	for {
		var waiting bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("reader bypassed issue lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// PDF replacement owns the issue first; reader must not hold its document.
	if _, err = tx.ExecContext(ctx, `SELECT id FROM hhc_web.online_bulletin_document WHERE id=$1 FOR UPDATE NOWAIT`, source.DocumentID); err != nil {
		t.Fatalf("reader locked document before issue: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
