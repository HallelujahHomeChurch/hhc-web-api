package recordings

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"time"
)

// CardProgress is a read-only projection, never a playback ownership claim.
type CardProgress struct {
	MediaVersion    string  `json:"mediaVersion"`
	PositionSeconds float64 `json:"positionSeconds"`
	Completed       bool    `json:"completed"`
}

// CardPlayback binds optional account history to the current eligible VOD version.
type CardPlayback struct {
	MediaVersion string
	Progress     *CardProgress
}

type cardProgressRepository interface {
	GetCardProgress(context.Context, string, []string, time.Time) (map[string]CardPlayback, error)
}

func (s *Service) GetCardProgress(ctx context.Context, user string, ids []string) (map[string]CardPlayback, error) {
	if !validLiveUUID(user) || len(ids) > 100 {
		return nil, ErrInvalid
	}
	for _, id := range ids {
		if !validLiveUUID(id) {
			return nil, ErrInvalid
		}
	}
	if len(ids) == 0 {
		return map[string]CardPlayback{}, nil
	}
	repo, ok := s.repo.(cardProgressRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return repo.GetCardProgress(ctx, user, ids, s.now())
}
