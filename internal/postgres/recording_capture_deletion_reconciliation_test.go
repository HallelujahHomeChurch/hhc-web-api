package postgres

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestDeletedCaptureStopsReconciliationOnlyAfterProducerAcknowledgement(t *testing.T) {
	repo, c, now := captureFixture(t)
	ctx := auditclient.WithProvenance(context.Background(), c.ActorID, "deleted-create")
	if _, err := repo.PrepareCapture(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	creates, deletions := 0, 0
	allowDeletion, tombstoned := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/priv/recordings/"+c.RecordingID {
			deletions++
			if !allowDeletion {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			tombstoned = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		creates++
		// Capacity conflicts before deletion and the permanent tombstone afterwards
		// both leave the original create unresolved at the consumer boundary.
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	client := assetclient.New(server.URL, "hhc-web-api", "")
	service := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(client)
	if err := service.ReconcileCaptures(ctx, 100); !errors.Is(err, assetclient.ErrConflict) || creates != 1 {
		t.Fatalf("initial create: calls=%d err=%v", creates, err)
	}
	if err := service.Delete(ctx, c.RecordingID, c.Create.ExpectedVersion, c.ActorID, true); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileDeletions(ctx); !errors.Is(err, assetclient.ErrUnavailable) {
		t.Fatalf("transient deletion: %v", err)
	}
	pending, err := repo.ListPendingCaptures(ctx, now, 100)
	if err != nil || len(pending) != 1 || pending[0].IntentID != c.IntentID {
		t.Fatalf("unacknowledged compensation lost: %+v %v", pending, err)
	}
	var before string
	if err := repo.db.QueryRow(`SELECT payload_json::text FROM hhc_web.member_recording_capture WHERE id=$1`, c.IntentID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	allowDeletion = true
	if err := service.ReconcileDeletions(ctx); err != nil {
		t.Fatal(err)
	}
	var acknowledged bool
	if err := repo.db.QueryRow(`SELECT deleted_at IS NOT NULL AND deletion_delivered_at IS NOT NULL FROM hhc_web.member_recording WHERE id=$1`, c.RecordingID).Scan(&acknowledged); err != nil || !acknowledged || !tombstoned || deletions != 2 {
		t.Fatalf("missing matching durable producer ACK: ack=%t tombstone=%t deletes=%d err=%v", acknowledged, tombstoned, deletions, err)
	}
	fresh := NewRecordingRepository(repo.db)
	restarted := recordings.NewService(fresh, "", nil, func() time.Time { return now }).WithPackages(client)
	for i := 0; i < 2; i++ {
		pending, err = fresh.ListPendingCaptures(ctx, now, 100)
		if err != nil || len(pending) != 0 {
			t.Fatalf("acknowledged deleted intent reselected after restart: %+v %v", pending, err)
		}
		if err := restarted.ReconcileCaptures(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	if creates != 1 {
		t.Fatalf("terminal tombstoned create retried: %d", creates)
	}
	var after string
	if err := repo.db.QueryRow(`SELECT payload_json::text FROM hhc_web.member_recording_capture WHERE id=$1`, c.IntentID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("selection rewrote immutable operation/receipt history")
	}
	saved, err := fresh.GetCapture(ctx, c.IntentID, c.RecordingID)
	if err != nil || saved.Active() || saved.Status.LiveEnabled || saved.Status.AutoPublish != "cancelled" || !saved.Operations[c.Create.OperationKey].Pending {
		t.Fatalf("terminal history/publication changed: %+v %v", saved, err)
	}
}

func TestCaptureDeletionSelectionRetainsUnsettledAndUnknownWork(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		deleted, acknowledged, active bool
		state                         *string
	}{
		{"before_ack", true, false, false, captureState("aborted")},
		{"nondeleted", false, false, false, captureState("expired")},
		{"active_nondeleted", false, false, true, captureState("uploading")},
		{"active_even_after_ack", true, true, true, captureState("aborted")},
		{"unknown_state", true, true, false, captureState("unknown")},
		{"missing_state", true, true, false, nil},
		{"nonterminal_state", true, true, false, captureState("uploading")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, c, now := captureFixture(t)
			ctx := auditclient.WithProvenance(context.Background(), c.ActorID, "selection-control")
			if _, err := repo.PrepareCapture(ctx, c, now); err != nil {
				t.Fatal(err)
			}
			if tc.deleted {
				if err := repo.Delete(ctx, c.RecordingID, c.Create.ExpectedVersion, c.ActorID, now, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.acknowledged {
				if err := repo.MarkDeletionDelivered(ctx, c.RecordingID); err != nil {
					t.Fatal(err)
				}
			}
			stateJSON := `null`
			if tc.state != nil {
				stateJSON = `"` + *tc.state + `"`
			}
			if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_capture SET active=$2,payload_json=jsonb_set(payload_json,'{Status,state}',$3::jsonb) WHERE id=$1`, c.IntentID, tc.active, stateJSON); err != nil {
				t.Fatal(err)
			}
			pending, err := repo.ListPendingCaptures(ctx, now, 100)
			if err != nil || len(pending) != 1 || pending[0].IntentID != c.IntentID {
				t.Fatalf("unsettled/unknown work suppressed: %+v %v", pending, err)
			}
		})
	}
}

func captureState(value string) *string { return &value }
