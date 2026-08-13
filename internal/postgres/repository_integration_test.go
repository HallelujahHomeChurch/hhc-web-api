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
		Slug: "first-news", DisplayDate: "2026-07-30", CoverAssetID: "asset-1",
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
	if err := repository.CompleteContentPublish(ctx, event, published, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteContentPublish(ctx, event, published, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("replayed news publish completion: %v", err)
	}
	public, err := repository.PublicContent(ctx, content.ModuleNews, "zh-Hant", 1, 20)
	if err != nil || len(public.Items) != 1 || public.Items[0].ImageURL != "/api/assets/public/asset-1/large" || public.Total != 1 {
		t.Fatalf("public=%#v err=%v", public, err)
	}
	detail, etag, err := repository.PublicNews(ctx, "zh-Hant", "first-news")
	if err != nil || detail.ID != item.ID || etag == "" {
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
	if err := repository.CompleteContentPublish(ctx, replacement, replacementAssets, now.Add(5*time.Minute)); err != nil {
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
	if err != nil || item.IsPublished || item.Status != content.StatusUnpublished {
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
