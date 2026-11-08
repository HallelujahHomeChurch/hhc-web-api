package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestCompensationRetainsDurableUserSource(t *testing.T) {
	db := openBulletinAuditDB(t)
	clearBulletinAuditDB(t, db)
	r := New(db)
	now := time.Now().UTC()
	parent := publication.Event{ID: platform.NewID(), EventType: "news.publish.ensure_asset", AggregateID: platform.NewID(), AggregateVersion: 1, ActorID: postgresBulletinAuditActor}
	parent.Payload, _ = json.Marshal(publication.ContentPublishPayload{ActorID: parent.ActorID, ContentID: parent.AggregateID})
	_, err := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api',$2,'news',$3,1,$4,'parent-test','delivered',$5,$5,$5)`, parent.ID, parent.EventType, parent.AggregateID, parent.Payload, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.FailPublish(context.Background(), parent, "asset-1", "grant-1", "synthetic", now); err != nil {
		t.Fatal(err)
	}
	child, found, err := r.Claim(context.Background(), now, time.Minute)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if child.ActorID != parent.ActorID {
		t.Fatal("durable actor lost")
	}
	var payload map[string]any
	if err := json.Unmarshal(child.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["actorId"] != parent.ActorID {
		t.Fatal("compensation source not persisted")
	}
}

func TestLegacyHomeCompensationUsesOnlyExactDurableManifest(t *testing.T) {
	for _, kind := range []string{"valid", "wrong_manifest", "wrong_version", "legacy_valid", "legacy_wrong_manifest", "legacy_wrong_version", "legacy_current_grant"} {
		t.Run(kind, func(t *testing.T) {
			db := pageGroupTestDatabase(t)
			ctx := context.Background()
			resetPageGroupTables(t, ctx, db)
			repo := New(db)
			now := time.Now().UTC()
			page, err := repo.CreateContent(ctx, content.ModulePages, homeV2IntegrationInput(t, "Home"), postgresBulletinAuditActor, "home-source", now)
			if err != nil {
				t.Fatal(err)
			}
			page, err = repo.PublishContent(ctx, content.ModulePages, page.ID, page.Version, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			parent, found, err := repo.Claim(ctx, now, time.Minute)
			if err != nil || !found {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE hhc_web.outbox_event SET status='delivered',payload_json=payload_json-'actorId' WHERE id=$1`, parent.ID); err != nil {
				t.Fatal(err)
			}
			if strings.TrimPrefix(kind, "legacy_") == "wrong_manifest" {
				if _, err := db.Exec(`UPDATE hhc_web.outbox_event SET payload_json=jsonb_set(payload_json,'{manifestSha256}','"wrong"') WHERE id=$1`, parent.ID); err != nil {
					t.Fatal(err)
				}
			}
			version := parent.AggregateVersion
			if strings.TrimPrefix(kind, "legacy_") == "wrong_version" {
				version++
			}
			if kind == "legacy_current_grant" {
				if _, err := db.Exec(`UPDATE hhc_web.page_item SET banner_public_grant_id='grant-1' WHERE content_id=$1`, page.ID); err != nil {
					t.Fatal(err)
				}
			}
			retirementKey := "publication:" + parent.ID + ":revoke:grant-1"
			if strings.HasPrefix(kind, "legacy_") {
				retirementKey = fmt.Sprintf("home:%s:revoke:grant-1", page.ID)
			}
			_, err = db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api','asset.grant.revoke','home',$2::uuid,$3::bigint,jsonb_build_object('assetId','asset-1','grantId','grant-1','contentId',($2::uuid)::text,'aggregateVersion',$3::bigint),$4,'pending',$5,$5,$5)`, platform.NewID(), page.ID, version, retirementKey, now)
			if err != nil {
				t.Fatal(err)
			}
			child, found, err := repo.Claim(ctx, now, time.Minute)
			if err != nil || !found {
				t.Fatal(err)
			}
			want := ""
			if strings.TrimPrefix(kind, "legacy_") == "valid" {
				want = postgresBulletinAuditActor
			}
			if child.ActorID != want {
				t.Fatalf("source mismatch for %s", kind)
			}
		})
	}
}
