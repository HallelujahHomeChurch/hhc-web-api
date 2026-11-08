package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

// The outer watch service and SQL subject fence are real. Only its subsequent
// repository Get is delayed, representing an unclassified slow read dependency.
type delayedWatchRepository struct {
	*RecordingRepository
	entered chan struct{}
	release chan struct{}
}

func (r *delayedWatchRepository) WithRecordingSubject(ctx context.Context, user string, read func(recordings.SubjectReader) error) error {
	return r.RecordingRepository.WithRecordingSubject(ctx, user, func(reader recordings.SubjectReader) error {
		return read(delayedSubjectReader{reader, r.entered, r.release})
	})
}

type delayedSubjectReader struct {
	recordings.SubjectReader
	entered chan struct{}
	release chan struct{}
}

func (r delayedSubjectReader) Get(ctx context.Context, id string) (recordings.Recording, error) {
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		return recordings.Recording{}, ctx.Err()
	}
	return r.SubjectReader.Get(ctx, id)
}

func TestSlowWatchDoesNotSerializeSameSubjectInteractions(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	repo.db.SetMaxOpenConns(10)
	repo.db.SetMaxIdleConns(10)
	// Measure lock progress, not new connection handshake latency under local load.
	var warm []*sql.Conn
	for range 10 {
		conn, e := repo.db.Conn(ctx)
		if e != nil {
			for _, c := range warm {
				c.Close()
			}
			t.Fatal(e)
		}
		warm = append(warm, conn)
	}
	for _, conn := range warm {
		conn.Close()
	}
	_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Announced = true
		b.View.Phase = "live"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	delayed := &delayedWatchRepository{repo, make(chan struct{}), make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(delayed.release) }) }
	defer unblock()
	watchSvc := recordings.NewService(delayed, "", nil, func() time.Time { return now })
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now })
	user := platform.NewID()
	watchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	watchDone := make(chan error, 1)
	go func() { _, err := watchSvc.ResolveRecordingWatch(watchCtx, b.View.RecordingID, user); watchDone <- err }()
	select {
	case <-delayed.entered:
	case <-watchCtx.Done():
		t.Fatal("watch did not enter delayed read")
	}
	// A different subject and readiness must still succeed: no global-pool claim.
	otherCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	_, err = repo.RecordingInteractions(otherCtx, b.View.RecordingID, platform.NewID(), now)
	stop()
	if err != nil {
		t.Fatalf("different subject blocked: %v", err)
	}
	ready := httptest.NewRecorder()
	assertDatabaseProbeAvailable(t, repo.db)
	httpapi.New(nil, repo.db, nil).Routes().ServeHTTP(ready, httptest.NewRequest("GET", "/health/ready", nil))
	if ready.Code != 200 {
		t.Fatalf("readiness unexpectedly unhealthy: %d", ready.Code)
	}
	t.Logf("different subject and readiness healthy; pool=%+v", repo.db.Stats())
	var wg sync.WaitGroup
	for _, operation := range []string{"heartbeat", "metrics", "interactions"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, done := context.WithTimeout(ctx, 2*time.Second)
			defer done()
			var e error
			switch operation {
			case "heartbeat":
				e = svc.RecordPlaybackHeartbeat(callCtx, b.View.RecordingID, user, recordings.PlaybackHeartbeat{PlaybackSessionID: platform.NewID(), Sequence: 1, State: "playing"})
			case "metrics":
				_, e = svc.GetPlaybackMetrics(callCtx, b.View.RecordingID, user, false)
			case "interactions":
				_, e = repo.RecordingInteractions(callCtx, b.View.RecordingID, user, now)
			}
			if e != nil {
				t.Errorf("same-subject %s serialized behind watch: %v", operation, e)
			}
		}()
	}
	wg.Wait()
	unblock()
	if err := <-watchDone; err != nil {
		t.Fatalf("watch failed after release: %v", err)
	}
	if _, err := repo.RecordingInteractions(ctx, b.View.RecordingID, user, now); err != nil {
		t.Fatalf("same subject failed after release: %v", err)
	}
}

func TestSharedWatchStillFencesAccountErasure(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	user := platform.NewID()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- repo.WithRecordingSubject(ctx, user, func(recordings.SubjectReader) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-entered
	cleanup := make(chan error, 1)
	go func() {
		_, _, e := New(repo.db).CleanupWatermarkAccount(ctx, user, "subject-test:"+platform.NewID())
		cleanup <- e
	}()
	select {
	case e := <-cleanup:
		t.Fatalf("erase overtook active watch: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := <-cleanup; e != nil {
		t.Fatal(e)
	}
	called := false
	e := repo.WithRecordingSubject(ctx, user, func(recordings.SubjectReader) error { called = true; return nil })
	if !errors.Is(e, bulletinwatermark.ErrRestricted) || called {
		t.Fatalf("post-erase callback=%v error=%v", called, e)
	}
	if _, e = repo.RecordingInteractions(ctx, b.View.RecordingID, user, now); !errors.Is(e, bulletinwatermark.ErrRestricted) {
		t.Fatalf("interaction after erase: %v", e)
	}
}

func TestInteractionUserSerializationSurvivesSharedLifecycleFence(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	id, user := b.View.RecordingID, platform.NewID()
	if _, e := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(jsonb_set(payload_json,'{View,announced}','true'),'{View,phase}','"live"') WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	sessions := []string{platform.NewID(), platform.NewID()}
	for _, session := range sessions {
		if e := repo.RecordPlaybackHeartbeat(ctx, id, user, recordings.PlaybackHeartbeat{PlaybackSessionID: session, Sequence: 1, State: "playing"}, 0, now); e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := repo.RecordPlaybackHeartbeat(ctx, id, user, recordings.PlaybackHeartbeat{PlaybackSessionID: sessions[i%2], Sequence: 2, State: "playing", DeltaPlayingMS: 15000}, 0, now.Add(15*time.Second))
			if e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	v, e := repo.GetPlaybackMetrics(ctx, id, user, false, now.Add(15*time.Second))
	if e != nil || v.WatchSeconds != 15 || v.TotalViews != 1 || v.LiveViews != 1 {
		t.Fatalf("duplicate overlap %+v %v", v, e)
	}
	input := recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "same mutation"}
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := repo.CreateRecordingComment(ctx, id, user, input, now)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- c.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("duplicate comment %s != %s", id, first)
		}
	}
	input.Text = "changed"
	if _, e := repo.CreateRecordingComment(ctx, id, user, input, now); !errors.Is(e, recordings.ErrConflict) {
		t.Fatalf("changed mutation: %v", e)
	}
}

func TestSubjectExclusiveRestrictionPrecedesNewReaders(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	user := platform.NewID()
	tx, e := repo.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = lockWatermarkSubject(ctx, tx, user); e != nil {
		t.Fatal(e)
	}
	// This is also the old-version subject lock: new shared requests must wait.
	for _, interaction := range []bool{false, true} {
		short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		called := false
		if interaction {
			_, e = repo.RecordingInteractions(short, b.View.RecordingID, user, now)
		} else {
			e = repo.WithRecordingSubject(short, user, func(recordings.SubjectReader) error { called = true; return nil })
		}
		stop()
		if e == nil || called {
			t.Fatalf("exclusive bypass interaction=%v called=%v err=%v", interaction, called, e)
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,false)`, watermarkSubjectHash(user)); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		result <- repo.WithRecordingSubject(ctx, user, func(recordings.SubjectReader) error { return errors.New("callback must not run") })
	}()
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, bulletinwatermark.ErrRestricted) {
		t.Fatalf("restriction not respected: %v", e)
	}
	if _, e = repo.RecordingInteractions(ctx, b.View.RecordingID, user, now); !errors.Is(e, bulletinwatermark.ErrRestricted) {
		t.Fatalf("interaction: %v", e)
	}
}

func TestInteractionSerializationRemainsUserWideAcrossRecordings(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	user := platform.NewID()
	principal, _ := auditclient.ProvenanceFromContext(ctx)
	other, e := repo.CreateBroadcast(ctx, recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "Second recording"}, principal.ActorID, now)
	if e != nil {
		t.Fatal(e)
	}
	recordingCleanup(t, repo.db, other.View.RecordingID)
	for _, id := range []string{b.View.RecordingID, other.View.RecordingID} {
		if _, e = repo.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); e != nil {
			t.Fatal(e)
		}
	}
	held, finishHeld, e := repo.interactionTransaction(ctx, b.View.RecordingID, user, now)
	if e != nil {
		t.Fatal(e)
	}
	defer finishHeld()
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = repo.RecordingInteractions(short, other.View.RecordingID, user, now)
	stop()
	if e == nil {
		t.Fatal("same-user other-recording interaction bypassed serialization")
	}
	if _, e = repo.RecordingInteractions(ctx, other.View.RecordingID, platform.NewID(), now); e != nil {
		t.Fatalf("different user blocked: %v", e)
	}
	if e = repo.WithRecordingSubject(ctx, user, func(recordings.SubjectReader) error { return nil }); e != nil {
		t.Fatalf("read-only subject blocked by interaction: %v", e)
	}
	if e = held.Rollback(); e != nil {
		t.Fatal(e)
	}
	finishHeld()
	if _, e = repo.RecordingInteractions(ctx, other.View.RecordingID, user, now); e != nil {
		t.Fatalf("cancelled waiter leaked lock: %v", e)
	}
}
