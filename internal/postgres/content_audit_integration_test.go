package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

const (
	postgresContentAuditActor   = "018f47d2-e5d1-7f3f-8f18-6c8e32621f71"
	postgresContentAuditRequest = "request-content-owner-1"
)

func TestContentMutationsCommitOneCanonicalAuditEvent(t *testing.T) {
	db := pageGroupTestDatabase(t)
	now := time.Date(2026, 9, 16, 4, 0, 0, 0, time.UTC)
	ctx := contentAuditContext()
	tests := []struct {
		name, action, module string
		mutate               func(*testing.T, *Repository) string
	}{
		{"create news", "cms.content.create", "news", func(t *testing.T, audited *Repository) string {
			item, err := audited.CreateContent(ctx, content.ModuleNews, contentAuditNewsInput("Created"), postgresContentAuditActor, "audit-content-create", now)
			if err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"update page family", "cms.content.update", "pages", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModuleLocations, contentAuditLocationInput("taipei", "Taipei"), now)
			if _, err := audited.UpdateContent(ctx, item.Module, item.ID, item.Version, contentAuditLocationInput("taipei", "Updated"), postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"restore news", "cms.content.restore", "news", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModuleNews, contentAuditNewsInput("First"), now)
			changed, err := New(db).UpdateContent(context.Background(), item.Module, item.ID, item.Version, contentAuditNewsInput("Second"), postgresContentAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := audited.RestoreContent(ctx, item.Module, item.ID, changed.Version, contentAuditNewsInput("First"), postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"publish news", "cms.content.publish", "news", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModuleNews, contentAuditNewsInput("Publish"), now)
			if _, err := audited.PublishContent(ctx, item.Module, item.ID, item.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"unpublish news", "cms.content.unpublish", "news", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModuleNews, contentAuditNewsInput("Unpublish"), now)
			preparePublishedContentAudit(t, db, item, now)
			if _, err := audited.UnpublishContent(ctx, item.Module, item.ID, item.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"delete news", "cms.content.delete", "news", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModuleNews, contentAuditNewsInput("Delete"), now)
			if err := audited.DeleteContent(ctx, item.Module, item.ID, item.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"publish page", "cms.content.publish", "pages", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModulePages, pageIntegrationInput(t, "privacy-policy", "Privacy"), now)
			if _, err := audited.PublishContent(ctx, item.Module, item.ID, item.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"unpublish page", "cms.content.unpublish", "pages", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModulePages, pageIntegrationInput(t, "privacy-policy", "Privacy"), now)
			published, err := New(db).PublishContent(context.Background(), item.Module, item.ID, item.Version, postgresContentAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := audited.UnpublishContent(ctx, item.Module, item.ID, published.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"publish home workflow", "cms.content.publish", "pages", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModulePages, homeV2IntegrationInput(t, "Home"), now)
			if _, err := audited.PublishContent(ctx, item.Module, item.ID, item.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"publish page group", "cms.content.publish", "pages", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModulePages, aboutGroupPageInput(t, "About"), now)
			if _, err := audited.PublishContent(ctx, item.Module, item.ID, item.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"unpublish page group", "cms.content.unpublish", "pages", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModulePages, aboutGroupPageInput(t, "About"), now)
			published, err := New(db).PublishContent(context.Background(), item.Module, item.ID, item.Version, postgresContentAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := audited.UnpublishContent(ctx, item.Module, item.ID, published.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
		{"restore page group", "cms.content.restore", "pages", func(t *testing.T, audited *Repository) string {
			item := seedContentAudit(t, db, content.ModulePages, aboutGroupPageInput(t, "About"), now)
			published, err := New(db).PublishContent(context.Background(), item.Module, item.ID, item.Version, postgresContentAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := New(db).UpdateContent(context.Background(), item.Module, item.ID, published.Version, aboutGroupPageInput(t, "Changed"), postgresContentAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := audited.RestorePageGroup(ctx, item.ID, published.Version, changed.Version, postgresContentAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return item.ID
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearContentAuditDB(t, db)
			resourceID := test.mutate(t, New(db).WithAudit(NewAuditOutbox(db)))
			events := readBulletinAuditEvents(t, db)
			if len(events) != 1 || events[0].Action != test.action || events[0].ResourceID != resourceID || events[0].ActorID != postgresContentAuditActor || events[0].RequestID != postgresContentAuditRequest || events[0].Metadata["module"] != test.module || len(events[0].Metadata) != 1 {
				t.Fatalf("events=%#v want action=%q resource=%q module=%q", events, test.action, resourceID, test.module)
			}
		})
	}
}

func TestContentMutationAuditFailureRollsBackBusinessState(t *testing.T) {
	db := pageGroupTestDatabase(t)
	clearContentAuditDB(t, db)
	now := time.Date(2026, 9, 16, 4, 0, 0, 0, time.UTC)
	item := seedContentAudit(t, db, content.ModuleLocations, contentAuditLocationInput("taipei", "Before"), now)
	if _, err := db.Exec(`CREATE OR REPLACE FUNCTION hhc_web.reject_content_audit_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.destination='audit-log' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; DROP TRIGGER IF EXISTS reject_content_audit_test ON hhc_web.outbox_event; CREATE TRIGGER reject_content_audit_test BEFORE INSERT ON hhc_web.outbox_event FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_content_audit_test()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS reject_content_audit_test ON hhc_web.outbox_event; DROP FUNCTION IF EXISTS hhc_web.reject_content_audit_test()`)
	})
	_, err := New(db).WithAudit(NewAuditOutbox(db)).UpdateContent(contentAuditContext(), item.Module, item.ID, item.Version, contentAuditLocationInput("taipei", "After"), postgresContentAuditActor, now)
	if !errors.Is(err, content.ErrAuditUnavailable) {
		t.Fatalf("err=%v", err)
	}
	stored, err := New(db).GetContent(context.Background(), item.Module, item.ID)
	if err != nil || stored.Version != item.Version || stored.Translations[0].Title != item.Translations[0].Title {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestContentMutationRequiresTrustedAuditProvenance(t *testing.T) {
	db := pageGroupTestDatabase(t)
	clearContentAuditDB(t, db)
	_, err := New(db).WithAudit(NewAuditOutbox(db)).CreateContent(context.Background(), content.ModuleNews, contentAuditNewsInput("Missing"), postgresContentAuditActor, "missing-content-provenance", time.Now().UTC())
	if !errors.Is(err, content.ErrAuditUnavailable) {
		t.Fatalf("err=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.content_entry`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("content=%d err=%v", count, err)
	}
}

func TestEndStatementPopupEmitsContentUpdate(t *testing.T) {
	db := pageGroupTestDatabase(t)
	clearContentAuditDB(t, db)
	now := time.Date(2026, 9, 16, 4, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	input := contentAuditNewsInput("Statement")
	input.PopupStartsAt, input.PopupEndsAt = &start, &end
	item := seedContentAudit(t, db, content.ModuleNews, input, now)
	pending, err := New(db).PublishContent(context.Background(), item.Module, item.ID, item.Version, postgresContentAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	var event publication.Event
	if err := db.QueryRow(`SELECT id,event_type,aggregate_id,aggregate_version,payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='news.publish.ensure_asset'`, item.ID).Scan(&event.ID, &event.EventType, &event.AggregateID, &event.AggregateVersion, &event.Payload); err != nil {
		t.Fatal(err)
	}
	if err := New(db).CompleteContentPublish(context.Background(), event, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db).WithAudit(NewAuditOutbox(db)).EndStatementPopup(contentAuditContext(), item.ID, pending.Version, postgresContentAuditActor, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	events := readBulletinAuditEvents(t, db)
	if len(events) != 1 || events[0].Action != "cms.content.update" || events[0].ResourceID != item.ID || events[0].Metadata["module"] != "news" {
		t.Fatalf("events=%#v", events)
	}
}

func contentAuditContext() context.Context {
	return auditclient.WithProvenance(context.Background(), postgresContentAuditActor, postgresContentAuditRequest)
}

func clearContentAuditDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`TRUNCATE hhc_web.cms_audit_event,hhc_web.outbox_event,hhc_web.publication_workflow,hhc_web.public_projection,hhc_web.page_publication_manifest,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func seedContentAudit(t *testing.T, db *sql.DB, module content.Module, input content.WriteInput, now time.Time) content.Item {
	t.Helper()
	item, err := New(db).CreateContent(context.Background(), module, input, postgresContentAuditActor, "seed-content-"+string(module)+"-"+input.Translations[0].Title, now)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func contentAuditNewsInput(title string) content.WriteInput {
	return content.WriteInput{Kind: content.KindStatement, Slug: "statement", DisplayDate: "2026-09-16", DetailLayout: "top", Translations: []content.Translation{{Locale: "zh-Hant", Title: title, Body: "Body"}}}
}

func contentAuditLocationInput(key, title string) content.WriteInput {
	return content.WriteInput{LocationKey: key, MapHref: "https://maps.example.com/" + key, SortOrder: 10, Translations: []content.Translation{{Locale: "zh-Hant", Title: title, Body: "Address"}}}
}

func preparePublishedContentAudit(t *testing.T, db *sql.DB, item content.Item, now time.Time) {
	t.Helper()
	payload := []byte(`{"id":"` + item.ID + `","title":"Published"}`)
	if _, err := db.Exec(`UPDATE hhc_web.content_entry SET status='published' WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,$2,$3,'zh-Hant','/zh-Hant/news/statement',$4,'etag',$5,$6)`, "audit-published:"+item.ID, item.Module, item.ID, item.Version, payload, now); err != nil {
		t.Fatal(err)
	}
}
