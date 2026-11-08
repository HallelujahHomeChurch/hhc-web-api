package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestBulletinTemplateSettingsCASHistoryAndAudit(t *testing.T) {
	db := accessDB(t)
	repo := New(db).WithAudit(NewAuditOutbox(db))
	ctx := bulletinAuditContext()
	initial, err := repo.BulletinTemplateSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var projectionsBefore int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='site_layout'`).Scan(&projectionsBefore); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_template_settings_revision WHERE version>$1`, initial.Version); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(`UPDATE hhc_web.bulletin_template_settings SET version=$1,text_json=$2 WHERE id=true`, initial.Version, mustJSON(initial.TemplateText)); err != nil {
			t.Error(err)
		}
	})
	input := initial.TemplateText
	input.VisionMission = "修改異象文字"
	if _, err = repo.SaveBulletinTemplateSettings(context.Background(), input, initial.Version, postgresBulletinAuditActor, time.Now()); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("unaudited write accepted", err)
	}
	if _, err = New(db).SaveBulletinTemplateSettings(ctx, input, initial.Version, postgresBulletinAuditActor, time.Now()); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("missing outbox accepted", err)
	}
	value, err := repo.SaveBulletinTemplateSettings(ctx, input, initial.Version, postgresBulletinAuditActor, time.Now())
	if err != nil || value.Version != initial.Version+1 || value.TemplateText != input {
		t.Fatal("save", value, err)
	}
	if _, err = repo.SaveBulletinTemplateSettings(ctx, initial.TemplateText, initial.Version, postgresBulletinAuditActor, time.Now()); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale settings accepted", err)
	}
	history, err := repo.BulletinTemplateSettingsHistory(ctx)
	if err != nil || len(history) < 2 || history[0].Version != value.Version || history[1].TemplateText != initial.TemplateText || history[0].CreatedBy != postgresBulletinAuditActor {
		t.Fatal("history changed or missing", history, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'action'='cms.site_settings.update' AND payload_json->>'resourceId'='singleton'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("non-atomic audit", count, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='site_layout'`).Scan(&count); err != nil || count != projectionsBefore {
		t.Fatal("private settings published site layout", count, err)
	}
}

func TestParserUpgradeRetryRetainsTemplateSnapshot(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "frozen", true: "legacy-null"}[legacy], func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			repo, source := seedOnlineLayout(t, db, now)
			repo = repo.WithAudit(NewAuditOutbox(db))
			ctx := bulletinAuditContext()
			initial, err := repo.BulletinTemplateSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_template_settings_revision WHERE version>$1`, initial.Version); err != nil {
					t.Error(err)
				}
				if _, err := db.Exec(`UPDATE hhc_web.bulletin_template_settings SET version=$1,text_json=$2 WHERE id=true`, initial.Version, mustJSON(initial.TemplateText)); err != nil {
					t.Error(err)
				}
			})
			input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 2, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum}
			previous, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed',extractor_version='previous-parser',template_snapshot_json=CASE WHEN $2 THEN NULL ELSE template_snapshot_json END WHERE id=$1`, previous.Job.ID, legacy); err != nil {
				t.Fatal(err)
			}
			changed := initial.TemplateText
			changed.VisionMission = "更新後的設定"
			if _, err = repo.SaveBulletinTemplateSettings(ctx, changed, initial.Version, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			input.OnlineVersion, input.Retry = previous.Version, true
			if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET source_asset_id='different-asset-same-bytes' WHERE id=$1`, previous.Job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now.Add(time.Second)); !errors.Is(err, bulletins.ErrConflict) {
				t.Fatal("upgrade reused another upload's snapshot", err)
			}
			if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET source_asset_id=$2 WHERE id=$1`, previous.Job.ID, input.SourceAssetID); err != nil {
				t.Fatal(err)
			}
			upgraded, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now.Add(time.Second))
			if err != nil || upgraded.Job.ID == previous.Job.ID {
				t.Fatal("upgrade", upgraded, err)
			}
			job, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(2*time.Second), time.Minute, 3)
			if err != nil || job == nil {
				t.Fatal("claim", job, err)
			}
			if legacy && job.TemplateSnapshot != nil || !legacy && (job.TemplateSnapshot == nil || *job.TemplateSnapshot != onlinebulletins.ConversionTemplateSettings()) {
				t.Fatal("parser upgrade replaced frozen input", job.TemplateSnapshot)
			}
		})
	}
}

func TestExtractionTemplateSnapshotSurvivesSettingsUpdateReplayAndRetry(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo = repo.WithAudit(NewAuditOutbox(db))
	ctx := bulletinAuditContext()
	initial, err := repo.BulletinTemplateSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_template_settings_revision WHERE version>$1`, initial.Version); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec(`UPDATE hhc_web.bulletin_template_settings SET version=$1,text_json=$2 WHERE id=true`, initial.Version, mustJSON(initial.TemplateText)); err != nil {
			t.Error(err)
		}
	})
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 2, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum}
	queued, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal("queue", err)
	}
	changed := initial.TemplateText
	changed.VisionMission = "新的未來異象文字"
	if _, err = repo.SaveBulletinTemplateSettings(ctx, changed, initial.Version, postgresBulletinAuditActor, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	input.OnlineVersion = queued.Version
	replay, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now.Add(2*time.Second))
	if err != nil || replay.Job.ID != queued.Job.ID || replay.Version != queued.Version {
		t.Fatal("replay changed job", replay, err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(3*time.Second), time.Minute, 3)
	if err != nil || job == nil || job.ID != queued.Job.ID || job.TemplateSnapshot == nil || *job.TemplateSnapshot != onlinebulletins.ConversionTemplateSettings() {
		t.Fatal("queued snapshot followed mutable settings", job, err)
	}
	if err = repo.FailOnlineBulletinExtraction(ctx, *job, "test_failure", now.Add(4*time.Second), 1); err != nil {
		t.Fatal(err)
	}
	input.Retry = true
	retried, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now.Add(5*time.Second))
	if err != nil || retried.Job.ID != job.ID {
		t.Fatal("retry", retried, err)
	}
	job, err = repo.ClaimOnlineBulletinExtraction(ctx, now.Add(6*time.Second), time.Minute, 3)
	if err != nil || job == nil || job.TemplateSnapshot == nil || *job.TemplateSnapshot != onlinebulletins.ConversionTemplateSettings() {
		t.Fatal("retry recaptured settings", job, err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local.Revision != 1 {
		t.Fatal("settings changed Local", state, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id='replacement' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	input.SourceAssetID, input.SourceChecksum, input.OnlineVersion, input.Retry = "replacement", strings.Repeat("b", 64), retried.Version, false
	replacement, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, postgresBulletinAuditActor, now.Add(7*time.Second))
	if err != nil || replacement.Job.ID == queued.Job.ID {
		t.Fatal("replacement queue", replacement, err)
	}
	job, err = repo.ClaimOnlineBulletinExtraction(ctx, now.Add(8*time.Second), time.Minute, 3)
	if err != nil || job == nil || job.ID != replacement.Job.ID || job.TemplateSnapshot == nil || *job.TemplateSnapshot != onlinebulletins.ConversionTemplateSettings() {
		t.Fatal("new upload did not capture backend rule", job, err)
	}
}
