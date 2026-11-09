package postgres

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"testing"
	"time"
)

func TestDelayedDraftUploadPreservesNewAutomaticReprocess(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo.WithAutomaticBulletinExtraction(true).WithLLMBulletinExtraction(true)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_revision WHERE issue_id=$1`, source.Canonical.IssueID)
	})
	issue, err := repo.PutVersion(bulletinAuditContext(), source.Canonical.IssueID, 1, bulletins.PutVersionInput{Series: "general", Locale: "zh-Hant", Title: "test title", Subtitle: "subtitle", PDFAssetID: "review-same-source", PDFFileName: "weekly.pdf"}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	old := claimUploadRequest(t, repo, issue.ID, now)
	state, err := repo.OnlineBulletinState(context.Background(), issue.ID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.QueueOnlineBulletinExtraction(auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, "review-new-authorized-request"), issue.ID, onlinebulletins.QueueExtractionInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, SourceAssetID: "review-same-source", SourceChecksum: source.SourceAssetChecksum, AutoPublish: true}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err = db.QueryRow(`SELECT auto_publish FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, issue.ID).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("setup failed auto=%v err=%v", enabled, err)
	}
	if err = repo.CompleteOnlineExtractionRequest(context.Background(), old, source.SourceAssetChecksum, now); err != nil {
		t.Fatal(err)
	}
	var request string
	if err = db.QueryRow(`SELECT auto_publish,auto_publish_request FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, issue.ID).Scan(&enabled, &request); err != nil {
		t.Fatal(err)
	}
	t.Logf("after delayed draft event: auto_publish=%v request=%q", enabled, request)
	if !enabled || request != "review-new-authorized-request" {
		t.Fatal("delayed draft upload revoked newer explicit publisher authority")
	}
}
