package postgres

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func outputReplayFixture(t *testing.T, profile string) (*RecordingRepository, *recordings.Service, recordings.BroadcastOutputReplayInput, recordings.Capture, context.Context, time.Time, *atomic.Int32) {
	t.Helper()
	repo, _, ctx, now, svc, actor, key := sourceFixture(t)
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := repo.db.QueryRow(`SELECT id::text FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	direct, err := repo.EnsureDirectBroadcast(ctx, source, actor, recordings.BroadcastMutationInput{OperationKey: "direct", ExpectedRevision: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, repo.db, direct.View.RecordingID)
	hash, _, _ := recordings.HashBroadcastSourceKey(key)
	output := platform.NewID()
	reserved, err := repo.PrepareBroadcastOutput(ctx, hash, output, actor, true, now, recordings.BroadcastOutputOptions{MediaProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := repo.UpdateCapture(ctx, reserved.Capture.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.Repeat("a", 32)
		c.Status.State = "uploading"
		op := c.Operations[c.Create.OperationKey]
		op.Pending = false
		op.Input = nil
		op.Receipt = &assetclient.RecordingCaptureReceipt{OperationKey: c.Create.OperationKey, Operation: "create", CaptureID: c.Status.CaptureID, AcceptedAt: now}
		c.Operations[c.Create.OperationKey] = op
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.UpdateBroadcast(ctx, direct.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: c.Status.CaptureID, Epoch: b.Epoch, ActorID: actor}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := b.Operations[output+".bind"].Receipt
	receipt.State = "completed"
	in := recordings.BroadcastOutputReplayInput{ProtocolVersion: recordings.BroadcastOutputReplayProtocolVersion, SourceID: source, OutputSessionID: output, RecordingID: b.View.RecordingID, CaptureID: capture.Status.CaptureID, Epoch: b.Epoch, Receipt: &receipt, StreamKey: key}
	if profile != "" {
		in.MediaProfile = &profile
	}
	calls := &atomic.Int32{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	t.Cleanup(provider.Close)
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
	t.Cleanup(account.Close)
	svc.WithPackages(assetclient.New(provider.URL, "hhc-web-api", "")).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	return repo, svc, in, capture, ctx, now, calls
}
func replayBusinessSnapshot(t *testing.T, repo *RecordingRepository) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"member_recording", "member_recording_capture", "member_recording_broadcast", "member_recording_broadcast_source", "member_recording_broadcast_output", "member_recording_broadcast_source_operation", "member_recording_studio_operation", "member_recording_broadcast_audit", "outbox_event"} {
		var raw string
		if err := repo.db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY to_jsonb(s)::text),'[]')::text FROM hhc_web.` + table + ` s`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out[table] = raw
	}
	return out
}
func assertReplayUnchanged(t *testing.T, repo *RecordingRepository, before map[string]string, calls *atomic.Int32) {
	t.Helper()
	for table, raw := range replayBusinessSnapshot(t, repo) {
		if raw != before[table] {
			t.Fatalf("replay wrote business table %s", table)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("replay called provider")
	}
}
func TestOutputReplayPreservesStalePreparedAndBusinessState(t *testing.T) {
	for _, kind := range []string{"active", "S10 active", "missing ledger", "missing ledger released", "changed global key", "changed global source and key released", "wrong source", "wrong recording", "wrong capture", "wrong epoch", "wrong profile", "wrong receipt", "wrong receipt released", "incomplete binding", "incomplete binding released", "missing capture", "media terminal input active", "deleted but input active", "revoked but input active", "generation changed but input active", "deleted released", "normal seal released", "stop only", "seal pending"} {
		t.Run(kind, func(t *testing.T) {
			profile := ""
			if kind == "S10 active" {
				profile = "obs-hls-10s-v1"
			}
			repo, svc, in, c, ctx, now, calls := outputReplayFixture(t, profile)
			originalSource := in.SourceID
			want := "unknown"
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := repo.db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "active", "S10 active":
				want = "active"
			case "missing ledger", "missing ledger released":
				if kind == "missing ledger released" {
					if _, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("aborted", "user_abort", now); return nil }); err != nil {
						t.Fatal(err)
					}
				}
				exec(`DELETE FROM hhc_web.member_recording_broadcast_output WHERE source_id=$1 AND output_id=$2`, in.SourceID, in.OutputSessionID)
			case "changed global key", "changed global source and key released":
				in.StreamKey = "hhc_stream_" + strings.Repeat("B", 42) + "A"
				hash, _, _ := recordings.HashBroadcastSourceKey(in.StreamKey)
				other := platform.NewID()
				if kind == "changed global source and key released" {
					if _, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("aborted", "user_abort", now); return nil }); err != nil {
						t.Fatal(err)
					}
					in.SourceID = other
				}
				exec(`INSERT INTO hhc_web.member_recording_broadcast_source(id,singleton,name,key_hash,suffix,direct_actor_id) VALUES($1,NULL,'Other',$2,'BBBA',$3)`, other, hash, c.ActorID)
				t.Cleanup(func() {
					_, _ = repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, other)
				})
			case "wrong source":
				in.SourceID = platform.NewID()
			case "wrong recording":
				in.RecordingID = platform.NewID()
			case "wrong capture":
				in.CaptureID = strings.Repeat("b", 32)
			case "wrong epoch":
				in.Epoch++
			case "wrong profile":
				p := "obs-hls-10s-v1"
				in.MediaProfile = &p
			case "wrong receipt", "wrong receipt released":
				if kind == "wrong receipt released" {
					if _, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("aborted", "user_abort", now); return nil }); err != nil {
						t.Fatal(err)
					}
				}
				in.Receipt.OperationKey = platform.NewID() + ".bind"
			case "incomplete binding", "incomplete binding released":
				if kind == "incomplete binding released" {
					if _, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("aborted", "user_abort", now); return nil }); err != nil {
						t.Fatal(err)
					}
				}
				_, err := repo.UpdateBroadcast(ctx, in.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
					b.View.Binding = nil
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "missing capture":
				exec(`DELETE FROM hhc_web.member_recording_capture WHERE id=$1`, c.IntentID)
			case "media terminal input active":
				exec(`UPDATE hhc_web.member_recording SET media_terminal=true WHERE id=$1`, in.RecordingID)
			case "deleted but input active":
				exec(`UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, in.RecordingID, now)
			case "revoked but input active":
				_, err := repo.UpdateBroadcast(ctx, in.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
					b.Revoked = true
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "generation changed but input active":
				exec(`UPDATE hhc_web.member_recording_broadcast_source SET key_generation=key_generation+1 WHERE id=$1`, in.SourceID)
			case "deleted released":
				r, err := repo.Get(ctx, in.RecordingID)
				if err != nil {
					t.Fatal(err)
				}
				if err = repo.Delete(ctx, in.RecordingID, r.Version, c.ActorID, now, true); err != nil {
					t.Fatal(err)
				}
				want = "terminal"
			case "normal seal released", "stop only", "seal pending":
				_, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
					c.Status.StopAcceptedAt = &now
					if kind != "stop only" {
						end := 3
						c.NormalEnd = true
						c.SealedEndSequence = &end
						c.Status.State = "validating"
						c.Operations["seal"] = recordings.CaptureOperation{Operation: "seal", Pending: kind == "seal pending", Receipt: &assetclient.RecordingCaptureReceipt{OperationKey: "seal", Operation: "seal", AcceptedAt: now, CaptureID: c.Status.CaptureID}}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				want = "active"
				if kind == "normal seal released" {
					want = "terminal"
				}
			}
			// sourceTransaction would clear this row and advance revision even on a miss.
			exec(`UPDATE hhc_web.member_recording_broadcast_source SET prepared=true,target_recording_id=NULL,revision=77 WHERE id=$1`, originalSource)
			before := replayBusinessSnapshot(t, repo)
			out, err := svc.ReplayBroadcastOutput(ctx, "replay-test", in)
			if want == "unknown" {
				if err == nil || out.Session != nil {
					t.Fatal("uncertain original output accepted")
				}
			} else if err != nil || out.State != want || out.SourceID != in.SourceID || out.OutputSessionID != in.OutputSessionID || out.RecordingID != in.RecordingID || out.CaptureID != in.CaptureID || out.Epoch != in.Epoch || out.Receipt != *in.Receipt {
				t.Fatalf("original replay result %s: %v", want, err)
			} else if (want == "active") != (out.Session != nil) {
				t.Fatal("session state contradicts durable original input")
			}
			assertReplayUnchanged(t, repo, before, calls)
		})
	}
}
func TestOutputReplayReleasedOriginalCannotAcquireConcurrentNewOutput(t *testing.T) {
	repo, _, in, c, ctx, now, calls := outputReplayFixture(t, "")
	if _, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("aborted", "user_abort", now); return nil }); err != nil {
		t.Fatal(err)
	}
	hash, _, _ := recordings.HashBroadcastSourceKey(in.StreamKey)
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		out, err := repo.ReadBroadcastOutputReplay(ctx, hash, in)
		if err == nil && (out.State != "terminal" || out.Capture.IntentID != c.IntentID) {
			err = errors.New("replay acquired a new capture")
		}
		errs <- err
	}()
	go func() {
		defer wg.Done()
		next, err := repo.PrepareBroadcastOutput(ctx, hash, platform.NewID(), c.ActorID, true, now)
		if err == nil {
			if next.Capture.IntentID == c.IntentID {
				err = errors.New("new output reused retired capture")
			}
			recordingCleanup(t, repo.db, next.Broadcast.View.RecordingID)
		}
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("replay called provider")
	}
}

func TestOutputReplayAuthorizationReleasesLocksAndObservesLateAbort(t *testing.T) {
	repo, svc, in, c, ctx, now, calls := outputReplayFixture(t, "")
	updates := make(chan error, 1)
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err := repo.UpdateCapture(readCtx, c.IntentID, func(c *recordings.Capture) error { c.Terminate("aborted", "user_abort", now); return nil })
		updates <- err
		if err != nil {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"allowed":true}`))
	}))
	defer account.Close()
	svc.WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	out, err := svc.ReplayBroadcastOutput(ctx, "replay-test", in)
	if err != nil || out.State != "terminal" || out.Session != nil {
		t.Fatalf("late abort was not fenced: %v", err)
	}
	if err = <-updates; err != nil {
		t.Fatalf("authorization held original DB locks: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("replay delivered queued abort")
	}
}
