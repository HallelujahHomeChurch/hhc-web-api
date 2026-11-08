package recordings

import (
	"testing"
	"time"
)

func TestPlaybackCreditDoesNotDoubleCountOverlapOrTrustPosition(t *testing.T) {
	now := time.Unix(100, 0)
	for _, v := range []struct {
		last, credited time.Time
		delta, want    int64
	}{{now.Add(-15 * time.Second), now.Add(-15 * time.Second), 15000, 15000}, {now.Add(-15 * time.Second), now.Add(-5 * time.Second), 15000, 5000}, {now.Add(-15 * time.Second), now, 15000, 0}, {now.Add(-5 * time.Second), now.Add(-20 * time.Second), 30000, 5000}, {time.Time{}, time.Time{}, 15000, 0}, {now.Add(time.Second), time.Time{}, 15000, 0}} {
		if got := PlaybackCredit(now, v.last, v.credited, v.delta); got != v.want {
			t.Fatalf("credit %d want %d", got, v.want)
		}
	}
}
