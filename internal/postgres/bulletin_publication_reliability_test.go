package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestManualPDFPreservesAutomaticOnlinePublication(t *testing.T) {
	for _, phase := range []string{"semantic-queued", "semantic-planning", "layout", "layout-notify"} {
		t.Run(phase, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			repo.WithExplicitOnlinePublication(true)
			t.Cleanup(func() {
				_, _ = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1`, source.Canonical.IssueID)
				_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, source.Canonical.IssueID)
				_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, source.Canonical.IssueID)
				_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID)
				_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
			})
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET auto_notify=true WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			var update *onlinebulletins.UpdateJob
			var layout *onlinebulletins.LayoutJob
			var err error
			finishUpdate := func() {
				t.Helper()
				proof := verifiedUpdateForTest(t, update.Input)
				for _, a := range proof.Attempts {
					if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *update, a, now); err != nil {
						t.Fatal(err)
					}
				}
				if err = repo.CompleteOnlineBulletinUpdate(ctx, *update, proof, now); err != nil {
					t.Fatal(err)
				}
				layout, err = repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
				if err != nil || layout == nil {
					t.Fatal("layout claim", err)
				}
			}
			if phase != "semantic-queued" {
				update, err = repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
				if err != nil || update == nil {
					t.Fatal("update claim", err)
				}
			}
			if phase == "layout" || phase == "layout-notify" {
				finishUpdate()
			}
			if _, err = repo.StartPublishSelection(bulletinAuditContext(), source.Canonical.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic", NotifySubscribers: phase == "layout-notify"}, 1, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.StartPublish(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", 1, false, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
				t.Fatal("stale CAS", err)
			}
			var enabled, notify, extractionEnabled bool
			if err = db.QueryRow(`SELECT v.auto_publish,v.auto_notify,e.auto_publish FROM hhc_web.bulletin_version v JOIN hhc_web.online_bulletin_extraction_job e ON e.issue_id=v.issue_id WHERE e.id=$1`, source.ID).Scan(&enabled, &notify, &extractionEnabled); err != nil || !enabled || !notify || !extractionEnabled {
				t.Fatalf("manual PDF cancelled online authority: %v %v %v %v", enabled, notify, extractionEnabled, err)
			}
			if phase == "semantic-queued" {
				update, err = repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
				if err != nil || update == nil {
					t.Fatal("update claim", err)
				}
			}
			if phase != "layout" && phase != "layout-notify" {
				finishUpdate()
			}
			if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
				t.Fatal(err)
			}
			var event publication.Event
			if err = db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, source.Canonical.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil); err != nil {
				t.Fatal(err)
			}
			event.EventType, event.AggregateID = "bulletin.publish.ensure_asset", source.Canonical.IssueID
			if err = repo.CompletePublish(ctx, event, "", "", now); err != nil {
				t.Fatal(err)
			}
			var count int
			wantPDFCommands := 0
			if phase == "layout-notify" {
				wantPDFCommands = 1
			}
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'`, source.Canonical.IssueID).Scan(&count); err != nil || count != wantPDFCommands {
				t.Fatal("manual no-notify published unexpected command", count, err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() { defer wg.Done(); _, e := repo.AutoPublishNextRecognition(ctx, now); errs <- e }()
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil {
					t.Fatal(e)
				}
			}
			state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil || state.PublishedRevision == nil {
				t.Fatal("online not published", err)
			}
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'`, source.Canonical.IssueID).Scan(&count); err != nil || count != 1 {
				t.Fatal("original automatic notification missing/duplicated", count, err)
			}
			if err = repo.CompletePublish(ctx, event, "", "", now); err != nil {
				t.Fatal("replay", err)
			}
			if progressed, e := repo.AutoPublishNextRecognition(ctx, now); e != nil || progressed {
				t.Fatal("duplicate release", progressed, e)
			}
		})
	}
}

func TestDelayedBulletinFailurePreservesTerminalAndNewClaims(t *testing.T) {
	for _, state := range []string{"cancelled", "completed", "new-claim", "lease-aba", "expired"} {
		t.Run(state, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			repo.WithExplicitOnlinePublication(true)
			t.Cleanup(func() {
				_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
			})
			// This fixture is an independent manual PDF workflow, not an active automatic plan.
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET auto_publish=false WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			workflow, err := repo.StartPublish(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", 1, false, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			var event publication.Event
			err = db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=1,claimed_until=$2 WHERE aggregate_id=$1 AND event_type='bulletin.publish.ensure_asset' RETURNING id::text,payload_json,aggregate_version,attempts,claimed_until`, source.Canonical.IssueID, now.Add(time.Minute)).Scan(&event.ID, &event.Payload, &event.AggregateVersion, &event.Attempts, &event.ClaimedUntil)
			if err != nil {
				t.Fatal(err)
			}
			event.EventType, event.AggregateID = "bulletin.publish.ensure_asset", source.Canonical.IssueID
			wantEvent, wantWorkflow := "failed", "cancelled"
			if state == "completed" {
				wantEvent, wantWorkflow = "delivered", "completed"
			}
			if state == "new-claim" || state == "lease-aba" || state == "expired" {
				wantEvent, wantWorkflow = "processing", "waiting_asset_scan"
			}
			if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET status=$2,attempts=2,claimed_until=$3,last_error='preserved' WHERE id=$1`, event.ID, wantEvent, now.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if state == "lease-aba" || state == "expired" {
				if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET attempts=1 WHERE id=$1`, event.ID); err != nil {
					t.Fatal(err)
				}
			}
			if state == "expired" {
				if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET claimed_until=$2 WHERE id=$1`, event.ID, event.ClaimedUntil); err != nil {
					t.Fatal(err)
				}
				now = event.ClaimedUntil.Add(time.Second)
			}
			if _, err = db.Exec(`UPDATE hhc_web.publication_workflow SET status=$2,error_detail='preserved' WHERE id=$1`, workflow.ID, wantWorkflow); err != nil {
				t.Fatal(err)
			}
			if state != "completed" {
				if err = repo.CompletePublish(ctx, event, "", "", now); !errors.Is(err, publication.ErrStalePublication) {
					t.Fatal("stale claim completed publication", err)
				}
			}
			rescheduler, ok := any(repo).(interface {
				RescheduleBulletinPublication(context.Context, publication.Event, string, time.Time, time.Time, bool) error
			})
			if !ok {
				t.Fatal("bulletin claim rescheduler missing")
			}
			for _, waiting := range []bool{false, true} {
				if err = rescheduler.RescheduleBulletinPublication(ctx, event, "late retry", now.Add(time.Minute), now, waiting); err != nil {
					t.Fatal(err)
				}
			}
			if err = repo.Fail(ctx, event, "late failure", now); err != nil {
				t.Fatal(err)
			}
			if err = repo.FailPublish(ctx, event, source.SourceAssetID, "", "late compensation", now); err != nil {
				t.Fatal(err)
			}
			var eventState, workflowState, detail string
			if err = db.QueryRow(`SELECT e.status,w.status,w.error_detail FROM hhc_web.outbox_event e JOIN hhc_web.publication_workflow w ON w.id=$2 WHERE e.id=$1`, event.ID, workflow.ID).Scan(&eventState, &workflowState, &detail); err != nil || eventState != wantEvent || workflowState != wantWorkflow || detail != "preserved" {
				t.Fatal("late worker overwrote authoritative state", eventState, workflowState, detail, err)
			}
		})
	}
}

func TestManualPDFBeforeRecognitionDoesNotCreateOrCancelAuthority(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "authorized"}[enabled], func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			id := seedOnlineExtraction(t, db, now)
			var issue string
			if err := db.QueryRow(`UPDATE hhc_web.online_bulletin_extraction_job SET auto_publish=$2 WHERE id=$1 RETURNING issue_id::text`, id, enabled).Scan(&issue); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, issue) })
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET auto_publish=$2,auto_publish_request='original' WHERE issue_id=$1`, issue, enabled); err != nil {
				t.Fatal(err)
			}
			repo := New(db).WithAudit(NewAuditOutbox(db))
			if _, err := repo.StartPublish(bulletinAuditContext(), issue, "general", "zh-Hant", 1, false, postgresBulletinAuditActor, now); enabled && !errors.Is(err, bulletins.ErrAutomaticPublicationActive) || !enabled && err != nil {
				t.Fatal(err)
			}
			var versionIntent, jobIntent bool
			var request string
			if err := db.QueryRow(`SELECT v.auto_publish,e.auto_publish,v.auto_publish_request FROM hhc_web.bulletin_version v JOIN hhc_web.online_bulletin_extraction_job e ON e.issue_id=v.issue_id WHERE e.id=$1`, id).Scan(&versionIntent, &jobIntent, &request); err != nil || versionIntent != enabled || jobIntent != enabled || request != "original" {
				t.Fatal("PDF action changed prior authority", versionIntent, jobIntent, request, err)
			}
		})
	}
}

func TestManualAndAutomaticPDFPublicationSerializeInBothOrders(t *testing.T) {
	for _, first := range []string{"manual", "automatic", "concurrent"} {
		t.Run(first, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			repo.WithExplicitOnlinePublication(true)
			t.Cleanup(func() {
				_, _ = db.Exec(`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1`, source.Canonical.IssueID)
			})
			job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			proof := verifiedUpdateForTest(t, job.Input)
			for _, a := range proof.Attempts {
				if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, a, now); err != nil {
					t.Fatal(err)
				}
			}
			if err = repo.CompleteOnlineBulletinUpdate(ctx, *job, proof, now); err != nil {
				t.Fatal(err)
			}
			layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
			if err != nil || layout == nil {
				t.Fatal(err)
			}
			if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
				t.Fatal(err)
			}
			manual := func() error {
				_, e := repo.StartPublishSelection(bulletinAuditContext(), source.Canonical.IssueID, bulletins.PublishInput{Series: "general", Locale: "zh-Hant", OnlinePublication: "continue_automatic"}, 1, postgresBulletinAuditActor, now)
				return e
			}
			automatic := func() error { _, e := repo.AutoPublishNextRecognition(ctx, now); return e }
			var manualErr, errorAuto error
			switch first {
			case "manual":
				manualErr = manual()
				errorAuto = automatic()
			case "automatic":
				errorAuto = automatic()
				manualErr = manual()
			case "concurrent":
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); manualErr = manual() }()
				go func() { defer wg.Done(); errorAuto = automatic() }()
				wg.Wait()
			}
			if errorAuto != nil {
				t.Fatal(errorAuto)
			}
			if manualErr != nil && !errors.Is(manualErr, bulletins.ErrPrecondition) && !errors.Is(manualErr, bulletins.ErrNotPublishable) {
				t.Fatal(manualErr)
			}
			var count int
			var enabled bool
			if err = db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.publication_workflow WHERE resource_id=$1),auto_publish FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID).Scan(&count, &enabled); err != nil || count != 1 || !enabled {
				t.Fatal("race duplicated workflow or cancelled authority", count, enabled, err)
			}
		})
	}
}
