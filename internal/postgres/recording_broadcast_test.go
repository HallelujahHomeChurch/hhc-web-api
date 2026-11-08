package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func broadcastFixture(t *testing.T) (*RecordingRepository, recordings.Broadcast, context.Context, time.Time) {
	t.Helper()
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "broadcast-test")
	now := time.Now().UTC().Truncate(time.Microsecond)
	b, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Broadcast", Policy: recordings.BroadcastPolicy{AutoEnd: true, AutoPublish: true}}, actor, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, db, b.View.RecordingID)
	return repo, b, ctx, now
}
func TestOnlyOneActiveBinding(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	in := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-1", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	_, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, in, now)
	if e != nil {
		t.Fatal(e)
	}
	other, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Other"}, actor.ActorID, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, other.View.RecordingID)
	in.OperationKey = "bind-2"
	if _, _, e = repo.PrepareBroadcastBinding(ctx, other.View.RecordingID, actor.ActorID, in, now); !errors.Is(e, recordings.ErrBroadcastActiveLimit) {
		t.Fatalf("second active %v", e)
	}
	if c.BroadcastEpoch != 1 || c.Status.LiveEnabled || c.Status.AutoPublish != "cancelled" {
		t.Fatalf("capture exposed before marker %+v", c)
	}
}
func TestFailedCreateRetryReusesCapture(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	in := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-replay", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	_, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, in, now)
	if e != nil {
		t.Fatal(e)
	}
	fresh := NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db))
	again, saved, e := fresh.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, in, now)
	if e != nil || saved.IntentID != c.IntentID || again.Epoch != 1 {
		t.Fatalf("retry lost aggregate %+v %+v %v", again, saved, e)
	}
}
func TestBroadcastScheduledDBExemptionAndImmutableCreate(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	r, e := repo.Get(ctx, b.View.RecordingID)
	if e != nil || r.Expired(now.Add(30*24*time.Hour)) {
		t.Fatalf("schedule expired %+v %v", r, e)
	}
	if e = repo.Expire(ctx, r.ID, now.Add(30*24*time.Hour)); !errors.Is(e, recordings.ErrConflict) {
		t.Fatalf("expiry worker changed scheduled %v", e)
	}
	var count int
	if e = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_broadcast_audit WHERE recording_id=$1`, r.ID).Scan(&count); e != nil || count != 1 {
		t.Fatalf("audit count %d %v", count, e)
	}
}
func TestBroadcastUnpublishRevokesRangeTransactionally(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	in := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-close", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	b, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, in, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.State = "uploading"
		c.Status.CaptureID = "cccccccccccccccccccccccccccccccc"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Unpublish(ctx, b.View.RecordingID, 1, actor.ActorID, now); e != nil {
		t.Fatal(e)
	}
	saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || !saved.Revoked || !saved.RangePending {
		t.Fatalf("broadcast range still exposed %+v %v", saved, e)
	}
}
func TestBroadcastCancelledReferenceHasThirtyDayGrace(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	ref := platform.NewID()
	if _, e := repo.db.Exec(`INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,scope,recording_id,created_at) VALUES($1,$2,'unused','broadcast-'||$3,$3,$4)`, ref, "broadcast:"+b.View.RecordingID, b.View.RecordingID, now); e != nil {
		t.Fatal(e)
	}
	_, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, r *recordings.Recording, c *recordings.Capture) error {
		b.CancelledAt = &now
		b.View.Phase = "cancelled"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.ReconcileBroadcastCleanup(ctx, now.Add(29*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	var wanted bool
	if e = repo.db.QueryRow(`SELECT wanted FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, ref).Scan(&wanted); e != nil || !wanted {
		t.Fatalf("released early %v %v", wanted, e)
	}
	if e = repo.ReconcileBroadcastCleanup(ctx, now.Add(30*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e = repo.db.QueryRow(`SELECT wanted FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, ref).Scan(&wanted); e != nil || wanted {
		t.Fatalf("not retired %v %v", wanted, e)
	}
	t.Cleanup(func() { repo.db.Exec(`DELETE FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, ref) })
}
func TestBroadcastAnnouncedIsNotSevenDayAbandonedDraft(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	_, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, r *recordings.Recording, c *recordings.Capture) error {
		b.View.Announced = true
		b.View.Phase = "scheduled"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.ReconcileBroadcastCleanup(ctx, now.Add(31*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.GetBroadcast(ctx, b.View.RecordingID); e != nil {
		t.Fatalf("announced schedule deleted %v", e)
	}
}
func TestBroadcastMarkerGatesVerifiedRangeAndStaffControl(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	capture := "dddddddddddddddddddddddddddddddd"
	verified := 3
	var policy assetclient.BroadcastRangeInput
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/priv/recording-captures":
			var in struct {
				RecordingID    string `json:"recordingId"`
				OperationKey   string `json:"operationKey"`
				BroadcastEpoch int64  `json:"broadcastEpoch"`
			}
			if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
				t.Error(e)
			}
			if in.BroadcastEpoch != 1 {
				t.Error("deny-all capability absent")
			}
			json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: capture, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: verified}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: in.OperationKey, Operation: "create", CaptureID: capture, AcceptedAt: now}})
		case r.Method == "PUT":
			json.NewDecoder(r.Body).Decode(&policy)
			json.NewEncoder(w).Encode(policy)
		case strings.HasSuffix(r.URL.Path, "/broadcast-projection"):
			state := "pending"
			var start, duration *float64
			if policy.StartSequence != nil && verified >= *policy.StartSequence && (policy.EndSequenceExclusive == nil || verified >= *policy.EndSequenceExclusive-1) {
				state = "ready"
				origin := float64(*policy.StartSequence) * 30.03
				start = &origin
				length := float64(verified-*policy.StartSequence+1) * 30.03
				if policy.EndSequenceExclusive != nil {
					length = float64(*policy.EndSequenceExclusive-*policy.StartSequence) * 30.03
				}
				duration = &length
			}
			json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: policy.RangeRevision, State: state, StartSequence: policy.StartSequence, EndSequenceExclusive: policy.EndSequenceExclusive, MediaStartSeconds: start, DurationSeconds: duration, Revision: 1})
		default:
			t.Errorf("unexpected producer %s %s", r.Method, r.URL.Path)
			w.WriteHeader(503)
		}
	}))
	defer producer.Close()
	s := recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", "")).WithBroadcastWriters(true)
	result, e := s.ControlBroadcast(ctx, b.View.RecordingID, p.ActorID, "announce", "", recordings.BroadcastMutationInput{OperationKey: "announce", ExpectedRevision: 1}, "")
	if e != nil {
		t.Fatal(e)
	}
	result, e = s.BindBroadcast(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: result.Broadcast.Revision}, ProtocolVersion: recordings.BroadcastProtocolVersion}, true)
	if e != nil {
		t.Fatal(e)
	}
	staff := platform.NewID()
	staffCtx := auditclient.WithProvenance(context.Background(), staff, "staff-control")
	result, e = s.ControlBroadcast(staffCtx, b.View.RecordingID, staff, "start", "", recordings.BroadcastMutationInput{OperationKey: "start", ExpectedRevision: result.Broadcast.Revision}, "")
	if e != nil {
		t.Fatal(e)
	}
	command := result.Broadcast.PendingCommand.CommandID
	title := "During pending"
	result, e = s.UpdateBroadcast(staffCtx, b.View.RecordingID, staff, recordings.BroadcastUpdateInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "title", ExpectedRevision: result.Broadcast.Revision}, Title: &title}, true)
	if e != nil {
		t.Fatal(e)
	}
	ack := recordings.BroadcastMarkerAckInput{OperationKey: "marker-start", CaptureID: capture, Epoch: 1, BoundarySequence: 8}
	if _, e = s.AcknowledgeBroadcast(staffCtx, b.View.RecordingID, staff, command, ack); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("staff uploader bypass %v", e)
	}
	if _, e = s.AcknowledgeBroadcast(ctx, b.View.RecordingID, p.ActorID, command, ack); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcileBroadcasts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	view, e := s.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || view.Phase != "start_pending" || policy.StartSequence == nil || *policy.StartSequence != 8 {
		t.Fatalf("unverified start exposed %+v %v", view, e)
	}
	verified = 8
	if e = s.ReconcileBroadcasts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	view, e = s.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || view.Phase != "live" {
		t.Fatalf("verified start unavailable %+v %v", view, e)
	}
	publicRecord, err := repo.Get(ctx, b.View.RecordingID)
	if err != nil || publicRecord.BroadcastStartedAt == nil {
		t.Fatalf("public start was not persisted: %+v %v", publicRecord, err)
	}
	firstStart := *publicRecord.BroadcastStartedAt
	if err = s.ReconcileBroadcast(ctx, b.View.RecordingID); err != nil {
		t.Fatal(err)
	}
	publicRecord, err = repo.Get(ctx, b.View.RecordingID)
	if err != nil || publicRecord.BroadcastStartedAt == nil || !publicRecord.BroadcastStartedAt.Equal(firstStart) {
		t.Fatal("public start changed on reconciliation", err)
	}
	member := platform.NewID()
	watch, e := s.ResolveRecordingWatch(ctx, b.View.RecordingID, member)
	if e != nil || watch.View != "live" || watch.Live == nil || watch.MediaStartSeconds == nil || *watch.MediaStartSeconds != 8*30.03 {
		t.Fatalf("direct resolver failed %+v %v", watch, e)
	}
	if _, e = s.UpdateBroadcast(staffCtx, b.View.RecordingID, staff, recordings.BroadcastUpdateInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "policy", ExpectedRevision: view.Revision}, Policy: &recordings.BroadcastPolicy{}}, true); !errors.Is(e, recordings.ErrConflict) {
		t.Fatalf("bound policy changed %v", e)
	}
	result, e = s.ControlBroadcast(staffCtx, b.View.RecordingID, staff, "end", "", recordings.BroadcastMutationInput{OperationKey: "end", ExpectedRevision: view.Revision}, "")
	if e != nil {
		t.Fatal(e)
	}
	command = result.Broadcast.PendingCommand.CommandID
	ack = recordings.BroadcastMarkerAckInput{OperationKey: "marker-end", CaptureID: capture, Epoch: 1, BoundarySequence: 20}
	if _, e = s.AcknowledgeBroadcast(ctx, b.View.RecordingID, p.ActorID, command, ack); e != nil {
		t.Fatal(e)
	}
	pendingEnd, err := repo.GetBroadcast(ctx, b.View.RecordingID)
	if err != nil || pendingEnd.EndedAt != nil {
		t.Fatal("unverified End recorded public end", err)
	}
	replayScope := platform.NewID()
	if e = repo.WithCaptureScope(ctx, b.View.RecordingID, capture, member, replayScope, now, false, func(recordings.Capture, string) error { return nil }); e != nil {
		t.Fatalf("end pending lost verified live prefix %v", e)
	}
	_, e = repo.UpdateCapture(ctx, capture, func(c *recordings.Capture) error {
		c.NormalEnd = true
		c.Status.State = "ready"
		c.Status.StopAcceptedAt = &now
		c.Status.Progress.Ended = true
		end := 21
		c.SealedEndSequence = &end
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Minute)
	verified = 20
	if e = s.ReconcileBroadcasts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || *saved.View.Boundaries.EndSequenceExclusive != 20 || saved.View.Phase != "processing" {
		t.Fatalf("Stop overwrote End marker %+v %v", saved, e)
	}
	if saved.EndedAt == nil || !saved.EndedAt.Equal(now) {
		t.Fatalf("public end missing: %v", saved.EndedAt)
	}
	firstEnd := *saved.EndedAt
	now = now.Add(time.Minute)
	if saved.View.Archive.Reason != nil || !saved.AutoPublicationActivated {
		t.Fatalf("bridge cancellation misreported as a blocked publication: %+v", saved.View.Archive)
	}
	for _, state := range []string{"blocked", "pending"} {
		if _, e = repo.UpdateCapture(ctx, capture, func(c *recordings.Capture) error { c.Status.AutoPublish = state; return nil }); e != nil {
			t.Fatal(e)
		}
		if e = s.ReconcileBroadcasts(context.Background(), 100); e != nil {
			t.Fatal(e)
		}
		current, err := repo.GetBroadcast(ctx, b.View.RecordingID)
		if err != nil {
			t.Fatal(err)
		}
		if current.EndedAt == nil || !current.EndedAt.Equal(firstEnd) {
			t.Fatal("public end changed during publication")
		}
		if state == "blocked" && (current.View.Archive.Reason == nil || *current.View.Archive.Reason != "auto_publish_blocked") || state == "pending" && current.View.Archive.Reason != nil {
			t.Fatalf("publication state %s not reflected: %+v", state, current.View.Archive)
		}
	}

	if e = repo.WithCaptureScope(ctx, b.View.RecordingID, capture, member, platform.NewID(), now, false, func(recordings.Capture, string) error { t.Fatal("unpublished archive minted live grant"); return nil }); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("completed B1 live admission %v", e)
	}
	if policy.MemberState != "live" {
		t.Fatalf("processing revoked existing media scope: %s", policy.MemberState)
	}
	if e = repo.WithCaptureScope(ctx, b.View.RecordingID, capture, member, replayScope, now, false, func(recordings.Capture, string) error { return nil }); e != nil {
		t.Fatalf("processing lost admitted replay scope: %v", e)
	}
	if e = repo.WithCaptureScope(ctx, b.View.RecordingID, capture, platform.NewID(), replayScope, now, false, func(recordings.Capture, string) error { t.Fatal("scope stolen"); return nil }); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("scope owner: %v", e)
	}
	// Publication changes authority to VOD, but an already admitted live scope
	// keeps its original replay deadline and cannot admit a new viewer.
	if _, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Phase, b.MemberState = "archived", "vod"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = repo.WithCaptureScope(ctx, b.View.RecordingID, capture, member, replayScope, now, false, func(recordings.Capture, string) error { return nil }); e != nil {
		t.Fatalf("archived lost admitted replay scope: %v", e)
	}
	if e = repo.WithCaptureScope(ctx, b.View.RecordingID, capture, member, platform.NewID(), now, false, func(recordings.Capture, string) error { t.Fatal("archive admitted a new live scope"); return nil }); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("archived admission: %v", e)
	}
	if _, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Phase, b.MemberState = "processing", "live"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	deadline := now.Add(time.Minute)
	if _, e = repo.UpdateCapture(ctx, capture, func(c *recordings.Capture) error { c.Status.ReplayUntil = &deadline; return nil }); e != nil {
		t.Fatal(e)
	}
	if e = repo.WithCaptureScope(ctx, b.View.RecordingID, capture, member, replayScope, deadline, false, func(recordings.Capture, string) error { t.Fatal("expired scope renewed"); return nil }); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("replay deadline: %v", e)
	}
	_, e = s.CancelCaptureAutoPublish(staffCtx, b.View.RecordingID, capture, staff, "cancel-auto", true)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcileBroadcasts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	c, e := repo.GetCapture(ctx, capture, b.View.RecordingID)
	if e != nil || c.Status.AutoPublish != "cancelled" {
		t.Fatalf("reconciler reactivated cancellation %+v %v", c, e)
	}
	cancelled, err := repo.GetBroadcast(ctx, b.View.RecordingID)
	if err != nil || cancelled.View.Archive.Reason == nil || *cancelled.View.Archive.Reason != "auto_publish_cancelled" {
		t.Fatalf("cancelled publication reason lost: %+v %v", cancelled.View.Archive, err)
	}

}
func TestBroadcastWritersGateAndAutoStartPermission(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	s := recordings.NewService(repo, "", nil, func() time.Time { return now })
	if _, e := s.CreateBroadcast(ctx, recordings.BroadcastCreateInput{Title: "new", OperationKey: "new"}, p.ActorID, true); !errors.Is(e, assetclient.ErrUnavailable) {
		t.Fatalf("disabled writer accepted %v", e)
	}
	s.WithBroadcastWriters(true)
	if _, e := s.CreateBroadcast(ctx, recordings.BroadcastCreateInput{Title: "auto", OperationKey: "auto", Policy: recordings.BroadcastPolicy{AutoStart: true}}, p.ActorID, false); !errors.Is(e, recordings.ErrForbidden) {
		t.Fatalf("autoStart without publish %v", e)
	}
	if _, e := s.GetBroadcast(ctx, b.View.RecordingID); e != nil {
		t.Fatal(e)
	}
}
func TestBroadcastFailedBeforePublicRebindIncrementsEpoch(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	in := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "first", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	b, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, in, now)
	if e != nil {
		t.Fatal(e)
	}
	_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("failed", "encoder_failure", now); return nil })
	if e != nil {
		t.Fatal(e)
	}
	b, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, r *recordings.Recording, c *recordings.Capture) error {
		b.Revoked = true
		b.View.Phase = "failed"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	in.OperationKey = "second"
	in.ExpectedRevision = b.View.Revision
	b, second, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, in, now)
	if e != nil || b.Epoch != 2 || second.IntentID == c.IntentID || b.Revoked {
		t.Fatalf("failed prelive retry rejected %+v %v", b, e)
	}
}
func TestBroadcastBindingAuditFailureRollsBackCapture(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	name := "b1_audit_failure_" + strings.ReplaceAll(b.View.RecordingID, "-", "")
	if _, e := repo.db.Exec(`ALTER TABLE hhc_web.member_recording_broadcast_audit ADD CONSTRAINT ` + name + ` CHECK(operation<>'bind')`); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { repo.db.Exec(`ALTER TABLE hhc_web.member_recording_broadcast_audit DROP CONSTRAINT ` + name) })
	in := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "failure", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	if _, _, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, in, now); e == nil {
		t.Fatal("audit failure accepted")
	}
	var count int
	if e := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_capture WHERE recording_id=$1`, b.View.RecordingID).Scan(&count); e != nil || count != 0 {
		t.Fatalf("capture persisted without audit %d %v", count, e)
	}
	saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || saved.CaptureIntentID != "" || saved.Epoch != 0 {
		t.Fatal("partial binding persisted")
	}
}
func TestBroadcastFailedCreateResponseRetriesSamePrivateIntent(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	calls := 0
	capture := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			RecordingID  string `json:"recordingId"`
			OperationKey string `json:"operationKey"`
			Epoch        int64  `json:"broadcastEpoch"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.OperationKey != "lost-create" || in.Epoch != 1 {
			t.Errorf("intent changed %+v", in)
		}
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: capture, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: in.OperationKey, Operation: "create", CaptureID: capture, AcceptedAt: now}})
	}))
	defer producer.Close()
	newService := func() *recordings.Service {
		return recordings.NewService(NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db)), "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", "")).WithBroadcastWriters(true)
	}
	in := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "lost-create", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	if _, e := newService().BindBroadcast(ctx, b.View.RecordingID, p.ActorID, in, true); !errors.Is(e, assetclient.ErrUnavailable) {
		t.Fatalf("failure not surfaced %v", e)
	}
	saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil {
		t.Fatal(e)
	}
	intent := saved.CaptureIntentID
	result, e := newService().BindBroadcast(ctx, b.View.RecordingID, p.ActorID, in, true)
	if e != nil || result.Broadcast.Binding == nil || result.Broadcast.Binding.CaptureID != capture || calls != 2 {
		t.Fatalf("retry failed %+v %v", result, e)
	}
	saved, e = repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || saved.CaptureIntentID != intent || saved.Epoch != 1 {
		t.Fatal("retry created second capture")
	}
}
func TestBroadcastCustomCoverSnapshotRetainIsFenced(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	selected := recordings.LiveCoverSelection{Mode: "custom", UploadID: "custom-image"}
	s := recordings.NewService(repo, "", nil, func() time.Time { return now })
	result, e := s.UpdateBroadcast(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastUpdateInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "select", ExpectedRevision: 1}, Cover: &selected}, true)
	if e != nil {
		t.Fatal(e)
	}
	b, e = repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil {
		t.Fatal(e)
	}
	ref := recordings.LiveCoverReference{ID: b.CoverSnapshot.ReferenceID, Target: "broadcast:" + b.View.RecordingID, CoverID: selected.UploadID, Scope: "broadcast-" + b.View.RecordingID, RecordingID: b.View.RecordingID, Wanted: true}
	retains := 0
	if e = repo.ApplyLiveCoverReference(ctx, ref, func(context.Context) error { retains++; return nil }); e != nil {
		t.Fatal(e)
	}
	if e = repo.ApplyLiveCoverReference(ctx, ref, func(context.Context) error { retains++; return nil }); e != nil || retains != 1 {
		t.Fatalf("retained twice %d %v", retains, e)
	}
	b, e = repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || b.View.Cover.CoverState != "ready" || b.View.Cover.Selection.UploadID != selected.UploadID {
		t.Fatalf("cover not ready %+v %v", b, e)
	}
	auto := recordings.LiveCoverSelection{Mode: "auto"}
	if _, e = s.UpdateBroadcast(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastUpdateInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "replace", ExpectedRevision: result.Broadcast.Revision}, Cover: &auto}, true); e != nil {
		t.Fatal(e)
	}
	if e = repo.ApplyLiveCoverReference(ctx, ref, func(context.Context) error { retains++; return nil }); !errors.Is(e, recordings.ErrConflict) {
		t.Fatalf("late retain resurrected %v", e)
	}
	t.Cleanup(func() {
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_live_cover_reference WHERE target=$1`, ref.Target)
	})
}

func TestBroadcastEmergencyClosePreservesFrozenBoundaries(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		t.Run(fmt.Sprint(frozen), func(t *testing.T) {
			repo, b, ctx, now := broadcastFixture(t)
			p, _ := auditclient.ProvenanceFromContext(ctx)
			_, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}, now)
			if e != nil {
				t.Fatal(e)
			}
			capture := strings.Repeat("e", 32)
			_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
				c.Status.CaptureID = capture
				c.Status.State = "uploading"
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			start, end := 8, 20
			b, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
				b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: p.ActorID}
				b.View.Boundaries.StartSequence = &start
				if frozen {
					b.View.Boundaries.EndSequenceExclusive = &end
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			// Emergency revocation must persist even when Asset is unavailable.
			s := recordings.NewService(repo, "", nil, func() time.Time { return now })
			result, e := s.ControlBroadcast(ctx, b.View.RecordingID, p.ActorID, "emergency-close", "", recordings.BroadcastMutationInput{OperationKey: "close", ExpectedRevision: b.View.Revision}, "privacy")
			if e != nil {
				t.Fatal(e)
			}
			got := result.Broadcast.Boundaries.EndSequenceExclusive
			if frozen && (got == nil || *got != 20) || !frozen && got != nil {
				t.Fatalf("emergency changed frozen boundary: %v", got)
			}
		})
	}
}

func TestBroadcastPublicationPersistsAuthorityIntent(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	start, end, seconds := 8, 20, 360.36
	b, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Boundaries.StartSequence = &start
		b.View.Boundaries.EndSequenceExclusive = &end
		b.MemberState = "blocked"
		b.Projection = &assetclient.BroadcastProjection{State: "ready", StartSequence: &start, EndSequenceExclusive: &end, DurationSeconds: &seconds}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = repo.db.Exec(`UPDATE hhc_web.member_recording SET uploaded_at=$2,expires_at=$3,package_id=$4 WHERE id=$1`, b.View.RecordingID, now, now.Add(24*time.Hour), strings.Repeat("f", 32))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Publish(ctx, b.View.RecordingID, 1, p.ActorID, now); e != nil {
		t.Fatal(e)
	}
	saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || saved.MemberState != "vod" || !saved.RangePending || saved.View.Phase != "archived" {
		t.Fatalf("publication lost authority intent %+v %v", saved, e)
	}
}
func TestBroadcastSchedulePresenceIsIdempotencyBound(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	s := recordings.NewService(repo, "", nil, func() time.Time { return now })
	title := "new title"
	in := recordings.BroadcastUpdateInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "same-key", ExpectedRevision: 1}, Title: &title}
	if _, e := s.UpdateBroadcast(ctx, b.View.RecordingID, p.ActorID, in, true); e != nil {
		t.Fatal(e)
	}
	in.ScheduleSet = true
	if _, e := s.UpdateBroadcast(ctx, b.View.RecordingID, p.ActorID, in, true); !errors.Is(e, recordings.ErrBroadcastOperation) {
		t.Fatalf("same key accepted schedule clear: %v", e)
	}
}

func TestBroadcastStaffPreviewPersistsOnlyBoundedAudit(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	_, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}, now)
	if e != nil {
		t.Fatal(e)
	}
	capture := strings.Repeat("a", 32)
	_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = capture
		c.Status.State = "uploading"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: p.ActorID}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	badPurpose := false
	var blockRenew atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blockRenew.Load() {
			entered <- struct{}{}
			<-release
		}
		var in assetclient.BroadcastPreviewGrantInput
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
		}
		purpose := "staff-preview"
		if badPurpose {
			purpose = "member"
		}
		json.NewEncoder(w).Encode(assetclient.BroadcastPreviewGrant{RecordingID: in.RecordingID, CaptureID: capture, Epoch: in.Epoch, Purpose: purpose, Scope: in.PlaybackScopeID, MediaPath: "/videos/" + in.RecordingID + "/captures/" + capture + "/previews/" + in.PlaybackScopeID + "/master.m3u8", ExchangeCredential: "credential-must-never-persist", IssuedAt: now, ExpiresAt: in.ExpiresAt})
	}))
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
		producer.Close()
	}()
	s := recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	staff := platform.NewID()
	staffCtx := auditclient.WithProvenance(context.Background(), staff, "staff-preview")
	grant, e := s.BroadcastPreview(staffCtx, b.View.RecordingID, staff, capture, 1, "")
	if e != nil || grant.Purpose != "staff-preview" || grant.ExpiresAt.Sub(now) > 5*time.Minute {
		t.Fatalf("preview invalid %+v %v", grant, e)
	}
	var audit string
	if e = repo.db.QueryRow(`SELECT receipt_json::text FROM hhc_web.member_recording_broadcast_audit WHERE recording_id=$1 AND operation='preview'`, b.View.RecordingID).Scan(&audit); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(audit, "credential-must-never-persist") || !strings.Contains(audit, staff) || !strings.Contains(audit, grant.PlaybackScopeID) {
		t.Fatalf("preview audit invalid %s", audit)
	}

	now = now.Add(4 * time.Minute)
	renewed, e := s.BroadcastPreview(staffCtx, b.View.RecordingID, staff, capture, 1, grant.PlaybackScopeID)
	if e != nil || renewed.MediaURL != grant.MediaURL || !renewed.ExpiresAt.After(grant.ExpiresAt) {
		t.Fatalf("renewal changed URL or failed: %+v %v", renewed, e)
	}
	fresh, e := NewRecordingRepository(repo.db).GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || !fresh.Previews[grant.PlaybackScopeID].ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("renewal not persisted: %v", e)
	}
	var audits int
	if e = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_broadcast_audit WHERE recording_id=$1 AND operation='preview'`, b.View.RecordingID).Scan(&audits); e != nil || audits != 2 {
		t.Fatalf("renewal audit missing: %d %v", audits, e)
	}
	// Two tabs can renew the same scope concurrently through the real row lock.
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			v, err := s.BroadcastPreview(staffCtx, b.View.RecordingID, staff, capture, 1, grant.PlaybackScopeID)
			if err == nil && v.MediaURL != grant.MediaURL {
				err = fmt.Errorf("concurrent renewal changed media URL")
			}
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	badPurpose = true
	if _, e = s.BroadcastPreview(staffCtx, b.View.RecordingID, staff, capture, 1, ""); !errors.Is(e, recordings.ErrConflict) {
		t.Fatalf("member credential accepted as staff preview %v", e)
	}
	badPurpose = false
	blockRenew.Store(true)
	renewedDone := make(chan error, 1)
	go func() {
		_, err := s.BroadcastPreview(staffCtx, b.View.RecordingID, staff, capture, 1, grant.PlaybackScopeID)
		renewedDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("renewal did not enter Asset")
	}
	revokeDone := make(chan error, 1)
	go func() {
		_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, staff, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			b.Revoked = true
			return nil
		})
		revokeDone <- err
	}()
	select {
	case err := <-revokeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("revoke blocked behind preview I/O")
	}
	released.Store(true)
	close(release)
	if err := <-renewedDone; !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("renewal crossed revoked authority: %v", err)
	}
	if _, err := s.BroadcastPreview(staffCtx, b.View.RecordingID, staff, capture, 1, grant.PlaybackScopeID); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("renewal revived revoked authority: %v", err)
	}

}

func TestBroadcastPolicyRetryAfterProducerAcceptedButProjectionFailed(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	_, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}, now)
	if e != nil {
		t.Fatal(e)
	}
	capture := strings.Repeat("a", 32)
	_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = capture
		c.Status.State = "uploading"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	start, end := 8, 20
	_, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: p.ActorID}
		b.View.Phase = "end_pending"
		b.View.Boundaries = recordings.BroadcastBoundaries{Revision: 3, StartSequence: &start, EndSequenceExclusive: &end}
		b.MemberState = "live"
		b.RangePending = true
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var authority assetclient.BroadcastRangeInput
	failRead := true
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var next assetclient.BroadcastRangeInput
			json.NewDecoder(r.Body).Decode(&next)
			if next.RangeRevision < authority.RangeRevision {
				w.WriteHeader(409)
				return
			}
			authority = next
			json.NewEncoder(w).Encode(next)
			return
		}
		if failRead {
			failRead = false
			w.WriteHeader(503)
			return
		}
		duration := 360.36
		origin := 240.24
		json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: authority.RangeRevision, State: "ready", StartSequence: &start, EndSequenceExclusive: &end, DurationSeconds: &duration, MediaStartSeconds: &origin, Revision: 1})
	}))
	defer producer.Close()
	service := func() *recordings.Service {
		return recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	}
	if e = service().ReconcileBroadcast(ctx, b.View.RecordingID); !errors.Is(e, assetclient.ErrUnavailable) {
		t.Fatalf("projection failure not surfaced %v", e)
	}
	saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || !saved.RangePending || saved.View.Boundaries.Revision != authority.RangeRevision {
		t.Fatalf("producer advanced beyond durable intent CMS=%d Asset=%d pending=%v err=%v", saved.View.Boundaries.Revision, authority.RangeRevision, saved.RangePending, e)
	}
	if e = service().ReconcileBroadcast(ctx, b.View.RecordingID); e != nil {
		t.Fatalf("fresh service retry stayed stale %v", e)
	}
	saved, e = repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || saved.RangePending || saved.MemberState != "live" || saved.RangeApplied != authority.RangeRevision {
		t.Fatalf("retry failed convergence %+v %v", saved, e)
	}
}
func TestBroadcastExplicitAutoCoverHasCaptureSourceScope(t *testing.T) {
	repo, _, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	b, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Auto", Cover: &recordings.LiveCoverSelection{Mode: "auto"}}, p.ActorID, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, b.View.RecordingID)
	if b.CoverSnapshot.Scope != "" {
		t.Fatalf("auto cover incorrectly owns source %q", b.CoverSnapshot.Scope)
	}
}

func TestAutomaticBindingPersistsStartBeforeMediaAndStop(t *testing.T) {
	for _, mode := range []string{"private-preview", "stop-before-media", "live-to-archive"} {
		t.Run(mode, func(t *testing.T) {
			automatic := mode != "private-preview"
			repo, b, ctx, now := broadcastFixture(t)
			principal, _ := auditclient.ProvenanceFromContext(ctx)
			b, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, principal.ActorID, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
				b.View.Policy.AutoStart = automatic
				b.View.Announced = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			capture := strings.Repeat("d", 32)
			var policy assetclient.BroadcastRangeInput
			mediaReady := false
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/covers"):
					json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "cover", UploadID: "cover", Kind: "auto", State: "ready"}}})
				case strings.HasSuffix(r.URL.Path, "/ready"):
					expiry := now.Add(720 * time.Hour)
					json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: capture, SessionID: capture, RecordingID: b.View.RecordingID, State: "ready", ReadyAt: &now, MediaExpiresAt: &expiry})
				case r.Method == "POST":
					json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: capture, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: "bind", Operation: "create", CaptureID: capture, AcceptedAt: now}})
				case r.Method == "PUT":
					if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
						t.Error(err)
					}
					json.NewEncoder(w).Encode(policy)
				default:
					state := "waiting"
					if mediaReady {
						state = "ready"
					}
					duration := 30.03
					origin := 0.0
					json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: policy.RangeRevision, State: state, StartSequence: policy.StartSequence, EndSequenceExclusive: policy.EndSequenceExclusive, DurationSeconds: &duration, MediaStartSeconds: &origin})
				}
			}))
			defer producer.Close()
			svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", "")).WithBroadcastWriters(true)
			result, err := svc.BindBroadcast(ctx, b.View.RecordingID, principal.ActorID, recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: b.View.Revision}}, true)
			if err != nil {
				t.Fatal(err)
			}
			if automatic && (result.Broadcast.Boundaries.StartSequence == nil || *result.Broadcast.Boundaries.StartSequence != 0 || result.Broadcast.Phase != "start_pending") {
				t.Fatalf("automatic intent not persisted before first media: %+v", result.Broadcast.Boundaries)
			}
			if !automatic && (result.Broadcast.Boundaries.StartSequence != nil || result.Broadcast.Phase != "preview") {
				t.Fatal("manual preview implicitly started")
			}
			saved, err := repo.GetBroadcast(ctx, b.View.RecordingID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.StartedAt != nil || saved.MemberState != "blocked" {
				t.Fatal("unverified media exposed")
			}
			if mode == "live-to-archive" {
				if err = svc.ReconcileBroadcasts(ctx, 100); err != nil {
					t.Fatal(err)
				}
				pending, err := repo.GetBroadcast(ctx, b.View.RecordingID)
				if err != nil {
					t.Fatal(err)
				}
				if pending.View.Phase != "start_pending" || pending.StartedAt != nil || pending.MemberState != "blocked" {
					t.Fatal("waiting media became public")
				}
				mediaReady = true
				_, err = repo.UpdateCapture(ctx, saved.CaptureIntentID, func(c *recordings.Capture) error {
					c.Status.Progress.LastSequence = 0
					c.Status.Progress.LastAdvancedAt = &now
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if err = svc.ReconcileBroadcasts(ctx, 100); err != nil {
						t.Fatal(err)
					}
				}
				live, err := repo.GetBroadcast(ctx, b.View.RecordingID)
				if err != nil {
					t.Fatal(err)
				}
				if live.View.Phase != "live" || live.StartedAt == nil || live.MemberState != "live" {
					t.Fatalf("verified media failed automatic start: %s %s", live.View.Phase, live.MemberState)
				}
			}
			_, err = repo.UpdateCapture(ctx, saved.CaptureIntentID, func(c *recordings.Capture) error {
				c.NormalEnd = true
				c.Status.StopAcceptedAt = &now
				c.Status.Progress.Ended = true
				c.Status.Progress.LastSequence = 0
				end := 1
				c.SealedEndSequence = &end
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = svc.ReconcileBroadcasts(ctx, 100); err != nil {
				t.Fatal(err)
			}
			saved, err = repo.GetBroadcast(ctx, b.View.RecordingID)
			if err != nil {
				t.Fatal(err)
			}
			if automatic && (saved.View.Phase == "cancelled" || saved.View.Boundaries.EndSequenceExclusive == nil || *saved.View.Boundaries.EndSequenceExclusive != 1) {
				t.Fatal("Stop lost automatic start/end intent")
			}
			if automatic {
				// A stopped short output must finalize without inventing a public StartedAt.
				mediaReady = true
				_, err = repo.UpdateCapture(ctx, saved.CaptureIntentID, func(c *recordings.Capture) error {
					c.Status.State = "ready"
					c.Status.PackageID = &capture
					c.LiveCover = nil
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.RecordPackageReady(ctx, b.View.RecordingID, capture, now); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 3; i++ {
					if err = svc.ReconcileBroadcasts(ctx, 100); err != nil {
						t.Fatal(err)
					}
				}
				account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
				defer account.Close()
				svc.WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
				if err = svc.ReconcileAutoPublication(context.Background(), 100); err != nil {
					t.Fatal(err)
				}
				if err = svc.ReconcileBroadcasts(ctx, 100); err != nil {
					t.Fatal(err)
				}
				final, err := repo.GetBroadcast(ctx, b.View.RecordingID)
				if err != nil {
					t.Fatal(err)
				}
				if final.View.Phase != "archived" || final.View.Archive.State != "published" || (mode == "stop-before-media" && final.StartedAt != nil || mode == "live-to-archive" && final.StartedAt == nil) {
					t.Fatalf("short automatic output did not naturally archive or invented public time: phase=%s archive=%s", final.View.Phase, final.View.Archive.State)
				}
			}
			if !automatic && saved.View.Phase != "cancelled" {
				t.Fatal("never-public preview was exposed")
			}
		})
	}
}

func TestBroadcastTerminalAuthorityCommitsBeforeConflictingAssetWrite(t *testing.T) {
	for _, failFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("initialConflict=%v", failFirst), func(t *testing.T) {
			repo, b, ctx, now := broadcastFixture(t)
			actor, _ := auditclient.ProvenanceFromContext(ctx)
			_, c, err := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-terminal", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}, now)
			if err != nil {
				t.Fatal(err)
			}
			capture := strings.Repeat("e", 32)
			_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
				c.Status.CaptureID = capture
				c.Terminate("failed", "media_invalid", now)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			start := 8
			_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
				b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor.ActorID}
				b.View.Phase, b.MemberState, b.RangePending = "live", "live", true
				b.StartedAt = &now
				b.CoverSnapshot.Settings.Selection = recordings.LiveCoverSelection{Mode: "custom", UploadID: platform.NewID()}
				b.CoverSnapshot.Settings.CoverState = "pending"
				b.View.Boundaries = recordings.BroadcastBoundaries{Revision: 3, StartSequence: &start}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			conflict := failFirst
			var authority assetclient.BroadcastRangeInput
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					json.NewDecoder(r.Body).Decode(&authority)
					saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
					if e != nil || !saved.Revoked || saved.MemberState != "blocked" || saved.View.Phase != "failed" || saved.View.Boundaries.Revision != 4 {
						t.Errorf("terminal authority was not committed before provider call: %+v %v", saved, e)
					}
					if conflict || !authority.Revoked || authority.MemberState != "blocked" {
						w.WriteHeader(http.StatusConflict)
						return
					}
					json.NewEncoder(w).Encode(authority)
					return
				}
				json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: authority.RangeRevision, State: "failed", StartSequence: &start})
			}))
			defer producer.Close()
			service := func() *recordings.Service {
				return recordings.NewService(NewRecordingRepository(repo.db).WithAudit(NewAuditOutbox(repo.db)), "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			}
			if failFirst {
				if err = service().ReconcileBroadcasts(context.Background(), 100); !errors.Is(err, assetclient.ErrConflict) {
					t.Fatalf("provider conflict was hidden: %v", err)
				}
				saved, err := repo.GetBroadcast(ctx, b.View.RecordingID)
				if err != nil || !saved.Revoked || saved.MemberState != "blocked" || !saved.RangePending {
					t.Fatalf("provider failure rolled back terminal authority: %+v %v", saved, err)
				}
				conflict = false
			}
			if err = service().ReconcileBroadcasts(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetBroadcast(ctx, b.View.RecordingID)
			if err != nil || saved.RangePending || saved.RangeApplied != 4 || saved.EndedAt == nil {
				t.Fatalf("fresh service did not converge: %+v %v", saved, err)
			}
			if _, err = service().ResolveRecordingWatch(ctx, b.View.RecordingID, platform.NewID()); !errors.Is(err, recordings.ErrNotFound) {
				t.Fatalf("terminal recording remained watchable: %v", err)
			}
		})
	}
}

func TestBroadcastInactiveCaptureObservesProducerTermination(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	_, c, err := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-stale-ready", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}, now)
	if err != nil {
		t.Fatal(err)
	}
	capture := strings.Repeat("f", 32)
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID, c.Status.State, c.NormalEnd = capture, "ready", true
		c.Status.PackageID, c.Status.StopAcceptedAt = &capture, &now
		c.Status.LiveEnabled, c.Status.AutoPublish = false, "pending"
		for key, operation := range c.Operations {
			operation.Pending = false
			c.Operations[key] = operation
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := repo.ListPendingCaptures(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range pending {
		if candidate.IntentID == c.IntentID {
			t.Fatal("fixture must be excluded from the independent capture refresh queue")
		}
	}
	start := 8
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor.ActorID}
		b.View.Phase, b.MemberState, b.RangePending = "live", "live", true
		b.View.Boundaries = recordings.BroadcastBoundaries{Revision: 3, StartSequence: &start}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var authority assetclient.BroadcastRangeInput
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HHC-Actor-ID") != actor.ActorID || r.Header.Get("X-HHC-Request-ID") != c.RequestID {
			t.Error("producer lookup lost durable owner provenance")
		}
		switch {
		case r.Method == http.MethodPut:
			json.NewDecoder(r.Body).Decode(&authority)
			if !authority.Revoked {
				w.WriteHeader(http.StatusConflict)
				return
			}
			saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
			current, readErr := repo.GetCapture(ctx, c.IntentID, b.View.RecordingID)
			if e != nil || readErr != nil || !saved.Revoked || saved.MemberState != "blocked" || current.Status.State != "failed" {
				t.Error("trusted terminal and revoked authority were not committed before range application")
			}
			json.NewEncoder(w).Encode(authority)
		case strings.HasSuffix(r.URL.Path, "/broadcast-projection"):
			json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: authority.RangeRevision, State: "failed", StartSequence: &start})
		default:
			if r.URL.Query().Get("limit") != "1" {
				t.Error("unbounded producer object lookup")
			}
			reason := "package_failed"
			json.NewEncoder(w).Encode(assetclient.RecordingCaptureStatus{CaptureID: capture, RecordingID: b.View.RecordingID, State: "failed", TerminalAt: &now, TerminalReason: &reason})
		}
	}))
	defer producer.Close()
	svc := recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	if err = svc.ReconcileBroadcasts(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetBroadcast(ctx, b.View.RecordingID)
	current, readErr := repo.GetCapture(ctx, c.IntentID, b.View.RecordingID)
	if err != nil || readErr != nil || saved.RangePending || !saved.Revoked || saved.MemberState != "blocked" || saved.RangeApplied != 4 || current.Status.State != "failed" || current.Status.LiveEnabled || current.Status.AutoPublish != "cancelled" || !current.Status.ExpiresAt.Equal(c.Status.ExpiresAt) {
		t.Fatalf("inactive stale capture did not converge without reopening or extending retention: broadcast=%+v errors=%v/%v", saved, err, readErr)
	}
}
