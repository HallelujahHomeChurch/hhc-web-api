package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSlowPreviewPreservesPoolAndReadiness(t *testing.T) {
	for _, mode := range []string{"source", "heartbeat", "mixed"} {
		t.Run(mode, func(t *testing.T) { testSlowPreviewPool(t, mode, false, false) })
	}
}
func TestSlowPreviewPreservesIndependentReplicaPool(t *testing.T) {
	testSlowPreviewPool(t, "source", true, false)
}
func TestSlowPreviewPreservesSeparateProcessPool(t *testing.T) {
	testSlowPreviewPool(t, "source", false, true)
}

func testSlowPreviewPool(t *testing.T, mode string, secondPool, subprocess bool) {
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
		b.StartedAt = &now
		b.View.Phase = "live"
		b.View.Announced = true
		b.MemberState = "live"
		start := 0
		b.View.Boundaries.StartSequence = &start
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: p.ActorID}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}

	repo.db.SetMaxOpenConns(10)
	warmRecordingPool(t, repo.db)
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
		var in assetclient.BroadcastPreviewGrantInput
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(assetclient.BroadcastPreviewGrant{RecordingID: in.RecordingID, CaptureID: capture, Epoch: 1, Purpose: "staff-preview", Scope: in.PlaybackScopeID, MediaPath: "/videos/" + in.RecordingID + "/captures/" + capture + "/previews/" + in.PlaybackScopeID + "/master.m3u8", ExchangeCredential: "synthetic", IssuedAt: now, ExpiresAt: in.ExpiresAt})
	}))
	defer func() { unblock(); producer.Close() }()
	svc := recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	done := make(chan error, 11)
	go func() {
		_, err := svc.BroadcastPreview(runCtx, b.View.RecordingID, p.ActorID, capture, 1, "")
		done <- err
	}()
	select {
	case <-entered:
	case <-runCtx.Done():
		t.Fatal("preview not entered")
	}
	if stats := repo.db.Stats(); stats.InUse != 0 {
		t.Fatalf("preview retained SQL connection: %+v", stats)
	}

	healthy := httptest.NewRecorder()
	assertDatabaseProbeAvailable(t, repo.db)
	httpapi.New(nil, repo.db, nil).Routes().ServeHTTP(healthy, httptest.NewRequest("GET", "/health/ready", nil))
	if healthy.Code != 200 {
		t.Fatalf("one held connection alone broke ready: %d", healthy.Code)
	}
	if _, err := repo.GetBroadcast(ctx, b.View.RecordingID); err != nil {
		t.Fatalf("plain read blocked before pool saturation: %v", err)
	}

	if subprocess {
		command := exec.CommandContext(runCtx, os.Args[0], "-test.run=^TestRecordingBudgetReplicaHelper$", "-test.v")
		command.Env = append(os.Environ(), "CMS_BUDGET_REPLICA_HELPER=1")
		output, err := command.CombinedOutput()
		t.Logf("independent replica process: %s", output)
		if err != nil {
			t.Fatalf("independent replica failed: %v", err)
		}
		unblock()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return
	}
	waitRepo := repo
	waiterCount := 8
	if secondPool {
		secondDB, err := sql.Open("pgx", os.Getenv("HHW_TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { unblock(); secondDB.Close() }()
		secondDB.SetMaxOpenConns(10)
		warmRecordingPool(t, secondDB)
		waitRepo = NewRecordingRepository(secondDB)
	}
	// Source writes and viewer heartbeats must complete while preview still waits on Asset.
	for i := 0; i < waiterCount; i++ {
		go func() {
			if mode == "heartbeat" || mode == "mixed" && i%2 == 0 {
				done <- svc.RecordPlaybackHeartbeat(runCtx, b.View.RecordingID, platform.NewID(), recordings.PlaybackHeartbeat{PlaybackSessionID: platform.NewID(), Sequence: 1, State: "playing"})
				return
			}
			_, err := waitRepo.GetBroadcastSource(runCtx)
			done <- err
		}()
	}
	for i := 0; i < waiterCount; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("source/viewer operation blocked by preview")
		}
	}
	if stats := waitRepo.db.Stats(); stats.InUse != 0 {
		t.Fatalf("preview retained pool after viewer writes: %+v", stats)
	}
	readCtx, stopRead := context.WithTimeout(ctx, 2*time.Second)
	_, readErr := waitRepo.GetBroadcast(readCtx, b.View.RecordingID)
	stopRead()
	t.Logf("plain control read after saturation: %v", readErr)
	if readErr != nil {
		t.Errorf("plain read blocked despite reserve: %v", readErr)
	}
	w := httptest.NewRecorder()
	started := time.Now()
	assertDatabaseProbeAvailable(t, waitRepo.db)
	httpapi.New(nil, waitRepo.db, nil).Routes().ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
	t.Logf("ready status=%d duration=%s pool=%+v", w.Code, time.Since(started), waitRepo.db.Stats())
	if w.Code != http.StatusOK {
		t.Errorf("slow Asset call exhausted reserved pool: readiness=%d", w.Code)
	}
	unblock()
	for i := 0; i < 1; i++ {
		if err := <-done; err != nil {
			t.Logf("completed waiter: %v", err)
		}
	}
	w = httptest.NewRecorder()
	assertDatabaseProbeAvailable(t, waitRepo.db)
	httpapi.New(nil, waitRepo.db, nil).Routes().ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
	if w.Code != http.StatusOK {
		t.Errorf("readiness did not recover: %d", w.Code)
	}
}

func TestRecordingBudgetReplicaHelper(t *testing.T) {
	if os.Getenv("CMS_BUDGET_REPLICA_HELPER") != "1" {
		t.Skip("subprocess fixture only")
	}
	db, err := sql.Open("pgx", os.Getenv("HHW_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	warmRecordingPool(t, db)
	repo := NewRecordingRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 10; i++ {
		if _, err := repo.GetBroadcastSource(ctx); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	assertDatabaseProbeAvailable(t, db)
	httpapi.New(nil, db, nil).Routes().ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
	if w.Code != 200 {
		t.Errorf("child readiness lost: %d", w.Code)
	}
	repo.transactionMu.Lock()
	active := repo.transactions
	repo.transactionMu.Unlock()
	if active != 0 {
		t.Errorf("child cancelled waits leaked: %d", active)
	}
}

// Hold all connections concurrently before returning them to the idle pool so
// reserve checks measure lock progress, not a new PostgreSQL handshake.
func warmRecordingPool(t *testing.T, db *sql.DB) {
	t.Helper()
	db.SetMaxIdleConns(10)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	held := make([]*sql.Conn, 0, 10)
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for i := 0; i < 10; i++ {
		c, e := db.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, c)
	}
}
