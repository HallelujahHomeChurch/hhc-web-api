package recordings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/google/uuid"
)

type pageCursor struct {
	UploadedAt time.Time `json:"at"`
	ID         string    `json:"id"`
	Version    int       `json:"v,omitempty"`
	Query      string    `json:"q,omitempty"`
}

func (s *Service) ListPage(ctx context.Context, cursor string, limit int, query string) ([]Recording, string, error) {
	query, err := NormalizeSearch(query)
	if err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 100 || len(cursor) > 256 {
		return nil, "", ErrInvalid
	}
	var after pageCursor
	var at *time.Time
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(data, &after) != nil || after.UploadedAt.IsZero() || len(after.ID) != 36 || uuid.Validate(after.ID) != nil || after.Query != searchDigest(query) || (after.Version != 1 && (after.Version != 0 || query != "")) {
			return nil, "", ErrInvalid
		}
		at = &after.UploadedAt
	}
	if err := s.syncLifecycle(ctx); err != nil {
		return nil, "", err
	}
	queryDone := diagnostics.StartRequestPhase(ctx, "list_query")
	values, err := s.repo.ListVisiblePage(ctx, s.now(), at, after.ID, limit+1, query)
	queryDone()
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(values) > limit {
		values = values[:limit]
		last := values[len(values)-1]
		if last.UploadedAt == nil {
			return nil, "", ErrConflict
		}
		data, _ := json.Marshal(pageCursor{UploadedAt: *last.UploadedAt, ID: last.ID, Version: 1, Query: searchDigest(query)})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	items := make([]Recording, 0, len(values))
	for _, value := range values {
		if !value.Visible(s.now()) {
			continue
		}
		duration, err := s.memberDuration(ctx, value)
		if errors.Is(err, assetclient.ErrNotFound) || errors.Is(err, assetclient.ErrForbidden) || errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		value.DurationSeconds = duration
		items = append(items, value)
	}
	return items, next, nil
}

// Member cards describe the playable public range, never the private raw capture.
func (s *Service) memberDuration(ctx context.Context, value Recording) (float64, error) {
	pkg, err := s.readyPackage(ctx, value)
	if err != nil {
		return 0, err
	}
	if value.Broadcast {
		repo, err := s.broadcastRepository()
		if err != nil {
			return 0, err
		}
		b, err := repo.GetBroadcast(ctx, value.ID)
		if err != nil {
			return 0, err
		}
		if !b.MemberVODReady() {
			return 0, ErrConflict
		}
		return *b.Projection.DurationSeconds, nil
	}
	if len(pkg.Renditions) > 0 {
		return pkg.Renditions[0].DurationSeconds, nil
	}
	return 0, nil
}
