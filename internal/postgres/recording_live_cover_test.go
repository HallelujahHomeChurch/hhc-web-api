package postgres

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestLiveCoverSnapshotSurvivesDefaultChangesAndCreateReplay(t *testing.T) {
	repo, c, now := captureFixture(t)
	ctx := context.Background()
	resetLiveCoverDefault(t, repo)
	first, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.LiveCover == nil || first.LiveCover.Settings.Selection.Mode != "auto" || first.LiveCover.Settings.InheritedRevision != 1 {
		t.Fatalf("missing auto snapshot: %+v", first.LiveCover)
	}
	_, err = repo.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_default SET payload_json=jsonb_set(payload_json,'{Settings,revision}','2') WHERE singleton`)
	if err != nil {
		t.Fatal(err)
	}
	again, err := repo.PrepareCapture(ctx, c, now)
	if err != nil || again.LiveCover.Settings.InheritedRevision != 1 {
		t.Fatalf("replay replaced snapshot: %+v %v", again.LiveCover, err)
	}
	settings, err := repo.GetLiveCoverSettings(ctx, "defaults")
	if err != nil || settings.Settings.Revision != 2 {
		t.Fatalf("defaults %+v %v", settings, err)
	}
}

func resetLiveCoverDefault(t *testing.T, repo *RecordingRepository) {
	t.Helper()
	raw := `{"Settings":{"revision":1,"selection":{"mode":"auto"},"coverState":"waiting"}}`
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_live_cover_default SET payload_json=$1`, raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_live_cover_reference WHERE target='defaults'`)
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_live_cover_operation WHERE target='defaults'`)
		repo.db.Exec(`UPDATE hhc_web.member_recording_live_cover_default SET payload_json=$1`, raw)
	})
}

func TestLiveCoverCASAndPendingSnapshotProtectRetiredDefault(t *testing.T) {
	repo, c, now := captureFixture(t)
	repo.WithAudit(NewAuditOutbox(repo.db))
	resetLiveCoverDefault(t, repo)
	ctx := auditclient.WithProvenance(context.Background(), c.ActorID, c.RequestID)
	prepare := func(target string, revision int64, upload string) recordings.LiveCoverOperation {
		op, err := repo.PrepareLiveCoverOperation(ctx, recordings.LiveCoverOperation{ID: platform.NewID(), Target: target, Actor: c.ActorID, Key: platform.NewID(), Expected: revision, Selection: recordings.LiveCoverSelection{Mode: "custom", UploadID: upload}}, now)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	initial := prepare("defaults", 1, "shared-cover")
	if _, err := repo.CommitLiveCoverOperation(ctx, initial, now, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.LiveCover.Settings.Selection.UploadID != "shared-cover" || c.LiveCover.Settings.InheritedRevision != 2 {
		t.Fatal("wrong snapshot", c.LiveCover)
	}
	a, b := prepare("defaults", 2, "cover-a"), prepare("defaults", 2, "cover-b")
	start := make(chan struct{})
	out := make(chan error, 2)
	var retains atomic.Int32
	retained := make(chan struct{})
	for _, op := range []recordings.LiveCoverOperation{a, b} {
		go func(op recordings.LiveCoverOperation) {
			<-start
			_, err := repo.CommitLiveCoverOperation(ctx, op, now, func(ctx context.Context) error {
				if retains.Add(1) == 2 {
					close(retained)
				}
				wait, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				select {
				case <-retained:
					return nil
				case <-wait.Done():
					return wait.Err()
				}
			})
			out <- err
		}(op)
	}
	close(start)
	e1, e2 := <-out, <-out
	if !((e1 == nil && errors.Is(e2, recordings.ErrPrecondition)) || (e2 == nil && errors.Is(e1, recordings.ErrPrecondition))) || retains.Load() != 2 {
		t.Fatalf("CAS %v %v retains %d", e1, e2, retains.Load())
	}
	release := func(_ context.Context, ref recordings.LiveCoverReference) error {
		if ref.ID == initial.ID {
			t.Fatal("released defaults before pending capture retained")
		}
		return nil
	}
	if err = repo.ReconcileLiveCoverReferences(ctx, now.Add(20*time.Minute), release); err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.Repeat("a", 32)
		c.Status.State = "uploading"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := recordings.LiveCoverReference{ID: c.LiveCover.ReferenceID, Target: c.IntentID, CoverID: "shared-cover", Scope: c.Status.CaptureID, RecordingID: c.RecordingID, Wanted: true}
	if err = repo.ApplyLiveCoverReference(ctx, ref, func(context.Context) error { return errors.New("response lost") }); err == nil {
		t.Fatal("claimed ambiguous retain")
	}
	if err = repo.ReconcileLiveCoverReferences(ctx, now.Add(22*time.Minute), release); err != nil {
		t.Fatal(err)
	}
	if err = repo.ApplyLiveCoverReference(ctx, ref, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	released := false
	if err = repo.ReconcileLiveCoverReferences(ctx, now.Add(24*time.Minute), func(_ context.Context, ref recordings.LiveCoverReference) error {
		if ref.ID == initial.ID {
			released = true
		}
		return nil
	}); err != nil || !released {
		t.Fatalf("old default not released %v %v", released, err)
	}
}

func TestLiveCoverSelectionValidation(t *testing.T) {
	for _, v := range []recordings.LiveCoverSelection{{Mode: "auto", UploadID: "unexpected"}, {Mode: "custom"}, {Mode: "invalid"}, {Mode: "custom", UploadID: "../cover"}} {
		if v.Valid() {
			t.Fatalf("accepted %+v", v)
		}
	}
	if !(recordings.LiveCoverSelection{Mode: "auto"}).Valid() {
		t.Fatal("auto rejected")
	}
}

func TestLiveCoverInheritanceSurvivesMetadataEditAndPendingCleanup(t *testing.T) {
	for _, scenario := range []string{"metadata edit", "pending cleanup"} {
		t.Run(scenario, func(t *testing.T) {
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
				c.LiveCover.Settings.CoverState = "ready"
				c.LiveCover.Settings.CoverID = "live-frame"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.RecordPackageReady(ctx, c.RecordingID, c.Status.CaptureID, now); err != nil {
				t.Fatal(err)
			}
			c, _ = repo.UpdateCapture(ctx, c.IntentID, func(*recordings.Capture) error { return nil })
			offline := errors.New("Asset unavailable")
			if err = repo.InheritLiveCover(ctx, c, "vod-frame-1", now, func(context.Context) error { return offline }); !errors.Is(err, offline) {
				t.Fatal(err)
			}
			if scenario == "metadata edit" {
				value, _ := repo.Get(ctx, c.RecordingID)
				if _, err = repo.UpdateTitle(ctx, c.RecordingID, value.Version, "Updated title", c.ActorID, now, true, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				var releases int
				if err = repo.ReconcileCoverReferences(ctx, now.Add(11*time.Minute), func(context.Context, recordings.CoverOperation) error { releases++; return nil }); err != nil {
					t.Fatal(err)
				}
				if releases != 0 {
					t.Errorf("released active inheritance: %d", releases)
				}
			}
			c, _ = repo.UpdateCapture(ctx, c.IntentID, func(*recordings.Capture) error { return nil })
			if err = repo.InheritLiveCover(ctx, c, "vod-frame-1", now.Add(12*time.Minute), func(context.Context) error { return nil }); err != nil {
				t.Fatal("inheritance could not recover", err)
			}
			value, _ := repo.Get(ctx, c.RecordingID)
			if value.SelectedCoverID != "vod-frame-1" {
				t.Fatal(value)
			}
		})
	}
}
