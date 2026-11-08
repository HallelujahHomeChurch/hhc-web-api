package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestCanonicalMetadataEditsForkOnlineLocalAndPreservePublication(t *testing.T) {
	for _, operation := range []string{"issue", "edition", "replace-pdf", "restore-cms", "unchanged", "other-edition", "measured-edition", "measured-unchanged"} {
		t.Run(operation, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			repo, source := seedOnlineLayout(t, db, now)
			ctx := context.Background()
			localRevision, onlineVersion := int64(1), int64(2)
			if operation == "measured-edition" || operation == "measured-unchanged" {
				if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, onlineVersion, now); err != nil {
					t.Fatal(err)
				}
				job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
				if err != nil || job == nil {
					t.Fatal(err)
				}
				measured := measuredLayout(t, *job)
				if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measured, now); err != nil {
					t.Fatal(err)
				}
				if _, err = repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: 4, Revision: 2, PageCount: len(measured.Document.Pages), LayoutValidationHash: measured.LayoutValidationHash, AcceptedIssues: []onlinebulletins.ReviewIssue{}}, "admin", now); err != nil {
					t.Fatal(err)
				}
				localRevision, onlineVersion = 2, 5
				t.Cleanup(func() {
					_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, source.DocumentID)
				})
			}
			metadata, _ := json.Marshal(source.Canonical.Metadata)
			if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by) VALUES($1,1,$2,'test')`, source.DocumentID, metadata); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=1 WHERE id=$1`, source.DocumentID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
				_, _ = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, source.DocumentID)
				_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, source.DocumentID)
			})
			var err error
			switch operation {
			case "issue":
				_, err = repo.UpdateIssue(ctx, source.Canonical.IssueID, 1, bulletins.UpdateIssueInput{IssueNumber: 987655, IssueDate: "2099-12-29"}, "admin", now)
			case "edition", "unchanged", "measured-edition", "measured-unchanged":
				title := "corrected title"
				if operation == "unchanged" || operation == "measured-unchanged" {
					title = source.Canonical.Metadata.Title
				}
				_, err = repo.UpdateVersion(ctx, source.Canonical.IssueID, "general", "zh-Hant", 1, title, "subtitle", "admin", now)
			case "replace-pdf", "other-edition":
				locale := "zh-Hant"
				if operation == "other-edition" {
					locale = "en"
				}
				_, err = repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: locale, Title: "corrected title", Subtitle: "subtitle", PDFAssetID: "replacement", PDFFileName: "replacement.pdf"}, postgresBulletinAuditActor, now)
			case "restore-cms":
				var issue bulletins.Issue
				issue, err = repo.GetIssue(ctx, source.Canonical.IssueID)
				if err != nil {
					t.Fatal(err)
				}
				issue.Versions[0].Title = "corrected title"
				raw, _ := json.Marshal(issue)
				if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_revision(issue_id,version,snapshot_json,created_by,created_at) VALUES($1,99,$2,'test',$3)`, source.Canonical.IssueID, raw, now); err != nil {
					t.Fatal(err)
				}
				_, err = repo.RestoreIssueRevision(ctx, source.Canonical.IssueID, 99, 1, "admin", now)
			}
			if err != nil {
				t.Fatal(err)
			}
			state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			// Compare persisted identity against the original ordered submission envelope.
			persisted := state.Local.Document
			persisted.LayoutManifest.ContentHash, persisted.LayoutManifest.LayoutValidationHash = "", ""
			var canonical onlinebulletins.CanonicalMetadata
			if err = db.QueryRow(`SELECT v.title,v.subtitle,i.issue_number,i.issue_date::text FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id WHERE v.issue_id=$1 AND v.series='general' AND v.locale='zh-Hant'`, source.Canonical.IssueID).Scan(&canonical.Title, &canonical.Subtitle, &canonical.IssueNumber, &canonical.Date); err != nil {
				t.Fatal(err)
			}
			originalBytes, err := json.Marshal(struct {
				Document  onlinebulletins.Document          `json:"document"`
				Canonical onlinebulletins.CanonicalMetadata `json:"canonicalMetadata"`
			}{persisted, canonical})
			if err != nil {
				t.Fatal(err)
			}
			expectedHash := sha256.Sum256(originalBytes)
			var storedHash string
			if err = db.QueryRow(`SELECT content_hash FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, state.Local.Revision).Scan(&storedHash); err != nil {
				t.Fatal(err)
			}
			if storedHash != hex.EncodeToString(expectedHash[:]) {
				t.Fatal("persisted canonical submission hash changed")
			}
			changed := operation != "unchanged" && operation != "other-edition" && operation != "measured-unchanged"
			if changed && (state.Local.Revision != localRevision+1 || state.Version != onlineVersion+1 || state.DraftStatus != "reviewRequired" || !state.MetadataSyncPending || state.Layout == nil || state.Layout.Status != "queued" || state.Confirmed || state.Local.Document.LayoutManifest.LayoutValidationHash != "") {
				t.Fatal("canonical change did not fork and revalidate Online")
			}
			if !changed && (state.Local.Revision != localRevision || state.Version != onlineVersion || state.MetadataSyncPending) {
				t.Fatal("unrelated or unchanged metadata forked Online")
			}
			if *state.PublishedRevision != 1 || *state.BaseRevision != 1 || *state.PublishedMetadata != source.Canonical.Metadata {
				t.Fatal("publication metadata changed or old proof reused")
			}
			if operation == "measured-unchanged" && !state.Confirmed {
				t.Fatal("no-op edit lost confirmation")
			}
		})
	}
}

func TestCMSRestoreCannotRemoveEditionWithOnlineHistory(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	issue, err := repo.GetIssue(context.Background(), source.Canonical.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	issue.Versions = []bulletins.Version{}
	raw, _ := json.Marshal(issue)
	if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_revision(issue_id,version,snapshot_json,created_by,created_at) VALUES($1,99,$2,'test',$3)`, issue.ID, raw, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, issue.ID) })
	if _, err = repo.RestoreIssueRevision(context.Background(), issue.ID, 99, 1, "admin", now); !errors.Is(err, bulletins.ErrOnlineDocumentExists) {
		t.Fatalf("restore deletion error=%v", err)
	}
}
