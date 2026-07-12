package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRepositoryPublishWaitsForAssetWorkflow(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.outbox_event,hhc_web.publication_workflow,hhc_web.public_projection,hhc_web.bulletin_version,hhc_web.bulletin_issue CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := New(db)
	now := time.Now().UTC()
	issue, err := repository.CreateIssue(ctx, "2026-07-12", "user-1", "create-1", now)
	if err != nil {
		t.Fatal(err)
	}
	issue, err = repository.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{Locale: "zh-Hant", Title: "週報", PDFAssetID: "asset-1", PDFFileName: "weekly.pdf"}, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PutVersion(ctx, issue.ID, 1, bulletins.PutVersionInput{Locale: "en", Title: "Weekly", PDFAssetID: "asset-2", PDFFileName: "weekly.pdf"}, "user-1", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatalf("stale update error = %v", err)
	}
	workflow, err := repository.StartPublish(ctx, issue.ID, "zh-Hant", issue.Version, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Status != "waiting_asset_scan" {
		t.Fatalf("workflow = %#v", workflow)
	}
	if _, err := repository.GetPublicLatest(ctx, "zh-Hant"); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatalf("public latest before grant = %v", err)
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset'`, issue.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("outbox events = %d", events)
	}
	event, found, err := repository.Claim(ctx, now, 30*time.Second)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if err := repository.CompletePublish(ctx, event, "grant-1", "https://www.alive.org.tw/api/assets/public/asset-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	public, err := repository.GetPublicLatest(ctx, "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if public.IssueDate != "2026-07-12" || public.DownloadURL != "https://www.alive.org.tw/api/assets/public/asset-1" {
		t.Fatalf("public = %#v", public)
	}
	published, err := repository.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != "published" || published.Versions[0].PublicGrantID != "grant-1" {
		t.Fatalf("published = %#v", published)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM hhc_web.outbox_event WHERE id=$1`, event.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("outbox status = %q", status)
	}
}

var _ publication.Repository = (*Repository)(nil)
