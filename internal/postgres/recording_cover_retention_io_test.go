package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func coverNetworkProbe(t *testing.T, repo *RecordingRepository) func(context.Context) error {
	t.Helper()
	return func(ctx context.Context) error {
		if n := repo.db.Stats().InUse; n != 0 {
			return fmt.Errorf("cover network call holds %d SQL connections", n)
		}
		probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		var one int
		return repo.db.QueryRowContext(probe, `SELECT 1`).Scan(&one)
	}
}

func TestCoverRetentionNetworkReleasesSQL(t *testing.T) {
	t.Run("recording selection and release", func(t *testing.T) {
		repo, c, now := captureFixture(t)
		repo.WithAudit(NewAuditOutbox(repo.db))
		ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
		pkg := strings.Repeat("a", 32)
		if err := repo.AddPackage(ctx, c.RecordingID, pkg, now); err != nil {
			t.Fatal(err)
		}
		value, err := repo.RecordPackageReady(ctx, c.RecordingID, pkg, now)
		if err != nil {
			t.Fatal(err)
		}
		op, err := repo.PrepareCoverOperation(ctx, recordings.CoverOperation{ID: platform.NewID(), RecordingID: c.RecordingID, PackageID: pkg, CoverID: "cover", Expected: value.Version, Selection: recordings.CoverSelection{Mode: "custom", UploadID: "cover"}, Actor: c.ActorID, Key: platform.NewID()}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.CommitCoverOperation(ctx, op, true, now, coverNetworkProbe(t, repo)); err != nil {
			t.Fatal(err)
		}
		if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET media_terminal=true WHERE id=$1`, c.RecordingID); err != nil {
			t.Fatal(err)
		}
		if err = repo.ReconcileCoverReferences(ctx, now, func(ctx context.Context, _ recordings.CoverOperation) error { return coverNetworkProbe(t, repo)(ctx) }); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("live settings and release", func(t *testing.T) {
		repo, c, now := captureFixture(t)
		repo.WithAudit(NewAuditOutbox(repo.db))
		resetLiveCoverDefault(t, repo)
		ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
		op, err := repo.PrepareLiveCoverOperation(ctx, recordings.LiveCoverOperation{ID: platform.NewID(), Target: "defaults", Actor: c.ActorID, Key: platform.NewID(), Expected: 1, Selection: recordings.LiveCoverSelection{Mode: "custom", UploadID: "cover"}}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.CommitLiveCoverOperation(ctx, op, now, coverNetworkProbe(t, repo)); err != nil {
			t.Fatal(err)
		}
		if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_live_cover_reference SET wanted=false WHERE id=$1`, op.ID); err != nil {
			t.Fatal(err)
		}
		if err = repo.ReconcileLiveCoverReferences(ctx, now.Add(11*time.Minute), func(ctx context.Context, _ recordings.LiveCoverReference) error {
			return coverNetworkProbe(t, repo)(ctx)
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("capture reference", func(t *testing.T) {
		repo, c, now := captureFixture(t)
		resetLiveCoverDefault(t, repo)
		ctx := context.Background()
		var err error
		c, err = repo.PrepareCapture(ctx, c, now)
		if err != nil {
			t.Fatal(err)
		}
		c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
			c.Status.CaptureID = strings.Repeat("b", 32)
			c.Status.State = "uploading"
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		ref := recordings.LiveCoverReference{ID: platform.NewID(), Target: c.IntentID, CoverID: "cover", Scope: c.Status.CaptureID, RecordingID: c.RecordingID, Wanted: true}
		if err = repo.EnsureLiveCoverReference(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if err = repo.ApplyLiveCoverReference(ctx, ref, coverNetworkProbe(t, repo)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("broadcast reference", func(t *testing.T) {
		repo, b, ctx, now := broadcastFixture(t)
		selection := recordings.LiveCoverSelection{Mode: "custom", UploadID: "cover"}
		p, _ := auditclient.ProvenanceFromContext(ctx)
		s := recordings.NewService(repo, "", nil, func() time.Time { return now })
		if _, err := s.UpdateBroadcast(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastUpdateInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID(), ExpectedRevision: 1}, Cover: &selection}, true); err != nil {
			t.Fatal(err)
		}
		b, err := repo.GetBroadcast(ctx, b.View.RecordingID)
		if err != nil {
			t.Fatal(err)
		}
		ref := recordings.LiveCoverReference{ID: b.CoverSnapshot.ReferenceID, Target: "broadcast:" + b.View.RecordingID, CoverID: "cover", Scope: b.CoverSnapshot.Scope, RecordingID: b.View.RecordingID, Wanted: true}
		if err = repo.ApplyLiveCoverReference(ctx, ref, coverNetworkProbe(t, repo)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("automatic cover", func(t *testing.T) {
		repo, _, c, ctx, _ := liveBroadcastCoverFixture(t, "auto")
		c, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.LiveCover.AutoUploadID = "fresh-cover"; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if err = repo.ApplyLiveAutoCover(ctx, c, assetclient.RecordingCoverUpload{UploadID: "fresh-cover", State: "ready"}, coverNetworkProbe(t, repo)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("inheritance", func(t *testing.T) {
		repo, c, now := captureFixture(t)
		resetLiveCoverDefault(t, repo)
		repo.WithAudit(NewAuditOutbox(repo.db))
		ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
		var err error
		c, err = repo.PrepareCapture(ctx, c, now)
		if err != nil {
			t.Fatal(err)
		}
		c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
			c.Status.CaptureID = strings.Repeat("f", 32)
			c.Status.PackageID = &c.Status.CaptureID
			c.Status.State = "ready"
			c.NormalEnd = true
			c.LiveCover.Settings.CoverID = "live-frame"
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.RecordPackageReady(ctx, c.RecordingID, c.Status.CaptureID, now); err != nil {
			t.Fatal(err)
		}
		c, err = repo.GetCapture(ctx, c.IntentID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err = repo.InheritLiveCover(ctx, c, "vod-cover", now, coverNetworkProbe(t, repo)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("studio reference", func(t *testing.T) {
		repo, _, ctx, now := broadcastFixture(t)
		ref := recordings.LiveCoverReference{ID: platform.NewID(), Target: "source:" + platform.NewID(), CoverID: "cover", Scope: "defaults", Wanted: true}
		if err := repo.EnsureLiveCoverReference(ctx, ref); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { repo.db.Exec(`DELETE FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, ref.ID) })
		if err := repo.RetainStudioCovers(ctx, now, 1, func(ctx context.Context, _ recordings.LiveCoverReference) error {
			return coverNetworkProbe(t, repo)(ctx)
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCoverRetainLateResponseCannotRestoreDisposedSelection(t *testing.T) {
	t.Run("pending custom operation disposed during retain", func(t *testing.T) {
		repo, c, now := captureFixture(t)
		repo.WithAudit(NewAuditOutbox(repo.db))
		resetLiveCoverDefault(t, repo)
		ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
		coverID := platform.NewID()
		op, err := repo.PrepareLiveCoverOperation(ctx, recordings.LiveCoverOperation{ID: platform.NewID(), Target: "defaults", Actor: c.ActorID, Key: platform.NewID(), Expected: 1, Selection: recordings.LiveCoverSelection{Mode: "custom", UploadID: coverID}}, now)
		if err != nil {
			t.Fatal(err)
		}
		released := false
		_, err = repo.CommitLiveCoverOperation(ctx, op, now, func(ctx context.Context) error {
			if err := coverNetworkProbe(t, repo)(ctx); err != nil {
				return err
			}
			// Even an ambiguous failed release permanently fences this reference.
			cleanupErr := repo.ReconcileLiveCoverReferences(ctx, now.Add(11*time.Minute), func(context.Context, recordings.LiveCoverReference) error {
				released = true
				return fmt.Errorf("synthetic lost release response")
			})
			if cleanupErr == nil {
				return fmt.Errorf("release error swallowed")
			}
			return nil
		})
		if err != recordings.ErrConflict || !released {
			t.Fatalf("late selection: released=%t err=%v", released, err)
		}
		cover, err := repo.GetLiveCoverSettings(ctx, "defaults")
		if err != nil || cover.Settings.Revision != 1 || cover.Settings.Selection.Mode != "auto" {
			t.Fatalf("disposed selection restored: %+v %v", cover, err)
		}
	})
	t.Run("automatic generation replaced during retain", func(t *testing.T) {
		repo, _, c, ctx, _ := liveBroadcastCoverFixture(t, "auto")
		c, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.LiveCover.AutoUploadID = "fresh-cover"; return nil })
		if err != nil {
			t.Fatal(err)
		}
		ref := c.LiveCover.AutoReferenceID
		err = repo.ApplyLiveAutoCover(ctx, c, assetclient.RecordingCoverUpload{UploadID: "fresh-cover", State: "ready"}, func(ctx context.Context) error {
			if err := coverNetworkProbe(t, repo)(ctx); err != nil {
				return err
			}
			var count int
			if err := repo.db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.member_recording_live_cover_reference WHERE id=$1 AND NOT applied AND wanted`, ref).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("retain started without durable reference")
			}
			_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
				c.LiveCover.AutoUploadID = "next-cover"
				c.LiveCover.AutoReferenceID = platform.NewID()
				return nil
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		current, err := repo.GetCapture(ctx, c.IntentID, "")
		if err != nil || current.LiveCover.AutoUploadID != "next-cover" || current.LiveCover.Settings.CoverID == "fresh-cover" {
			t.Fatalf("old generation restored: %+v %v", current.LiveCover, err)
		}
		var applied bool
		if err = repo.db.QueryRow(`SELECT applied FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, ref).Scan(&applied); err != nil || applied {
			t.Fatalf("stale reference applied: %t %v", applied, err)
		}
	})
	t.Run("broadcast revoked during retain", func(t *testing.T) {
		repo, b, ctx, now := broadcastFixture(t)
		selection := recordings.LiveCoverSelection{Mode: "custom", UploadID: "cover"}
		p, _ := auditclient.ProvenanceFromContext(ctx)
		s := recordings.NewService(repo, "", nil, func() time.Time { return now })
		if _, err := s.UpdateBroadcast(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastUpdateInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID(), ExpectedRevision: 1}, Cover: &selection}, true); err != nil {
			t.Fatal(err)
		}
		b, err := repo.GetBroadcast(ctx, b.View.RecordingID)
		if err != nil {
			t.Fatal(err)
		}
		ref := recordings.LiveCoverReference{ID: b.CoverSnapshot.ReferenceID, Target: "broadcast:" + b.View.RecordingID, CoverID: "cover", Scope: b.CoverSnapshot.Scope, RecordingID: b.View.RecordingID, Wanted: true}
		err = repo.ApplyLiveCoverReference(ctx, ref, func(ctx context.Context) error {
			if err := coverNetworkProbe(t, repo)(ctx); err != nil {
				return err
			}
			_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
				b.Revoked = true
				return nil
			})
			return err
		})
		if err != recordings.ErrConflict {
			t.Fatalf("revoked retain committed: %v", err)
		}
	})
	t.Run("studio reference retired during retain", func(t *testing.T) {
		repo, _, ctx, now := broadcastFixture(t)
		ref := recordings.LiveCoverReference{ID: platform.NewID(), Target: "source:" + platform.NewID(), CoverID: "cover", Scope: "defaults", Wanted: true}
		if err := repo.EnsureLiveCoverReference(ctx, ref); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { repo.db.Exec(`DELETE FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, ref.ID) })
		if err := repo.RetainStudioCovers(ctx, now, 1, func(ctx context.Context, _ recordings.LiveCoverReference) error {
			if err := coverNetworkProbe(t, repo)(ctx); err != nil {
				return err
			}
			_, err := repo.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET wanted=false WHERE id=$1`, ref.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var applied bool
		if err := repo.db.QueryRow(`SELECT applied FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, ref.ID).Scan(&applied); err != nil || applied {
			t.Fatalf("retired reference applied: %t %v", applied, err)
		}
	})
}
