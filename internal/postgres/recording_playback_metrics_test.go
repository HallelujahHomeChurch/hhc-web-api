package postgres

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestPlaybackMetricsVisitsOverlapTTLAndReplay(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user := b.View.RecordingID, p.ActorID
	if _, e := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(jsonb_set(payload_json,'{View,announced}','true'),'{View,phase}','"live"') WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	s1, s2 := platform.NewID(), platform.NewID()
	beat := func(session string, seq int64, state string, delta int64, at time.Time) {
		t.Helper()
		if e := repo.RecordPlaybackHeartbeat(ctx, id, user, recordings.PlaybackHeartbeat{PlaybackSessionID: session, Sequence: seq, State: state, Position: 100, DeltaPlayingMS: delta}, 0, at); e != nil {
			t.Fatal(e)
		}
	}
	beat(s1, 1, "playing", 0, now)
	beat(s2, 1, "playing", 0, now)
	beat(s1, 2, "playing", 15000, now.Add(15*time.Second))
	beat(s2, 2, "playing", 15000, now.Add(15*time.Second))
	beat(s1, 2, "playing", 15000, now.Add(20*time.Second))
	v, e := repo.GetPlaybackMetrics(ctx, id, user, false, now.Add(20*time.Second))
	if e != nil || v.TotalViews != 1 || v.LiveViews != 1 || v.UniqueViewers != 1 || v.CurrentViewers != 1 || v.WatchSeconds != 15 {
		t.Fatalf("overlap %+v %v", v, e)
	}
	if e = repo.RollupPlaybackMetrics(ctx, now.Add(20*time.Second)); e != nil {
		t.Fatal(e)
	}
	if e = repo.RollupPlaybackMetrics(ctx, now.Add(20*time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,phase}','"processing"') WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	beat(s1, 3, "playing", 15000, now.Add(30*time.Second))
	beat(s1, 4, "paused", 0, now.Add(31*time.Second))
	v, e = repo.GetPlaybackMetrics(ctx, id, user, false, now.Add(90*time.Second))
	if e != nil || v.TotalViews != 1 || v.LiveViews != 1 || v.ReplayViews != 1 || v.CurrentViewers != 0 || v.WatchSeconds != 30 || v.PeakConcurrent != 1 {
		t.Fatalf("transition %+v %v", v, e)
	}
	beat(s1, 5, "playing", 0, now.Add(31*time.Minute))
	beat(s1, 6, "playing", 15000, now.Add(31*time.Minute+15*time.Second))
	v, e = repo.GetPlaybackMetrics(ctx, id, user, false, now.Add(32*time.Minute))
	if e != nil || v.TotalViews != 2 || v.ReplayViews != 2 || v.UniqueViewers != 1 || v.WatchSeconds != 45 {
		t.Fatalf("new visit %+v %v", v, e)
	}
	if e = repo.RollupPlaybackMetrics(ctx, now.Add(8*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	v, e = repo.GetPlaybackMetrics(ctx, id, user, true, now.Add(8*24*time.Hour))
	if e != nil || v.TotalViews != 2 || v.UniqueViewers != 1 || v.CurrentViewers != 0 {
		t.Fatalf("retention %+v %v", v, e)
	}
}

func TestPlaybackMetricsLocalLoad(t *testing.T) {
	if os.Getenv("HHC_VIDEO_LOAD_TEST") != "1" {
		t.Skip("explicit local load measurement")
	}
	for _, n := range []int{100, 500} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			repo, b, ctx, now := broadcastFixture(t)
			id := b.View.RecordingID
			if _, e := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(jsonb_set(payload_json,'{View,announced}','true'),'{View,phase}','"live"') WHERE recording_id=$1`, id); e != nil {
				t.Fatal(e)
			}
			limit := make(chan struct{}, 32)
			results := make(chan time.Duration, n)
			failures := make(chan error, n)
			var wg sync.WaitGroup
			started := time.Now()
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					limit <- struct{}{}
					defer func() { <-limit }()
					user, session := platform.NewID(), platform.NewID()
					start := time.Now()
					for seq := int64(1); seq <= 2; seq++ {
						if e := repo.RecordPlaybackHeartbeat(ctx, id, user, recordings.PlaybackHeartbeat{PlaybackSessionID: session, Sequence: seq, State: "playing", Position: float64(seq * 15), DeltaPlayingMS: 15000}, 0, now.Add(time.Duration(seq-1)*15*time.Second)); e != nil {
							failures <- e
							return
						}
					}
					results <- time.Since(start)
				}()
			}
			wg.Wait()
			close(results)
			close(failures)
			for e := range failures {
				t.Error(e)
			}
			samples := []time.Duration{}
			for d := range results {
				samples = append(samples, d)
			}
			slices.Sort(samples)
			metrics, e := repo.GetPlaybackMetrics(ctx, id, platform.NewID(), true, now.Add(16*time.Second))
			if e != nil || metrics.TotalViews != int64(n) || metrics.CurrentViewers != int64(n) {
				t.Fatalf("load counters: %+v %v", metrics, e)
			}
			if len(samples) > 0 {
				t.Logf("local DB burst: viewers=%d writes=%d elapsed=%s per-viewer-two-heartbeat p95=%s; excludes Gateway/Account/network", n, n*2, time.Since(started), samples[len(samples)*95/100])
			}
		})
	}
}
