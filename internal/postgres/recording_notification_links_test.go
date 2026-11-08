package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"testing"
	"time"
)

func TestRecordingNotificationCanonicalLinksAndReplay(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			t.Run(fmt.Sprintf("mapped=%t/live=%t", mapped, live), func(t *testing.T) {
				db := accessDB(t)
				ctx := context.Background()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				id, actor := platform.NewID(), platform.NewID()
				publicID := "aB_012-xyzQ"
				if mapped {
					if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_public_id(recording_id,public_id) VALUES($1,$2)`, id, publicID); err != nil {
						t.Fatal(err)
					}
				}
				if err = enqueueRecordingNotification(ctx, tx, id, "First", actor, 1, time.Now(), live); err != nil {
					t.Fatal(err)
				}
				if !mapped {
					if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_public_id(recording_id,public_id) VALUES($1,$2)`, id, publicID); err != nil {
						t.Fatal(err)
					}
				}
				if err = enqueueRecordingNotification(ctx, tx, id, "Later", actor, 2, time.Now(), live); err != nil {
					t.Fatal(err)
				}
				var raw []byte
				var count int
				if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1`, id).Scan(&count); err != nil || count != 1 {
					t.Fatalf("count=%d err=%v", count, err)
				}
				if err = tx.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.outbox_event WHERE aggregate_id=$1`, id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var payload publication.NotificationSubmissionPayload
				if err = json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Source == nil || payload.Source.RecordingID != id {
					t.Fatal("changed recording identity")
				}
				if len(payload.Notifications) != 2 {
					t.Fatal("changed channels")
				}
				for _, notification := range payload.Notifications {
					if len(notification.Translations) != 5 {
						t.Fatal("missing locales")
					}
					for locale, translation := range notification.Translations {
						want := "/" + locale + "/member-videos/" + id
						if mapped {
							want = "/video/" + publicID
						}
						if translation.ActionURL != want || translation.Body != "First" || notification.RequiredEntitlements[locale] != "video.meeting-recordings.access" {
							t.Fatalf("translation=%+v want=%s", translation, want)
						}
					}
				}
			})
		}
	}
}
