package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func publishedBroadcastForWithdrawal(t *testing.T) (*RecordingRepository, recordings.Recording, context.Context, time.Time) {
	t.Helper()
	repo, b, ctx, now := broadcastFixture(t)
	repo.WithVODRepublish(true)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	pkg := strings.Repeat("f", 32)
	b, capture, err := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID(), ExpectedRevision: b.View.Revision}, ProtocolVersion: recordings.BroadcastProtocolVersion}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateCapture(ctx, capture.IntentID, func(c *recordings.Capture) error {
		c.NormalEnd = true
		c.Status.CaptureID = pkg
		c.Status.State = "ready"
		c.Status.PackageID = &pkg
		c.Status.StopAcceptedAt = &now
		c.Status.AutoPublish = "published"
		c.VerifiedReadyAt = &now
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	start, end, seconds := 8, 20, 360.36
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.Epoch = 1
		b.View.Announced = true
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: pkg, Epoch: 1, ActorID: actor.ActorID}
		b.View.Boundaries.StartSequence, b.View.Boundaries.EndSequenceExclusive = &start, &end
		b.Projection = &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: b.View.Boundaries.Revision, State: "ready", StartSequence: &start, EndSequenceExclusive: &end, DurationSeconds: &seconds}
		b.StartedAt, b.EndedAt = &now, &now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET uploaded_at=$2,ready_at=$2,expires_at=$3,package_id=$4 WHERE id=$1`, b.View.RecordingID, now, now.Add(24*time.Hour), strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	value, err := repo.Publish(ctx, b.View.RecordingID, 1, actor.ActorID, now)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeWithdrawalRange(t, repo, value.ID, ctx, now)
	return repo, value, ctx, now
}

// Model only the external authority acknowledgement, keeping real CMS transactions.
func acknowledgeWithdrawalRange(t *testing.T, repo *RecordingRepository, id string, ctx context.Context, now time.Time) {
	t.Helper()
	_, err := repo.UpdateBroadcast(ctx, id, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.RangePending = false
		b.RangeApplied = b.View.Boundaries.Revision
		b.Projection.RangeRevision = b.RangeApplied
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompletedBroadcastCanExplicitlyRepublishAfterWithdrawal(t *testing.T) {
	repo, value, ctx, now := publishedBroadcastForWithdrawal(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	original, err := repo.GetBroadcast(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := repo.Unpublish(ctx, value.ID, value.Version, actor.ActorID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, err := repo.GetBroadcast(ctx, value.ID)
	if err != nil || withdrawn.Revoked || !withdrawn.PublicationWithdrawn || !withdrawn.RangePending || withdrawn.MemberState != "blocked" || withdrawn.Projection.State != "ready" || draft.Status != "draft" {
		t.Fatalf("withdrawal destroyed ready media authority: %+v %v", withdrawn, err)
	}
	if !withdrawn.EndedAt.Equal(*original.EndedAt) || *withdrawn.View.Boundaries.StartSequence != 8 || *withdrawn.View.Boundaries.EndSequenceExclusive != 20 {
		t.Fatal("withdrawal changed immutable timeline")
	}
	if _, err = repo.Publish(ctx, value.ID, draft.Version, actor.ActorID, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("published before authority acknowledged denial: %v", err)
	}
	// Reload from durable storage, including the withdrawal marker.
	repo = NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db))
	acknowledgeWithdrawalRange(t, repo, value.ID, ctx, now)
	key := platform.NewID()
	published, err := repo.PublishOperation(ctx, value.ID, draft.Version, actor.ActorID, key, now.Add(2*time.Second))
	if err != nil || published.Current.Status != "published" {
		t.Fatalf("explicit republish: %+v %v", published, err)
	}
	next, err := repo.GetBroadcast(ctx, value.ID)
	if err != nil || next.Revoked || next.MemberState != "vod" || !next.RangePending || next.View.Boundaries.Revision <= withdrawn.View.Boundaries.Revision {
		t.Fatalf("new authority intent missing: %+v %v", next, err)
	}
	if next.PublicationWithdrawn || next.MemberVODReady() {
		t.Fatal("publish marker or pending playback fence invalid")
	}
	if !published.Current.ExpiresAt.Equal(*value.ExpiresAt) || published.Current.PackageID != value.PackageID {
		t.Fatal("republish replaced media or extended retention")
	}
	if _, err = repo.Unpublish(ctx, value.ID, published.Current.Version, actor.ActorID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	replay, err := repo.PublishOperation(ctx, value.ID, draft.Version, actor.ActorID, key, now.Add(4*time.Second))
	if err != nil || replay.Current.Status != "draft" || replay.Outcome != "state_changed" {
		t.Fatalf("old operation republished: %+v %v", replay, err)
	}
	var notifications int
	if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='recording.notification.submit'`, value.ID).Scan(&notifications); err != nil || notifications != 0 {
		t.Fatalf("republish duplicated broadcast notification: %d %v", notifications, err)
	}
}

func TestDeletingCompletedBroadcastStillPermanentlyRevokes(t *testing.T) {
	for _, withdrawFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(withdrawFirst), func(t *testing.T) {
			repo, value, ctx, now := publishedBroadcastForWithdrawal(t)
			actor, _ := auditclient.ProvenanceFromContext(ctx)
			if withdrawFirst {
				var err error
				value, err = repo.Unpublish(ctx, value.ID, value.Version, actor.ActorID, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.Delete(ctx, value.ID, value.Version, actor.ActorID, now, true); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := repo.db.QueryRow(`SELECT payload_json FROM hhc_web.member_recording_broadcast WHERE recording_id=$1`, value.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var b recordings.Broadcast
			if err := json.Unmarshal(raw, &b); err != nil {
				t.Fatal(err)
			}
			if !b.Revoked || b.MemberState != "blocked" {
				t.Fatal("deletion became reversible")
			}

		})
	}
}

func TestDefaultRepositoryKeepsLegacyRevocationUntilRolloutOptIn(t *testing.T) {
	repo, value, ctx, now := publishedBroadcastForWithdrawal(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	unset := NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db))
	if _, err := unset.Unpublish(ctx, value.ID, value.Version, actor.ActorID, now); err != nil {
		t.Fatal(err)
	}
	b, err := repo.GetBroadcast(ctx, value.ID)
	if err != nil || !b.Revoked || b.PublicationWithdrawn {
		t.Fatal("reversible withdrawal enabled before rollout opt-in")
	}
}

func TestWithdrawnHistoryLeavesReconciliationAfterDenialAcknowledgement(t *testing.T) {
	repo, value, ctx, now := publishedBroadcastForWithdrawal(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	if _, err := repo.Unpublish(ctx, value.ID, value.Version, actor.ActorID, now); err != nil {
		t.Fatal(err)
	}
	picked, err := repo.ListBroadcastReconciliation(ctx, now, 100)
	if err != nil || len(picked) != 1 || !picked[0].RangePending {
		t.Fatalf("pending denial lost: %d %v", len(picked), err)
	}
	acknowledgeWithdrawalRange(t, repo, value.ID, ctx, now)
	picked, err = repo.ListBroadcastReconciliation(ctx, now, 100)
	if err != nil || len(picked) != 0 {
		t.Fatalf("settled withdrawal keeps polling: %d %v", len(picked), err)
	}
}

func TestVODWithdrawalDoesNotRestoreTerminalOrRevokedAuthority(t *testing.T) {
	for _, kind := range []string{"revoked", "unfinished", "terminal capture", "terminal media"} {
		t.Run(kind, func(t *testing.T) {
			repo, value, ctx, now := publishedBroadcastForWithdrawal(t)
			actor, _ := auditclient.ProvenanceFromContext(ctx)
			_, err := repo.UpdateBroadcast(ctx, value.ID, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
				switch kind {
				case "revoked":
					b.RevokePublic()
				case "unfinished":
					b.View.Boundaries.EndSequenceExclusive = nil
				case "terminal capture":
					c.Status.State = "failed"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "terminal media" {
				if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET media_terminal=true WHERE id=$1`, value.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = repo.Unpublish(ctx, value.ID, value.Version, actor.ActorID, now); err != nil {
				t.Fatal(err)
			}
			b, err := repo.GetBroadcast(ctx, value.ID)
			if err != nil || !b.Revoked || b.PublicationWithdrawn || b.MemberState != "blocked" {
				t.Fatalf("unsafe reversible %s: %+v %v", kind, b, err)
			}
		})
	}
}

func TestExpiredWithdrawnVODCannotRepublishOrExtendRetention(t *testing.T) {
	repo, value, ctx, now := publishedBroadcastForWithdrawal(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	draft, err := repo.Unpublish(ctx, value.ID, value.Version, actor.ActorID, now)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeWithdrawalRange(t, repo, value.ID, ctx, now)
	if _, err = repo.PublishOperation(ctx, value.ID, draft.Version, actor.ActorID, platform.NewID(), now.Add(25*time.Hour)); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("expired republish: %v", err)
	}
	b, err := repo.GetBroadcast(ctx, value.ID)
	if err != nil || !b.PublicationWithdrawn || b.MemberState != "blocked" {
		t.Fatal("failed publish cleared denial")
	}
}
