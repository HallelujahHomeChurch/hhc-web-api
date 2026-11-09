package postgres

import (
	"context"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"testing"
	"time"
)

func TestAutomaticSourceRequestFencesEarlierDraftLayout(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprint(claimed), func(t *testing.T) {
			db := accessDB(t)
			ctx := context.Background()
			now := time.Now().UTC()
			repo, source := seedOnlineLayout(t, db, now)
			repo.WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true)
			before, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.SaveOnlineBulletinDraft(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: before.CanonicalVersion, OnlineVersion: before.Version, Components: before.Local.Document.Components}, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			var old *onlinebulletins.LayoutJob
			if claimed {
				old, err = repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
				if err != nil || old == nil {
					t.Fatal(err)
				}
			}
			current, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: current.CanonicalVersion, OnlineVersion: current.Version, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum, AutoPublish: true}, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			if claimed {
				if err = repo.CompleteOnlineBulletinLayout(ctx, *old, measuredLayout(t, *old), now); err == nil {
					t.Fatal("earlier layout can advance Local after a newer automatic source request")
				}
			} else if next, e := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3); e != nil || next != nil {
				t.Fatal("earlier queued layout retained automatic source authority", e)
			}
		})
	}

}
