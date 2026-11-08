package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func streamKeyFixture(t *testing.T) (*RecordingRepository, recordings.Broadcast, context.Context, time.Time, *recordings.Service, string, recordings.BroadcastStreamKeyInput) {
	t.Helper()
	repo, b, ctx, now := broadcastFixture(t)
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	key := "hhc_live_" + b.View.RecordingID + "_" + base64.RawURLEncoding.EncodeToString(raw)
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithBroadcastWriters(true)
	input := recordings.BroadcastStreamKeyInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "configure-key", ExpectedRevision: b.View.Revision}, StreamKey: key}
	return repo, b, ctx, now, svc, key, input
}
func TestBroadcastStreamKeyPersistenceAndReplay(t *testing.T) {
	repo, b, ctx, now, svc, key, in := streamKeyFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	meta, e := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in)
	if e != nil || !meta.Configured || !meta.CanRotate || meta.Revision != 2 {
		t.Fatalf("metadata %+v %v", meta, e)
	}
	again, e := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in)
	if e != nil || again.Revision != meta.Revision {
		t.Fatalf("retry %+v %v", again, e)
	}
	wrong := in
	wrong.StreamKey = key[:len(key)-43] + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, e = svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, wrong); !errors.Is(e, recordings.ErrBroadcastOperation) {
		t.Fatalf("changed retry %v", e)
	}
	other := platform.NewID()
	otherCtx := auditclient.WithProvenance(ctx, other, "other")
	if _, e = svc.SetBroadcastStreamKey(otherCtx, b.View.RecordingID, other, in); !errors.Is(e, recordings.ErrBroadcastOperation) {
		t.Fatalf("other actor replay %v", e)
	}
	var persisted, audit string
	if e = repo.db.QueryRow(`SELECT payload_json::text FROM hhc_web.member_recording_broadcast WHERE recording_id=$1`, b.View.RecordingID).Scan(&persisted); e != nil {
		t.Fatal(e)
	}
	if e = repo.db.QueryRow(`SELECT receipt_json::text FROM hhc_web.member_recording_broadcast_audit WHERE recording_id=$1 AND operation='stream-key'`, b.View.RecordingID).Scan(&audit); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(persisted, key) || strings.Contains(audit, key) || strings.Contains(persisted, key[len(key)-43:]) {
		t.Fatal("plaintext credential persisted")
	}
	hash, _, _ := recordings.HashBroadcastStreamKey(b.View.RecordingID, key)
	if !strings.Contains(persisted, hash) {
		t.Fatal("digest not persisted")
	}
	wire, _ := json.Marshal(meta)
	if strings.Contains(string(wire), key) || strings.Contains(string(wire), hash) {
		t.Fatal("metadata exposes credential")
	}
	bind := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 2}, ProtocolVersion: recordings.BroadcastProtocolVersion}
	if _, _, e = repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, bind, now); !errors.Is(e, recordings.ErrBroadcastStreamKey) {
		t.Fatalf("legacy bypass %v", e)
	}
	if _, _, e = repo.PrepareBroadcastStreamKeyBinding(ctx, b.View.RecordingID, p.ActorID, bind, strings.Repeat("0", 64), now); !errors.Is(e, recordings.ErrBroadcastStreamKey) {
		t.Fatalf("wrong key %v", e)
	}
	bound, c, e := repo.PrepareBroadcastStreamKeyBinding(ctx, b.View.RecordingID, p.ActorID, bind, hash, now)
	if e != nil || c.IntentID == "" || bound.Epoch != 1 {
		t.Fatalf("bind %v", e)
	}
	_, same, e := repo.PrepareBroadcastStreamKeyBinding(ctx, b.View.RecordingID, p.ActorID, bind, hash, now)
	if e != nil || same.IntentID != c.IntentID {
		t.Fatalf("bind replay %v", e)
	}
	if _, _, e = repo.PrepareBroadcastStreamKeyBinding(ctx, b.View.RecordingID, p.ActorID, bind, strings.Repeat("0", 64), now); !errors.Is(e, recordings.ErrBroadcastStreamKey) {
		t.Fatalf("wrong-key replay %v", e)
	}
	if _, _, e = repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, bind, now); !errors.Is(e, recordings.ErrBroadcastStreamKey) {
		t.Fatalf("legacy replay bypass %v", e)
	}
	in.OperationKey = "rotate"
	in.ExpectedRevision = bound.View.Revision
	if _, e = svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in); !errors.Is(e, recordings.ErrConflict) {
		t.Fatalf("rotated pending intent %v", e)
	}
}
func TestBroadcastStreamKeyConcurrentBindAndRotation(t *testing.T) {
	for _, rotation := range []bool{false, true} {
		t.Run(map[bool]string{false: "two-binds", true: "rotate-vs-bind"}[rotation], func(t *testing.T) {
			repo, b, ctx, now, svc, key, in := streamKeyFixture(t)
			p, _ := auditclient.ProvenanceFromContext(ctx)
			if _, e := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in); e != nil {
				t.Fatal(e)
			}
			hash, _, _ := recordings.HashBroadcastStreamKey(b.View.RecordingID, key)
			bind := recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-a", ExpectedRevision: 2}, ProtocolVersion: recordings.BroadcastProtocolVersion}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, _, e := repo.PrepareBroadcastStreamKeyBinding(ctx, b.View.RecordingID, p.ActorID, bind, hash, now)
				results <- e
			}()
			go func() {
				defer wg.Done()
				<-start
				if rotation {
					in.OperationKey = "rotate"
					in.ExpectedRevision = 2
					in.StreamKey = key[:len(key)-43] + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
					_, e := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in)
					results <- e
				} else {
					second := bind
					second.OperationKey = "bind-b"
					_, _, e := repo.PrepareBroadcastStreamKeyBinding(ctx, b.View.RecordingID, p.ActorID, second, hash, now)
					results <- e
				}
			}()
			close(start)
			wg.Wait()
			close(results)
			success := 0
			for e := range results {
				if e == nil {
					success++
				} else if !errors.Is(e, recordings.ErrPrecondition) && !errors.Is(e, recordings.ErrConflict) && !errors.Is(e, recordings.ErrBroadcastStreamKey) {
					t.Fatal(e)
				}
			}
			if success != 1 {
				t.Fatalf("%d winners", success)
			}
			current, e := repo.GetBroadcast(ctx, b.View.RecordingID)
			if e != nil {
				t.Fatal(e)
			}
			if current.CaptureIntentID != "" && current.StreamKeyHash != hash {
				t.Fatal("bound old credential after rotation")
			}
		})
	}
}

func TestBroadcastStreamKeyReplayedRotationCannotClaimOldCredentialCurrent(t *testing.T) {
	repo, b, ctx, _, svc, key, in := streamKeyFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	if _, err := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in); err != nil {
		t.Fatal(err)
	}
	replacement := in
	replacement.OperationKey = "rotate"
	replacement.ExpectedRevision = 2
	replacement.StreamKey = key[:len(key)-43] + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, err := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in); !errors.Is(err, recordings.ErrBroadcastOperation) {
		t.Fatalf("stale key replay succeeded: %v", err)
	}
	current, _ := repo.GetBroadcast(ctx, b.View.RecordingID)
	if current.View.Revision != 3 {
		t.Fatal("replay changed revision")
	}
}

func TestBroadcastStreamKeyProducerFailureRetainsIntentAndPolicyPermission(t *testing.T) {
	repo, b, ctx, now, svc, key, in := streamKeyFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	if _, e := svc.SetBroadcastStreamKey(ctx, b.View.RecordingID, p.ActorID, in); e != nil {
		t.Fatal(e)
	}
	bind := recordings.BroadcastStreamKeyBindInput{BroadcastBindInput: recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 2}, ProtocolVersion: recordings.BroadcastProtocolVersion}, StreamKey: key}
	if _, e := svc.BindBroadcastWithStreamKey(ctx, b.View.RecordingID, p.ActorID, bind, false); !errors.Is(e, recordings.ErrForbidden) {
		t.Fatalf("autopublish without publish: %v", e)
	}
	initial, _ := repo.GetBroadcast(ctx, b.View.RecordingID)
	if initial.CaptureIntentID != "" {
		t.Fatal("denied bind reserved intent")
	}
	if _, e := svc.BindBroadcastWithStreamKey(ctx, b.View.RecordingID, p.ActorID, bind, true); !errors.Is(e, assetclient.ErrUnavailable) {
		t.Fatalf("missing producer %v", e)
	}
	reserved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || reserved.CaptureIntentID == "" {
		t.Fatalf("missing durable intent %v", e)
	}
	if _, e = svc.BindBroadcastWithStreamKey(ctx, b.View.RecordingID, p.ActorID, bind, true); !errors.Is(e, assetclient.ErrUnavailable) {
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
	result, e := svc.BindBroadcastWithStreamKey(ctx, b.View.RecordingID, p.ActorID, bind, true)
	if e != nil || result.Broadcast.Binding == nil || result.Broadcast.Binding.CaptureID != captureID {
		t.Fatalf("recovery %+v %v", result, e)
	}
	current, _ := repo.GetBroadcast(ctx, b.View.RecordingID)
	if current.CaptureIntentID != reserved.CaptureIntentID {
		t.Fatal("recovery replaced capture")
	}
}
