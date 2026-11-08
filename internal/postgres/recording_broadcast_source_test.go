package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sourceFixture(t *testing.T) (*RecordingRepository, recordings.Broadcast, context.Context, time.Time, *recordings.Service, string, string) {
	t.Helper()
	repo, b, ctx, now := broadcastFixture(t)
	if _, e := repo.db.Exec(`DELETE FROM hhc_web.member_recording_studio_operation; DELETE FROM hhc_web.member_recording_broadcast_output; DELETE FROM hhc_web.member_recording_broadcast_source_operation; UPDATE hhc_web.member_recording_broadcast_source SET revision=1,key_hash='',suffix=NULL,target_recording_id=NULL,direct_settings='{}',direct_actor_id=NULL,direct_recording_id=NULL,prepared=false,deleted_at=NULL`); e != nil {
		t.Fatal(e)
	}
	p, _ := auditclient.ProvenanceFromContext(ctx)
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithBroadcastWriters(true)
	return repo, b, ctx, now, svc, p.ActorID, "hhc_stream_" + strings.Repeat("A", 43)
}
func TestBroadcastSourceReplayAndOwnership(t *testing.T) {
	repo, b, ctx, now, svc, actor, key := sourceFixture(t)
	set := recordings.BroadcastSourceStreamKeyInput{OperationKey: "source-key", ExpectedSourceRevision: 1, StreamKey: key}
	status, e := svc.SetBroadcastSourceStreamKey(ctx, actor, set)
	if e != nil || status.Revision != 2 || !status.Configured {
		t.Fatalf("key %+v %v", status, e)
	}
	if _, e = svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key}); !errors.Is(e, recordings.ErrBroadcastSourceNotPrepared) {
		t.Fatalf("empty target %v", e)
	}
	target := recordings.BroadcastSourceTargetInput{OperationKey: "source-target", ExpectedSourceRevision: 2, RecordingID: b.View.RecordingID, ExpectedRevision: 1}
	status, e = svc.SetBroadcastSourceTarget(ctx, actor, target)
	if e != nil || status.Revision != 3 {
		t.Fatalf("target %+v %v", status, e)
	}
	again, e := svc.SetBroadcastSourceTarget(ctx, actor, target)
	if e != nil || again.Revision != 3 {
		t.Fatalf("replay %+v %v", again, e)
	}
	if _, e = svc.SetBroadcastSourceStreamKey(ctx, actor, set); !errors.Is(e, recordings.ErrBroadcastOperation) {
		t.Fatalf("obsolete replay %v", e)
	}
	resolved, e := svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key})
	if e != nil || resolved.BroadcastRevision != 2 {
		t.Fatalf("resolve %+v %v", resolved, e)
	}
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	bind := recordings.BroadcastSourceBindInput{BroadcastBindInput: recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "source-bind", ExpectedRevision: 2}, ProtocolVersion: recordings.BroadcastProtocolVersion}, RecordingID: b.View.RecordingID, ExpectedSourceRevision: 3}
	if _, _, e = repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor, bind.BroadcastBindInput, now); !errors.Is(e, recordings.ErrBroadcastStreamKey) {
		t.Fatalf("legacy bypass %v", e)
	}
	if _, _, e = repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, false, now); !errors.Is(e, recordings.ErrForbidden) {
		t.Fatalf("publish bypass %v", e)
	}
	_, capture, e := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
	if e != nil {
		t.Fatal(e)
	}
	_, retry, e := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
	if e != nil || retry.IntentID != capture.IntentID {
		t.Fatalf("retry %v", e)
	}
	if _, _, e = repo.PrepareBroadcastSourceBinding(ctx, actor, bind, strings.Repeat("f", 64), true, now); !errors.Is(e, recordings.ErrBroadcastStreamKey) {
		t.Fatalf("key replay bypass %v", e)
	}
	status, e = svc.GetBroadcastSource(ctx)
	if e != nil || !status.InputBusy || status.CanRotate {
		t.Fatalf("busy %+v %v", status, e)
	}
	set.OperationKey = "rotate"
	set.ExpectedSourceRevision = 3
	if _, e = svc.SetBroadcastSourceStreamKey(ctx, actor, set); !errors.Is(e, recordings.ErrBroadcastSourceBusy) {
		t.Fatalf("rotate reserved %v", e)
	}
	var all string
	if e = repo.db.QueryRow(`SELECT (SELECT row_to_json(s)::text FROM hhc_web.member_recording_broadcast_source s)||coalesce((SELECT jsonb_agg(to_jsonb(o))::text FROM hhc_web.member_recording_broadcast_source_operation o),'')`).Scan(&all); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(all, key) {
		t.Fatal("secret persisted")
	}
}

func TestBroadcastSourceDeletionClearsTargetAndFencesOldRevisions(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, bind := preparedSource(t)
	recording, err := repo.Get(ctx, b.View.RecordingID)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(ctx, recording.ID, recording.Version+1, actor, now, true); !errors.Is(err, recordings.ErrPrecondition) {
		t.Fatalf("stale deletion: %v", err)
	}
	before, err := svc.GetBroadcastSource(ctx)
	if err != nil || before.Revision != 3 || before.TargetRecordingID == nil {
		t.Fatalf("failed delete changed source: %+v %v", before, err)
	}
	if err = repo.Delete(ctx, recording.ID, recording.Version, actor, now, true); err != nil {
		t.Fatal(err)
	}
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || source.TargetRecordingID != nil || source.Revision != 4 || !source.Configured || source.InputBusy || !source.CanRotate || source.Suffix == nil || *source.Suffix != *before.Suffix {
		t.Fatalf("deleted target retained: %+v %v", source, err)
	}
	if _, err = svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key}); !errors.Is(err, recordings.ErrBroadcastSourceNotPrepared) {
		t.Fatalf("deleted target resolved: %v", err)
	}
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	if _, _, err = repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now); !errors.Is(err, recordings.ErrBroadcastOperation) {
		t.Fatalf("old source revision bound: %v", err)
	}
	next, err := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Next service"}, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, next.View.RecordingID)
	input := recordings.BroadcastSourceTargetInput{OperationKey: "next-target", ExpectedSourceRevision: 3, RecordingID: next.View.RecordingID, ExpectedRevision: 1}
	if _, err = svc.SetBroadcastSourceTarget(ctx, actor, input); !errors.Is(err, recordings.ErrPrecondition) {
		t.Fatalf("stale Console retargeted: %v", err)
	}
	input.ExpectedSourceRevision = 4
	if _, err = svc.SetBroadcastSourceTarget(ctx, actor, input); err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(ctx, recording.ID, recording.Version, actor, now, true); err != nil {
		t.Fatalf("delete replay: %v", err)
	}
	source, err = svc.GetBroadcastSource(ctx)
	if err != nil || source.Revision != 5 || source.TargetRecordingID == nil || *source.TargetRecordingID != next.View.RecordingID {
		t.Fatalf("delete replay cleared next target: %+v %v", source, err)
	}
	if _, err = svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key}); err != nil {
		t.Fatalf("key no longer usable: %v", err)
	}
}

func TestBroadcastSourceDeletionRollsBackTargetWithoutAudit(t *testing.T) {
	repo, b, ctx, now, svc, actor, _, _ := preparedSource(t)
	recording, err := repo.Get(ctx, b.View.RecordingID)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(context.Background(), recording.ID, recording.Version, actor, now, true); !errors.Is(err, recordings.ErrAuditUnavailable) {
		t.Fatalf("delete without audit: %v", err)
	}
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || source.Revision != 3 || source.TargetRecordingID == nil || *source.TargetRecordingID != recording.ID {
		t.Fatalf("uncommitted delete changed target: %+v %v", source, err)
	}
	if _, err = repo.Get(ctx, recording.ID); err != nil {
		t.Fatalf("uncommitted deletion persisted: %v", err)
	}
}

func TestBroadcastSourceDeletionKeepsUnrelatedOccupiedInput(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, bind := preparedSource(t)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	if _, _, err := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now); err != nil {
		t.Fatal(err)
	}
	other, err := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Unrelated"}, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, other.View.RecordingID)
	if err = repo.Delete(ctx, other.View.RecordingID, 1, actor, now, true); err != nil {
		t.Fatal(err)
	}
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || source.Revision != 3 || !source.InputBusy || source.CanRotate || source.TargetRecordingID == nil || *source.TargetRecordingID != b.View.RecordingID {
		t.Fatalf("unrelated delete changed occupied input: %+v %v", source, err)
	}
}

func TestBroadcastSourceDeletionUsesInputTransactionLock(t *testing.T) {
	repo, b, ctx, now, svc, actor, _, _ := preparedSource(t)
	tx, finishTx, err := repo.sourceTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer finishTx()
	blocked, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err = repo.Delete(blocked, b.View.RecordingID, 1, actor, now, true)
	if err == nil || !errors.Is(blocked.Err(), context.DeadlineExceeded) {
		t.Fatalf("delete bypassed input lock: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || source.Revision != 3 || source.TargetRecordingID == nil {
		t.Fatalf("cancelled delete changed source: %+v %v", source, err)
	}
}

func TestBroadcastSourceCanReplaceHistoricallyDeletedTarget(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, _ := preparedSource(t)
	// Reproduce a target left behind by the previous soft-delete implementation.
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, b.View.RecordingID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetBroadcast(ctx, b.View.RecordingID); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("historical event still visible: %v", err)
	}
	next, err := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Next service"}, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, next.View.RecordingID)
	source, err := svc.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "replace-deleted", ExpectedSourceRevision: 3, RecordingID: next.View.RecordingID, ExpectedRevision: 1})
	if err != nil || source.Revision != 4 || source.TargetRecordingID == nil || *source.TargetRecordingID != next.View.RecordingID {
		t.Fatalf("cannot replace historical target: %+v %v", source, err)
	}
	if _, err = svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key}); err != nil {
		t.Fatalf("original key no longer usable: %v", err)
	}
}

func TestBroadcastSourceCleanupClearsExpiredDraftTarget(t *testing.T) {
	repo, b, ctx, now, svc, _, key, _ := preparedSource(t)
	if err := repo.ReconcileBroadcastCleanup(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetBroadcast(ctx, b.View.RecordingID); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("stale draft not retired: %v", err)
	}
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || source.TargetRecordingID != nil || source.Revision != 4 || !source.Configured || source.InputBusy {
		t.Fatalf("cleanup retained deleted target: %+v %v", source, err)
	}
	if err = repo.ReconcileBroadcastCleanup(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	source, err = svc.GetBroadcastSource(ctx)
	if err != nil || source.Revision != 4 {
		t.Fatalf("cleanup repeated revision change: %+v %v", source, err)
	}
	if _, err = svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key}); !errors.Is(err, recordings.ErrBroadcastSourceNotPrepared) {
		t.Fatalf("expired draft resolved: %v", err)
	}
}

func TestBroadcastSourceCleanupOfHistoricalDeletionKeepsInputOccupied(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, bind := preparedSource(t)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	if _, _, err := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, b.View.RecordingID, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReconcileBroadcastCleanup(ctx, now); err != nil {
		t.Fatal(err)
	}
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || source.TargetRecordingID != nil || source.Revision != 4 || !source.InputBusy || source.CanRotate {
		t.Fatalf("dangling target cleanup released active input: %+v %v", source, err)
	}
}
func TestBroadcastSourceInputOccupancyDerivation(t *testing.T) {
	repo, b, ctx, now, _, actor, _ := sourceFixture(t)
	in := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: b.View.Revision}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	_, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor, in, now)
	if e != nil {
		t.Fatal(e)
	}
	end := 3
	c.Status.CaptureID = "capture-id"
	c.NormalEnd = true
	c.Status.StopAcceptedAt = &now
	c.SealedEndSequence = &end
	c.Status.State = "freezing"
	receipt := &assetclient.RecordingCaptureReceipt{OperationKey: "seal", Operation: "seal", AcceptedAt: now, CaptureID: c.Status.CaptureID}
	for _, tc := range []struct {
		name, state             string
		pending, accepted, want bool
	}{{"missing receipt", "freezing", false, false, true}, {"pending", "freezing", true, true, true}, {"accepted freezing", "freezing", false, true, false}, {"accepted validating", "validating", false, true, false}, {"uploading", "uploading", false, true, true}, {"ready", "ready", false, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			c.Status.State = tc.state
			op := recordings.CaptureOperation{Operation: "seal", Pending: tc.pending}
			if tc.accepted {
				op.Receipt = receipt
			}
			c.Operations["seal"] = op
			raw, _ := json.Marshal(c)
			// Old writers and reconciliation update only payload/active, never input_active.
			if _, e := repo.db.Exec(`UPDATE hhc_web.member_recording_capture SET payload_json=$2,active=$3 WHERE id=$1`, c.IntentID, raw, c.Active()); e != nil {
				t.Fatal(e)
			}
			var got bool
			if e := repo.db.QueryRow(`SELECT input_active FROM hhc_web.member_recording_capture WHERE id=$1`, c.IntentID).Scan(&got); e != nil || got != tc.want {
				t.Fatalf("occupied %v want %v %v", got, tc.want, e)
			}
		})
	}
}

func preparedSource(t *testing.T) (*RecordingRepository, recordings.Broadcast, context.Context, time.Time, *recordings.Service, string, string, recordings.BroadcastSourceBindInput) {
	t.Helper()
	repo, b, ctx, now, svc, actor, key := sourceFixture(t)
	if _, e := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "target", ExpectedSourceRevision: 2, RecordingID: b.View.RecordingID, ExpectedRevision: 1}); e != nil {
		t.Fatal(e)
	}
	bind := recordings.BroadcastSourceBindInput{BroadcastBindInput: recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 2}, ProtocolVersion: recordings.BroadcastProtocolVersion}, StreamKey: key, RecordingID: b.View.RecordingID, ExpectedSourceRevision: 3}
	return repo, b, ctx, now, svc, actor, key, bind
}
func TestBroadcastSourceSealedCaptureAllowsNextAndPinsReplay(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, bind := preparedSource(t)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	_, c, e := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
	if e != nil {
		t.Fatal(e)
	}
	next, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: "next-event", Title: "next"}, actor, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, next.View.RecordingID)
	target := recordings.BroadcastSourceTargetInput{OperationKey: "next-target", ExpectedSourceRevision: 3, RecordingID: next.View.RecordingID, ExpectedRevision: 1}
	if _, e = svc.SetBroadcastSourceTarget(ctx, actor, target); !errors.Is(e, recordings.ErrBroadcastSourceBusy) {
		t.Fatalf("changed reserved target %v", e)
	}
	end := 3
	_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.NormalEnd = true
		c.Status.StopAcceptedAt = &now
		c.SealedEndSequence = &end
		c.Status.State = "validating"
		c.Status.CaptureID = strings.Repeat("c", 32)
		c.Operations["seal"] = recordings.CaptureOperation{Operation: "seal", Receipt: &assetclient.RecordingCaptureReceipt{OperationKey: "seal", Operation: "seal", AcceptedAt: now, CaptureID: c.Status.CaptureID}}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.SetBroadcastSourceTarget(ctx, actor, target); e != nil {
		t.Fatal(e)
	}
	resolved, e := svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key})
	if e != nil || resolved.RecordingID != next.View.RecordingID {
		t.Fatalf("resolve next %+v %v", resolved, e)
	}
	_, replay, e := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
	if e != nil || replay.IntentID != c.IntentID || replay.RecordingID != b.View.RecordingID {
		t.Fatalf("moved replay %+v %v", replay, e)
	}
	stale := bind
	stale.OperationKey = "stale-resolve"
	if _, _, e = repo.PrepareBroadcastSourceBinding(ctx, actor, stale, hash, true, now); !errors.Is(e, recordings.ErrPrecondition) {
		t.Fatalf("stale source revision %v", e)
	}
	bind2 := bind
	bind2.OperationKey = "bind-next"
	bind2.RecordingID = next.View.RecordingID
	bind2.ExpectedSourceRevision = 4
	if _, _, e = repo.PrepareBroadcastSourceBinding(ctx, actor, bind2, hash, true, now); e != nil {
		t.Fatal(e)
	}
	var active, inputs int
	if e = repo.db.QueryRow(`SELECT count(*) FILTER(WHERE active),count(*) FILTER(WHERE input_active) FROM hhc_web.member_recording_capture`).Scan(&active, &inputs); e != nil || active != 2 || inputs != 1 {
		t.Fatalf("active=%d inputs=%d %v", active, inputs, e)
	}
	_, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.Revoked = true
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now); !errors.Is(e, recordings.ErrBroadcastOperation) {
		t.Fatalf("revoked replay %v", e)
	}
}
func TestBroadcastSourceRotationAndBindSerialize(t *testing.T) {
	for _, kind := range []string{"rotate", "bind", "target"} {
		t.Run(kind, func(t *testing.T) {
			repo, _, ctx, now, svc, actor, key, bind := preparedSource(t)
			hash, _, _ := recordings.HashBroadcastSourceKey(key)
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				_, _, e := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
				results <- e
			}()
			go func() {
				<-start
				var e error
				switch kind {
				case "rotate":
					_, e = svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "rotate", ExpectedSourceRevision: 3, StreamKey: "hhc_stream_" + strings.Repeat("B", 42) + "A"})
				case "target":
					_, e = svc.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "reprepare", ExpectedSourceRevision: 3, RecordingID: bind.RecordingID, ExpectedRevision: 2})
				default:
					other := bind
					other.OperationKey = "other-bind"
					_, _, e = repo.PrepareBroadcastSourceBinding(ctx, actor, other, hash, true, now)
				}
				results <- e
			}()
			close(start)
			wins := 0
			for i := 0; i < 2; i++ {
				e := <-results
				if e == nil {
					wins++
				} else if !errors.Is(e, recordings.ErrConflict) && !errors.Is(e, recordings.ErrPrecondition) && !errors.Is(e, recordings.ErrBroadcastStreamKey) {
					t.Fatal(e)
				}
			}
			if wins != 1 {
				t.Fatalf("winners %d", wins)
			}
		})
	}
}

func TestBroadcastSourceTwoConsolesPrepareDifferentEvents(t *testing.T) {
	repo, immediate, ctx, now, svc, actor, key := sourceFixture(t)
	otherActor := platform.NewID()
	otherCtx := auditclient.WithProvenance(context.Background(), otherActor, "other-console")
	scheduledAt := now.Add(time.Hour)
	scheduled, err := repo.CreateBroadcast(otherCtx, recordings.BroadcastCreateInput{OperationKey: "future-event", Title: "Future event", ScheduledAt: &scheduledAt}, otherActor, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, scheduled.View.RecordingID)
	if _, err = svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	start, results := make(chan struct{}), make(chan error, 2)
	for _, request := range []struct {
		ctx       context.Context
		actor, id string
	}{{ctx, actor, immediate.View.RecordingID}, {otherCtx, otherActor, scheduled.View.RecordingID}} {
		go func() {
			<-start
			_, err := svc.SetBroadcastSourceTarget(request.ctx, request.actor, recordings.BroadcastSourceTargetInput{OperationKey: request.id, ExpectedSourceRevision: 2, RecordingID: request.id, ExpectedRevision: 1})
			results <- err
		}()
	}
	close(start)
	wins := 0
	for range 2 {
		if err := <-results; err == nil {
			wins++
		} else if !errors.Is(err, recordings.ErrPrecondition) {
			t.Fatal(err)
		}
	}
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || wins != 1 || source.Revision != 3 || source.InputBusy || source.TargetRecordingID == nil {
		t.Fatalf("prepare must reserve only one target, not input: wins=%d source=%+v err=%v", wins, source, err)
	}
	resolved, err := svc.ResolveBroadcastSource(ctx, recordings.BroadcastSourceResolveInput{StreamKey: key})
	if err != nil || resolved.RecordingID != *source.TargetRecordingID {
		t.Fatalf("resolver did not follow the winning Console: %+v %v", resolved, err)
	}
	var captures, targetOperations int
	if err = repo.db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.member_recording_capture),(SELECT count(*) FROM hhc_web.member_recording_broadcast_source_operation WHERE operation='target')`).Scan(&captures, &targetOperations); err != nil || captures != 0 || targetOperations != 1 {
		t.Fatalf("prepare created capture or committed losing intent: captures=%d operations=%d err=%v", captures, targetOperations, err)
	}
}

func TestBroadcastSourceBindingAndDifferentTargetSerialize(t *testing.T) {
	repo, _, ctx, now, svc, actor, key, bind := preparedSource(t)
	scheduledAt := now.Add(time.Hour)
	next, err := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: "future-event", Title: "Future event", ScheduledAt: &scheduledAt}, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, next.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	start := make(chan struct{})
	bound, retargeted := make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, _, err := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
		bound <- err
	}()
	go func() {
		<-start
		_, err := svc.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "next-target", ExpectedSourceRevision: 3, RecordingID: next.View.RecordingID, ExpectedRevision: 1})
		retargeted <- err
	}()
	close(start)
	bindErr, targetErr := <-bound, <-retargeted
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bindErr == nil {
		if !errors.Is(targetErr, recordings.ErrBroadcastSourceBusy) || !source.InputBusy || source.TargetRecordingID == nil || *source.TargetRecordingID != bind.RecordingID {
			t.Fatalf("bound input moved: source=%+v targetErr=%v", source, targetErr)
		}
	} else if !errors.Is(bindErr, recordings.ErrPrecondition) || targetErr != nil || source.InputBusy || source.TargetRecordingID == nil || *source.TargetRecordingID != next.View.RecordingID {
		t.Fatalf("retargeted input accepted stale binding: source=%+v bindErr=%v targetErr=%v", source, bindErr, targetErr)
	}
}
func TestBroadcastSourceRejectsLegacyKeyAndDeletedReplay(t *testing.T) {
	repo, b, ctx, now, svc, actor, key := sourceFixture(t)
	perEvent := "hhc_live_" + b.View.RecordingID + "_" + strings.Repeat("A", 43)
	if _, e := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, actor, recordings.BroadcastStreamKeyInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "legacy-key", ExpectedRevision: 1}, StreamKey: perEvent}); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "target", ExpectedSourceRevision: 1, RecordingID: b.View.RecordingID, ExpectedRevision: 2}); !errors.Is(e, recordings.ErrBroadcastSourceNotPrepared) {
		t.Fatalf("silently migrated legacy key %v", e)
	}
	_ = repo
	_ = now
	_ = key
}
func TestBroadcastSourceMalformedHistoricalSealStaysOccupied(t *testing.T) {
	repo, _, _, _, _, _, _ := sourceFixture(t)
	for _, end := range []any{"oops", map[string]any{"x": 1}, nil} {
		raw, _ := json.Marshal(map[string]any{"NormalEnd": true, "SealedEndSequence": end, "Status": map[string]any{"state": "validating", "stopAcceptedAt": "2026-10-14T00:00:00Z"}})
		var busy bool
		if e := repo.db.QueryRow(`SELECT hhc_web.capture_input_active(true,$1,1)`, raw).Scan(&busy); e != nil || !busy {
			t.Fatalf("malformed %v %v", busy, e)
		}
	}
}

func TestBroadcastSourceProducerFailureRetainsIntentAndPolicyPermission(t *testing.T) {
	repo, b, ctx, now, svc, actor, key, bind := preparedSource(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	_ = actor
	if _, e := svc.BindBroadcastSource(ctx, p.ActorID, bind, false); !errors.Is(e, recordings.ErrForbidden) {
		t.Fatalf("autopublish without publish: %v", e)
	}
	initial, _ := repo.GetBroadcast(ctx, b.View.RecordingID)
	if initial.CaptureIntentID != "" {
		t.Fatal("denied bind reserved intent")
	}
	if _, e := svc.BindBroadcastSource(ctx, p.ActorID, bind, true); !errors.Is(e, assetclient.ErrUnavailable) {
		t.Fatalf("missing producer %v", e)
	}
	reserved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || reserved.CaptureIntentID == "" {
		t.Fatalf("missing durable intent %v", e)
	}
	if _, e = svc.BindBroadcastSource(ctx, p.ActorID, bind, true); !errors.Is(e, assetclient.ErrUnavailable) {
		t.Fatal(e)
	}
	replay, _ := repo.GetBroadcast(ctx, b.View.RecordingID)
	if replay.CaptureIntentID != reserved.CaptureIntentID || replay.Epoch != 1 {
		t.Fatal("retry created a second intent")
	}
	other, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "legacy"}, p.ActorID, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, other.View.RecordingID)
	legacy := bind.BroadcastBindInput
	legacy.ExpectedRevision = 1
	if _, _, e = repo.PrepareBroadcastBinding(ctx, other.View.RecordingID, p.ActorID, legacy, now); !errors.Is(e, recordings.ErrBroadcastActiveLimit) {
		t.Fatalf("active capture bypass %v", e)
	}
	captureID := "cccccccccccccccccccccccccccccccc"
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		if strings.Contains(string(body), key) || strings.Contains(string(body), "streamKey") {
			t.Error("credential reached asset")
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: captureID, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: "bind", Operation: "create", CaptureID: captureID, AcceptedAt: now}})
	}))
	defer producer.Close()
	svc.WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	result, e := svc.BindBroadcastSource(ctx, p.ActorID, bind, true)
	if e != nil || result.Broadcast.Binding == nil || result.Broadcast.Binding.CaptureID != captureID {
		t.Fatalf("recovery %+v %v", result, e)
	}
	current, _ := repo.GetBroadcast(ctx, b.View.RecordingID)
	if current.CaptureIntentID != reserved.CaptureIntentID {
		t.Fatal("recovery replaced capture")
	}
}

func TestBroadcastSourceReplayRejectsRevokedDeletedAndSuperseded(t *testing.T) {
	for _, state := range []string{"revoked", "deleted", "epoch", "terminal"} {
		t.Run(state, func(t *testing.T) {
			repo, b, ctx, now, svc, actor, key, bind := preparedSource(t)
			hash, _, _ := recordings.HashBroadcastSourceKey(key)
			_, c, e := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
			if e != nil {
				t.Fatal(e)
			}
			switch state {
			case "deleted":
				_, e = repo.db.Exec(`UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, b.View.RecordingID, now)
			case "terminal":
				_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("aborted", "user_abort", now); return nil })
			default:
				_, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
					if state == "epoch" {
						b.Epoch++
					} else {
						b.Revoked = true
					}
					return nil
				})
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = svc.BindBroadcastSource(ctx, actor, bind, true); !errors.Is(e, recordings.ErrBroadcastOperation) {
				t.Fatalf("replay %s: %v", state, e)
			}
		})
	}
}
func TestBroadcastSourceSealEvidenceRequiresB1AndReceipt(t *testing.T) {
	repo, _, _, now, _, _, _ := sourceFixture(t)
	end := 3
	c := recordings.Capture{NormalEnd: true, SealedEndSequence: &end, Status: recordings.CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: strings.Repeat("c", 32), State: "validating"}, StopAcceptedAt: &now}, Operations: map[string]recordings.CaptureOperation{"seal": {Operation: "seal", Receipt: &assetclient.RecordingCaptureReceipt{OperationKey: "seal", Operation: "seal", AcceptedAt: now, CaptureID: strings.Repeat("c", 32)}}}}
	for _, tc := range []struct {
		name   string
		epoch  any
		mutate func(*recordings.Capture)
		want   bool
	}{{"valid", 1, func(*recordings.Capture) {}, false}, {"legacy C1", nil, func(*recordings.Capture) {}, true}, {"no stop", 1, func(c *recordings.Capture) { c.Status.StopAcceptedAt = nil }, true}, {"wrong receipt capture", 1, func(c *recordings.Capture) { c.Operations["seal"].Receipt.CaptureID = strings.Repeat("d", 32) }, true}} {
		raw, _ := json.Marshal(c)
		var candidate recordings.Capture
		json.Unmarshal(raw, &candidate)
		tc.mutate(&candidate)
		raw, _ = json.Marshal(candidate)
		var busy bool
		if e := repo.db.QueryRow(`SELECT hhc_web.capture_input_active(true,$1,$2)`, raw, tc.epoch).Scan(&busy); e != nil || busy != tc.want {
			t.Fatalf("%s busy=%v %v", tc.name, busy, e)
		}
	}
}

func TestBroadcastSourceAutomaticPublicationReadsOwnership(t *testing.T) {
	repo, b, ctx, now, _, actor, key, bind := preparedSource(t)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	_, c, e := repo.PrepareBroadcastSourceBinding(ctx, actor, bind, hash, true, now)
	if e != nil {
		t.Fatal(e)
	}
	end := 3
	_, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
		b.Projection = &assetclient.BroadcastProjection{State: "ready"}
		b.View.Boundaries.EndSequenceExclusive = &end
		c.Status.CaptureID = strings.Repeat("c", 32)
		c.Status.AutoPublish = "pending"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.PublishCapture(ctx, c.IntentID, now, false, now); e != nil {
		t.Fatal(e)
	}
	current, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || !current.SourceManaged {
		t.Fatalf("ownership lost %v", e)
	}
}
