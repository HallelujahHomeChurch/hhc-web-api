package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestTranslationRateLimitsAndAudit(t *testing.T) {
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
	repository := New(db)
	now := time.Date(2026, 8, 11, 12, 34, 45, 0, time.FixedZone("test", 8*60*60))
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `DELETE FROM hhc_web.translation_cooldown; DELETE FROM hhc_web.translation_rate_limit; DELETE FROM hhc_web.cms_audit_event WHERE action='translation_preview'`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("actor and deployment limits roll back atomically", func(t *testing.T) {
		reset(t)
		if err := reserveTranslation(ctx, repository, "actor-a", now, 1, 3, 1); err != nil {
			t.Fatal(err)
		}
		if err := reserveTranslation(ctx, repository, "actor-a", now, 1, 3, 2); !errors.Is(err, translation.ErrRateLimited) {
			t.Fatalf("actor limit error = %v", err)
		}
		var deploymentCount int
		if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.translation_rate_limit WHERE scope='deployment:minute'`).Scan(&deploymentCount); err != nil {
			t.Fatal(err)
		}
		if deploymentCount != 1 {
			t.Fatalf("deployment count after actor rollback = %d", deploymentCount)
		}
		for _, actor := range []string{"actor-b", "actor-c"} {
			if err := reserveTranslation(ctx, repository, actor, now, 1, 3, 1); err != nil {
				t.Fatal(err)
			}
		}
		if err := reserveTranslation(ctx, repository, "actor-d", now, 1, 3, 1); !errors.Is(err, translation.ErrRateLimited) {
			t.Fatalf("deployment limit error = %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.translation_rate_limit WHERE scope='deployment:minute'`).Scan(&deploymentCount); err != nil {
			t.Fatal(err)
		}
		if deploymentCount != 3 {
			t.Fatalf("deployment count = %d", deploymentCount)
		}
	})

	t.Run("minute rollover", func(t *testing.T) {
		reset(t)
		if err := reserveTranslation(ctx, repository, "actor-a", now, 1, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := reserveTranslation(ctx, repository, "actor-a", now.Add(time.Minute), 1, 1, 2); err != nil {
			t.Fatal(err)
		}
		var rows int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.translation_rate_limit`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 6 {
			t.Fatalf("counter rows = %d", rows)
		}
	})

	t.Run("concurrent actor limit", func(t *testing.T) {
		reset(t)
		runConcurrentReservations(t, repository, now, 20, 5, 20, func(int) string { return "same-actor" }, 5)
		var actorCount, deploymentCount int
		if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.translation_rate_limit WHERE scope='actor:minute:same-actor'`).Scan(&actorCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.translation_rate_limit WHERE scope='deployment:minute'`).Scan(&deploymentCount); err != nil {
			t.Fatal(err)
		}
		if actorCount != 5 || deploymentCount != 5 {
			t.Fatalf("actor count = %d, deployment count = %d", actorCount, deploymentCount)
		}
	})

	t.Run("concurrent deployment limit", func(t *testing.T) {
		reset(t)
		runConcurrentReservations(t, repository, now, 20, 1, 7, func(index int) string { return fmt.Sprintf("actor-%d", index) }, 7)
		var deploymentCount, actorCount int
		if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.translation_rate_limit WHERE scope='deployment:minute'`).Scan(&deploymentCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(sum(count),0) FROM hhc_web.translation_rate_limit WHERE scope LIKE 'actor:minute:%'`).Scan(&actorCount); err != nil {
			t.Fatal(err)
		}
		if deploymentCount != 7 || actorCount != 7 {
			t.Fatalf("deployment count = %d, actor count = %d", deploymentCount, actorCount)
		}
	})

	t.Run("old windows are deleted", func(t *testing.T) {
		reset(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.translation_rate_limit(scope,window_start,count) VALUES('old', $1, 1)`, now.UTC().Add(-72*time.Hour).Truncate(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := reserveTranslation(ctx, repository, "actor-a", now, 1, 1, 1); err != nil {
			t.Fatal(err)
		}
		var old int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.translation_rate_limit WHERE scope='old'`).Scan(&old); err != nil {
			t.Fatal(err)
		}
		if old != 0 {
			t.Fatalf("old counter rows = %d", old)
		}
	})

	t.Run("resource target cooldown and source-version bypass", func(t *testing.T) {
		reset(t)
		reservation := translation.Reservation{
			Actor: "actor-a", ResourceType: "news", ResourceID: "10000000-0000-4000-8000-000000000001", SourceVersion: 1, TargetLocale: "ja", Now: now,
			ActorMinuteLimit: 10, DeploymentMinuteLimit: 10, ActorDailyLimit: 10, DeploymentDailyLimit: 10, Cooldown: 10 * time.Minute,
		}
		if err := repository.ReserveTranslation(ctx, reservation); err != nil {
			t.Fatal(err)
		}
		var limited *translation.RateLimitError
		if err := repository.ReserveTranslation(ctx, reservation); !errors.As(err, &limited) || limited.RetryAfter != 10*time.Minute {
			t.Fatalf("cooldown error = %#v", err)
		}
		reservation.SourceVersion = 2
		if err := repository.ReserveTranslation(ctx, reservation); err != nil {
			t.Fatalf("new source version was blocked: %v", err)
		}
		reservation.SourceVersion = 1
		reservation.Now = now.Add(10 * time.Minute)
		if err := repository.ReserveTranslation(ctx, reservation); err != nil {
			t.Fatalf("expired cooldown was blocked: %v", err)
		}
	})

	t.Run("failed preview releases only cooldown and retains attempt counters", func(t *testing.T) {
		reset(t)
		reservation := translation.Reservation{
			Actor: "actor-a", ResourceType: "news", ResourceID: "10000000-0000-4000-8000-000000000001", SourceVersion: 1, TargetLocale: "ja", Now: now,
			ActorMinuteLimit: 10, DeploymentMinuteLimit: 10, ActorDailyLimit: 10, DeploymentDailyLimit: 10, Cooldown: 10 * time.Minute,
		}
		if err := repository.ReserveTranslation(ctx, reservation); err != nil {
			t.Fatal(err)
		}
		if err := repository.ReleaseTranslation(ctx, reservation); err != nil {
			t.Fatal(err)
		}
		reservation.Now = now.Add(time.Second)
		if err := repository.ReserveTranslation(ctx, reservation); err != nil {
			t.Fatalf("released cooldown still blocked: %v", err)
		}
		var actorMinute, actorDay int
		if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.translation_rate_limit WHERE scope='actor:minute:actor-a'`).Scan(&actorMinute); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.translation_rate_limit WHERE scope='actor:day:actor-a'`).Scan(&actorDay); err != nil {
			t.Fatal(err)
		}
		if actorMinute != 2 || actorDay != 2 {
			t.Fatalf("attempt counters = minute %d day %d", actorMinute, actorDay)
		}
	})

	t.Run("actor and deployment daily budgets return UTC reset", func(t *testing.T) {
		reset(t)
		for version := int64(1); version <= 2; version++ {
			reservation := translation.Reservation{Actor: "actor-a", ResourceType: "news", ResourceID: "resource-a", SourceVersion: version, TargetLocale: "ja", Now: now.Add(time.Duration(version) * time.Minute), ActorMinuteLimit: 10, DeploymentMinuteLimit: 10, ActorDailyLimit: 2, DeploymentDailyLimit: 10, Cooldown: time.Minute}
			if err := repository.ReserveTranslation(ctx, reservation); err != nil {
				t.Fatal(err)
			}
		}
		actorThird := translation.Reservation{Actor: "actor-a", ResourceType: "news", ResourceID: "resource-a", SourceVersion: 3, TargetLocale: "ja", Now: now.Add(3 * time.Minute), ActorMinuteLimit: 10, DeploymentMinuteLimit: 10, ActorDailyLimit: 2, DeploymentDailyLimit: 10, Cooldown: time.Minute}
		var actorLimited *translation.RateLimitError
		if err := repository.ReserveTranslation(ctx, actorThird); !errors.As(err, &actorLimited) || actorLimited.RetryAfter != time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).Sub(actorThird.Now.UTC()) {
			t.Fatalf("actor daily error = %#v", err)
		}

		reset(t)
		for index, actor := range []string{"actor-a", "actor-b"} {
			if err := repository.ReserveTranslation(ctx, translation.Reservation{Actor: actor, ResourceType: "news", ResourceID: "resource", SourceVersion: int64(index + 1), TargetLocale: "ja", Now: now.Add(time.Duration(index) * time.Minute), ActorMinuteLimit: 10, DeploymentMinuteLimit: 10, ActorDailyLimit: 10, DeploymentDailyLimit: 2, Cooldown: time.Minute}); err != nil {
				t.Fatal(err)
			}
		}
		deploymentThird := translation.Reservation{Actor: "actor-c", ResourceType: "news", ResourceID: "resource", SourceVersion: 3, TargetLocale: "ja", Now: now.Add(2 * time.Minute), ActorMinuteLimit: 10, DeploymentMinuteLimit: 10, ActorDailyLimit: 10, DeploymentDailyLimit: 2, Cooldown: time.Minute}
		var deploymentLimited *translation.RateLimitError
		if err := repository.ReserveTranslation(ctx, deploymentThird); !errors.As(err, &deploymentLimited) || deploymentLimited.RetryAfter != time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).Sub(deploymentThird.Now.UTC()) {
			t.Fatalf("deployment daily error = %#v", err)
		}
	})

	t.Run("concurrent identical target reserves once", func(t *testing.T) {
		reset(t)
		reservation := translation.Reservation{Actor: "actor-a", ResourceType: "news", ResourceID: "resource", SourceVersion: 1, TargetLocale: "ja", Now: now, ActorMinuteLimit: 100, DeploymentMinuteLimit: 100, ActorDailyLimit: 100, DeploymentDailyLimit: 100, Cooldown: 10 * time.Minute}
		start := make(chan struct{})
		var success atomic.Int32
		var wait sync.WaitGroup
		for range 20 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				err := repository.ReserveTranslation(context.Background(), reservation)
				if err == nil {
					success.Add(1)
				} else if !errors.Is(err, translation.ErrRateLimited) {
					t.Errorf("reservation error = %v", err)
				}
			}()
		}
		close(start)
		wait.Wait()
		if success.Load() != 1 {
			t.Fatalf("successful reservations = %d", success.Load())
		}
	})

	t.Run("audit stores metadata allowlist only", func(t *testing.T) {
		reset(t)
		event := translation.AuditEvent{
			Action: "translation_preview", ResourceType: "news", ResourceID: "10000000-0000-4000-8000-000000000001", Actor: "actor-1",
			SourceVersion: 12, SourceLocale: "zh-Hant", TargetLocale: "ja", Provider: "azure-openai", Deployment: "cms-translator",
			PromptVersion: translation.PromptVersion, CharacterCount: 321, Duration: 1250 * time.Millisecond, Outcome: "succeeded", CreatedAt: now,
		}
		if err := repository.RecordTranslationAudit(ctx, event); err != nil {
			t.Fatal(err)
		}
		var action, resourceType, resourceID, actor string
		var payload []byte
		var createdAt time.Time
		if err := db.QueryRowContext(ctx, `SELECT action,resource_type,resource_id::text,actor,payload_json,created_at FROM hhc_web.cms_audit_event WHERE resource_id=$1`, event.ResourceID).Scan(&action, &resourceType, &resourceID, &actor, &payload, &createdAt); err != nil {
			t.Fatal(err)
		}
		if action != event.Action || resourceType != event.ResourceType || resourceID != event.ResourceID || actor != event.Actor || !createdAt.Equal(now.UTC()) {
			t.Fatalf("audit columns = %q %q %q %q %s", action, resourceType, resourceID, actor, createdAt)
		}
		var metadata map[string]any
		if err := json.Unmarshal(payload, &metadata); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"sourceVersion": float64(12), "sourceLocale": "zh-Hant", "targetLocale": "ja", "provider": "azure-openai", "deployment": "cms-translator",
			"promptVersion": translation.PromptVersion, "characterCount": float64(321), "durationMs": float64(1250), "outcome": "succeeded",
		}
		if !reflect.DeepEqual(metadata, want) {
			t.Fatalf("audit metadata = %#v", metadata)
		}
	})
}

func runConcurrentReservations(t *testing.T, repository *Repository, now time.Time, attempts, actorLimit, deploymentLimit int, actor func(int) string, wantSuccess int32) {
	t.Helper()
	start := make(chan struct{})
	errorsFound := make(chan error, attempts)
	var success atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			err := reserveTranslation(context.Background(), repository, actor(index), now, actorLimit, deploymentLimit, int64(index+1))
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, translation.ErrRateLimited) {
				errorsFound <- err
			}
		}(index)
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("reservation error = %v", err)
	}
	if success.Load() != wantSuccess {
		t.Fatalf("successful reservations = %d, want %d", success.Load(), wantSuccess)
	}
}

func reserveTranslation(ctx context.Context, repository *Repository, actor string, now time.Time, actorLimit, deploymentLimit int, sourceVersion int64) error {
	return repository.ReserveTranslation(ctx, translation.Reservation{
		Actor: actor, ResourceType: "news", ResourceID: "10000000-0000-4000-8000-000000000001", SourceVersion: sourceVersion, TargetLocale: "ja", Now: now,
		ActorMinuteLimit: actorLimit, DeploymentMinuteLimit: deploymentLimit, ActorDailyLimit: 1_000, DeploymentDailyLimit: 10_000, Cooldown: 10 * time.Minute,
	})
}

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
	workflow, err := repository.StartPublish(ctx, issue.ID, "zh-Hant", issue.Version, false, "user-1", now)
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
	byNumber, err := repository.GetPublicByNumber(ctx, 1700, "zh-Hant")
	if err != nil || byNumber.DownloadURL != public.DownloadURL {
		t.Fatalf("public by number = %#v err=%v", byNumber, err)
	}
	if _, err := repository.GetPublicByNumber(ctx, 1700, "en"); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatalf("missing locale by number error = %v", err)
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
	if _, err := repository.StartPublish(ctx, replacement.ID, "zh-Hant", replacement.Version, false, "user-1", now.Add(3*time.Minute)); err != nil {
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

func TestRepositoryRejectsJapaneseAndKoreanBulletinLifecycle(t *testing.T) {
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

	now := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	service := bulletins.NewService(New(db), func() time.Time { return now })
	issue, err := service.CreateIssue(ctx, bulletins.CreateIssueInput{IssueNumber: 1733, IssueDate: "2026-08-16"}, "user-1", "three-edition-bulletin")
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"ja", "ko"} {
		if _, err := service.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{Locale: locale, Title: "Weekly", PDFAssetID: "asset-" + locale, PDFFileName: "weekly.pdf"}, "user-1"); !errors.Is(err, bulletins.ErrInvalid) {
			t.Fatalf("locale=%s error=%v", locale, err)
		}
	}
	var versions, events int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_version WHERE issue_id=$1`, issue.ID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1`, issue.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if versions != 0 || events != 0 {
		t.Fatalf("versions=%d events=%d", versions, events)
	}
}

func TestRepositoryQueuesOneNotificationAfterSuccessfulBulletinPublish(t *testing.T) {
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
	now := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)
	issue, err := repository.CreateIssue(ctx, 1732, "2026-08-09", "user-1", "notify-create", now)
	if err != nil {
		t.Fatal(err)
	}
	issue, err = repository.PutVersion(ctx, issue.ID, issue.Version, bulletins.PutVersionInput{Locale: "zh-Hant", Title: "繁體標題", Subtitle: "繁體副標", PDFAssetID: "asset-zh", PDFFileName: "weekly.pdf"}, "user-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartPublish(ctx, issue.ID, "zh-Hant", issue.Version, true, "user-1", now); err != nil {
		t.Fatal(err)
	}
	publish, found, err := repository.Claim(ctx, now, 30*time.Second)
	if err != nil || !found {
		t.Fatalf("claim publish found=%v err=%v", found, err)
	}
	if err := repository.CompletePublish(ctx, publish, "grant-zh", "https://www.alive.org.tw/assets/asset-zh", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompletePublish(ctx, publish, "grant-zh", "https://www.alive.org.tw/assets/asset-zh", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	notification, found, err := repository.Claim(ctx, now.Add(time.Minute), 30*time.Second)
	if err != nil || !found || notification.EventType != "bulletin.notification.queue" {
		t.Fatalf("notification=%#v found=%v err=%v", notification, found, err)
	}
	var notificationPayload publication.BulletinNotificationPayload
	if err := json.Unmarshal(notification.Payload, &notificationPayload); err != nil {
		t.Fatal(err)
	}
	if len(notificationPayload.Translations) != 3 || notificationPayload.Translations["en"].Body != englishBulletinNotificationFallback || notificationPayload.Translations["zh-Hant"].Body != "繁體副標" {
		t.Fatalf("notification payload=%#v", notificationPayload)
	}
	if err := repository.Fail(ctx, notification, "engagement timeout", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	failed, err := repository.GetIssue(ctx, issue.ID)
	if err != nil || failed.NotificationStatus != "failed" || failed.NotificationErrorCode != "NOTIFICATION_QUEUE_FAILED" {
		t.Fatalf("failed issue=%#v err=%v", failed, err)
	}

	withEnglish, err := repository.PutVersion(ctx, failed.ID, failed.Version, bulletins.PutVersionInput{Locale: "en", Title: "English title", PDFAssetID: "asset-en", PDFFileName: "weekly-en.pdf"}, "user-1", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartPublish(ctx, withEnglish.ID, "en", withEnglish.Version, true, "user-1", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	publishEnglish, found, err := repository.Claim(ctx, now.Add(3*time.Minute), 30*time.Second)
	if err != nil || !found || publishEnglish.EventType != "bulletin.publish.ensure_asset" {
		t.Fatalf("english publish=%#v found=%v err=%v", publishEnglish, found, err)
	}
	if err := repository.CompletePublish(ctx, publishEnglish, "grant-en", "https://www.alive.org.tw/assets/asset-en", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	retriedNotification, found, err := repository.Claim(ctx, now.Add(4*time.Minute), 30*time.Second)
	if err != nil || !found || retriedNotification.ID != notification.ID || string(retriedNotification.Payload) != string(notification.Payload) {
		t.Fatalf("retried notification=%#v found=%v err=%v", retriedNotification, found, err)
	}
	var notificationCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.queue'`, issue.ID).Scan(&notificationCount); err != nil || notificationCount != 1 {
		t.Fatalf("notification count=%d err=%v", notificationCount, err)
	}
	if err := repository.CompleteNotification(ctx, retriedNotification, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	queued, err := repository.GetIssue(ctx, issue.ID)
	if err != nil || queued.NotificationStatus != "queued" || queued.NotificationQueuedAt == nil {
		t.Fatalf("queued issue=%#v err=%v", queued, err)
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
	if _, err := repository.StartPublish(ctx, issue.ID, "zh-Hant", issue.Version, false, "user-1", now); err != nil {
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
	if _, err := repository.StartPublish(ctx, issue.ID, "zh-Hant", issue.Version, false, "user-1", now); err != nil {
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
		Slug: "first-news", AuthorName: "王牧師", DisplayDate: "2026-07-30", CoverAssetID: "asset-1",
		Translations: []content.Translation{{Locale: "zh-Hant", Title: "最新消息"}, {Locale: "en", Title: "News"}},
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
	published := []publication.PublishedAsset{{Usage: "detail", AssetID: "asset-1", GrantID: "grant-1", PublicURL: "/api/assets/public/asset-1"}}
	firstSuccessfulPublish := now.Add(2 * time.Minute)
	expectedFirstPublishedAt := firstSuccessfulPublish.Truncate(time.Microsecond)
	if err := repository.CompleteContentPublish(ctx, event, published, firstSuccessfulPublish); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteContentPublish(ctx, event, published, firstSuccessfulPublish); err != nil {
		t.Fatalf("replayed news publish completion: %v", err)
	}
	public, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 1, 20)
	if err != nil || len(public.Items) != 1 || public.Items[0].ImageURL != "/api/assets/public/asset-1/large" || public.Total != 1 {
		t.Fatalf("public=%#v err=%v", public, err)
	}
	detail, etag, err := repository.PublicNews(ctx, "zh-Hant", "first-news")
	if err != nil || detail.ID != item.ID || etag == "" || detail.AuthorName != "王牧師" || detail.FirstPublishedAt == nil || !detail.FirstPublishedAt.Equal(expectedFirstPublishedAt) || detail.LastPublishedAt == nil || !detail.LastPublishedAt.Equal(expectedFirstPublishedAt) {
		t.Fatalf("detail=%#v etag=%q err=%v", detail, etag, err)
	}
	fallback, fallbackETag, err := repository.PublicNews(ctx, "ja", "first-news")
	if err != nil || fallback.Title != "最新消息" || fallbackETag == etag || fallback.ResolvedLocale != "zh-Hant" || strings.Join(fallback.AvailableLocales, ",") != "zh-Hant,en" || fallback.Href != "/ja/news/first-news" {
		t.Fatalf("fallback detail=%#v etag=%q err=%v", fallback, fallbackETag, err)
	}
	japanese, err := repository.PublicContent(ctx, content.ModuleNews, "ja", 1, 20)
	if err != nil || len(japanese.Items) != 1 || japanese.Items[0].Title != "最新消息" || japanese.Items[0].ResolvedLocale != "zh-Hant" || strings.Join(japanese.Items[0].AvailableLocales, ",") != "zh-Hant,en" || japanese.Items[0].Href != "/ja/news/first-news" {
		t.Fatalf("Japanese fallback list=%#v err=%v", japanese, err)
	}
	english, err := repository.PublicContent(ctx, content.ModuleNews, "en", 1, 20)
	if err != nil || len(english.Items) != 1 || english.Items[0].Title != "News" || english.Items[0].ResolvedLocale != "en" || strings.Join(english.Items[0].AvailableLocales, ",") != "zh-Hant,en" {
		t.Fatalf("English list=%#v err=%v", english, err)
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
	stillPublic, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 1, 20)
	if err != nil || len(stillPublic.Items) != 1 || stillPublic.Items[0].ImageURL != "/api/assets/public/asset-1/large" {
		t.Fatalf("public during replacement=%#v err=%v", stillPublic, err)
	}
	replacement, found, err := repository.Claim(ctx, now.Add(4*time.Minute), 30*time.Second)
	if err != nil || !found || replacement.EventType != "news.publish.ensure_asset" {
		t.Fatalf("replacement=%#v found=%v err=%v", replacement, found, err)
	}
	replacementAssets := []publication.PublishedAsset{{Usage: "detail", AssetID: "asset-2", GrantID: "grant-2", PublicURL: "/api/assets/public/asset-2"}}
	republishTime := now.Add(5 * time.Minute)
	expectedRepublishTime := republishTime.Truncate(time.Microsecond)
	if err := repository.CompleteContentPublish(ctx, replacement, replacementAssets, republishTime); err != nil {
		t.Fatal(err)
	}
	detail, replacementETag, err := repository.PublicNews(ctx, "zh-Hant", "first-news")
	if err != nil || detail.Title != "更新消息" || detail.ImageURL != "/api/assets/public/asset-2/large" || replacementETag == etag || detail.FirstPublishedAt == nil || !detail.FirstPublishedAt.Equal(expectedFirstPublishedAt) || detail.LastPublishedAt == nil || !detail.LastPublishedAt.Equal(expectedRepublishTime) {
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
	if values, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 1, 20); err != nil || len(values.Items) != 0 {
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
	if err != nil || item.IsPublished || item.Status != content.StatusUnpublished || item.FirstPublishedAt == nil || !item.FirstPublishedAt.Equal(expectedFirstPublishedAt) || item.PublishedAt == nil || !item.PublishedAt.Equal(expectedRepublishTime) {
		t.Fatalf("unpublished=%#v err=%v", item, err)
	}
}

func TestNewsPublicationSupportsZeroOrTwoImages(t *testing.T) {
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

	for _, test := range []struct {
		name, detailID, homeID, detailURL, homeURL string
		expectedAssets                             int
	}{
		{name: "without images"},
		{name: "with detail and home images", detailID: "asset-detail", homeID: "asset-home", detailURL: "/assets/asset-detail", homeURL: "/assets/asset-home", expectedAssets: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.outbox_event,hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.news_item,hhc_web.content_entry CASCADE`); err != nil {
				t.Fatal(err)
			}
			repository := New(db)
			now := time.Now().UTC()
			input := content.WriteInput{
				Slug: "optional-images", DisplayDate: "2026-08-03", CoverAssetID: test.detailID, HomeCoverAssetID: test.homeID,
				Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息"}},
			}
			item, err := repository.CreateContent(ctx, content.ModuleNews, input, "user-1", "optional-images-"+test.name, now)
			if err != nil {
				t.Fatal(err)
			}
			item, err = repository.PublishContent(ctx, content.ModuleNews, item.ID, item.Version, "user-1", now)
			if err != nil {
				t.Fatal(err)
			}
			event, found, err := repository.Claim(ctx, now, 30*time.Second)
			if err != nil || !found {
				t.Fatalf("claim found=%v err=%v", found, err)
			}
			assets := []publication.PublishedAsset{}
			if test.detailID != "" {
				assets = append(assets, publication.PublishedAsset{Usage: "detail", AssetID: test.detailID, GrantID: "grant-detail", PublicURL: test.detailURL})
			}
			if test.homeID != "" {
				assets = append(assets, publication.PublishedAsset{Usage: "home", AssetID: test.homeID, GrantID: "grant-home", PublicURL: test.homeURL})
			}
			if err := repository.CompleteContentPublish(ctx, event, assets, now); err != nil {
				t.Fatal(err)
			}
			public, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 1, 20)
			if err != nil || len(public.Items) != 1 || public.Items[0].ImageURL != suffixLarge(test.detailURL) || public.Items[0].HomeImageURL != suffixLarge(test.homeURL) {
				t.Fatalf("public=%#v err=%v", public, err)
			}
			item, err = repository.GetContent(ctx, content.ModuleNews, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.UnpublishContent(ctx, content.ModuleNews, item.ID, item.Version, "user-1", now); err != nil {
				t.Fatal(err)
			}
			unpublish, found, err := repository.Claim(ctx, now, 30*time.Second)
			if err != nil || !found {
				t.Fatalf("unpublish claim found=%v err=%v", found, err)
			}
			var payload publication.ContentUnpublishPayload
			if err := json.Unmarshal(unpublish.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Assets) != test.expectedAssets {
				t.Fatalf("assets=%#v", payload.Assets)
			}
		})
	}
}

func suffixLarge(value string) string {
	if value == "" {
		return ""
	}
	return value + "/large"
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
	seedPageGroupPage(t, ctx, repository, "home", now)
	service := content.NewService(repository, func() time.Time { return now })
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
	input.DeleteLocales = []string{"en"}
	item, err = service.UpdateContent(ctx, content.ModuleVideos, item.ID, item.Version, input, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PublishContent(ctx, content.ModuleVideos, item.ID, item.Version, "user-1", now); err != nil {
		t.Fatal(err)
	}
	english, err := repository.PublicContent(ctx, content.ModuleVideos, "en", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(english.Items) != 1 || english.Items[0].Title != "影片" {
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
	seedPageGroupPage(t, ctx, repository, "home", now)
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
	seedPageGroupPage(t, ctx, repository, "about", now)
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

	public, err := repository.PublicContent(ctx, content.ModuleHistory, "zh-Hant", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := publicHistoryDates(public.Items); strings.Join(got, ",") != "1988,1988-03,1990-09-02," {
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

func TestLocationContentLifecycle(t *testing.T) {
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
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	service := content.NewService(repository, func() time.Time { return now })
	taipeiInput := locationWriteInput("taipei", "https://maps.example.com/taipei", 10)
	taipei, err := service.CreateContent(ctx, content.ModuleLocations, taipeiInput, "admin", "location-taipei")
	if err != nil {
		t.Fatal(err)
	}
	if public, err := service.PublicLocations(ctx, "ja"); err != nil || len(public) != 0 {
		t.Fatalf("draft public=%#v err=%v", public, err)
	}
	admin, err := service.ListContent(ctx, content.ModuleLocations, content.ListOptions{})
	if err != nil || len(admin.Items) != 1 || admin.Items[0].LocationKey != "taipei" || admin.Items[0].MapHref != taipeiInput.MapHref || admin.Items[0].Translations[0].Body == "" {
		t.Fatalf("admin=%#v err=%v", admin, err)
	}

	zhongli, err := service.CreateContent(ctx, content.ModuleLocations, locationWriteInput("zhongli", "https://maps.example.com/zhongli", 2), "admin", "location-zhongli")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []content.Item{taipei, zhongli} {
		if _, err := service.PublishContent(ctx, content.ModuleLocations, item.ID, item.Version, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	var projectionCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='locations' AND resource_id=$1`, taipei.ID).Scan(&projectionCount); err != nil || projectionCount != 5 {
		t.Fatalf("projectionCount=%d err=%v", projectionCount, err)
	}
	public, err := service.PublicLocations(ctx, "ja")
	if err != nil || len(public) != 2 || public[0].ID != "zhongli" || public[1].ID != "taipei" || public[0].Name != "ja-zhongli" {
		t.Fatalf("public=%#v err=%v", public, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type='locations' AND resource_id=$1 AND locale='ja'`, zhongli.ID); err != nil {
		t.Fatal(err)
	}
	public, err = service.PublicLocations(ctx, "ja")
	if err != nil || len(public) != 1 || public[0].ID != "taipei" {
		t.Fatalf("exact locale public=%#v err=%v", public, err)
	}

	taipei, err = service.GetContent(ctx, content.ModuleLocations, taipei.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := locationWriteInput("taipei", "https://maps.example.com/taipei-new", 30)
	for index := range changed.Translations {
		changed.Translations[index].Body += "-changed"
	}
	if taipei, err = service.UpdateContent(ctx, content.ModuleLocations, taipei.ID, taipei.Version, changed, "admin"); err != nil {
		t.Fatal(err)
	}
	if taipei, err = service.PublishContent(ctx, content.ModuleLocations, taipei.ID, taipei.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	beforeRestore, err := service.PublicLocations(ctx, "en")
	if err != nil || beforeRestore[1].Address != "en-taipei-address-changed" || beforeRestore[1].MapHref != changed.MapHref {
		t.Fatalf("published change=%#v err=%v", beforeRestore, err)
	}
	if taipei, err = service.RestoreContent(ctx, content.ModuleLocations, taipei.ID, 1, taipei.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	stillPublic, err := service.PublicLocations(ctx, "en")
	if err != nil || stillPublic[1].Address != "en-taipei-address-changed" || stillPublic[1].MapHref != changed.MapHref {
		t.Fatalf("restore changed projection=%#v err=%v", stillPublic, err)
	}
	if taipei, err = service.PublishContent(ctx, content.ModuleLocations, taipei.ID, taipei.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		items, err := service.PublicLocations(ctx, locale)
		if err != nil || len(items) == 0 {
			t.Fatalf("locale=%s items=%#v err=%v", locale, items, err)
		}
		taipei := items[len(items)-1]
		if taipei.ID != "taipei" || taipei.Name != locale+"-taipei" || taipei.Address != locale+"-taipei-address" || taipei.MapHref != taipeiInput.MapHref {
			t.Fatalf("locale=%s items=%#v", locale, items)
		}
	}
	zhongli, err = service.GetContent(ctx, content.ModuleLocations, zhongli.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UnpublishContent(ctx, content.ModuleLocations, zhongli.ID, zhongli.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	public, err = service.PublicLocations(ctx, "zh-Hant")
	if err != nil || len(public) != 1 || public[0].ID != "taipei" {
		t.Fatalf("unpublished public=%#v err=%v", public, err)
	}
}

func TestFixedEditorialPageLifecycle(t *testing.T) {
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
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	service := content.NewService(repository, func() time.Time { return now })
	homeInput := aboutGroupPageInput(t, "About")
	home, err := repository.CreateContent(ctx, content.ModulePages, homeInput, "content-seed:pages-v1", "page:about", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.PublicEditorialPage(ctx, "about", "ja"); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("draft public err=%v", err)
	}
	home, err = service.PublishContent(ctx, content.ModulePages, home.ID, home.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	first, firstETag, err := service.PublicEditorialPage(ctx, "about", "ja")
	if err != nil || first.ResolvedLocale != "ja" || len(first.AvailableLocales) != 5 || first.Version != home.Version {
		t.Fatalf("first=%#v etag=%q err=%v", first, firstETag, err)
	}
	changed := aboutGroupPageInput(t, "Changed About")
	home, err = service.UpdateContent(ctx, content.ModulePages, home.ID, home.Version, changed, "admin")
	if err != nil {
		t.Fatal(err)
	}
	stillPublic, stillETag, err := service.PublicEditorialPage(ctx, "about", "ja")
	if err != nil || stillETag != firstETag || string(stillPublic.Content) != string(first.Content) {
		t.Fatalf("draft changed public=%#v etag=%q err=%v", stillPublic, stillETag, err)
	}
	home, err = service.PublishContent(ctx, content.ModulePages, home.ID, home.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	publishedChange, changedETag, err := service.PublicEditorialPage(ctx, "about", "ja")
	if err != nil || changedETag == firstETag || !strings.Contains(string(publishedChange.Content), "Changed About") {
		t.Fatalf("changed=%#v etag=%q err=%v", publishedChange, changedETag, err)
	}
	changedETags := make(map[string]string, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		_, etag, err := service.PublicEditorialPage(ctx, "about", locale)
		if err != nil {
			t.Fatalf("changed %s: %v", locale, err)
		}
		changedETags[locale] = etag
	}
	home, err = service.RestoreContent(ctx, content.ModulePages, home.ID, 2, home.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if afterRestore, etag, err := service.PublicEditorialPage(ctx, "about", "ja"); err != nil || etag != changedETag || string(afterRestore.Content) != string(publishedChange.Content) {
		t.Fatalf("restore changed public=%#v etag=%q err=%v", afterRestore, etag, err)
	}
	home, err = service.PublishContent(ctx, content.ModulePages, home.ID, home.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		restored, etag, err := service.PublicEditorialPage(ctx, "about", locale)
		var payload struct {
			Data struct {
				HeroTitle string `json:"heroTitle"`
			} `json:"data"`
		}
		payloadErr := json.Unmarshal(restored.Content, &payload)
		if err != nil || payloadErr != nil || restored.Version != home.Version || !restored.PublishedAt.Equal(*home.PublishedAt) || etag == changedETags[locale] || payload.Data.HeroTitle != "About" {
			t.Fatalf("restored %s=%#v etag=%q changed=%q heroTitle=%q err=%v payloadErr=%v", locale, restored, etag, changedETags[locale], payload.Data.HeroTitle, err, payloadErr)
		}
	}
	revisions, err := service.ContentRevisions(ctx, content.ModulePages, home.ID)
	if err != nil || len(revisions) != 3 || revisions[0].GroupManifest == nil {
		t.Fatalf("revisions=%d err=%v", len(revisions), err)
	}
	privacyInput := pageIntegrationInput(t, "privacy-policy", "Privacy")
	privacy, err := repository.CreateContent(ctx, content.ModulePages, privacyInput, "content-seed:pages-v1", "page:privacy-policy", now)
	if err != nil {
		t.Fatal(err)
	}
	privacy, err = service.PublishContent(ctx, content.ModulePages, privacy.ID, privacy.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE projection_key='page:ko:privacy-policy'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.PublicEditorialPage(ctx, "privacy-policy", "ko"); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("legal fallback err=%v", err)
	}
	termsInput := pageIntegrationInput(t, "terms-of-use", "Terms")
	terms, err := repository.CreateContent(ctx, content.ModulePages, termsInput, "content-seed:pages-v1", "page:terms-of-use", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_fixed_page_projection_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection ADD CONSTRAINT test_fixed_page_projection_failure CHECK (projection_key <> 'page:ko:terms-of-use')`); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_fixed_page_projection_failure`)
	if _, err := repository.PublishContent(ctx, content.ModulePages, terms.ID, terms.Version, "admin", now); err == nil {
		t.Fatal("page publish unexpectedly succeeded with a projection write failure")
	}
	rolledBack, err := repository.GetContent(ctx, content.ModulePages, terms.ID)
	if err != nil || rolledBack.Status != content.StatusDraft || rolledBack.Version != terms.Version {
		t.Fatalf("rolledBack=%#v err=%v", rolledBack, err)
	}
	var projectionCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hhc_web.public_projection WHERE resource_id=$1`, terms.ID).Scan(&projectionCount); err != nil || projectionCount != 0 {
		t.Fatalf("projectionCount=%d err=%v", projectionCount, err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT test_fixed_page_projection_failure`); err != nil {
		t.Fatal(err)
	}
	home, err = service.GetContent(ctx, content.ModulePages, home.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UnpublishContent(ctx, content.ModulePages, home.ID, home.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.PublicEditorialPage(ctx, "about", "ja"); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("unpublished err=%v", err)
	}
}

func TestPageGroupChildMutation(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		module  content.Module
		pageKey string
		input   content.WriteInput
	}{
		{content.ModuleVideos, "home", content.WriteInput{YouTubeVideoID: "K3ckFWeSQ-k", Translations: []content.Translation{{Locale: "zh-Hant", Title: "影片"}}}},
		{content.ModuleHistory, "about", content.WriteInput{EventDate: "1988-03", Translations: []content.Translation{{Locale: "zh-Hant", Title: "沿革", Body: "事件"}}}},
	}
	for _, test := range tests {
		t.Run(string(test.module), func(t *testing.T) {
			if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
				t.Fatal(err)
			}
			page, err := repository.CreateContent(ctx, content.ModulePages, pageGroupPageInput(test.pageKey), "seed", "page:"+test.pageKey, now)
			if err != nil {
				t.Fatal(err)
			}
			child, err := repository.CreateContent(ctx, test.module, test.input, "admin", "child:"+string(test.module), now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil || page.Version != 2 || page.Status != content.StatusDraft {
				t.Fatalf("page after create=%#v err=%v", page, err)
			}

			if _, err := repository.CreateContent(ctx, test.module, test.input, "admin", "child:"+string(test.module), now.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			unchanged, err := repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil || unchanged.Version != page.Version {
				t.Fatalf("page after idempotent replay=%#v err=%v", unchanged, err)
			}

			changedInput := test.input
			if test.module == content.ModuleVideos {
				changedInput.HomeEligible = true
			} else {
				changedInput.EventDate = "1988-04"
			}
			child, err = repository.UpdateContent(ctx, test.module, child.ID, child.Version, changedInput, "admin", now.Add(3*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil || page.Version != 3 {
				t.Fatalf("page after update=%#v err=%v", page, err)
			}

			for _, status := range []string{content.StatusPublishing, content.StatusUnpublishing} {
				if _, err := db.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status=$2 WHERE id=$1`, page.ID, status); err != nil {
					t.Fatal(err)
				}
				if _, err := repository.CreateContent(ctx, test.module, test.input, "admin", "blocked:"+status+":"+string(test.module), now.Add(4*time.Minute)); !errors.Is(err, content.ErrConflict) {
					t.Fatalf("create while %s: %v", status, err)
				}
				if _, err := repository.UpdateContent(ctx, test.module, child.ID, child.Version, changedInput, "admin", now.Add(4*time.Minute)); !errors.Is(err, content.ErrConflict) {
					t.Fatalf("update while %s: %v", status, err)
				}
				if err := repository.DeleteContent(ctx, test.module, child.ID, child.Version, "admin", now.Add(4*time.Minute)); !errors.Is(err, content.ErrConflict) {
					t.Fatalf("delete while %s: %v", status, err)
				}
				current, err := repository.GetContent(ctx, test.module, child.ID)
				if err != nil || current.Version != child.Version || !reflect.DeepEqual(current.Translations, child.Translations) {
					t.Fatalf("child changed while %s: %#v err=%v", status, current, err)
				}
			}
			if _, err := db.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='draft' WHERE id=$1`, page.ID); err != nil {
				t.Fatal(err)
			}
			if err := repository.DeleteContent(ctx, test.module, child.ID, child.Version, "admin", now.Add(5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.GetContent(ctx, test.module, child.ID); !errors.Is(err, content.ErrNotFound) {
				t.Fatalf("hard-deleted child err=%v", err)
			}
			page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil || page.Version != 4 {
				t.Fatalf("page after hard delete=%#v err=%v", page, err)
			}
		})
	}
}

func TestPageGroupPendingRemoval(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		module  content.Module
		pageKey string
		input   content.WriteInput
	}{
		{content.ModuleVideos, "home", content.WriteInput{YouTubeVideoID: "K3ckFWeSQ-k", Translations: []content.Translation{{Locale: "zh-Hant", Title: "影片"}}}},
		{content.ModuleHistory, "about", content.WriteInput{EventDate: "1988-03", Translations: []content.Translation{{Locale: "zh-Hant", Title: "沿革", Body: "事件"}}}},
	} {
		t.Run(string(test.module), func(t *testing.T) {
			if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
				t.Fatal(err)
			}
			page, err := repository.CreateContent(ctx, content.ModulePages, pageGroupPageInput(test.pageKey), "seed", "page:"+test.pageKey, now)
			if err != nil {
				t.Fatal(err)
			}
			child, err := repository.CreateContent(ctx, test.module, test.input, "admin", "child:"+string(test.module), now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published' WHERE id=$1`, child.ID); err != nil {
				t.Fatal(err)
			}
			projection := []byte(fmt.Sprintf(`{"id":%q,"title":"live"}`, child.ID))
			if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,$2,$3,'zh-Hant','/',1,'live-etag',$4,$5)`, "live:"+string(test.module), test.module, child.ID, projection, now); err != nil {
				t.Fatal(err)
			}
			var baselineProjection []byte
			if err := db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, test.module, child.ID).Scan(&baselineProjection); err != nil {
				t.Fatal(err)
			}

			if err := repository.DeleteContent(ctx, test.module, child.ID, child.Version, "admin", now.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			pending, err := repository.GetContent(ctx, test.module, child.ID)
			if err != nil || pending.Status != content.StatusPendingRemoval || pending.Version != child.Version+1 || !pending.IsPublished {
				t.Fatalf("pending=%#v err=%v", pending, err)
			}
			page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil || page.Version != 3 {
				t.Fatalf("page after pending removal=%#v err=%v", page, err)
			}
			var liveProjection []byte
			if err := db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, test.module, child.ID).Scan(&liveProjection); err != nil || string(liveProjection) != string(baselineProjection) {
				t.Fatalf("projection=%s err=%v", liveProjection, err)
			}
			revision, err := repository.ContentRevision(ctx, test.module, child.ID, pending.Version)
			if err != nil || revision.Snapshot.Status != content.StatusPendingRemoval {
				t.Fatalf("revision=%#v err=%v", revision, err)
			}

			cancelled, err := repository.UpdateContent(ctx, test.module, child.ID, pending.Version, test.input, "admin", now.Add(3*time.Minute))
			if err != nil || cancelled.Status != content.StatusDraft || cancelled.Version != pending.Version+1 || !cancelled.IsPublished {
				t.Fatalf("cancelled=%#v err=%v", cancelled, err)
			}
			page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil || page.Version != 4 {
				t.Fatalf("page after cancel=%#v err=%v", page, err)
			}

			if _, err := db.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, test.module, child.ID); err != nil {
				t.Fatal(err)
			}
			manifest := []byte(fmt.Sprintf(`{"items":[{"id":%q}]}`, child.ID))
			if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.page_publication_manifest(page_id,page_version,child_module,manifest_sha256,manifest_json,publication_status,created_by,created_at) VALUES($1,$2,$3,$4,$5,'published','admin',$6)`, page.ID, page.Version, test.module, strings.Repeat("0", 64), manifest, now); err != nil {
				t.Fatal(err)
			}
			if err := repository.DeleteContent(ctx, test.module, child.ID, cancelled.Version, "admin", now.Add(4*time.Minute)); err != nil {
				t.Fatal(err)
			}
			preserved, err := repository.GetContent(ctx, test.module, child.ID)
			if err != nil || preserved.Status != content.StatusPendingRemoval || preserved.IsPublished {
				t.Fatalf("manifest-preserved=%#v err=%v", preserved, err)
			}
		})
	}
}

func pageGroupTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("HHW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func pageGroupPageInput(key string) content.WriteInput {
	template, route, _ := content.PageDefinition(key)
	return content.WriteInput{
		PageKey: key, PageTemplate: template, RoutePath: route, Indexable: true,
		Translations: []content.Translation{{Locale: "zh-Hant", Title: key, BodyJSON: json.RawMessage(`{}`)}},
	}
}

func seedPageGroupPage(t *testing.T, ctx context.Context, repository *Repository, key string, now time.Time) {
	t.Helper()
	if _, err := repository.CreateContent(ctx, content.ModulePages, pageGroupPageInput(key), "test", "test-page:"+key, now); err != nil {
		t.Fatal(err)
	}
}

func pageIntegrationInput(t *testing.T, key, title string) content.WriteInput {
	t.Helper()
	template, route, _ := content.PageDefinition(key)
	var payload json.RawMessage
	if template == "home.v1" {
		payload = json.RawMessage(fmt.Sprintf(`{"schemaVersion":1,"template":"home.v1","data":{"heroTitle":%q,"heroSubtitle":"Welcome","newsTitle":"News","moreNews":"More","weeklyTitle":"Weekly","downloadWeekly":"Download","videosTitle":"Videos","videosSubtitle":"Music","watchMore":"Watch","aboutTitle":"About","aboutBody":"About us","aboutCta":"Meet us","locationsTitle":"Locations","mapLink":"Map"}}`, title))
	} else {
		payload = json.RawMessage(fmt.Sprintf(`{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":%q,"heroSubtitle":"","updatedAtLabel":"Updated","updatedAt":"August 4, 2026","intro":"Intro","sections":[{"title":"Section","body":["Paragraph"]}]}}`, title))
	}
	translations := make([]content.Translation, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		pageTitle, summary, err := content.PagePayloadMetadata(key, payload)
		if err != nil {
			t.Fatal(err)
		}
		translations = append(translations, content.Translation{Locale: locale, Title: pageTitle, Summary: summary, BodyJSON: payload})
	}
	return content.WriteInput{PageKey: key, PageTemplate: template, RoutePath: route, Indexable: true, Translations: translations}
}

func TestHomeV2PagePersistenceAndDefinitionConstraints(t *testing.T) {
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

	for index, test := range []struct {
		name, key, template, route string
		valid                      bool
	}{
		{"legacy home", "home", "home.v1", "/", true},
		{"home v2", "home", "home.v2", "/", true},
		{"home v2 wrong route", "home", "home.v2", "/home", false},
		{"home v2 wrong key", "about", "home.v2", "/about", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			id := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1)
			if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_entry(id,module,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,'pages',$2,'test','test',now(),now())`, id, "home-v2-constraint-"+test.name); err != nil {
				t.Fatal(err)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.page_item(content_id,page_key,page_template,route_path) VALUES($1,$2,$3,$4)`, id, test.key, test.template, test.route)
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid page definition was accepted")
			}
		})
	}

	repository := New(db)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	input := homeV2IntegrationInput(t, "Home")
	created, err := repository.CreateContent(ctx, content.ModulePages, input, "admin", "page:home-v2", now)
	if err != nil {
		t.Fatal(err)
	}
	if created.BannerAssetID != input.BannerAssetID || created.Links != input.Links || !reflect.DeepEqual(created.Locations, input.Locations) {
		t.Fatalf("created=%#v", created)
	}
	input.BannerAssetID = "banner-2"
	input.Locations[0].Translations[0].Address = "changed address"
	updated, err := repository.UpdateContent(ctx, content.ModulePages, created.ID, created.Version, input, "admin", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repository.ContentRevision(ctx, content.ModulePages, updated.ID, updated.Version)
	if err != nil || revision.Snapshot.BannerAssetID != "banner-2" || !reflect.DeepEqual(revision.Snapshot.Links, input.Links) || !reflect.DeepEqual(revision.Snapshot.Locations, input.Locations) {
		t.Fatalf("revision=%#v err=%v", revision, err)
	}
}

func TestHomeV2PublicationReplacesAndRetiresBannerGrants(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.outbox_event,hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := New(db)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	service := content.NewService(repository, func() time.Time { return now })

	legacy, err := repository.CreateContent(ctx, content.ModulePages, pageIntegrationInput(t, "home", "Legacy Home"), "seed", "page:home", now)
	if err != nil {
		t.Fatal(err)
	}
	firstVideo, err := repository.CreateContent(ctx, content.ModuleVideos, videoGroupInput("First Video"), "admin", "video:first", now)
	if err != nil {
		t.Fatal(err)
	}
	removedInput := videoGroupInput("Removed Video")
	removedInput.YouTubeVideoID = "dQw4w9WgXcQ"
	removedVideo, err := repository.CreateContent(ctx, content.ModuleVideos, removedInput, "admin", "video:removed", now)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err = repository.GetContent(ctx, content.ModulePages, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err = service.PublishContent(ctx, content.ModulePages, legacy.ID, legacy.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	legacyPublic, _, err := repository.PublicEditorialPage(ctx, "home", "ja")
	if err != nil || legacyPublic.Template != "home.v1" {
		t.Fatalf("legacy=%#v err=%v", legacyPublic, err)
	}

	v2Input := homeV2IntegrationInput(t, "Home V2")
	draft, err := repository.RestoreContent(ctx, content.ModulePages, legacy.ID, legacy.Version, v2Input, "migration", now.Add(time.Minute))
	if err != nil || draft.PageTemplate != "home.v2" || !draft.IsPublished {
		t.Fatalf("draft=%#v err=%v", draft, err)
	}
	draft, err = service.PublishContent(ctx, content.ModulePages, draft.ID, draft.Version, "admin")
	if err != nil || draft.Status != content.StatusPublishing {
		t.Fatalf("publishing=%#v err=%v", draft, err)
	}
	stillLegacy, _, err := repository.PublicEditorialPage(ctx, "home", "ja")
	if err != nil || stillLegacy.Template != "home.v1" {
		t.Fatalf("during publish=%#v err=%v", stillLegacy, err)
	}
	publish, found, err := repository.Claim(ctx, now.Add(2*time.Minute), 30*time.Second)
	if err != nil || !found || publish.EventType != "home.publish.ensure_asset" {
		t.Fatalf("publish=%#v found=%v err=%v", publish, found, err)
	}
	if err := repository.CompleteContentPublish(ctx, publish, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-1", PublicURL: "/assets/banner-1"}}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	v2Public, _, err := repository.PublicEditorialPage(ctx, "home", "ja")
	var publicPayload struct {
		Data struct {
			BannerImageURL string                       `json:"bannerImageUrl"`
			Locations      []content.PublicHomeLocation `json:"locations"`
		} `json:"data"`
	}
	decodeErr := json.Unmarshal(v2Public.Content, &publicPayload)
	if err != nil || decodeErr != nil || v2Public.Template != "home.v2" || publicPayload.Data.BannerImageURL != "/assets/banner-1" || len(publicPayload.Data.Locations) != 1 || publicPayload.Data.Locations[0].Name != "ja location" {
		t.Fatalf("v2=%#v err=%v", v2Public, err)
	}
	firstVideo, _ = repository.GetContent(ctx, content.ModuleVideos, firstVideo.ID)
	firstVideo, err = service.UpdateContent(ctx, content.ModuleVideos, firstVideo.ID, firstVideo.Version, videoGroupInput("Changed Video"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	removedVideo, _ = repository.GetContent(ctx, content.ModuleVideos, removedVideo.ID)
	if err := service.DeleteContent(ctx, content.ModuleVideos, removedVideo.ID, removedVideo.Version, "admin"); err != nil {
		t.Fatal(err)
	}

	current, err := repository.GetContent(ctx, content.ModulePages, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	v2Input.BannerAssetID = "banner-2"
	v2Input.Translations[3].BodyJSON = json.RawMessage(strings.ReplaceAll(string(v2Input.Translations[3].BodyJSON), "Home V2", "Updated V2"))
	updated, err := service.UpdateContent(ctx, content.ModulePages, current.ID, current.Version, v2Input, "admin")
	if err != nil {
		t.Fatal(err)
	}
	updated, err = service.PublishContent(ctx, content.ModulePages, updated.ID, updated.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	replacement, found, err := repository.Claim(ctx, now.Add(4*time.Minute), 30*time.Second)
	if err != nil || !found || replacement.EventType != "home.publish.ensure_asset" {
		t.Fatalf("replacement=%#v found=%v err=%v", replacement, found, err)
	}
	if err := repository.CompleteContentPublish(ctx, replacement, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-2", GrantID: "grant-2", PublicURL: "/assets/banner-2"}}, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	retireFirst, found, err := repository.Claim(ctx, now.Add(5*time.Minute), 30*time.Second)
	if err != nil || !found || retireFirst.EventType != "asset.grant.revoke" || !strings.Contains(string(retireFirst.Payload), "grant-1") {
		t.Fatalf("retire=%#v found=%v err=%v", retireFirst, found, err)
	}
	if err := repository.Complete(ctx, retireFirst.ID, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}

	current, err = repository.GetContent(ctx, content.ModulePages, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	unpublished, err := service.UnpublishContent(ctx, content.ModulePages, current.ID, current.Version, "admin")
	if err != nil || unpublished.Status != content.StatusUnpublished || unpublished.IsPublished || unpublished.BannerPublicGrantID != "" {
		t.Fatalf("unpublished=%#v err=%v", unpublished, err)
	}
	retireSecond, found, err := repository.Claim(ctx, now.Add(7*time.Minute), 30*time.Second)
	if err != nil || !found || retireSecond.EventType != "asset.grant.revoke" || !strings.Contains(string(retireSecond.Payload), "grant-2") {
		t.Fatalf("retire=%#v found=%v err=%v", retireSecond, found, err)
	}
	if err := repository.Complete(ctx, retireSecond.ID, now.Add(8*time.Minute)); err != nil {
		t.Fatal(err)
	}

	republished, err := service.PublishContent(ctx, content.ModulePages, unpublished.ID, unpublished.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	republish, found, err := repository.Claim(ctx, now.Add(9*time.Minute), 30*time.Second)
	if err != nil || !found || republish.EventType != "home.publish.ensure_asset" {
		t.Fatalf("republish=%#v found=%v err=%v", republish, found, err)
	}
	if err := repository.CompleteContentPublish(ctx, republish, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-2", GrantID: "grant-3", PublicURL: "/assets/banner-2"}}, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	current, err = repository.GetContent(ctx, content.ModulePages, republished.ID)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := service.RestoreContent(ctx, content.ModulePages, current.ID, legacy.Version, current.Version, "admin")
	if err != nil || restored.PageTemplate != "home.v1" || restored.BannerAssetID != "" {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	firstVideo, _ = repository.GetContent(ctx, content.ModuleVideos, firstVideo.ID)
	removedVideo, _ = repository.GetContent(ctx, content.ModuleVideos, removedVideo.ID)
	if firstVideo.Status != content.StatusDraft || removedVideo.Status != content.StatusDraft || groupTranslationTitle(firstVideo, "zh-Hant") != "First Video zh-Hant" {
		t.Fatalf("restored first=%#v removed=%#v", firstVideo, removedVideo)
	}
	restored, err = service.PublishContent(ctx, content.ModulePages, restored.ID, restored.Version, "admin")
	if err != nil || restored.Status != content.StatusPublished || restored.BannerPublicGrantID != "" {
		t.Fatalf("legacy republish=%#v err=%v", restored, err)
	}
	firstVideo, _ = repository.GetContent(ctx, content.ModuleVideos, firstVideo.ID)
	removedVideo, _ = repository.GetContent(ctx, content.ModuleVideos, removedVideo.ID)
	var videoProjections int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='videos'`).Scan(&videoProjections); err != nil || videoProjections != 10 || firstVideo.Status != content.StatusPublished || removedVideo.Status != content.StatusPublished {
		t.Fatalf("first=%#v removed=%#v projections=%d err=%v", firstVideo, removedVideo, videoProjections, err)
	}
	retireThird, found, err := repository.Claim(ctx, now.Add(11*time.Minute), 30*time.Second)
	if err != nil || !found || retireThird.EventType != "asset.grant.revoke" || !strings.Contains(string(retireThird.Payload), "grant-3") {
		t.Fatalf("retire=%#v found=%v err=%v", retireThird, found, err)
	}
}

func TestHomeV2PublishFailureDoesNotOverwriteSupersedingDraft(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.outbox_event,hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := New(db)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	service := content.NewService(repository, func() time.Time { return now })
	draft, err := repository.CreateContent(ctx, content.ModulePages, homeV2IntegrationInput(t, "Home"), "admin", "page:home-v2-failure", now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PublishContent(ctx, content.ModulePages, draft.ID, draft.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	event, found, err := repository.Claim(ctx, now.Add(time.Minute), 30*time.Second)
	if err != nil || !found {
		t.Fatalf("event=%#v found=%v err=%v", event, found, err)
	}
	if err := repository.FailContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-1"}}, "failed", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	current, err := repository.GetContent(ctx, content.ModulePages, draft.ID)
	if err != nil || current.Status != content.StatusPublishFailed || current.Version != event.AggregateVersion {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	compensation, found, err := repository.Claim(ctx, now.Add(3*time.Minute), 30*time.Second)
	if err != nil || !found || compensation.EventType != "asset.grant.revoke" || !strings.Contains(string(compensation.Payload), "grant-1") {
		t.Fatalf("compensation=%#v found=%v err=%v", compensation, found, err)
	}
	if err := repository.Complete(ctx, compensation.ID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateContent(ctx, content.ModulePages, current.ID, current.Version, homeV2IntegrationInput(t, "Updated Home"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	publishing, err := service.PublishContent(ctx, content.ModulePages, updated.ID, updated.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	event, found, err = repository.Claim(ctx, now.Add(5*time.Minute), 30*time.Second)
	if err != nil || !found || event.AggregateVersion != publishing.Version {
		t.Fatalf("event=%#v found=%v err=%v", event, found, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='draft',version=version+1 WHERE id=$1`, draft.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.FailContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-2"}}, "superseded", now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	current, err = repository.GetContent(ctx, content.ModulePages, draft.ID)
	if err != nil || current.Status != content.StatusDraft || current.Version != publishing.Version+1 {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	compensation, found, err = repository.Claim(ctx, now.Add(7*time.Minute), 30*time.Second)
	if err != nil || !found || compensation.EventType != "asset.grant.revoke" || !strings.Contains(string(compensation.Payload), "grant-2") {
		t.Fatalf("compensation=%#v found=%v err=%v", compensation, found, err)
	}
}

func homeV2IntegrationInput(t *testing.T, title string) content.WriteInput {
	t.Helper()
	payload := json.RawMessage(fmt.Sprintf(`{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":%q,"heroSubtitle":"Welcome","kingdomJoyDescription":"Kingdom joy","aboutDescription":"About us"}}`, title))
	translations := make([]content.Translation, 0, 5)
	locationTranslations := make([]content.HomeLocationTranslation, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		pageTitle, summary, err := content.PagePayloadMetadata("home", payload)
		if err != nil {
			t.Fatal(err)
		}
		translations = append(translations, content.Translation{Locale: locale, Title: pageTitle, Summary: summary, BodyJSON: payload})
		locationTranslations = append(locationTranslations, content.HomeLocationTranslation{Locale: locale, Name: locale + " location", Address: locale + " address"})
	}
	return content.WriteInput{
		PageKey: "home", PageTemplate: "home.v2", RoutePath: "/", Indexable: true, BannerAssetID: "banner-1",
		Links:        content.HomeLinks{ChurchYouTube: "https://youtube.com/@hhc", ChurchFacebook: "https://facebook.com/hhc", MusicYouTube: "https://youtube.com/@music"},
		Locations:    []content.HomeLocation{{Key: "taipei", MapHref: "https://maps.example.com/taipei", SortOrder: 10, Translations: locationTranslations}},
		Translations: translations,
	}
}

func TestSiteSettingsLifecycle(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.site_setting_revision,hhc_web.site_setting_locale,hhc_web.site_setting_set CASCADE; DELETE FROM hhc_web.public_projection WHERE resource_type='site_layout'`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	input := siteSettingsWriteInput()
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_set(id,status,version,external_links_json,created_by,updated_by,created_at,updated_at) VALUES('default','draft',1,$1,'seed','seed',$2,$2)`, mustJSON(input.Links), now); err != nil {
		t.Fatal(err)
	}
	if err := replaceSiteSettingsRows(ctx, db, input); err != nil {
		t.Fatal(err)
	}

	repository := NewSiteSettingsRepository(db)
	service := sitesettings.NewService(repository, func() time.Time { return now })
	saved, err := service.Save(ctx, input, 1, "admin")
	if err != nil || saved.Version != 2 || saved.Status != sitesettings.StatusDraft {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
	if _, err := service.Save(ctx, input, 1, "admin"); !errors.Is(err, sitesettings.ErrPrecondition) {
		t.Fatalf("stale save err=%v", err)
	}
	published, err := service.Publish(ctx, saved.Version, "admin")
	if err != nil || published.Version != 3 || published.Status != sitesettings.StatusPublished {
		t.Fatalf("published=%#v err=%v", published, err)
	}
	var projectionCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='site_layout'`).Scan(&projectionCount); err != nil || projectionCount != 5 {
		t.Fatalf("projection count=%d err=%v", projectionCount, err)
	}
	var publishedPayload []byte
	if err := db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE projection_key='site_layout:ja'`).Scan(&publishedPayload); err != nil || !strings.Contains(string(publishedPayload), `"href": "/ja/about"`) && !strings.Contains(string(publishedPayload), `"href":"/ja/about"`) {
		t.Fatalf("ja projection=%s err=%v", publishedPayload, err)
	}

	changed := siteSettingsWriteInput()
	changed.Locales[3].SiteName = "changed-ja"
	draft, err := service.Save(ctx, changed, published.Version, "admin")
	if err != nil || draft.Status != sitesettings.StatusDraft {
		t.Fatalf("draft=%#v err=%v", draft, err)
	}
	var stillPublished []byte
	if err := db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE projection_key='site_layout:ja'`).Scan(&stillPublished); err != nil || string(stillPublished) != string(publishedPayload) {
		t.Fatalf("draft changed public projection=%s err=%v", stillPublished, err)
	}
	unpublishedAfterSave, err := service.Unpublish(ctx, draft.Version, "admin")
	if err != nil || unpublishedAfterSave.Status != sitesettings.StatusUnpublished {
		t.Fatalf("unpublish after save=%#v err=%v", unpublishedAfterSave, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='site_layout'`).Scan(&projectionCount); err != nil || projectionCount != 0 {
		t.Fatalf("projection count after draft unpublish=%d err=%v", projectionCount, err)
	}
	republishedDraft, err := service.Publish(ctx, unpublishedAfterSave.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	var changedPublished []byte
	if err := db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE projection_key='site_layout:ja'`).Scan(&changedPublished); err != nil || !strings.Contains(string(changedPublished), "changed-ja") {
		t.Fatalf("changed projection=%s err=%v", changedPublished, err)
	}
	restored, err := service.Restore(ctx, saved.Version, republishedDraft.Version, "admin")
	if err != nil || restored.Status != sitesettings.StatusDraft || restored.Locales[3].SiteName == "changed-ja" {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE projection_key='site_layout:ja'`).Scan(&stillPublished); err != nil || string(stillPublished) != string(changedPublished) {
		t.Fatalf("restore changed public projection=%s err=%v", stillPublished, err)
	}
	unpublished, err := service.Unpublish(ctx, restored.Version, "admin")
	if err != nil || unpublished.Status != sitesettings.StatusUnpublished {
		t.Fatalf("unpublished=%#v err=%v", unpublished, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='site_layout'`).Scan(&projectionCount); err != nil || projectionCount != 0 {
		t.Fatalf("projection count=%d err=%v", projectionCount, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE hhc_web.site_setting_locale SET site_name='' WHERE setting_set_id='default' AND locale='ko'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Publish(ctx, unpublished.Version, "admin", now); !errors.Is(err, sitesettings.ErrNotPublishable) {
		t.Fatalf("incomplete republish err=%v", err)
	}
}

func replaceSiteSettingsRows(ctx context.Context, db *sql.DB, input sitesettings.WriteInput) error {
	for _, locale := range input.Locales {
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_locale(setting_set_id,locale,site_name,english_name,copyright_holder,all_rights_reserved,seo_title_suffix,seo_description_fallback,header_items_json,legal_items_json) VALUES('default',$1,$2,$3,$4,$5,$6,$7,$8,$9)`, locale.Locale, locale.SiteName, locale.EnglishName, locale.CopyrightHolder, locale.AllRightsReserved, locale.SEOTitleSuffix, locale.SEODescriptionFallback, mustJSON(locale.Header), mustJSON(locale.Legal)); err != nil {
			return err
		}
	}
	return nil
}

func siteSettingsWriteInput() sitesettings.WriteInput {
	locales := make([]sitesettings.LocaleSettings, 0, len(sitesettings.SupportedLocales))
	for _, locale := range sitesettings.SupportedLocales {
		locales = append(locales, sitesettings.LocaleSettings{
			Locale: locale, SiteName: locale + " site", EnglishName: "Hallelujah Home Church",
			CopyrightHolder: locale + " holder", AllRightsReserved: locale + " rights",
			SEOTitleSuffix: locale + " title", SEODescriptionFallback: locale + " description",
			Header: []sitesettings.NavItem{
				{Key: "about", Label: locale + " about", Href: "/{locale}/about", Visible: true},
				{Key: "news", Label: locale + " news", Href: "/{locale}/news", Visible: true},
				{Key: "literature-ministry", Label: locale + " literature", Href: "/{locale}/literature-ministry", Visible: true},
			},
			Legal: []sitesettings.NavItem{
				{Key: "privacy-policy", Label: locale + " privacy", Href: "/{locale}/privacy-policy", Visible: true},
				{Key: "terms-of-use", Label: locale + " terms", Href: "/{locale}/terms-of-use", Visible: true},
			},
		})
	}
	return sitesettings.WriteInput{Locales: locales, Links: sitesettings.ExternalLinks{
		ChurchYouTube:  "https://youtube.com/@hhc33?si=public",
		ChurchFacebook: "https://www.facebook.com/www.alive.org.tw/",
		MusicYouTube:   "https://youtube.com/@gkpmusic777",
	}}
}

func TestLegacyContentRetriesPreserveOmittedDetailLayoutFingerprint(t *testing.T) {
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
	service := content.NewService(repository, time.Now)
	seedPageGroupPage(t, ctx, repository, "home", time.Now().UTC())
	seedPageGroupPage(t, ctx, repository, "about", time.Now().UTC())
	for _, test := range []struct {
		name   string
		module content.Module
		input  content.WriteInput
	}{
		{name: "history", module: content.ModuleHistory, input: content.WriteInput{EventDate: "1988-03", Translations: []content.Translation{{Locale: "zh-Hant", Title: "沿革", Body: "事件"}}}},
		{name: "video", module: content.ModuleVideos, input: content.WriteInput{YouTubeVideoID: "K3ckFWeSQ-k", Translations: []content.Translation{{Locale: "zh-Hant", Title: "影片"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := "legacy-fingerprint-" + test.name
			legacy := test.input
			legacy.DetailLayout = "top"
			created, err := repository.CreateContent(ctx, test.module, legacy, "admin", key, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			retried, err := service.CreateContent(ctx, test.module, test.input, "admin", key)
			if err != nil || retried.ID != created.ID {
				t.Fatalf("created=%#v retried=%#v err=%v", created, retried, err)
			}
		})
	}
}

func locationWriteInput(key, mapHref string, sortOrder int) content.WriteInput {
	locales := []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}
	translations := make([]content.Translation, len(locales))
	for index, locale := range locales {
		translations[index] = content.Translation{Locale: locale, Title: locale + "-" + key, Body: locale + "-" + key + "-address"}
	}
	return content.WriteInput{LocationKey: key, MapHref: mapHref, SortOrder: sortOrder, Translations: translations}
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
