package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestOnlineHistoryUsesImmutableMetadataAndBoundedKeyset(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	if _, err := repo.RestoreOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.RestoreInput{CanonicalVersion: 1, OnlineVersion: 2, Revision: 1}, "admin", now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateVersion(ctx, source.Canonical.IssueID, "general", "zh-Hant", 1, "corrected title", "subtitle", "admin", now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	page, err := repo.OnlineBulletinHistory(ctx, source.Canonical.IssueID, "general", "zh-Hant", 0, 2)
	if err != nil || page.DocumentID != source.DocumentID || len(page.Items) != 2 || page.Items[0].Revision != 3 || page.Items[1].Revision != 2 || page.NextBefore == nil || *page.NextBefore != 2 {
		t.Fatal("first history page", err)
	}
	page, err = repo.OnlineBulletinHistory(ctx, source.Canonical.IssueID, "general", "zh-Hant", *page.NextBefore, 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].Revision != 1 || page.NextBefore != nil {
		t.Fatal("second history page", err)
	}
	for _, revision := range []int64{1, 2, 3} {
		detail, err := repo.OnlineBulletinRevision(ctx, source.Canonical.IssueID, "general", "zh-Hant", revision)
		if err != nil || detail.Revision != revision || detail.DocumentID != source.DocumentID || detail.Document.IssueID != source.Canonical.IssueID {
			t.Fatal("history detail", err)
		}
		want := source.Canonical.Metadata.Title
		if revision == 3 {
			want = "corrected title"
		}
		if detail.CanonicalMetadata.Title != want {
			t.Fatal("historical preview used current metadata")
		}
	}
	if _, err = repo.OnlineBulletinRevision(ctx, source.Canonical.IssueID, "general", "en", 1); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("cross edition history", err)
	}
	if _, err = repo.OnlineBulletinHistory(ctx, source.Canonical.IssueID, "general", "zh-Hant", 0, 51); !errors.Is(err, bulletins.ErrInvalid) {
		t.Fatal("unbounded history", err)
	}
}
