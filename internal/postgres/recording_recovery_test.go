package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestD2SnapshotPersistsWithoutDelayingRevocation(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	principal, _ := auditclient.ProvenanceFromContext(ctx)
	_, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, principal.ActorID, recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 1}}, now)
	if e != nil {
		t.Fatal(e)
	}
	capture := strings.Repeat("a", 32)
	_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.RecoveryVersion = recordings.BroadcastRecoveryVersion
		c.Status.CaptureID = capture
		c.Status.State = "uploading"
		c.Status.ExpiresAt = now.Add(time.Hour)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET source_managed=true WHERE recording_id=$1`, b.View.RecordingID); e != nil {
		t.Fatal(e)
	}
	start := 0
	reason := "signal_grace"
	_, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.StartedAt = &now
		b.View.Phase = "live"
		b.View.Policy.AutoStart = true
		b.View.Policy.AutoEnd = true
		b.View.Boundaries.StartSequence = &start
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: principal.ActorID}
		b.View.Health = recordings.BroadcastHealth{State: "interrupted", Reason: &reason}
		version, common := recordings.BroadcastRecoveryVersion, 20
		b.Recovery = recordings.BroadcastRecovery{Version: &version, State: "interrupted", CommonConfirmedEndSequenceExclusive: &common}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		common := 21
		deadline := now.Add(5 * time.Minute)
		json.NewEncoder(w).Encode(assetclient.BroadcastSignal{RecoveryVersion: recordings.BroadcastRecoveryVersion, State: "interrupted", Deadline: &deadline, LastCommonConfirmedAt: &now, CommonConfirmedEndSequenceExclusive: &common})
	}))
	defer producer.Close()
	defer unblock()
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	control := make(chan error, 1)
	go func() {
		_, e := svc.GetBroadcastControlD2(ctx, b.View.RecordingID, principal.ActorID, capture, 1)
		control <- e
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("signal never entered")
	}
	revoke := make(chan error, 1)
	go func() {
		_, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			b.RevokePublic()
			return nil
		})
		revoke <- e
	}()
	select {
	case e := <-revoke:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("signal observation blocked revocation")
	}
	unblock()
	if e = <-control; e != nil {
		t.Fatal(e)
	}
	got, e := svc.GetBroadcastControlD2(ctx, b.View.RecordingID, principal.ActorID, capture, 1)
	if e != nil {
		t.Fatal(e)
	}
	if got.PublicEnd == nil || got.PublicEnd.Reason != "revoked" || got.Recovery.Version == nil || got.Recovery.CommonConfirmedEndSequenceExclusive == nil || *got.Recovery.CommonConfirmedEndSequenceExclusive != 20 {
		t.Fatalf("persisted authority %+v", got)
	}
	fresh := NewRecordingRepository(repo.db)
	saved, e := fresh.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || saved.PublicEnd == nil || !saved.Revoked {
		t.Fatalf("reconstruction lost end %v", e)
	}
}

func TestRecoveryPreferenceDoesNotUpgradeManualTargetOrLegacyReplay(t *testing.T) {
	for _, kind := range []string{"manual", "manual-auto", "scheduled", "legacy-json"} {
		t.Run(kind, func(t *testing.T) {
			repo, b, ctx, now, _, actor, key, _ := preparedSource(t)
			if kind == "manual-auto" || kind == "scheduled" {
				_, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
					b.View.Policy.AutoStart = true
					b.View.Policy.AutoEnd = true
					b.View.Announced = true
					if kind == "scheduled" {
						at := now.Add(time.Hour)
						b.View.ScheduledAt = &at
					}
					return nil
				})
				if e != nil {
					t.Fatal(e)
				}
			}
			hash, _, _ := recordings.HashBroadcastSourceKey(key)
			output := platform.NewID()
			requested := recordings.BroadcastRecoveryVersion
			if kind == "legacy-json" {
				requested = ""
			}
			option := recordings.BroadcastOutputOptions{RecoveryVersion: requested}
			first, e := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, option)
			if e != nil {
				t.Fatal(e)
			}
			if first.Capture.RecoveryVersion != "" || first.Capture.RequestedRecoveryVersion != requested {
				t.Fatal("manual/legacy authority upgraded")
			}
			if kind == "legacy-json" {
				if _, e = repo.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_capture SET payload_json=payload_json-'RequestedRecoveryVersion'-'RecoveryVersion' WHERE id=$1`, first.Capture.IntentID); e != nil {
					t.Fatal(e)
				}
			}
			again, e := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, option)
			if e != nil || again.Capture.IntentID != first.Capture.IntentID || again.Capture.RecoveryVersion != "" {
				t.Fatalf("replay %v", e)
			}
			changed := ""
			if requested == "" {
				changed = recordings.BroadcastRecoveryVersion
			}
			if _, e = repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, recordings.BroadcastOutputOptions{RecoveryVersion: changed}); !errors.Is(e, recordings.ErrConflict) {
				t.Fatalf("changed requested capability accepted: %v", e)
			}
		})
	}
}

func TestRecoveryOpenCreationRace(t *testing.T) {
	for _, change := range []string{"stop", "revoke", "schedule"} {
		t.Run(change, func(t *testing.T) {
			repo, b, ctx, now, svc, _, key, _ := preparedSource(t)
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			svc.WithStudioWriters(true)
			accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
			defer accounts.Close()
			svc.WithPublicationAuthorizer(accountclient.New(accounts.URL, "hhc-web-api"))
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			output := platform.NewID()
			capture := strings.Repeat("c", 32)
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: capture, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: output + ".bind", Operation: "create", CaptureID: capture, AcceptedAt: now}})
			}))
			defer producer.Close()
			defer unblock()
			svc.WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			input := recordings.BroadcastOutputOpenInput{ProtocolVersion: recordings.BroadcastOutputProtocolVersion, OutputSessionID: output, StreamKey: key, RecoveryVersion: recordings.BroadcastRecoveryVersion}
			done := make(chan error, 1)
			go func() { _, e := svc.OpenBroadcastOutput(ctx, "race", input); done <- e }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("create not entered")
			}
			_, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
				switch change {
				case "stop":
					c.Status.StopAcceptedAt = &now
				case "revoke":
					b.RevokePublic()
				case "schedule":
					future := now.Add(time.Hour)
					b.View.ScheduledAt = &future
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			unblock()
			e = <-done
			if change == "schedule" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("ended authority issued a binding")
			}
			saved, e := repo.GetBroadcast(ctx, b.View.RecordingID)
			if e != nil {
				t.Fatal(e)
			}
			if change != "schedule" && saved.View.Binding != nil {
				t.Fatal("stale creation revived binding")
			}
			if change == "schedule" {
				c, e := repo.GetCapture(ctx, capture, b.View.RecordingID)
				if e != nil || c.RecoveryVersion != "" || c.RequestedRecoveryVersion != recordings.BroadcastRecoveryVersion {
					t.Fatalf("manual authority upgraded after reservation: %v", e)
				}
			}
		})
	}
}

func TestRecoveryOpenTransientFailureKeepsSessionPreference(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			repo, b, ctx, _, svc, _, key, _ := preparedSource(t)
			svc.WithStudioWriters(true)
			accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
			defer accounts.Close()
			svc.WithPublicationAuthorizer(accountclient.New(accounts.URL, "hhc-web-api"))
			calls := 0
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) }))
			defer producer.Close()
			svc.WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			in := recordings.BroadcastOutputOpenInput{ProtocolVersion: recordings.BroadcastOutputProtocolVersion, OutputSessionID: platform.NewID(), StreamKey: key, RecoveryVersion: recordings.BroadcastRecoveryVersion}
			for i := 0; i < 2; i++ {
				if _, e := svc.OpenBroadcastOutput(ctx, "transient", in); e == nil {
					t.Fatal("transient producer error accepted")
				}
			}
			var count int
			var requested string
			if e := repo.db.QueryRow(`SELECT count(*), min(payload_json->>'RequestedRecoveryVersion') FROM hhc_web.member_recording_capture WHERE recording_id=$1`, b.View.RecordingID).Scan(&count, &requested); e != nil {
				t.Fatal(e)
			}
			if count != 1 || requested != recordings.BroadcastRecoveryVersion || calls != 2 {
				t.Fatalf("retry changed reservation: captures=%d calls=%d", count, calls)
			}
			in.RecoveryVersion = ""
			if _, e := svc.OpenBroadcastOutput(ctx, "downgrade", in); !errors.Is(e, recordings.ErrConflict) {
				t.Fatalf("downgrade accepted: %v", e)
			}
			if calls != 2 {
				t.Fatal("downgrade reached producer")
			}
		})
	}
}
