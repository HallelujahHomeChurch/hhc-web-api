package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

const (
	postgresSiteSettingsAuditActor   = "018f47d2-e5d1-7f3f-8f18-6c8e32621f71"
	postgresSiteSettingsAuditRequest = "request-site-settings-1"
)

func TestSiteSettingsMutationsCommitOneCanonicalAuditEvent(t *testing.T) {
	db := pageGroupTestDatabase(t)
	now := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	ctx := auditclient.WithProvenance(context.Background(), postgresSiteSettingsAuditActor, postgresSiteSettingsAuditRequest)
	tests := []struct {
		name, action string
		mutate       func(*testing.T, *SiteSettingsRepository)
	}{
		{"update", "cms.site_settings.update", func(t *testing.T, repo *SiteSettingsRepository) {
			_, err := repo.Save(ctx, siteSettingsWriteInput(), 1, postgresSiteSettingsAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"publish", "cms.site_settings.publish", func(t *testing.T, repo *SiteSettingsRepository) {
			_, err := repo.Publish(ctx, 1, postgresSiteSettingsAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"unpublish", "cms.site_settings.unpublish", func(t *testing.T, repo *SiteSettingsRepository) {
			published, err := NewSiteSettingsRepository(db).Publish(context.Background(), 1, "seed", now)
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.Unpublish(ctx, published.Version, postgresSiteSettingsAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"restore", "cms.site_settings.restore", func(t *testing.T, repo *SiteSettingsRepository) {
			seeded, err := NewSiteSettingsRepository(db).Save(context.Background(), siteSettingsWriteInput(), 1, "seed", now)
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.Restore(ctx, seeded.Version, seeded.Version, postgresSiteSettingsAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seedSiteSettingsAudit(t, db, now)
			test.mutate(t, NewSiteSettingsRepository(db).WithAudit(NewAuditOutbox(db)))
			events := readBulletinAuditEvents(t, db)
			if len(events) != 1 || events[0].Action != test.action || events[0].ResourceID != "singleton" || events[0].ActorID != postgresSiteSettingsAuditActor || events[0].RequestID != postgresSiteSettingsAuditRequest || len(events[0].Metadata) != 0 {
				t.Fatalf("events=%#v want action=%q", events, test.action)
			}
		})
	}
}

func TestSiteSettingsMutationAuditFailureRollsBackBusinessState(t *testing.T) {
	db := pageGroupTestDatabase(t)
	now := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	seedSiteSettingsAudit(t, db, now)
	if _, err := db.Exec(`CREATE OR REPLACE FUNCTION hhc_web.reject_site_settings_audit_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.destination='audit-log' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; DROP TRIGGER IF EXISTS reject_site_settings_audit_test ON hhc_web.outbox_event; CREATE TRIGGER reject_site_settings_audit_test BEFORE INSERT ON hhc_web.outbox_event FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_site_settings_audit_test()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS reject_site_settings_audit_test ON hhc_web.outbox_event; DROP FUNCTION IF EXISTS hhc_web.reject_site_settings_audit_test()`)
	})
	ctx := auditclient.WithProvenance(context.Background(), postgresSiteSettingsAuditActor, postgresSiteSettingsAuditRequest)
	_, err := NewSiteSettingsRepository(db).WithAudit(NewAuditOutbox(db)).Save(ctx, siteSettingsWriteInput(), 1, postgresSiteSettingsAuditActor, now)
	if !errors.Is(err, sitesettings.ErrAuditUnavailable) {
		t.Fatalf("err=%v", err)
	}
	stored, err := NewSiteSettingsRepository(db).Get(context.Background())
	if err != nil || stored.Version != 1 {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestSiteSettingsMutationRequiresTrustedAuditProvenance(t *testing.T) {
	db := pageGroupTestDatabase(t)
	now := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	seedSiteSettingsAudit(t, db, now)
	_, err := NewSiteSettingsRepository(db).WithAudit(NewAuditOutbox(db)).Save(context.Background(), siteSettingsWriteInput(), 1, postgresSiteSettingsAuditActor, now)
	if !errors.Is(err, sitesettings.ErrAuditUnavailable) {
		t.Fatalf("err=%v", err)
	}
	stored, err := NewSiteSettingsRepository(db).Get(context.Background())
	if err != nil || stored.Version != 1 {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func seedSiteSettingsAudit(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.site_setting_revision,hhc_web.site_setting_locale,hhc_web.site_setting_set,hhc_web.outbox_event CASCADE; DELETE FROM hhc_web.public_projection WHERE resource_type='site_layout'`); err != nil {
		t.Fatal(err)
	}
	input := siteSettingsWriteInput()
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_set(id,status,version,external_links_json,created_by,updated_by,created_at,updated_at) VALUES('default','draft',1,$1,'seed','seed',$2,$2)`, mustJSON(input.Links), now); err != nil {
		t.Fatal(err)
	}
	if err := replaceSiteSettingsRows(ctx, db, input); err != nil {
		t.Fatal(err)
	}
}
