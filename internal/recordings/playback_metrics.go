package recordings

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"math"
	"time"
)

const MetricVersion = "hhc-video-1"

type PlaybackHeartbeat struct {
	PlaybackSessionID string  `json:"playbackSessionId"`
	Sequence          int64   `json:"sequence"`
	State             string  `json:"state"`
	Position          float64 `json:"position"`
	DeltaPlayingMS    int64   `json:"deltaPlayingMs"`
}

func (v PlaybackHeartbeat) Validate() error {
	if !validLiveUUID(v.PlaybackSessionID) || v.Sequence < 1 || v.Sequence > 9007199254740991 || v.Position < 0 || v.Position > 7*86400 || math.IsNaN(v.Position) || math.IsInf(v.Position, 0) || v.DeltaPlayingMS < 0 || v.DeltaPlayingMS > 30000 {
		return ErrInvalid
	}
	switch v.State {
	case "playing", "paused", "waiting", "ended":
		return nil
	}
	return ErrInvalid
}

// Credit wall time once across overlapping tabs, never seek displacement or playback rate.
func PlaybackCredit(now, lastSeen, creditedUntil time.Time, deltaMS int64) int64 {
	if lastSeen.IsZero() || !now.After(lastSeen) || deltaMS <= 0 {
		return 0
	}
	start := now.Add(-time.Duration(deltaMS) * time.Millisecond)
	if start.Before(lastSeen) {
		start = lastSeen
	}
	if start.Before(creditedUntil) {
		start = creditedUntil
	}
	if !now.After(start) {
		return 0
	}
	return now.Sub(start).Milliseconds()
}

type PlaybackMetrics struct {
	CurrentViewers int64     `json:"currentViewers"`
	PeakConcurrent int64     `json:"peakConcurrent"`
	LiveViews      int64     `json:"liveViews"`
	ReplayViews    int64     `json:"replayViews"`
	TotalViews     int64     `json:"totalViews"`
	UniqueViewers  int64     `json:"uniqueViewers"`
	WatchSeconds   float64   `json:"watchSeconds"`
	MeasuredAt     time.Time `json:"measuredAt"`
	TrackingSince  time.Time `json:"trackingSince"`
	MetricVersion  string    `json:"metricVersion"`
	Status         string    `json:"status"`
}
type PlaybackMetricsRepository interface {
	RecordPlaybackHeartbeat(context.Context, string, string, PlaybackHeartbeat, float64, time.Time) error
	GetPlaybackMetrics(context.Context, string, string, bool, time.Time) (PlaybackMetrics, error)
	RollupPlaybackMetrics(context.Context, time.Time) error
}

func (s *Service) RecordPlaybackHeartbeat(ctx context.Context, id, user string, input PlaybackHeartbeat) error {
	if e := input.Validate(); e != nil {
		return e
	}
	repo, ok := s.repo.(PlaybackMetricsRepository)
	if !ok {
		return assetclient.ErrUnavailable
	}
	duration := float64(0)
	if input.State == "ended" {
		recording, e := s.repo.Get(ctx, id)
		if e != nil {
			return e
		}
		if recording.Visible(s.now()) {
			value, e := s.memberDuration(ctx, recording)
			if e != nil {
				return e
			}
			duration = value
		}
	}
	return repo.RecordPlaybackHeartbeat(ctx, id, user, input, duration, s.now())
}
func (s *Service) GetPlaybackMetrics(ctx context.Context, id, user string, admin bool) (PlaybackMetrics, error) {
	if !validLiveUUID(id) {
		return PlaybackMetrics{}, ErrInvalid
	}
	repo, ok := s.repo.(PlaybackMetricsRepository)
	if !ok {
		return PlaybackMetrics{}, assetclient.ErrUnavailable
	}
	return repo.GetPlaybackMetrics(ctx, id, user, admin, s.now())
}
func (s *Service) RollupPlaybackMetrics(ctx context.Context) error {
	repo, ok := s.repo.(PlaybackMetricsRepository)
	if !ok {
		return assetclient.ErrUnavailable
	}
	return repo.RollupPlaybackMetrics(ctx, s.now())
}

func (s *Service) WithVideoAnalytics(enabled bool) *Service { s.videoAnalytics = enabled; return s }
func (s *Service) VideoAnalyticsEnabled() bool              { return s.videoAnalytics }
