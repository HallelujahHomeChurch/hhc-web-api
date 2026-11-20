package postgres

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"strings"
	"sync"
	"testing"
)

func TestStudioOpenPinsOutputAndRejectsOtherOutput(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	direct, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, direct.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	output := platform.NewID()
	first, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now)
	if err != nil || first.Capture.IntentID != again.Capture.IntentID || first.Broadcast.View.RecordingID != again.Broadcast.View.RecordingID {
		t.Fatalf("retry changed identity: %v", err)
	}
	if _, err = repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), actor, true, now); !errors.Is(err, recordings.ErrBroadcastSourceBusy) {
		t.Fatalf("second output: %v", err)
	}
	// A stale output must never acquire a later binding on the same event.
	_, err = repo.UpdateBroadcast(ctx, direct.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.Epoch++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now); !errors.Is(err, recordings.ErrBroadcastOutputTerminal) {
		t.Fatalf("old output accepted changed binding: %v", err)
	}

}
func TestStudioDirectEnsureConcurrentSameDraft(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source)
	var wg sync.WaitGroup
	results := make(chan recordings.Broadcast, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, e := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "same", ExpectedRevision: 2}, now)
			results <- b
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for b := range results {
		if id != "" && id != b.View.RecordingID {
			t.Fatal("two drafts")
		}
		if id == "" {
			recordingCleanup(t, repo.db, b.View.RecordingID)
		}
		id = b.View.RecordingID
	}
}

func TestStudioOutputEndDoesNotReleaseUnsealedCapture(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source)
	b, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, b.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	output := platform.NewID()
	first, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		end := 3
		b.View.Boundaries.EndSequenceExclusive = &end
		b.View.Phase = "processing"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, false, now)
	if err != nil || again.Capture.IntentID != first.Capture.IntentID {
		t.Fatalf("End/disabled writers lost recovery: %v", err)
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_capture SET active=false WHERE id=$1`, first.Capture.IntentID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now); !errors.Is(err, recordings.ErrBroadcastOutputTerminal) {
		t.Fatalf("settled output reopened: %v", err)
	}
	if _, err = repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), actor, false, now); !errors.Is(err, assetclient.ErrUnavailable) {
		t.Fatalf("disabled writer created output: %v", err)
	}
}
func TestStudioPreparedScheduleWinsOverDirect(t *testing.T) {
	repo, scheduled, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source)
	direct, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, direct.View.RecordingID)
	_, err = svc.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "scheduled", ExpectedSourceRevision: 3, RecordingID: scheduled.View.RecordingID, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	repo.WithDirectHandoff(true)
	sources, err := repo.ListStudioSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].DirectRecordingID == nil || *sources[0].DirectRecordingID != direct.View.RecordingID || sources[0].TargetRecordingID == nil || *sources[0].TargetRecordingID != scheduled.View.RecordingID {
		t.Fatalf("source snapshot mixed up direct and scheduled recordings: %v", err)
	}
	_, err = repo.EnsureDirectBroadcast(ctx, source, platform.NewID(), recordings.DirectPrepareInput{OperationKey: "next", ExpectedRevision: sources[0].Revision, PrepareNext: true}, now)
	if !errors.Is(err, recordings.ErrBroadcastSourceBusy) {
		t.Fatalf("explicit preparation displaced scheduled recording: %v", err)
	}
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	out, err := repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), actor, true, now)
	if err != nil || out.Broadcast.View.RecordingID != scheduled.View.RecordingID {
		t.Fatalf("prepared schedule ignored: %v", err)
	}
}

func TestStudioDirectRemembersMetadataAfterPreviousDeletion(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, e := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); e != nil {
		t.Fatal(e)
	}
	var source string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source)
	first, e := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "first", ExpectedRevision: 2}, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, first.View.RecordingID)
	_, e = repo.UpdateBroadcast(ctx, first.View.RecordingID, actor, now, func(ctx context.Context, _ *recordings.Broadcast, r *recordings.Recording, _ *recordings.Capture) error {
		r.Title = "Remember title"
		r.Description = "Remember description"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, first.View.RecordingID, now); e != nil {
		t.Fatal(e)
	}
	next, e := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "next", ExpectedRevision: 3, PrepareNext: true}, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, next.View.RecordingID)
	if next.View.RecordingID == first.View.RecordingID || next.View.Title != "Remember title" || next.View.Description != "Remember description" {
		t.Fatal("next direct lost prior settings")
	}
}

func TestStudioConcurrentSourcesRespectGlobalCapacity(t *testing.T) {
	repo, _, ctx, now, svc, actor, _ := sourceFixture(t)
	svc.WithStudioWriters(true)
	keys := []string{"hhc_stream_" + strings.Repeat("A", 43), "hhc_stream_" + strings.Repeat("B", 42) + "A"}
	sources := make([]recordings.StudioSource, 2)
	for i, key := range keys {
		s, e := svc.MutateStudioSource(ctx, "", actor, "create", recordings.StudioSourceInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID()}, Name: key[len(key)-3:], StreamKey: key})
		if e != nil {
			t.Fatal(e)
		}
		sources[i] = s
		t.Cleanup(func() {
			repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_output WHERE source_id=$1`, s.ID)
			repo.db.Exec(`DELETE FROM hhc_web.member_recording_studio_operation WHERE source_id=$1`, s.ID)
			repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, s.ID)
		})
		b, e := repo.EnsureDirectBroadcast(ctx, s.ID, actor, recordings.DirectPrepareInput{OperationKey: platform.NewID(), ExpectedRevision: 1}, now)
		if e != nil {
			t.Fatal(e)
		}
		recordingCleanup(t, repo.db, b.View.RecordingID)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, key := range keys {
		go func(key string) {
			<-start
			hash, _, _ := recordings.HashBroadcastSourceKey(key)
			_, e := repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), actor, true, now)
			results <- e
		}(key)
	}
	close(start)
	wins, conflicts := 0, 0
	for range keys {
		e := <-results
		if e == nil {
			wins++
		} else if errors.Is(e, recordings.ErrBroadcastOutputCapacity) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("capacity winners=%d conflicts=%d", wins, conflicts)
	}
	for _, s := range sources {
		var active bool
		if e := repo.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c JOIN hhc_web.member_recording_broadcast b ON b.recording_id=c.recording_id WHERE b.source_id=$1 AND c.input_active)`, s.ID).Scan(&active); e != nil {
			t.Fatal(e)
		}
		if !active {
			continue
		}
		var rev int64
		if e := repo.db.QueryRow(`SELECT revision FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, s.ID).Scan(&rev); e != nil {
			t.Fatal(e)
		}
		for _, kind := range []string{"key", "delete"} {
			in := recordings.StudioSourceInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID(), ExpectedRevision: rev}}
			if kind == "key" {
				in.StreamKey = "hhc_stream_" + strings.Repeat("C", 42) + "A"
			}
			if _, e := svc.MutateStudioSource(ctx, s.ID, actor, kind, in); !errors.Is(e, recordings.ErrBroadcastSourceBusy) {
				t.Fatalf("occupied %s: %v", kind, e)
			}
		}
	}
}

func TestStudioCancelledReadyDirectCreatesNextDraft(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	first, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "first", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, first.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	output := platform.NewID()
	bound, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateBroadcast(ctx, first.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
		b.View.Phase = "cancelled"
		b.CancelledAt = &now
		c.Status.State = "ready"
		c.NormalEnd = true
		c.Status.Progress.Ended = true
		c.Status.StopAcceptedAt = &now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_capture SET active=false WHERE id=$1`, bound.Capture.IntentID); err != nil {
		t.Fatal(err)
	}
	var revision int64
	repo.db.QueryRow(`SELECT revision FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, source).Scan(&revision)
	next, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "next", ExpectedRevision: revision, PrepareNext: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if next.View.RecordingID == first.View.RecordingID {
		t.Fatal("cancelled ready capture reused as direct event")
	}
	recordingCleanup(t, repo.db, next.View.RecordingID)
	if _, err = repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now); !errors.Is(err, recordings.ErrBroadcastOutputTerminal) {
		t.Fatalf("old output rebound: %v", err)
	}
	newOutput, err := repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), actor, true, now)
	if err != nil || newOutput.Broadcast.View.RecordingID != next.View.RecordingID {
		t.Fatalf("new output failed to use next draft: %v", err)
	}
}

func TestStudioOutputPinsTenSecondProfile(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	direct, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, direct.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	output := platform.NewID()
	first, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, recordings.BroadcastOutputOptions{MediaProfile: "obs-hls-10s-v1"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, recordings.BroadcastOutputOptions{MediaProfile: "obs-hls-10s-v1"})
	if err != nil || first.Capture.IntentID != again.Capture.IntentID || again.Capture.SegmentFrames != 300 {
		t.Fatalf("profile retry: %v", err)
	}
	if _, err = repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("changed profile: %v", err)
	}
}

func TestStudioOutputPinsRecoveryCapability(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	direct, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, direct.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	output := platform.NewID()
	first, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, recordings.BroadcastOutputOptions{RecoveryVersion: recordings.BroadcastRecoveryVersion})
	if err != nil {
		t.Fatal(err)
	}
	again, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, recordings.BroadcastOutputOptions{RecoveryVersion: recordings.BroadcastRecoveryVersion})
	if err != nil || first.Capture.IntentID != again.Capture.IntentID || again.Capture.RecoveryVersion != recordings.BroadcastRecoveryVersion || again.Capture.RequestedRecoveryVersion != recordings.BroadcastRecoveryVersion {
		t.Fatalf("recovery retry: %v", err)
	}
	if _, err = repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatalf("changed recovery version: %v", err)
	}
}

func TestStudioLegacyEnsureCannotAcquireExistingDirectOwner(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := repo.db.QueryRow("SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton").Scan(&source); err != nil {
		t.Fatal(err)
	}
	first, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.DirectPrepareInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, first.View.RecordingID)
	other := platform.NewID()
	observed, err := repo.EnsureDirectBroadcast(ctx, source, other, recordings.DirectPrepareInput{OperationKey: "legacy-other", ExpectedRevision: 3}, now)
	if err != nil || observed.View.RecordingID != first.View.RecordingID {
		t.Fatalf("legacy current: %v", err)
	}
	var owner string
	var revision int64
	if err := repo.db.QueryRow("SELECT direct_actor_id::text, revision FROM hhc_web.member_recording_broadcast_source WHERE id=$1", source).Scan(&owner, &revision); err != nil {
		t.Fatal(err)
	}
	if owner != actor || revision != 3 {
		t.Fatalf("legacy ensure changed owner/revision (%t, %d)", owner != actor, revision)
	}
}

func TestStudioSealedDirectHandoff(t *testing.T) {
	for _, initiator := range []string{"studio", "obs", "concurrent"} {
		t.Run(initiator, func(t *testing.T) {
			repo, _, ctx, now, svc, actor, key := sourceFixture(t)
			if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
				t.Fatal(err)
			}
			sources, err := repo.ListStudioSources(ctx)
			if err != nil || len(sources) != 1 {
				t.Fatalf("read sources: %v", err)
			}
			source := sources[0].ID
			snapshot := func() recordings.StudioSource {
				t.Helper()
				sources, err := repo.ListStudioSources(ctx)
				if err != nil || len(sources) != 1 {
					t.Fatalf("source snapshot: %v", err)
				}
				return sources[0]
			}
			ensure := func(who, key string, next bool) (recordings.Broadcast, error) {
				t.Helper()
				return repo.EnsureDirectBroadcast(ctx, source, who, recordings.DirectPrepareInput{OperationKey: key, ExpectedRevision: snapshot().Revision, PrepareNext: next}, now)
			}
			a, err := ensure(actor, "first", false)
			if err != nil {
				t.Fatal(err)
			}
			recordingCleanup(t, repo.db, a.View.RecordingID)
			hash, _, _ := recordings.HashBroadcastSourceKey(key)
			oldOutput := platform.NewID()
			old, err := repo.PrepareBroadcastOutput(ctx, hash, oldOutput, actor, true, now)
			if err != nil {
				t.Fatal(err)
			}
			captureID := strings.Repeat("a", 32)
			setReceipt := func(kind, receiptCapture string) {
				t.Helper()
				_, err = repo.UpdateBroadcast(ctx, a.View.RecordingID, actor, now, func(_ context.Context, _ *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
					c.Status.CaptureID = captureID
					c.Status.StopAcceptedAt = &now
					c.Status.State = "uploading"
					if kind == "seal" {
						end := 212
						c.SealedEndSequence = &end
						c.NormalEnd = true
						c.Status.State = "validating"
					}
					c.Operations[kind] = recordings.CaptureOperation{ActorID: actor, Operation: kind, Receipt: &assetclient.RecordingCaptureReceipt{OperationKey: kind, Operation: kind, AcceptedAt: now, CaptureID: receiptCapture}}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			legacy := recordings.DirectPrepareInput{OperationKey: "legacy-view", ExpectedRevision: snapshot().Revision}
			other := platform.NewID()
			view, err := repo.EnsureDirectBroadcast(ctx, source, other, legacy, now)
			if err != nil || view.View.RecordingID != a.View.RecordingID {
				t.Fatalf("legacy active view: %v", err)
			}
			repo.WithDirectHandoff(true)
			setReceipt("stop", captureID)
			if _, err = ensure(actor, "stop-only", true); !errors.Is(err, recordings.ErrBroadcastSourceBusy) {
				t.Fatalf("stop-only handoff: %v", err)
			}
			setReceipt("seal", strings.Repeat("b", 32))
			if snapshot().DirectInputReleased {
				t.Fatal("accepted a seal from a different capture")
			}
			setReceipt("stop", strings.Repeat("b", 32))
			setReceipt("seal", captureID)
			if snapshot().DirectInputReleased {
				t.Fatal("accepted a stop from a different capture")
			}
			setReceipt("stop", captureID)
			setReceipt("seal", captureID)
			current := snapshot()
			if current.DirectRecordingID == nil || *current.DirectRecordingID != a.View.RecordingID || !current.DirectInputReleased || !current.DirectHandoffEnabled || current.InputBusy {
				t.Fatal("released direct snapshot is incorrect")
			}
			repo.WithDirectHandoff(false)
			if _, err = ensure(actor, "off", true); !errors.Is(err, recordings.ErrBroadcastSourceBusy) {
				t.Fatalf("disabled handoff: %v", err)
			}
			repo.WithDirectHandoff(true)
			if _, err := ensure(other, "old-console-after-stop", false); !errors.Is(err, recordings.ErrStudioUpdateRequired) {
				t.Fatalf("outdated console received success for an ended event: %v", err)
			}
			var nextID string
			if initiator == "studio" {
				next, err := ensure(actor, "next", true)
				if err != nil {
					t.Fatal(err)
				}
				nextID = next.View.RecordingID
				recordingCleanup(t, repo.db, nextID)
			}
			type prepareResult struct {
				b   recordings.Broadcast
				err error
			}
			var pending chan prepareResult
			if initiator == "concurrent" {
				input := recordings.DirectPrepareInput{OperationKey: "concurrent", ExpectedRevision: snapshot().Revision, PrepareNext: true}
				pending = make(chan prepareResult, 1)
				go func() {
					b, err := repo.EnsureDirectBroadcast(ctx, source, actor, input, now)
					pending <- prepareResult{b, err}
				}()
			}
			newOutput := platform.NewID()
			opening, err := repo.PrepareBroadcastOutput(ctx, hash, newOutput, actor, true, now)
			if err != nil {
				t.Fatal(err)
			}
			if pending != nil {
				result := <-pending
				if result.err == nil && result.b.View.RecordingID != opening.Broadcast.View.RecordingID {
					t.Fatal("concurrent operations created different direct recordings")
				}
				if result.err != nil && !errors.Is(result.err, recordings.ErrPrecondition) && !errors.Is(result.err, recordings.ErrBroadcastSourceBusy) {
					t.Fatal(result.err)
				}
			}
			if nextID == "" {
				nextID = opening.Broadcast.View.RecordingID
				recordingCleanup(t, repo.db, nextID)
			}
			if nextID == a.View.RecordingID || opening.Broadcast.View.RecordingID != nextID || opening.Capture.IntentID == old.Capture.IntentID {
				t.Fatal("new output did not use an independent binding")
			}
			before := snapshot()
			b, err := ensure(other, "another-tab", false)
			if err != nil || b.View.RecordingID != nextID || snapshot().Revision != before.Revision {
				t.Fatalf("passive open modified source: %v", err)
			}
			owner, err := repo.BroadcastOutputOwner(ctx, hash, platform.NewID())
			if err != nil || owner != actor {
				t.Fatalf("output owner changed: %v", err)
			}
			replay, err := repo.EnsureDirectBroadcast(ctx, source, other, legacy, now)
			if err != nil || replay.View.RecordingID != a.View.RecordingID {
				t.Fatalf("old receipt followed new source: %v", err)
			}
			if _, err = repo.PrepareBroadcastOutput(ctx, hash, oldOutput, actor, true, now); !errors.Is(err, recordings.ErrBroadcastOutputTerminal) {
				t.Fatalf("old output was not terminal: %v", err)
			}
			repo.WithDirectHandoff(false)
			retry, err := repo.PrepareBroadcastOutput(ctx, hash, newOutput, actor, false, now)
			if err != nil || retry.Capture.IntentID != opening.Capture.IntentID {
				t.Fatalf("rollback lost original reservation: %v", err)
			}
		})
	}
}
