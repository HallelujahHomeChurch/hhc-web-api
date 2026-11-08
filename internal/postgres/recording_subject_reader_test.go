package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestWatchUsesOnlySubjectConnection(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Announced = true
		b.View.Phase = "scheduled"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	repo.db.SetMaxOpenConns(10)
	var held []*sql.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for i := 0; i < 9; i++ {
		c, e := repo.db.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, c)
	}
	request, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now })
	view, err := svc.ResolveRecordingWatch(request, b.View.RecordingID, platform.NewID())
	if err != nil || view.View != "waiting" {
		t.Fatalf("watch needed second checkout: view=%s err=%v stats=%+v", view.View, err, repo.db.Stats())
	}
}

func subjectLiveFixture(t *testing.T) (*RecordingRepository, recordings.Broadcast, context.Context, time.Time) {
	t.Helper()
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	b, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID(), ExpectedRevision: b.View.Revision}}, now)
	if e != nil {
		t.Fatal(e)
	}
	capture := strings.ReplaceAll(platform.NewID(), "-", "")
	_, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = capture
		c.Status.State = "uploading"
		c.Status.LiveEnabled = true
		c.Status.ExpiresAt = now.Add(time.Hour)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	b, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Announced = true
		b.View.Phase = "live"
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: b.Epoch, ActorID: actor.ActorID}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return repo, b, ctx, now
}

func TestSubjectReaderFiftyLiveWatchBurst(t *testing.T) {
	repo, b, ctx, now := subjectLiveFixture(t)
	_, other, otherCtx, otherNow := broadcastFixture(t)
	_, err := repo.UpdateBroadcast(otherCtx, other.View.RecordingID, "", otherNow, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Announced = true
		b.View.Phase = "scheduled"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	repo.db.SetMaxOpenConns(10)
	repo.db.SetMaxIdleConns(10)
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now })
	for _, sameUser := range []bool{false, true} {
		for _, sameRecording := range []bool{false, true} {
			t.Run(fmt.Sprintf("same_user_%t_same_recording_%t", sameUser, sameRecording), func(t *testing.T) {
				request, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				start := make(chan struct{})
				results := make(chan error, 50)
				user := platform.NewID()
				at := time.Now()
				for i := 0; i < 50; i++ {
					u := user
					if !sameUser {
						u = platform.NewID()
					}
					id := b.View.RecordingID
					if !sameRecording && i%2 == 0 {
						id = other.View.RecordingID
					}
					go func() {
						<-start
						v, e := svc.ResolveRecordingWatch(request, id, u)
						if e == nil && ((id == b.View.RecordingID && (v.View != "live" || v.Live == nil)) || (id != b.View.RecordingID && v.View != "waiting")) {
							e = fmt.Errorf("view=%s live=%v", v.View, v.Live != nil)
						}
						results <- e
					}()
				}
				close(start)
				failed := 0
				for i := 0; i < 50; i++ {
					if e := <-results; e != nil {
						failed++
						t.Log(e)
					}
				}
				t.Logf("success=%d/50 duration=%s pool=%+v", 50-failed, time.Since(at), repo.db.Stats())
				if failed != 0 {
					t.Fatalf("%d requests failed; fast rejection is not successful playback", failed)
				}
			})
		}
	}
	// A committed terminal authority update must be seen on subsequent reads.
	_, e := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
		b.Revoked = true
		c.Status.State = "failed"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	v, e := svc.ResolveRecordingWatch(ctx, b.View.RecordingID, platform.NewID())
	if e != nil || v.View == "live" || v.Live != nil {
		t.Fatalf("terminal authority ignored: %+v %v", v, e)
	}
}

func TestSubjectReaderCancellationReleasesFence(t *testing.T) {
	repo, b, ctx, _ := broadcastFixture(t)
	user := platform.NewID()
	hold, e := repo.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback()
	if e = lockWatermarkSubject(ctx, hold, user); e != nil {
		t.Fatal(e)
	}
	cancelled, done := context.WithTimeout(ctx, 50*time.Millisecond)
	defer done()
	called := false
	e = repo.WithRecordingSubject(cancelled, user, func(recordings.SubjectReader) error { called = true; return nil })
	if e == nil || called {
		t.Fatalf("canceled waiter entered callback: %v", e)
	}
	if e = hold.Rollback(); e != nil {
		t.Fatal(e)
	}
	next, finish := context.WithTimeout(ctx, time.Second)
	defer finish()
	if e = repo.WithRecordingSubject(next, user, func(r recordings.SubjectReader) error { _, e := r.Get(next, b.View.RecordingID); return e }); e != nil {
		t.Fatal(e)
	}
}

func TestSubjectReaderRetainsDSRExclusion(t *testing.T) {
	repo, b, ctx, _ := broadcastFixture(t)
	user := platform.NewID()
	request, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- repo.WithRecordingSubject(request, user, func(r recordings.SubjectReader) error {
			if _, e := r.Get(request, b.View.RecordingID); e != nil {
				return e
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-request.Done():
				return request.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-request.Done():
		t.Fatal(request.Err())
	}
	tx, e := repo.db.BeginTx(request, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	short, stop := context.WithTimeout(request, 50*time.Millisecond)
	e = lockWatermarkSubject(short, tx, user)
	stop()
	tx.Rollback()
	if e == nil {
		t.Fatal("DSR crossed an active reader")
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	tx, e = repo.db.BeginTx(request, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = lockWatermarkSubject(request, tx, user); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(request, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,true)`, watermarkSubjectHash(user)); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	defer repo.db.Exec(`DELETE FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1`, watermarkSubjectHash(user))
	called := false
	e = repo.WithRecordingSubject(request, user, func(recordings.SubjectReader) error { called = true; return nil })
	if !errors.Is(e, bulletinwatermark.ErrRestricted) || called {
		t.Fatalf("restriction bypass: %v called=%v", e, called)
	}
}

func TestSubjectReadQueueLeavesReadyAndControlCapacity(t *testing.T) {
	repo, b, ctx, now := subjectLiveFixture(t)
	repo.db.SetMaxOpenConns(10)
	repo.db.SetMaxIdleConns(10)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now })
	var holds []func()
	for i := 0; i < 8; i++ {
		_, finish, e := repo.beginRecordingTransaction(ctx)
		if e != nil {
			t.Fatal(e)
		}
		holds = append(holds, finish)
	}
	defer func() {
		for _, finish := range holds {
			finish()
		}
	}()
	request, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, e := svc.ResolveRecordingWatch(request, b.View.RecordingID, platform.NewID())
		done <- e
	}()
	<-started
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		repo.transactionMu.Lock()
		waiting := repo.subjectWaiters
		repo.transactionMu.Unlock()
		if waiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("read did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		call, stop := context.WithTimeout(ctx, 500*time.Millisecond)
		control, e := svc.GetBroadcastControlD2(call, b.View.RecordingID, actor.ActorID, b.View.Binding.CaptureID, b.Epoch)
		if e != nil || control.Recovery.State != "unsupported" {
			stop()
			t.Fatalf("pure control read blocked by waiters: %+v %v", control, e)
		}
		ready := httptest.NewRecorder()
		assertDatabaseProbeAvailable(t, repo.db)
		httpapi.New(nil, repo.db, nil).Routes().ServeHTTP(ready, httptest.NewRequest("GET", "/health/ready", nil).WithContext(call))
		stop()
		if ready.Code != 200 {
			t.Fatalf("ready=%d", ready.Code)
		}
	}
	if e := <-done; !errors.Is(e, recordings.ErrTransactionCapacity) {
		t.Fatalf("slow holder should time out queue, got %v", e)
	}
	if stats := repo.db.Stats(); stats.InUse != 8 || stats.WaitCount != 0 {
		t.Fatalf("queued reads consumed reserved slots: %+v", stats)
	}
	for _, finish := range holds {
		finish()
	}
	v, e := svc.ResolveRecordingWatch(ctx, b.View.RecordingID, platform.NewID())
	if e != nil || v.View != "live" {
		t.Fatalf("release did not recover reads: %s %v", v.View, e)
	}
}
