package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestBulletinRevisionRestoreKeepsPublicProjectionAndCreatesDraft(t *testing.T) {
	databaseURL := os.Getenv("HHW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.bulletin_revision,hhc_web.outbox_event,hhc_web.publication_workflow,hhc_web.public_projection,hhc_web.bulletin_version,hhc_web.bulletin_issue CASCADE`); err != nil {
		t.Fatal(err)
	}

	repository := New(db)
	now := time.Now().UTC()
	issue, err := repository.CreateIssue(ctx, 1732, "2026-08-02", "user-1", "revision-issue", now)
	if err != nil {
		t.Fatal(err)
	}
	issue, err = repository.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{
		Locale: "zh-Hant", Title: "原始週報", PDFAssetID: "asset-zh", PDFFileName: "weekly.pdf",
	}, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	targetRevision := issue.Version
	issue, err = repository.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{
		Locale: "en", Title: "Weekly", PDFAssetID: "asset-en", PDFFileName: "weekly-en.pdf",
	}, "user-1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreIssueRevision(ctx, issue.ID, targetRevision, issue.Version-1, "user-2", now.Add(2*time.Minute)); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatalf("stale restore error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES('revision-public','bulletin_issue',$1,'zh-Hant','/bulletins/2026-08-02',2,'etag','{"sentinel":"unchanged"}',$2)`, issue.ID, now); err != nil {
		t.Fatal(err)
	}

	restored, err := repository.RestoreIssueRevision(ctx, issue.ID, targetRevision, issue.Version, "user-2", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != "draft" || restored.Version != issue.Version+1 || len(restored.Versions) != 1 || restored.Versions[0].Locale != "zh-Hant" || restored.Versions[0].Title != "原始週報" {
		t.Fatalf("restored=%#v", restored)
	}
	var projection string
	if err := db.QueryRowContext(ctx, `SELECT payload_json::text FROM hhc_web.public_projection WHERE projection_key='revision-public'`).Scan(&projection); err != nil {
		t.Fatal(err)
	}
	if projection != `{"sentinel": "unchanged"}` {
		t.Fatalf("projection=%s", projection)
	}
	revisions, err := repository.IssueRevisions(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 3 || revisions[0].Version != restored.Version || revisions[0].Snapshot.Status != "draft" {
		t.Fatalf("revisions=%#v", revisions)
	}
	retainedAsset := false
	for _, version := range revisions[1].Snapshot.Versions {
		retainedAsset = retainedAsset || version.PDFAssetID == "asset-en"
	}
	if len(revisions[1].Snapshot.Versions) != 2 || !retainedAsset {
		t.Fatalf("revision asset retention=%#v", revisions[1])
	}

	changedNumber := 1700
	conflicting := restored
	conflicting.IssueNumber = &changedNumber
	conflicting.IssueDate = "2026-07-26"
	payload, _ := json.Marshal(conflicting)
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_revision(issue_id,version,snapshot_json,created_by,created_at) VALUES($1,99,$2,'user-1',$3)`, issue.ID, payload, now); err != nil {
		t.Fatal(err)
	}
	beforeRevisionCount := len(revisions) + 1
	if _, err := repository.RestoreIssueRevision(ctx, issue.ID, 99, restored.Version, "user-2", now.Add(3*time.Minute)); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatalf("public metadata-changing restore error=%v", err)
	}
	unchanged, err := repository.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != restored.Version || unchanged.Status != restored.Status || unchanged.IssueNumber == nil || *unchanged.IssueNumber != 1732 || unchanged.IssueDate != "2026-08-02" {
		t.Fatalf("issue changed after rejected restore=%#v", unchanged)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload_json::text FROM hhc_web.public_projection WHERE projection_key='revision-public'`).Scan(&projection); err != nil {
		t.Fatal(err)
	}
	if projection != `{"sentinel": "unchanged"}` {
		t.Fatalf("projection changed after rejected restore=%s", projection)
	}
	revisions, err = repository.IssueRevisions(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != beforeRevisionCount {
		t.Fatalf("revision audit changed after rejected restore: before=%d after=%d", beforeRevisionCount, len(revisions))
	}
}
