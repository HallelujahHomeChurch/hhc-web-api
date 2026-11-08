package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStatementDeletedAfterImageRemovalRetiresHistoricalAsset(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	r := New(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	input := content.WriteInput{Kind: content.KindStatement, Slug: "statement-" + platform.NewID(), DisplayDate: "2026-09-20", Translations: []content.Translation{{Locale: "zh-Hant", Title: "聲明", Body: "原文", BodyJSON: json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"photo","type":"image","assetId":"asset-old","alt":{"mode":"text","text":"照片"}}]}`)}}}
	item, err := r.CreateContent(ctx, content.ModuleNews, input, "actor", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE aggregate_id=$1`, item.ID)
		_, _ = db.Exec(`DELETE FROM hhc_web.content_entry WHERE id=$1`, item.ID)
	})
	input.Translations[0].BodyJSON = json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"copy","type":"paragraph","content":[{"type":"text","text":"原文"}]}]}`)
	updated, err := r.UpdateContent(ctx, content.ModuleNews, item.ID, item.Version, input, "actor", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteContent(ctx, content.ModuleNews, item.ID, updated.Version, "actor", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var retired int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='asset.owner.delete' AND payload_json->>'assetId'='asset-old'`, item.ID).Scan(&retired); err != nil || retired != 1 {
		t.Fatalf("historical asset delete event count=%d err=%v", retired, err)
	}
}

func TestStatementStoredWithoutGeneratedSummary(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	end := now.Add(time.Hour)
	r := New(db)
	in := content.WriteInput{Kind: content.KindStatement, Slug: "statement-" + platform.NewID(), DisplayDate: "2026-09-07", PopupStartsAt: &now, PopupEndsAt: &end, Translations: []content.Translation{{Locale: "zh-Hant", Title: "聲明", Body: "原文", Summary: "人工摘要"}}}
	item, err := r.CreateContent(ctx, content.ModuleNews, in, "actor", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != content.KindStatement || item.PopupStartsAt == nil || item.Translations[0].Summary != "人工摘要" {
		t.Fatalf("statement fields not retained: %+v", item)
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.summary.generate'`, item.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("summary generated: %d %v", n, err)
	}
}

func TestStatementPublicationWindowAndPublicIsolation(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	now := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	end := now.Add(time.Hour)
	makeItem := func(start, finish time.Time) content.Item {
		in := content.WriteInput{Kind: content.KindStatement, Slug: "statement-" + platform.NewID(), DisplayDate: "2026-09-07", PopupStartsAt: &start, PopupEndsAt: &finish, Translations: []content.Translation{{Locale: "zh-Hant", Title: "聲明", Body: "原文"}}}
		item, err := r.CreateContent(ctx, content.ModuleNews, in, "actor", platform.NewID(), now)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Exec(`DELETE FROM hhc_web.public_projection WHERE resource_id=$1`, item.ID)
			db.Exec(`DELETE FROM hhc_web.content_entry WHERE id=$1`, item.ID)
		})
		return item
	}
	first := makeItem(now, end)
	second := makeItem(now, end)
	request := content.StatementNotificationRequest{RequestID: platform.NewID(), Channels: []string{"email", "web_push"}, Subject: "教會聲明", Body: "請閱讀聲明全文"}
	pending, err := r.PublishStatement(ctx, first.ID, first.Version, "actor", request, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.PublishContent(ctx, content.ModuleNews, second.ID, second.Version, "actor", now); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("pending overlap accepted: %v", err)
	}
	var event publication.Event
	event.AggregateID = first.ID
	if err = db.QueryRow(`SELECT id,payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.publish.ensure_asset'`, first.ID).Scan(&event.ID, &event.Payload); err != nil {
		t.Fatal(err)
	}
	event.EventType = "news.publish.ensure_asset"
	event.AggregateVersion = pending.Version
	if err = r.CompleteContentPublish(ctx, event, nil, now); err != nil {
		t.Fatal(err)
	}
	var submission publication.NotificationSubmissionPayload
	var submissionPayload []byte
	if err = db.QueryRow(`SELECT payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='content.notification.submit'`, first.ID).Scan(&submissionPayload); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(submissionPayload, &submission); err != nil || len(submission.Notifications) != 2 || submission.Notifications[0].Translations["zh-Hant"].Body != request.Body {
		t.Fatalf("notification submission: %+v %v", submission, err)
	}
	if err = r.CompleteContentPublish(ctx, event, nil, now); err != nil {
		t.Fatal(err)
	}
	var submissionCount int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='content.notification.submit'`, first.ID).Scan(&submissionCount); err != nil || submissionCount != 1 {
		t.Fatalf("completion duplicate count=%d err=%v", submissionCount, err)
	}
	active, err := r.ActiveStatement(ctx, "ja", now)
	if err != nil || active.Statement == nil || active.Statement.ResolvedLocale != "zh-Hant" || active.Statement.Body != "原文" {
		t.Fatalf("active fallback: %+v %v", active, err)
	}
	page, err := r.PublicContent(ctx, content.ModuleNews, "zh-Hant", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Kind == content.KindStatement {
			t.Fatal("statement leaked into news")
		}
	}
	detail, _, err := r.PublicNews(ctx, "ja", first.Slug)
	if err != nil || !strings.Contains(detail.Href, "/statements/") {
		t.Fatalf("detail path: %+v %v", detail, err)
	}
	active, err = r.ActiveStatement(ctx, "zh-Hant", end)
	if err != nil || active.Statement != nil {
		t.Fatalf("expiry: %+v %v", active, err)
	}
	if _, _, err = r.PublicNews(ctx, "zh-Hant", first.Slug); err != nil {
		t.Fatal("expiry removed article", err)
	}

	ended, err := r.EndStatementPopup(ctx, first.ID, pending.Version, "actor", now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ended.PopupEndsAt == nil || !ended.PopupEndsAt.Equal(now.Add(30*time.Minute)) {
		t.Fatal("end not saved")
	}
	if ended.PublishedPopupEndsAt == nil || !ended.PublishedPopupEndsAt.Equal(end) {
		t.Fatal("draft overwrote published window before completion")
	}
	// Complete the end revision, then unpublish without changing the accepted notification.
	if err = db.QueryRow(`SELECT id,payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.publish.ensure_asset' AND aggregate_version=$2`, first.ID, ended.Version).Scan(&event.ID, &event.Payload); err != nil {
		t.Fatal(err)
	}
	event.AggregateVersion = ended.Version
	if err = r.CompleteContentPublish(ctx, event, nil, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	unpublished, err := r.UnpublishContent(ctx, content.ModuleNews, first.ID, ended.Version, "actor", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.PublicNews(ctx, "zh-Hant", first.Slug); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("unpublished detail: %v", err)
	}
	var cancellationCount int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='statement.notification.queue' AND idempotency_key LIKE '%:cancel'`, first.ID).Scan(&cancellationCount); err != nil || cancellationCount != 0 {
		t.Fatalf("unpublish coupled to notification cancellation count=%d err=%v", cancellationCount, err)
	}
	if err = db.QueryRow(`SELECT id,payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.unpublish.revoke_asset' AND aggregate_version=$2`, first.ID, unpublished.Version).Scan(&event.ID, &event.Payload); err != nil {
		t.Fatal(err)
	}
	event.AggregateVersion = unpublished.Version
	if err = r.CompleteContentUnpublish(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	if err = r.DeleteContent(ctx, content.ModuleNews, first.ID, unpublished.Version, "actor", now); err != nil {
		t.Fatal(err)
	}
	var retainedPayload []byte
	if err = db.QueryRow(`SELECT payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='content.notification.submit'`, first.ID).Scan(&retainedPayload); err != nil || string(retainedPayload) != string(submissionPayload) {
		t.Fatalf("source deletion changed notification payload=%s err=%v", retainedPayload, err)
	}

	next := makeItem(end, end.Add(time.Hour))
	if _, err = r.PublishContent(ctx, content.ModuleNews, next.ID, next.Version, "actor", now); err != nil {
		t.Fatal("adjacent window blocked", err)
	}
}

func TestStatementConcurrentPublication(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	start := time.Date(2042, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	items := make([]content.Item, 2)
	for i := range items {
		items[i], err = r.CreateContent(ctx, content.ModuleNews, content.WriteInput{Kind: content.KindStatement, Slug: "statement-" + platform.NewID(), DisplayDate: "2026-09-07", PopupStartsAt: &start, PopupEndsAt: &end, Translations: []content.Translation{{Locale: "zh-Hant", Title: "聲明", Body: "原文"}}}, "actor", platform.NewID(), start)
		if err != nil {
			t.Fatal(err)
		}
		id := items[i].ID
		t.Cleanup(func() {
			if _, err := db.Exec(`DELETE FROM hhc_web.content_entry WHERE id=$1`, id); err != nil {
				t.Error(err)
			}
		})
	}
	barrier := make(chan struct{})
	results := make(chan error, 2)
	for _, item := range items {
		go func() {
			<-barrier
			_, err := r.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "actor", start)
			results <- err
		}()
	}
	close(barrier)
	successes, conflicts := 0, 0
	for range items {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, content.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestGeneralNewsPublicationNotifications(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	now := time.Now().UTC()
	item, err := r.CreateContent(ctx, content.ModuleNews, content.WriteInput{Kind: content.KindGeneral, Slug: "news-" + platform.NewID(), DisplayDate: "2026-09-07", DetailLayout: "top", Translations: []content.Translation{{Locale: "zh-Hant", Title: "最新消息", Body: "內容"}}}, "actor", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.news_summary_state SET status='ready' WHERE entry_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	request := content.StatementNotificationRequest{RequestID: platform.NewID(), Channels: []string{"email", "web_push"}, Subject: "最新消息", Body: "閱讀全文"}
	pending, err := r.PublishStatement(ctx, item.ID, item.Version, "actor", request, now)
	if err != nil {
		t.Fatal(err)
	}
	event := publication.Event{AggregateID: item.ID, AggregateVersion: pending.Version, EventType: "news.publish.ensure_asset"}
	if err = db.QueryRow(`SELECT id,payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.publish.ensure_asset'`, item.ID).Scan(&event.ID, &event.Payload); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = r.CompleteContentPublish(ctx, event, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	var payload publication.NotificationSubmissionPayload
	var encoded []byte
	if err = db.QueryRow(`SELECT payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='content.notification.submit'`, item.ID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, &payload); err != nil || len(payload.Notifications) != 2 || payload.Notifications[0].Translations["zh-Hant"].ActionURL != "/zh-Hant/news/"+item.Slug || payload.Notifications[0].Translations["zh-Hant"].Subject != "最新消息" {
		t.Fatalf("wrong news notification: %+v err=%v", payload, err)
	}
	if _, err = r.UnpublishContent(ctx, content.ModuleNews, item.ID, pending.Version, "actor", now); err != nil {
		t.Fatal(err)
	}
	var cancelCount int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND idempotency_key LIKE '%:cancel'`, item.ID).Scan(&cancelCount); err != nil || cancelCount != 0 {
		t.Fatalf("unpublish queued cancellation count=%d err=%v", cancelCount, err)
	}
}
