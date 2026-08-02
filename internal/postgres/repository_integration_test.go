package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
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
	issue, err := repository.CreateIssue(ctx, 1700, "2026-07-12", "user-1", "create-1", now)
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
	if event.CreatedAt.IsZero() {
		t.Fatal("claimed event is missing its workflow start time")
	}
	if err := repository.Defer(ctx, event.ID, "asset scan pending", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := db.QueryRowContext(ctx, `SELECT attempts FROM hhc_web.outbox_event WHERE id=$1`, event.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("deferred attempts = %d", attempts)
	}
	event, found, err = repository.Claim(ctx, now.Add(2*time.Minute), 30*time.Second)
	if err != nil || !found {
		t.Fatalf("reclaim found=%v err=%v", found, err)
	}
	if err := repository.CompletePublish(ctx, event, "grant-1", "https://www.alive.org.tw/api/assets/public/asset-1", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompletePublish(ctx, event, "grant-1", "https://www.alive.org.tw/api/assets/public/asset-1", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("replayed publish completion: %v", err)
	}
	public, err := repository.GetPublicLatest(ctx, "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if public.IssueDate != "2026-07-12" || public.DownloadURL != "https://www.alive.org.tw/api/assets/public/asset-1?filename=1700-%E9%80%B1%E5%A0%B1.pdf" || public.DownloadFileName != "1700-週報.pdf" {
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

	replacement, err := repository.PutVersion(ctx, published.ID, published.Version, bulletins.PutVersionInput{
		Locale: "zh-Hant", Title: "新週報", PDFAssetID: "asset-2", PDFFileName: "weekly-2.pdf",
	}, "user-1", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stillPublic, err := repository.GetPublicLatest(ctx, "zh-Hant"); err != nil || stillPublic.DownloadURL != public.DownloadURL {
		t.Fatalf("public during replacement = %#v err=%v", stillPublic, err)
	}
	if _, err := repository.StartPublish(ctx, replacement.ID, "zh-Hant", replacement.Version, "user-1", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	publishReplacement, found, err := repository.Claim(ctx, now.Add(3*time.Minute), 30*time.Second)
	if err != nil || !found {
		t.Fatalf("claim replacement found=%v err=%v", found, err)
	}
	if err := repository.CompletePublish(ctx, publishReplacement, "grant-2", "https://www.alive.org.tw/api/assets/public/asset-2", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	retire, found, err := repository.Claim(ctx, now.Add(4*time.Minute), 30*time.Second)
	if err != nil || !found || retire.EventType != "bulletin.asset.retire" {
		t.Fatalf("retire=%#v found=%v err=%v", retire, found, err)
	}
	if err := repository.Complete(ctx, retire.ID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	current, err := repository.GetIssue(ctx, published.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Unpublish(ctx, current.ID, "zh-Hant", current.Version, "user-1", now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetPublicLatest(ctx, "zh-Hant"); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatalf("public latest after unpublish = %v", err)
	}
	unpublish, found, err := repository.Claim(ctx, now.Add(6*time.Minute), 30*time.Second)
	if err != nil || !found || unpublish.EventType != "bulletin.unpublish.revoke_asset" {
		t.Fatalf("unpublish=%#v found=%v err=%v", unpublish, found, err)
	}
	if err := repository.CompleteUnpublish(ctx, unpublish, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteUnpublish(ctx, unpublish, now.Add(7*time.Minute)); err != nil {
		t.Fatalf("replayed unpublish completion: %v", err)
	}
	unpublished, err := repository.GetIssue(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unpublished.Status != "unpublished" || unpublished.Versions[0].Status != "unpublished" {
		t.Fatalf("unpublished = %#v", unpublished)
	}
}

func TestBulletinDeleteCascadesAndQueuesReferencedAssets(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.cms_audit_event,hhc_web.outbox_event,hhc_web.publication_workflow,hhc_web.public_projection,hhc_web.bulletin_revision,hhc_web.bulletin_version,hhc_web.bulletin_issue CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := New(db)
	now := time.Now().UTC()
	issue, err := repository.CreateIssue(ctx, 1701, "2026-08-23", "user-1", "delete-issue", now)
	if err != nil {
		t.Fatal(err)
	}
	issue, err = repository.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{
		Locale: "zh-Hant", Title: "週報", PDFAssetID: "asset-current", PDFFileName: "weekly.pdf",
	}, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := issue
	snapshot.Versions[0].PDFAssetID = "asset-revision"
	payload, _ := json.Marshal(snapshot)
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_revision(issue_id,version,snapshot_json,created_by,created_at) VALUES($1,99,$2,'user-1',$3)`, issue.ID, payload, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteIssue(ctx, issue.ID, issue.Version-1, "user-1", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatalf("stale delete error=%v", err)
	}
	if err := repository.DeleteIssue(ctx, issue.ID, issue.Version, "user-1", now); err != nil {
		t.Fatal(err)
	}
	var issues, revisions, audit, cleanup int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID).Scan(&issues)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_revision WHERE issue_id=$1`, issue.ID).Scan(&revisions)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.cms_audit_event WHERE resource_id=$1 AND action='delete'`, issue.ID).Scan(&audit)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='asset.owner.delete'`, issue.ID).Scan(&cleanup)
	if issues != 0 || revisions != 0 || audit != 1 || cleanup != 2 {
		t.Fatalf("issues=%d revisions=%d audit=%d cleanup=%d", issues, revisions, audit, cleanup)
	}
}

func TestBulletinRejectsCrossLocaleMutationDuringPublication(t *testing.T) {
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
	issue, err := repository.CreateIssue(ctx, 1702, "2026-08-02", "user-1", "issue-cross-locale", now)
	if err != nil {
		t.Fatal(err)
	}
	issue, err = repository.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{
		Locale: "zh-Hant", Title: "週報", PDFAssetID: "asset-1", PDFFileName: "weekly.pdf",
	}, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartPublish(ctx, issue.ID, "zh-Hant", issue.Version, "user-1", now); err != nil {
		t.Fatal(err)
	}
	current, err := repository.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.PutVersion(ctx, issue.ID, current.Version, bulletins.PutVersionInput{
		Locale: "en", Title: "Weekly", PDFAssetID: "asset-2", PDFFileName: "weekly-en.pdf",
	}, "user-1", now)
	if !errors.Is(err, bulletins.ErrNotPublishable) {
		t.Fatalf("cross-locale mutation error=%v", err)
	}
}

func TestFailedPublishPersistsGrantCompensation(t *testing.T) {
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
	issue, err := repository.CreateIssue(ctx, 1703, "2026-08-09", "user-1", "compensation-issue", now)
	if err != nil {
		t.Fatal(err)
	}
	issue, err = repository.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{
		Locale: "zh-Hant", Title: "週報", PDFAssetID: "asset-1", PDFFileName: "weekly.pdf",
	}, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartPublish(ctx, issue.ID, "zh-Hant", issue.Version, "user-1", now); err != nil {
		t.Fatal(err)
	}
	event, found, err := repository.Claim(ctx, now, 30*time.Second)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if err := repository.FailPublish(ctx, event, "asset-1", "grant-1", "database unavailable", now); err != nil {
		t.Fatal(err)
	}

	var originalStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM hhc_web.outbox_event WHERE id=$1`, event.ID).Scan(&originalStatus); err != nil {
		t.Fatal(err)
	}
	if originalStatus != "failed" {
		t.Fatalf("original status=%q", originalStatus)
	}
	compensation, found, err := repository.Claim(ctx, now, 30*time.Second)
	if err != nil || !found || compensation.EventType != "asset.grant.revoke" {
		t.Fatalf("compensation=%#v found=%v err=%v", compensation, found, err)
	}
	var payload publication.ContentUnpublishPayload
	if err := json.Unmarshal(compensation.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AssetID != "asset-1" || payload.GrantID != "grant-1" {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestNewsPublicationKeepsLiveProjectionUntilReplacement(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.outbox_event,hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.news_item,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := New(db)
	now := time.Now().UTC()
	input := content.WriteInput{
		Slug: "first-news", DisplayDate: "2026-07-30", CoverAssetID: "asset-1",
		Translations: []content.Translation{{Locale: "zh-Hant", Title: "最新消息"}},
	}
	item, err := repository.CreateContent(ctx, content.ModuleNews, input, "user-1", "news-create-1", now)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := input
	conflicting.Slug = "different-news"
	if _, err := repository.CreateContent(ctx, content.ModuleNews, conflicting, "user-1", "news-create-1", now); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("idempotency payload mismatch error=%v", err)
	}
	item, err = repository.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "user-1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != content.StatusPublishing {
		t.Fatalf("status=%q", item.Status)
	}
	event, found, err := repository.Claim(ctx, now.Add(time.Minute), 30*time.Second)
	if err != nil || !found || event.EventType != "news.publish.ensure_asset" {
		t.Fatalf("event=%#v found=%v err=%v", event, found, err)
	}
	if err := repository.CompleteContentPublish(ctx, event, "grant-1", "/api/assets/public/asset-1", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteContentPublish(ctx, event, "grant-1", "/api/assets/public/asset-1", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("replayed news publish completion: %v", err)
	}
	public, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 20)
	if err != nil || len(public) != 1 || public[0].ImageURL != "/api/assets/public/asset-1/large" {
		t.Fatalf("public=%#v err=%v", public, err)
	}
	detail, etag, err := repository.PublicNews(ctx, "zh-Hant", "first-news")
	if err != nil || detail.ID != item.ID || etag == "" {
		t.Fatalf("detail=%#v etag=%q err=%v", detail, etag, err)
	}
	fallback, fallbackETag, err := repository.PublicNews(ctx, "en", "first-news")
	if err != nil || fallback.Title != "最新消息" || fallbackETag != etag {
		t.Fatalf("fallback detail=%#v etag=%q err=%v", fallback, fallbackETag, err)
	}
	english, err := repository.PublicContent(ctx, content.ModuleNews, "en", 20)
	if err != nil || len(english) != 1 || english[0].Title != "最新消息" {
		t.Fatalf("fallback list=%#v err=%v", english, err)
	}

	input.CoverAssetID = "asset-2"
	input.Translations[0].Title = "更新消息"
	item, err = repository.UpdateContent(ctx, content.ModuleNews, item.ID, item.Version, input, "user-1", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !item.IsPublished || item.Status != content.StatusDraft {
		t.Fatalf("draft=%#v", item)
	}
	item, err = repository.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "user-1", now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	stillPublic, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 20)
	if err != nil || len(stillPublic) != 1 || stillPublic[0].ImageURL != "/api/assets/public/asset-1/large" {
		t.Fatalf("public during replacement=%#v err=%v", stillPublic, err)
	}
	replacement, found, err := repository.Claim(ctx, now.Add(4*time.Minute), 30*time.Second)
	if err != nil || !found || replacement.EventType != "news.publish.ensure_asset" {
		t.Fatalf("replacement=%#v found=%v err=%v", replacement, found, err)
	}
	if err := repository.CompleteContentPublish(ctx, replacement, "grant-2", "/api/assets/public/asset-2", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	detail, replacementETag, err := repository.PublicNews(ctx, "zh-Hant", "first-news")
	if err != nil || detail.Title != "更新消息" || detail.ImageURL != "/api/assets/public/asset-2/large" || replacementETag == etag {
		t.Fatalf("replacement detail=%#v etag=%q err=%v", detail, replacementETag, err)
	}
	retire, found, err := repository.Claim(ctx, now.Add(5*time.Minute), 30*time.Second)
	if err != nil || !found || retire.EventType != "asset.grant.revoke" {
		t.Fatalf("retire=%#v found=%v err=%v", retire, found, err)
	}
	if err := repository.Complete(ctx, retire.ID, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}

	item, err = repository.GetContent(ctx, content.ModuleNews, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	item, err = repository.UnpublishContent(ctx, content.ModuleNews, item.ID, item.Version, "user-1", now.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != content.StatusUnpublishing {
		t.Fatalf("status=%q", item.Status)
	}
	if values, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 20); err != nil || len(values) != 0 {
		t.Fatalf("public after unpublish request=%#v err=%v", values, err)
	}
	if _, _, err := repository.PublicNews(ctx, "zh-Hant", "first-news"); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("detail after unpublish request err=%v", err)
	}
	unpublish, found, err := repository.Claim(ctx, now.Add(7*time.Minute), 30*time.Second)
	if err != nil || !found || unpublish.EventType != "news.unpublish.revoke_asset" {
		t.Fatalf("unpublish=%#v found=%v err=%v", unpublish, found, err)
	}
	if err := repository.CompleteContentUnpublish(ctx, unpublish, now.Add(8*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteContentUnpublish(ctx, unpublish, now.Add(8*time.Minute)); err != nil {
		t.Fatalf("replayed news unpublish completion: %v", err)
	}
	item, err = repository.GetContent(ctx, content.ModuleNews, item.ID)
	if err != nil || item.IsPublished || item.Status != content.StatusUnpublished {
		t.Fatalf("unpublished=%#v err=%v", item, err)
	}
}

func TestContentRepublishRemovesDeletedLocaleProjection(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.video_item,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := New(db)
	now := time.Now().UTC()
	input := content.WriteInput{
		YouTubeVideoID: "K3ckFWeSQ-k",
		Translations: []content.Translation{
			{Locale: "zh-Hant", Title: "影片"},
			{Locale: "en", Title: "Video"},
		},
	}
	item, err := repository.CreateContent(ctx, content.ModuleVideos, input, "user-1", "video-locales", now)
	if err != nil {
		t.Fatal(err)
	}
	item, err = repository.PublishContent(ctx, content.ModuleVideos, item.ID, item.Version, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	input.Translations = input.Translations[:1]
	item, err = repository.UpdateContent(ctx, content.ModuleVideos, item.ID, item.Version, input, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PublishContent(ctx, content.ModuleVideos, item.ID, item.Version, "user-1", now); err != nil {
		t.Fatal(err)
	}
	english, err := repository.PublicContent(ctx, content.ModuleVideos, "en", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(english) != 1 || english[0].Title != "影片" {
		t.Fatalf("English fallback=%#v", english)
	}
}

func TestContentDeleteCascadesRevisionsAndKeepsAudit(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.cms_audit_event,hhc_web.outbox_event,hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.video_item,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}

	repository := New(db)
	now := time.Now().UTC()
	item, err := repository.CreateContent(ctx, content.ModuleVideos, content.WriteInput{
		YouTubeVideoID: "K3ckFWeSQ-k",
		Translations:   []content.Translation{{Locale: "zh-Hant", Title: "影片"}},
	}, "user-1", "video-delete", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteContent(ctx, content.ModuleVideos, item.ID, item.Version-1, "user-1", now); !errors.Is(err, content.ErrPrecondition) {
		t.Fatalf("stale delete error=%v", err)
	}
	if err := repository.DeleteContent(ctx, content.ModuleVideos, item.ID, item.Version, "user-1", now); err != nil {
		t.Fatal(err)
	}
	var entries, revisions, audit int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.content_entry WHERE id=$1`, item.ID).Scan(&entries)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.content_revision WHERE entry_id=$1`, item.ID).Scan(&revisions)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.cms_audit_event WHERE resource_id=$1 AND action='delete'`, item.ID).Scan(&audit)
	if entries != 0 || revisions != 0 || audit != 1 {
		t.Fatalf("entries=%d revisions=%d audit=%d", entries, revisions, audit)
	}
}

func TestNewsDeleteRejectsPublicState(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.news_item,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}

	repository := New(db)
	now := time.Now().UTC()
	item, err := repository.CreateContent(ctx, content.ModuleNews, content.WriteInput{
		Slug:        "delete-guard",
		DisplayDate: "2026-07-31",
		Translations: []content.Translation{{
			Locale: "zh-Hant",
			Title:  "刪除保護",
		}},
	}, "user-1", "news-delete-guard", now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO hhc_web.public_projection(
			projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at
		) VALUES('news:delete-guard','news',$1,'zh-Hant','/zh-Hant/news/delete-guard',1,'etag','{}',$2)`,
		item.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteContent(ctx, content.ModuleNews, item.ID, item.Version, "user-1", now); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("projection delete error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE hhc_web.news_item SET public_grant_id='grant-1' WHERE entry_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteContent(ctx, content.ModuleNews, item.ID, item.Version, "user-1", now); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("grant delete error=%v", err)
	}
}

func TestContentListSearchesTitlesAndUsesStableTypedSorting(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.news_item,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}

	repository := New(db)
	now := time.Now().UTC()
	values := []content.WriteInput{
		{Slug: "alpha-old", DisplayDate: "2026-07-01", Translations: []content.Translation{{Locale: "zh-Hant", Title: "Alpha 舊消息", Body: strings.Repeat("x", 1000)}, {Locale: "en", Title: "Alpha old"}}},
		{Slug: "beta", DisplayDate: "2026-07-02", Translations: []content.Translation{{Locale: "zh-Hant", Title: "Beta 消息"}}},
		{Slug: "alpha-new", DisplayDate: "2026-07-03", Translations: []content.Translation{{Locale: "zh-Hant", Title: "最新 ALPHA"}}},
	}
	for index, input := range values {
		if _, err := repository.CreateContent(ctx, content.ModuleNews, input, "user-1", input.Slug, now.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	page, err := repository.ListContent(ctx, content.ModuleNews, content.ListOptions{
		Query: "alpha", Status: content.StatusDraft, Sort: "displayDate", Direction: "asc", Page: 1, PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].Slug != "alpha-old" || len(page.Items[0].Translations) != 2 {
		t.Fatalf("page=%#v", page)
	}
	if page.Items[0].Translations[1].Body != "" {
		t.Fatalf("list response includes body")
	}
	page, err = repository.ListContent(ctx, content.ModuleNews, content.ListOptions{
		Query: "alpha", Status: content.StatusDraft, Sort: "displayDate", Direction: "asc", Page: 2, PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Slug != "alpha-new" {
		t.Fatalf("page=%#v", page)
	}
}

func TestHistoryUsesCanonicalEventDateOrderingAndIndex(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}

	repository := New(db)
	now := time.Now().UTC()
	dates := []string{"1988", "", "1990-09-02", "1988-03"}
	for index, eventDate := range dates {
		item, err := repository.CreateContent(ctx, content.ModuleHistory, content.WriteInput{
			EventDate: eventDate,
			Translations: []content.Translation{{
				Locale: "zh-Hant", Title: "事件 " + eventDate, Body: "內容", DateLabel: "顯示日期",
			}},
		}, "user-1", fmt.Sprintf("history-%d", index), now.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.PublishContent(ctx, content.ModuleHistory, item.ID, item.Version, "user-1", now); err != nil {
			t.Fatal(err)
		}
	}

	admin, err := repository.ListContent(ctx, content.ModuleHistory, content.ListOptions{
		Sort: "eventDate", Direction: "desc", Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := historyDates(admin.Items); strings.Join(got, ",") != "1990-09-02,1988-03,1988," {
		t.Fatalf("admin dates=%v", got)
	}

	public, err := repository.PublicContent(ctx, content.ModuleHistory, "zh-Hant", 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := publicHistoryDates(public); strings.Join(got, ",") != "1988,1988-03,1990-09-02," {
		t.Fatalf("public dates=%v", got)
	}

	var indexDefinition string
	if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname='hhc_web' AND indexname='history_event_event_date_idx'`).Scan(&indexDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexDefinition, "event_date DESC NULLS LAST, entry_id DESC") {
		t.Fatalf("unexpected history date index: %s", indexDefinition)
	}
}

func historyDates(items []content.Item) []string {
	values := make([]string, len(items))
	for index := range items {
		values[index] = items[index].EventDate
	}
	return values
}

func publicHistoryDates(items []content.PublicItem) []string {
	values := make([]string, len(items))
	for index := range items {
		values[index] = items[index].EventDate
	}
	return values
}

var _ publication.Repository = (*Repository)(nil)
