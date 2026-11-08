package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestLegacyRetirementRequiresExactDeliveredPublication(t *testing.T) {
	for _, kind := range []string{"valid", "wrong_key", "wrong_type", "wrong_status", "wrong_parent_content", "wrong_parent_version", "wrong_child_content", "wrong_child_version", "wrong_child_key", "explicit_invalid_actor", "current_detail_grant", "current_home_grant", "current_inline_grant"} {
		t.Run(kind, func(t *testing.T) {
			db := openBulletinAuditDB(t)
			clearBulletinAuditDB(t, db)
			if _, err := db.Exec(`TRUNCATE hhc_web.content_entry CASCADE`); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			now := time.Now().UTC()
			id := platform.NewID()
			parent := publication.ContentPublishPayload{ContentID: id, AggregateVersion: 7, ActorID: postgresBulletinAuditActor}
			child := publication.ContentUnpublishPayload{ContentID: id, AggregateVersion: 7, AssetID: "asset-old", GrantID: "grant-old"}
			parentKey, childKey := fmt.Sprintf("news:%s:publish:v7", id), fmt.Sprintf("news:%s:revoke:grant-old", id)
			parentType, parentStatus := "news.publish.ensure_asset", "delivered"
			switch kind {
			case "wrong_key":
				parentKey += ":extra"
			case "wrong_type":
				parentType = "home.publish.ensure_asset"
			case "wrong_status":
				parentStatus = "failed"
			case "wrong_parent_content":
				parent.ContentID = platform.NewID()
			case "wrong_parent_version":
				parent.AggregateVersion++
			case "wrong_child_content":
				child.ContentID = platform.NewID()
			case "wrong_child_version":
				child.AggregateVersion++
			case "wrong_child_key":
				childKey += ":extra"
			case "explicit_invalid_actor":
				child.ActorID = "invalid"
			}
			if strings.HasPrefix(kind, "current_") {
				if _, err := db.Exec(`INSERT INTO hhc_web.content_entry(id,module,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1::uuid,'news',($1::uuid)::text,'seed','seed',$2,$2)`, id, now); err != nil {
					t.Fatal(err)
				}
				if kind == "current_inline_grant" {
					if _, err := db.Exec(`INSERT INTO hhc_web.content_published_asset(entry_id,asset_id,grant_id,public_url,published_version) VALUES($1,'asset-old','grant-old','/assets/old',7)`, id); err != nil {
						t.Fatal(err)
					}
				} else {
					detailGrant, homeGrant := "", ""
					if kind == "current_detail_grant" {
						detailGrant = "grant-old"
					} else {
						homeGrant = "grant-old"
					}
					if _, err := db.Exec(`INSERT INTO hhc_web.news_item(entry_id,slug,display_date,public_grant_id,home_public_grant_id) VALUES($1,'retirement-test',CURRENT_DATE,$2,$3)`, id, detailGrant, homeGrant); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, row := range []struct {
				kind, status, key string
				payload           any
			}{{parentType, parentStatus, parentKey, parent}, {"asset.grant.revoke", "pending", childKey, child}} {
				payload, err := json.Marshal(row.payload)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'asset-api',$2,'news',$3,7,$4,$5,$6,$7,$7,$7)`, platform.NewID(), row.kind, id, payload, row.key, row.status, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			event, found, err := New(db).Claim(ctx, now, time.Minute)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			want := ""
			if kind == "valid" {
				want = postgresBulletinAuditActor
			}
			if kind == "explicit_invalid_actor" {
				want = "invalid"
			}
			if event.ActorID != want {
				t.Fatalf("actor=%q want=%q", event.ActorID, want)
			}
		})
	}
}
