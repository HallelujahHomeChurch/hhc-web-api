package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStudioNamedSourcesCannotClaimEachOthersDraft(t *testing.T) {
	repo, _, ctx, now, svc, actor, _ := sourceFixture(t)
	svc.WithStudioWriters(true)
	create := func(name, key string) recordings.StudioSource {
		v, e := svc.MutateStudioSource(ctx, "", actor, "create", recordings.StudioSourceInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID()}, Name: name, StreamKey: key})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			repo.db.Exec(`DELETE FROM hhc_web.member_recording_studio_operation WHERE source_id=$1`, v.ID)
			repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, v.ID)
		})
		return v
	}
	a := create("A", "hhc_stream_"+strings.Repeat("A", 43))
	b := create("B", "hhc_stream_"+strings.Repeat("B", 42)+"A")
	direct, e := repo.EnsureDirectBroadcast(ctx, a.ID, actor, recordings.BroadcastMutationInput{OperationKey: "direct", ExpectedRevision: a.Revision}, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, direct.View.RecordingID)
	_, e = repo.SetStudioSourceTarget(ctx, b.ID, actor, recordings.BroadcastSourceTargetInput{OperationKey: "steal", ExpectedSourceRevision: b.Revision, ExpectedRevision: direct.View.Revision, RecordingID: direct.View.RecordingID}, now)
	if !errors.Is(e, recordings.ErrBroadcastSourceNotPrepared) {
		t.Fatalf("cross-source adoption: %v", e)
	}
}

func TestStudioPreparedSourceExcludesOtherConsole(t *testing.T) {
	repo, a, ctx, now, svc, actor, key := sourceFixture(t)
	svc.WithStudioWriters(true)
	if _, e := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); e != nil {
		t.Fatal(e)
	}
	var first string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&first)
	other, e := svc.MutateStudioSource(ctx, "", actor, "create", recordings.StudioSourceInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID()}, Name: "Other", StreamKey: "hhc_stream_" + strings.Repeat("B", 42) + "A"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_studio_operation WHERE source_id=$1`, other.ID)
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, other.ID)
	})
	b, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Scheduled"}, actor, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, b.View.RecordingID)
	_, e = repo.SetStudioSourceTarget(ctx, first, actor, recordings.BroadcastSourceTargetInput{OperationKey: "first", ExpectedSourceRevision: 2, ExpectedRevision: 1, RecordingID: a.View.RecordingID}, now)
	if e != nil {
		t.Fatal(e)
	}
	_, e = repo.SetStudioSourceTarget(ctx, other.ID, actor, recordings.BroadcastSourceTargetInput{OperationKey: "other", ExpectedSourceRevision: 1, ExpectedRevision: 1, RecordingID: b.View.RecordingID}, now)
	if !errors.Is(e, recordings.ErrBroadcastSourceBusy) {
		t.Fatalf("second prepared source: %v", e)
	}
}

func TestStudioScheduleDoesNotReserveInput(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, e := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); e != nil {
		t.Fatal(e)
	}
	var source string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source)
	in := recordings.StudioScheduleInput{OperationKey: "scheduled", SourceID: source, Title: "Future", Description: "", ScheduledAt: &now}
	scheduled, e := repo.CreateStudioSchedule(ctx, actor, in, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, scheduled.Broadcast.RecordingID)
	replay, e := repo.CreateStudioSchedule(ctx, actor, in, now)
	if e != nil || replay.Broadcast.RecordingID != scheduled.Broadcast.RecordingID {
		t.Fatal("schedule replay changed event", e)
	}
	var prepared bool
	repo.db.QueryRow(`SELECT prepared FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, source).Scan(&prepared)
	if prepared {
		t.Fatal("future schedule occupied input")
	}
	if scheduled.Broadcast.Policy.AutoStart || !scheduled.Broadcast.Policy.AutoEnd || !scheduled.Broadcast.Policy.AutoPublish || scheduled.Broadcast.Announced {
		t.Fatal("unexpected scheduled policy")
	}
	direct, e := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.BroadcastMutationInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, direct.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	out, e := repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), actor, true, now)
	if e != nil || out.Broadcast.View.RecordingID != direct.View.RecordingID {
		t.Fatal("unprepared schedule captured direct input", e)
	}
}

func TestStudioCustomCoverHasIndependentSourceAndEventReferences(t *testing.T) {
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, e := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); e != nil {
		t.Fatal(e)
	}
	var source string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source)
	first, e := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.BroadcastMutationInput{OperationKey: "first", ExpectedRevision: 2}, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, first.View.RecordingID)
	upload := strings.Repeat("a", 32)
	first, e = repo.UpdateBroadcast(ctx, first.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.CoverSnapshot.Settings.Selection = recordings.LiveCoverSelection{Mode: "custom", UploadID: upload}
		b.CoverSnapshot.Settings.CoverState = "pending"
		b.CoverSnapshot.Scope = "defaults"
		b.CoverSnapshot.ReferenceID = platform.NewID()
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_live_cover_reference WHERE cover_id=$1`, upload)
	})
	var owner string
	if e = repo.db.QueryRow(`SELECT recording_id FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, first.CoverSnapshot.ReferenceID).Scan(&owner); e != nil || owner != "" {
		t.Fatalf("reusable cover still depends on event owner: %q %v", owner, e)
	}

	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, first.View.RecordingID, now); e != nil {
		t.Fatal(e)
	}
	next, e := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.BroadcastMutationInput{OperationKey: "next", ExpectedRevision: 3}, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, next.View.RecordingID)
	if next.CoverSnapshot.ReferenceID == first.CoverSnapshot.ReferenceID || next.CoverSnapshot.Scope != first.CoverSnapshot.Scope {
		t.Fatal("cover identity was not independently inherited")
	}
	retained := 0
	if e = repo.RetainStudioCovers(ctx, now, 10, func(_ context.Context, ref recordings.LiveCoverReference) error {
		retained++
		if ref.RecordingID != "" || ref.Scope != first.CoverSnapshot.Scope {
			t.Fatal("wrong retained asset owner")
		}
		return nil
	}); e != nil || retained != 1 {
		t.Fatalf("source reference: %d %v", retained, e)
	}
	ref := recordings.LiveCoverReference{ID: next.CoverSnapshot.ReferenceID, Target: "broadcast:" + next.View.RecordingID, CoverID: upload, Scope: first.CoverSnapshot.Scope, RecordingID: "", Wanted: true}
	if e = repo.ApplyLiveCoverReference(ctx, ref, func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("recordingId") != "" {
			t.Error("read changed asset owner")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("same-jpeg"))
	}))
	defer media.Close()
	svc.WithPackages(assetclient.New(media.URL, "hhc-web-api", ""))
	data, e := svc.BroadcastCoverContent(ctx, next.View.RecordingID, "", "", false)
	if e != nil || string(data) != "same-jpeg" {
		t.Fatal("inherited cover unavailable", e)
	}
}

func TestStudioCancelledPreparationReleasesCapacity(t *testing.T) {
	repo, b, ctx, now, svc, actor, key := sourceFixture(t)
	if _, e := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); e != nil {
		t.Fatal(e)
	}
	var id string
	repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&id)
	if _, e := repo.SetStudioSourceTarget(ctx, id, actor, recordings.BroadcastSourceTargetInput{OperationKey: "prepare", ExpectedSourceRevision: 2, ExpectedRevision: 1, RecordingID: b.View.RecordingID}, now); e != nil {
		t.Fatal(e)
	}
	current, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.ControlBroadcast(ctx, b.View.RecordingID, actor, "cancel", "", recordings.BroadcastMutationInput{OperationKey: "cancel", ExpectedRevision: current.View.Revision}, ""); e != nil {
		t.Fatal(e)
	}
	tx, finishTx, e := repo.sourceTransaction(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer finishTx()
	var prepared bool
	if e = tx.QueryRow(`SELECT prepared FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, id).Scan(&prepared); e != nil {
		t.Fatal(e)
	}
	if prepared {
		t.Fatal("cancelled unbound event retained capacity")
	}
}

func TestStudioScheduledSourceCanChangeBeforeBinding(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			repo, _, ctx, now, svc, actor, key := sourceFixture(t)
			svc.WithStudioWriters(true)
			_, e := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key})
			if e != nil {
				t.Fatal(e)
			}
			var old string
			repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&old)
			other, e := svc.MutateStudioSource(ctx, "", actor, "create", recordings.StudioSourceInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "other"}, Name: "Other", StreamKey: "hhc_stream_" + strings.Repeat("B", 42) + "A"})
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_output WHERE source_id=$1`, other.ID)
				repo.db.Exec(`DELETE FROM hhc_web.member_recording_studio_operation WHERE source_id=$1`, other.ID)
				repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, other.ID)
			})
			b, e := repo.CreateStudioSchedule(ctx, actor, recordings.StudioScheduleInput{OperationKey: "schedule", SourceID: old, Title: "Scheduled", ScheduledAt: &now}, now)
			if e != nil {
				t.Fatal(e)
			}
			recordingCleanup(t, repo.db, b.Broadcast.RecordingID)
			if prepared {
				_, e = repo.SetStudioSourceTarget(ctx, old, actor, recordings.BroadcastSourceTargetInput{OperationKey: "prepare", ExpectedSourceRevision: 2, ExpectedRevision: 1, RecordingID: b.Broadcast.RecordingID}, now)
			} else {
				_, e = svc.MutateStudioSource(ctx, old, actor, "delete", recordings.StudioSourceInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "delete-old", ExpectedRevision: 2}})
			}
			if e != nil {
				t.Fatal(e)
			}
			in := recordings.StudioSourceAssignment{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "move", ExpectedRevision: 1}, SourceID: other.ID, ExpectedSourceRevision: 1}
			moved, e := repo.AssignStudioSource(ctx, b.Broadcast.RecordingID, actor, in, now)
			if e != nil {
				t.Fatal(e)
			}
			if moved.Broadcast.RecordingID != b.Broadcast.RecordingID || *moved.SourceID != other.ID {
				t.Fatal("changed URL or lost source")
			}
			if _, e = repo.AssignStudioSource(ctx, b.Broadcast.RecordingID, actor, in, now); e != nil {
				t.Fatal("replay", e)
			}
			stale := in
			stale.OperationKey = "stale"
			if _, e = repo.AssignStudioSource(ctx, b.Broadcast.RecordingID, actor, stale, now); !errors.Is(e, recordings.ErrPrecondition) {
				t.Fatal("lost CAS", e)
			}
			if prepared {
				hash, _, _ := recordings.HashBroadcastSourceKey("hhc_stream_" + strings.Repeat("B", 42) + "A")
				out, e := repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), actor, true, now)
				if e != nil || out.Broadcast.View.RecordingID != b.Broadcast.RecordingID {
					t.Fatal("moved reservation lost", e)
				}
				in.OperationKey = "bound"
				in.ExpectedRevision = out.Broadcast.View.Revision
				repo.db.QueryRow(`SELECT revision FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, other.ID).Scan(&in.ExpectedSourceRevision)
				if _, e = repo.AssignStudioSource(ctx, b.Broadcast.RecordingID, actor, in, now); !errors.Is(e, recordings.ErrBroadcastSourceNotPrepared) {
					t.Fatal("bound reassignment allowed", e)
				}
			}
		})
	}
}

func TestStudioSignalHistoryRecordsTransitionsOnlyAndIsBounded(t *testing.T) {
	repo, b, ctx, _, _, _, _ := sourceFixture(t)
	id := b.View.RecordingID
	var raw string
	if err := repo.db.QueryRow(`SELECT payload_json::text FROM hhc_web.member_recording_broadcast WHERE recording_id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		signal := "healthy"
		if i%2 == 0 {
			signal = "interrupted"
		}
		if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,health,state}',to_jsonb($2::text)) WHERE recording_id=$1`, id, signal); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=payload_json WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	view, err := repo.GetStudioBroadcast(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.SignalHistory) != 200 || view.SignalHistory[0].Signal != "interrupted" || view.SignalHistory[1].Signal != "healthy" {
		t.Fatalf("unexpected observations: %+v", view.SignalHistory)
	}
	if view.SignalHistory[0].ObservedAt.Before(view.SignalHistory[1].ObservedAt) {
		t.Fatal("observations not newest first")
	}
}
