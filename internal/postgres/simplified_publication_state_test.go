package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"strings"
	"testing"
	"time"
)

func TestSimplifiedHistoryBoundedAndHealthyWaitUncharged(t *testing.T) {
	db, repo, source, id, now := seedJourney(t)
	for i := 0; i < 30; i++ {
		event := claimSimplifiedRelease(t, db, id, now)
		if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now); e != nil {
			t.Fatal(e)
		}
	}
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	op := state.DerivedPublication
	if op == nil || op.History == nil || len(op.History) != 0 || op.FailureCount == nil || *op.FailureCount != 0 || op.Attempts == nil || *op.Attempts != 0 {
		t.Fatalf("healthy wait fabricated failures %#v", op)
	}
	for i := 0; i < 25; i++ {
		event := claimSimplifiedRelease(t, db, id, now)
		if e = repo.RescheduleSimplifiedRelease(context.Background(), event, now, now); e != nil {
			t.Fatal(e)
		}
	}
	state, e = repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	op = state.DerivedPublication
	if len(op.History) != 20 || !op.HasMore || op.FailureCount == nil || *op.FailureCount != 25 || op.LastFailure == nil || op.HistoryCoverage != "complete" {
		t.Fatalf("bounded history %#v", op)
	}
	// All external cancellation writers use the trigger; cancellation is not a failure.
	if _, e = db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='publication_withdrawn',updated_at=$2 WHERE id=$1`, id, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	state, e = repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	op = state.DerivedPublication
	if op.Status != "cancelled" || *op.FailureCount != 25 || op.LastFailure.Code != "simplified_publication_failed" || op.History[0].Kind != "cancelled" {
		t.Fatalf("cancellation lost history %#v", op)
	}
}
func TestInitialNotificationProjectionExactGenerationAndNoCanonicalInference(t *testing.T) {
	for _, initialVersion := range []int{0, 97} {
		t.Run(fmt.Sprint(initialVersion), func(t *testing.T) {
			db, repo, source, _, now := seedJourney(t)
			id := platform.NewID()
			for generation := 0; generation < 2; generation++ {
				command := platform.NewID()
				if generation == 0 {
					command = id
				}
				payload := fmt.Sprintf(`{"name":"Synthetic command","actorId":%q,"requestId":%q,"idempotencyKey":%q,"notifications":[{"channel":"email","audienceType":"bulletin_members"}],"source":{"kind":"bulletin","issueId":%q,"series":"general","notificationGeneration":%d}}`, postgresBulletinAuditActor, command, fmt.Sprintf("notification:v2:bulletin:%s:general:g%d", source.Canonical.IssueID, generation), source.Canonical.IssueID, generation)
				if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,attempts,last_error,next_attempt_at,created_at,updated_at)VALUES($1::uuid,'engagement-api','bulletin.notification.submit','bulletin',$2,$3,$4,$1::text,'delivered',2,'', $5,$5,$5)`, command, source.Canonical.IssueID, initialVersion, payload, now.Add(time.Duration(generation)*time.Second)); e != nil {
					t.Fatal(e)
				}
			}
			for _, locale := range []string{"zh-Hant", "zh-Hans"} {
				state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", locale)
				if e != nil {
					t.Fatal(e)
				}
				n := state.Notification
				if n == nil || n.ID != id || n.Status != "accepted" || n.Generation != 0 || n.CanonicalVersion != nil || n.Attempts == nil || *n.Attempts != 2 {
					t.Fatalf("initial command changed %#v", n)
				}
			}
		})
	}
}
func TestSimplifiedLegacyHistoryUnknownAndRawErrorsSanitized(t *testing.T) {
	db, repo, source, id, now := seedJourney(t)
	if _, e := db.Exec(`DELETE FROM hhc_web.online_bulletin_simplified_operation WHERE operation_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	op := state.DerivedPublication
	if op == nil || op.HistoryCoverage != "unknown" || op.FailureCount != nil || op.Attempts != nil || op.SourceCurrent != nil || op.History == nil || op.LastFailure != nil {
		t.Fatalf("invented legacy history %#v", op)
	}
	if _, e = db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='private raw source and model error',updated_at=$2 WHERE id=$1`, id, now); e != nil {
		t.Fatal(e)
	}
	state, e = repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	op = state.DerivedPublication
	if op.HistoryCoverage != "partial" || op.ErrorCode != "simplified_publication_failed" || op.LastFailure == nil || op.LastFailure.Code != "simplified_publication_failed" {
		t.Fatalf("raw failure leaked/invented coverage %#v", op)
	}
}
func TestSimplifiedProjectionOrdersPublicationCausally(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			db, repo, source, oldID, now := seedJourney(t)
			// The original publication command is generation6 in this fixture. A newer
			// command for the same immutable revision is generation8, independent of clocks.
			var payload []byte
			if e := db.QueryRow(`SELECT payload_json FROM hhc_web.outbox_event WHERE id=$1`, oldID).Scan(&payload); e != nil {
				t.Fatal(e)
			}
			orderedID := []byte(oldID)
			for j := len(orderedID) - 1; j >= 0; j-- {
				if orderedID[j] == '-' {
					continue
				}
				n := strings.IndexByte("0123456789abcdef", orderedID[j])
				if n == 0 {
					orderedID[j] = 'f'
					continue
				}
				orderedID[j] = "0123456789abcdef"[n-1]
				break
			}
			id := string(orderedID)
			created := now
			if reverse {
				created = now.Add(-time.Hour)
			}
			if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) SELECT $2::uuid,destination,event_type,aggregate_type,aggregate_id,aggregate_version,jsonb_set(payload_json-'jobId','{sourcePublicationVersion}','8'), 'online-bulletin:'||aggregate_id::text||':simplified-release:8','pending',$3,$3,$3 FROM hhc_web.outbox_event WHERE id=$1`, oldID, id, created); e != nil {
				t.Fatal(e)
			}
			state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
			if e != nil {
				t.Fatal(e)
			}
			if state.DerivedPublication == nil || state.DerivedPublication.OperationID != id {
				t.Fatalf("timestamp/UUID order hid current publication command: %#v", state.DerivedPublication)
			}
			// Mutation must select the same causal operation as the read projection.
			if _, e = db.Exec(`UPDATE hhc_web.online_bulletin_document SET version=8 WHERE id=$1`, source.DocumentID); e != nil {
				t.Fatal(e)
			}
			hant, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = repo.QueueOnlineBulletinConversion(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: state.Version}, postgresBulletinAuditActor, now); e != nil {
				t.Fatal("causal resume", e)
			}
			var bound string
			if e = db.QueryRow(`SELECT publication_event_id::text FROM hhc_web.online_bulletin_conversion_job WHERE issue_id=$1 ORDER BY created_at DESC LIMIT 1`, source.Canonical.IssueID).Scan(&bound); e != nil || bound != id {
				t.Fatal("resume selected older operation", bound, e)
			}

		})
	}
}
func TestSimplifiedTransientFailureHealthyWaitThenSuccessRetainsFailure(t *testing.T) {
	db, repo, source, id, now := seedJourney(t)
	event := claimSimplifiedRelease(t, db, id, now)
	if e := repo.RescheduleSimplifiedRelease(context.Background(), event, now, now); e != nil {
		t.Fatal(e)
	}
	failed, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	if failed.DerivedPublication.ErrorCode != "simplified_publication_failed" {
		t.Fatal("current transient error missing")
	}
	waiting := claimSimplifiedRelease(t, db, id, now)
	if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), waiting, now); e != nil {
		t.Fatal(e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	op := state.DerivedPublication
	if op.ErrorCode != "" || op.LastFailure == nil || op.LastFailure.Code != "simplified_publication_failed" || *op.FailureCount != 1 || len(op.History) != 1 {
		t.Fatalf("healthy wait erased retained failure or retained current error: %#v", op)
	}
	completeJourneyProof(t, repo, now)
	success := claimSimplifiedRelease(t, db, id, now)
	if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), success, now); e != nil {
		t.Fatal(e)
	}
	state, e = repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	op = state.DerivedPublication
	if state.PublishedRevision == nil || op.Status != "completed" || op.ErrorCode != "" || *op.FailureCount != 1 || len(op.History) != 1 || op.LastFailure == nil {
		t.Fatalf("success lost diagnostics %#v", op)
	}
}
func TestSimplifiedProjectionCannotOrderCompetingUnknownSameRevision(t *testing.T) {
	db, repo, source, id, now := seedJourney(t)
	if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) SELECT $2::uuid,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json-'sourcePublicationVersion',$2::text,'pending',$3,$3,$3 FROM hhc_web.outbox_event WHERE id=$1`, id, platform.NewID(), now); e != nil {
		t.Fatal(e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	if state.DerivedPublication != nil {
		t.Fatal("fabricated ordering with competing unknown publication", state.DerivedPublication.OperationID)
	}
}
func TestSimplifiedAmbiguousResumeRejectsBeforeFallback(t *testing.T) {
	db, repo, source, id, now := seedJourney(t)
	if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) SELECT $2::uuid,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json-'sourcePublicationVersion',$2::text,'pending',$3,$3,$3 FROM hhc_web.outbox_event WHERE id=$1`, id, platform.NewID(), now); e != nil {
		t.Fatal(e)
	}
	hant, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if e != nil {
		t.Fatal(e)
	}
	hans, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	before := journeyEffects(t, db, source.Canonical.IssueID)
	_, e = repo.QueueOnlineBulletinConversion(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version}, postgresBulletinAuditActor, now)
	if !errors.Is(e, bulletins.ErrConflict) {
		t.Fatal("ambiguous release fell through to conversion", e)
	}
	if after := journeyEffects(t, db, source.Canonical.IssueID); after != before {
		t.Fatal("ambiguous mutation effects", before, after)
	}
}
func TestSimplifiedSoleLegacyCurrentRevisionDoesNotFallBackToConversion(t *testing.T) {
	db, repo, source, id, now := seedJourney(t)
	if _, e := db.Exec(`DELETE FROM hhc_web.online_bulletin_simplified_operation WHERE operation_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) SELECT $2::uuid,destination,event_type,aggregate_type,aggregate_id,1,payload_json-'sourcePublicationVersion',$2::text,'failed',$3,$3,$3 FROM hhc_web.outbox_event WHERE id=$1`, id, platform.NewID(), now); e != nil {
		t.Fatal(e)
	}
	hant, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if e != nil {
		t.Fatal(e)
	}
	hans, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	if hans.DerivedPublication == nil || hans.DerivedPublication.OperationID != id || hans.DerivedPublication.HistoryCoverage != "unknown" {
		t.Fatal("sole current legacy operation hidden", hans.DerivedPublication)
	}
	before := journeyEffects(t, db, source.Canonical.IssueID)
	_, e = repo.QueueOnlineBulletinConversion(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version}, postgresBulletinAuditActor, now)
	if e != nil {
		t.Fatal(e)
	}
	if after := journeyEffects(t, db, source.Canonical.IssueID); after != before {
		t.Fatal("legacy current resume created competing draft", before, after)
	}
}
func TestInitialNotificationMissingRequestEvidenceStaysUnknown(t *testing.T) {
	db, repo, source, _, now := seedJourney(t)
	id := platform.NewID()
	if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)VALUES($1::uuid,'engagement-api','bulletin.notification.submit','bulletin',$2::uuid,97,jsonb_build_object('source',jsonb_build_object('kind','bulletin','issueId',$2::text,'series','general','notificationGeneration',0)),$1::text,'delivered',$3,$3,$3)`, id, source.Canonical.IssueID, now); e != nil {
		t.Fatal(e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil {
		t.Fatal(e)
	}
	if state.Notification != nil && state.Notification.Status != "unknown" {
		t.Fatalf("source-only legacy row fabricated acceptance %#v", state.Notification)
	}
}
func TestInitialNotificationActualCommandsAndLegacyIdentity(t *testing.T) {
	for _, mode := range []string{"automatic", "manual-first", "legacy-general", "legacy-key-explicit-series", "invalid-request", "invalid-key", "numeric-request"} {
		t.Run(mode, func(t *testing.T) {
			db, repo, source, _, now := seedJourney(t)
			issue := source.Canonical.IssueID
			if mode == "manual-first" {
				if _, e := repo.SubmitBulletinNotification(bulletinAuditContext(), issue, "general", postgresBulletinAuditActor, platform.NewID(), now); e != nil {
					t.Fatal(e)
				}
			} else {
				tx, e := db.Begin()
				if e != nil {
					t.Fatal(e)
				}
				if e = enqueueBulletinPublicationNotification(bulletinAuditContext(), tx, issue, "general", sql.NullInt64{Int64: 987654, Valid: true}, postgresBulletinAuditActor, 97, now); e != nil {
					tx.Rollback()
					t.Fatal(e)
				}
				if e = tx.Commit(); e != nil {
					t.Fatal(e)
				}
			}
			var initial string
			if e := db.QueryRow(`SELECT id::text FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'`, issue).Scan(&initial); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "legacy-general":
				if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET payload_json=jsonb_set(payload_json#-'{source,series}','{idempotencyKey}',to_jsonb('notification:v2:bulletin:'||$2::text)) WHERE id=$1`, initial, issue); e != nil {
					t.Fatal(e)
				}
			case "legacy-key-explicit-series":
				if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET payload_json=jsonb_set(payload_json,'{idempotencyKey}',to_jsonb('notification:v2:bulletin:'||$2::text)) WHERE id=$1`, initial, issue); e != nil {
					t.Fatal(e)
				}
			case "invalid-request":
				if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET payload_json=jsonb_set(payload_json,'{requestId}',to_jsonb(repeat('x',129))) WHERE id=$1`, initial); e != nil {
					t.Fatal(e)
				}
			case "invalid-key":
				if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET payload_json=jsonb_set(payload_json,'{idempotencyKey}','"unrelated-key"') WHERE id=$1`, initial); e != nil {
					t.Fatal(e)
				}
			case "numeric-request":
				if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET payload_json=jsonb_set(payload_json,'{requestId}','123') WHERE id=$1`, initial); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := db.Exec(`UPDATE hhc_web.outbox_event SET status='delivered',attempts=2 WHERE id=$1`, initial); e != nil {
				t.Fatal(e)
			}
			// A later actual manual g1 command cannot replace the initial command.
			if _, e := repo.SubmitBulletinNotification(bulletinAuditContext(), issue, "general", postgresBulletinAuditActor, platform.NewID(), now.Add(time.Second)); e != nil {
				t.Fatal(e)
			}
			state, e := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
			if e != nil {
				t.Fatal(e)
			}
			want := "accepted"
			if mode == "invalid-request" || mode == "invalid-key" || mode == "numeric-request" || mode == "legacy-key-explicit-series" {
				want = "unknown"
			}
			if state.Notification == nil || state.Notification.ID != initial || state.Notification.Status != want || state.Notification.Generation != 0 || state.Notification.CanonicalVersion != nil {
				t.Fatal("initial command evidence drift", mode, state.Notification)
			}
			raw, _ := json.Marshal(state.Notification)
			if strings.Contains(string(raw), postgresBulletinAuditActor) || strings.Contains(string(raw), "idempotencyKey") || strings.Contains(string(raw), "requestId") {
				t.Fatal("private command provenance exposed")
			}
		})
	}
}
