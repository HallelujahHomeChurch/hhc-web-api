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
	candidates := make([]Recording, 0, len(values))
	bindings := make([]assetclient.RecordingLifecycleBinding, 0, len(values))
	for _, value := range values {
		if !value.Visible(s.now()) {
			continue
		}
		candidates = append(candidates, value)
		bindings = append(bindings, assetclient.RecordingLifecycleBinding{RecordingID: value.ID, PackageID: value.PackageID})
	}
	if len(bindings) == 0 {
		return items, next, nil
	}
	if s.packages == nil {
		return nil, "", assetclient.ErrUnavailable
	}
	packageDone := diagnostics.StartRequestPhase(ctx, "package_calls")
	ready, err := s.packages.GetReadyRecordingPackages(ctx, bindings)
	packageDone()
	if err != nil {
		return nil, "", err
	}
	// Validate the entire response before applying any package projection.
	for _, result := range ready {
		if result.Status == "ready" && !s.validReadyPackage(result.RecordingID, result.PackageID, *result.Package) {
			return nil, "", assetclient.ErrUnavailable
		}
	}
	for i, value := range candidates {
		if ready[i].Status != "ready" || !value.Visible(s.now()) {
			continue
		}
		pkg := *ready[i].Package
		err := s.applyReadyPackage(ctx, value, pkg)
		if errors.Is(err, assetclient.ErrNotFound) || errors.Is(err, assetclient.ErrForbidden) || errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		duration, err := s.memberPackageDuration(ctx, value, pkg)
		if errors.Is(err, ErrConflict) {
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
	return s.memberPackageDuration(ctx, value, pkg)
}

func (s *Service) memberPackageDuration(ctx context.Context, value Recording, pkg assetclient.RecordingPackageStatus) (float64, error) {
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
