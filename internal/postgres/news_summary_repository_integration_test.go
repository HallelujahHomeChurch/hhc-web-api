package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"os"
	"testing"
	"time"
)

func TestNewsSummarySaveOwnsSummaryAndQueuesOnce(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
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
	repo := New(db)
	now := time.Now().UTC()
	input := content.WriteInput{Slug: "summary-" + platform.NewID(), DisplayDate: "2026-09-06", Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息", Body: "正文", Summary: "forged"}}}
	item, err := repo.CreateContent(ctx, content.ModuleNews, input, "actor", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Translations[0].Summary != "" {
		t.Fatalf("client summary accepted: %q", item.Translations[0].Summary)
	}
	if item.Translations[0].SummaryStatus != "pending" {
		t.Fatalf("status=%s", item.Translations[0].SummaryStatus)
	}
	if _, err = repo.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "actor", now); !errors.Is(err, content.ErrSummaryProcessing) {
		t.Fatalf("publish err=%v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.summary.generate'`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	for i := 0; i < 2; i++ {
		item, err = repo.UpdateContent(ctx, content.ModuleNews, item.ID, item.Version, input, "actor", now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.summary.generate'`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unchanged count=%d err=%v", count, err)
	}
	var version int64
	var status string
	if err = db.QueryRow(`SELECT source_version,status FROM hhc_web.news_summary_state WHERE entry_id=$1`, item.ID).Scan(&version, &status); err != nil || version != 1 || status != "pending" {
		t.Fatalf("version=%d status=%s err=%v", version, status, err)
	}
	input.Translations[0].Body = "另一正文"
	item, err = repo.UpdateContent(ctx, content.ModuleNews, item.ID, item.Version, input, "actor", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT source_version FROM hhc_web.news_summary_state WHERE entry_id=$1`, item.ID).Scan(&version); err != nil || version != item.Version {
		t.Fatalf("version=%d item=%d err=%v", version, item.Version, err)
	}
}

func TestNewsSummaryClaimFencing(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := New(db)
	now := time.Now().UTC()
	input := content.WriteInput{Slug: "summary-" + platform.NewID(), DisplayDate: "2026-09-06", Translations: []content.Translation{{Locale: "zh-Hant", Title: "A", Body: "body"}}}
	item, err := repo.CreateContent(ctx, content.ModuleNews, input, "author", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	var event publication.Event
	if err = db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=2,claimed_until=$2 WHERE aggregate_id=$1 RETURNING id,event_type,aggregate_id,aggregate_version,payload_json,attempts`, item.ID, now.Add(time.Minute)).Scan(&event.ID, &event.EventType, &event.AggregateID, &event.AggregateVersion, &event.Payload, &event.Attempts); err != nil {
		t.Fatal(err)
	}
	old := event
	old.Attempts = 1
	if err = repo.FinishNewsSummary(ctx, old, "stale", "", time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.FinishNewsSummary(ctx, event, "expired lease", "", time.Time{}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	request, current, err := repo.LoadNewsSummary(ctx, event)
	if err != nil || !current || request.Title != "A" || request.Actor != "author" {
		t.Fatalf("request=%+v current=%v err=%v", request, current, err)
	}
	input.AuthorName = "new author"
	item, err = repo.UpdateContent(ctx, content.ModuleNews, item.ID, item.Version, input, "editor", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.FinishNewsSummary(ctx, event, "AI summary", "", time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetContent(ctx, content.ModuleNews, item.ID)
	if err != nil || loaded.Translations[0].Summary != "AI summary" || loaded.Version != item.Version || loaded.AuthorName != "new author" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func completeSummaryFixture(t *testing.T, db *sql.DB, id string, now time.Time) {
	t.Helper()
	rows, err := db.Query(`UPDATE hhc_web.outbox_event SET status='processing',attempts=attempts+1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='news.summary.generate' AND status='pending' RETURNING id,event_type,aggregate_id,aggregate_version,payload_json,attempts`, id, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var events []publication.Event
	for rows.Next() {
		var event publication.Event
		if err := rows.Scan(&event.ID, &event.EventType, &event.AggregateID, &event.AggregateVersion, &event.Payload, &event.Attempts); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, event := range events {
		if err := New(db).FinishNewsSummary(context.Background(), event, "AI fixture summary", "", time.Time{}, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewsSummaryLifecycleVersionsRetryRestoreAndDelete(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := New(db)
	now := time.Now().UTC()
	input := content.WriteInput{Slug: "summary-" + platform.NewID(), DisplayDate: "2026-09-06", Translations: []content.Translation{{Locale: "zh-Hant", Title: "A", Body: "正文"}}}
	item, err := repo.CreateContent(ctx, content.ModuleNews, input, "actor", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(locale string) publication.Event {
		t.Helper()
		var event publication.Event
		if err := db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=attempts+1,claimed_until=$3 WHERE aggregate_id=$1 AND payload_json->>'locale'=$2 AND status='pending' RETURNING id,event_type,aggregate_id,aggregate_version,payload_json,attempts`, item.ID, locale, now.Add(time.Minute)).Scan(&event.ID, &event.EventType, &event.AggregateID, &event.AggregateVersion, &event.Payload, &event.Attempts); err != nil {
			t.Fatal(err)
		}
		return event
	}
	save := func() {
		t.Helper()
		var err error
		item, err = repo.UpdateContent(ctx, content.ModuleNews, item.ID, item.Version, input, "editor", now)
		if err != nil {
			t.Fatal(err)
		}
	}
	load := func() content.Item {
		t.Helper()
		result, err := repo.GetContent(ctx, content.ModuleNews, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := claim("zh-Hant")
	input.Translations[0].Title = "B"
	save()
	second := claim("zh-Hant")
	input.Translations[0].Title = "A"
	save()
	third := claim("zh-Hant")
	if first.AggregateVersion == third.AggregateVersion {
		t.Fatal("A-B-A reused version")
	}
	if err = repo.FinishNewsSummary(ctx, third, "current A", "", time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	for _, event := range []publication.Event{first, second} {
		if err = repo.FinishNewsSummary(ctx, event, "stale", "", time.Time{}, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := load().Translations[0]; got.Summary != "current A" || got.SummaryStatus != "ready" {
		t.Fatalf("old response won: %+v", got)
	}
	input.CoverAssetID = "new-image"
	input.DisplayDate = "2026-09-07"
	input.Translations[0].Summary = "forged"
	save()
	var pending int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND status='pending'`, item.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("metadata triggered work count=%d err=%v", pending, err)
	}
	if got := load().Translations[0].Summary; got != "current A" {
		t.Fatalf("client overwrote %q", got)
	}
	input.Translations = append(input.Translations, content.Translation{Locale: "en", Title: "News", Body: "Body"})
	save()
	english := claim("en")
	if err = repo.FinishNewsSummary(ctx, english, "", "AI_FILTERED", time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "actor", now); !errors.Is(err, content.ErrSummaryProcessing) {
		t.Fatalf("failed publish=%v", err)
	}
	save()
	retried := claim("en")
	if retried.ID != english.ID || retried.Attempts <= english.Attempts {
		t.Fatalf("retry lost claim fencing: old=%+v new=%+v", english, retried)
	}
	if err = repo.FinishNewsSummary(ctx, english, "expired owner", "", time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.FinishNewsSummary(ctx, retried, "English summary", "", time.Time{}, now); err != nil {
		t.Fatal(err)
	}
	input.Translations = input.Translations[:1]
	save()
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.news_summary_state WHERE entry_id=$1 AND locale='en'`, item.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("deleted state count=%d err=%v", pending, err)
	}
	input.Translations[0].Body = "restored body"
	item, err = repo.RestoreContent(ctx, content.ModuleNews, item.ID, item.Version, input, "actor", now)
	if err != nil {
		t.Fatal(err)
	}
	restored := claim("zh-Hant")
	if _, current, err := repo.LoadNewsSummary(ctx, restored); err != nil || !current {
		t.Fatalf("restore current=%v err=%v", current, err)
	}
	if err = repo.DeleteContent(ctx, content.ModuleNews, item.ID, item.Version, "actor", now); err != nil {
		t.Fatal(err)
	}
	if _, current, err := repo.LoadNewsSummary(ctx, restored); err != nil || current {
		t.Fatalf("deleted current=%v err=%v", current, err)
	}
	if err = repo.FinishNewsSummary(ctx, restored, "deleted response", "", time.Time{}, now); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyNewsPublishQueuesSummaryWithoutChangingPublicProjection(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := New(db)
	now := time.Now().UTC()
	input := content.WriteInput{Slug: "summary-" + platform.NewID(), DisplayDate: "2026-09-06", Translations: []content.Translation{{Locale: "zh-Hant", Title: "Legacy", Body: "Body"}}}
	item, err := repo.CreateContent(ctx, content.ModuleNews, input, "actor", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DELETE FROM hhc_web.news_summary_state WHERE entry_id=$1`, `DELETE FROM hhc_web.outbox_event WHERE aggregate_id=$1`, `UPDATE hhc_web.content_translation SET summary='legacy excerpt' WHERE entry_id=$1`} {
		if _, err = db.Exec(query, item.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'news',$2,'zh-Hant','/news/legacy',1,'sentinel','{"summary":"public SEO summary"}',$3)`, "summary:"+item.ID, item.ID, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetContent(ctx, content.ModuleNews, item.ID)
	if err != nil || loaded.Translations[0].SummaryStatus != "pending" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err = repo.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "actor", now); !errors.Is(err, content.ErrSummaryProcessing) {
		t.Fatalf("publish=%v", err)
	}
	var summary string
	if err = db.QueryRow(`SELECT payload_json->>'summary' FROM hhc_web.public_projection WHERE resource_id=$1`, item.ID).Scan(&summary); err != nil || summary != "public SEO summary" {
		t.Fatalf("public=%q err=%v", summary, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.summary.generate'`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("queued=%d err=%v", count, err)
	}
	completeSummaryFixture(t, db, item.ID, now)
	if _, err = repo.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "actor", now); err != nil {
		t.Fatal(err)
	}
}
