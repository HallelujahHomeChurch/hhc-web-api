package recordings

import (
	"context"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
)

type LifecycleRepository interface {
	LifecycleBindings(context.Context) ([]assetclient.RecordingLifecycleBinding, error)
	ApplyLifecycle(context.Context, assetclient.RecordingLifecycleSnapshot) error
}

func (s *Service) syncLifecycle(ctx context.Context) error {
	_, err := s.syncLifecycleSnapshot(ctx, nil)
	return err
}

func (s *Service) syncLifecycleSnapshot(ctx context.Context, sources []assetclient.RecordingSourceLifecycleBinding) (assetclient.RecordingLifecycleSnapshot, error) {
	defer diagnostics.StartRequestPhase(ctx, "lifecycle")()
	var empty assetclient.RecordingLifecycleSnapshot
	repo, ok := s.repo.(LifecycleRepository)
	if !ok {
		return empty, nil
	} // Legacy/non-package repositories have no media projection.
	if s.packages == nil {
		return empty, assetclient.ErrUnavailable
	}
	bindings, err := repo.LifecycleBindings(ctx)
	if err != nil {
		return empty, err
	}
	return s.syncLifecycleBindings(ctx, repo, bindings, sources)
}

func (s *Service) syncLifecycleBindings(ctx context.Context, repo LifecycleRepository, bindings []assetclient.RecordingLifecycleBinding, sources []assetclient.RecordingSourceLifecycleBinding) (assetclient.RecordingLifecycleSnapshot, error) {
	var empty assetclient.RecordingLifecycleSnapshot
	if s.packages == nil {
		return empty, assetclient.ErrUnavailable
	}
	v, err := s.packages.RecordingLifecycleWithSources(ctx, bindings, sources)
	if err != nil {
		return empty, err
	}
	if v.Policy.RetentionDays < 1 || v.Policy.RetentionDays > 365 || v.Policy.Revision < 1 || len(v.Items) != len(bindings) {
		return empty, ErrConflict
	}
	wanted := make(map[string]string, len(bindings))
	for _, b := range bindings {
		wanted[b.PackageID] = b.RecordingID
	}
	for _, p := range v.Items {
		if wanted[p.PackageID] == "" || wanted[p.PackageID] != p.RecordingID || p.SessionID != p.PackageID || p.RetentionRevision < 1 || p.RetentionRevision > v.Policy.Revision {
			return empty, ErrConflict
		}
		delete(wanted, p.PackageID)
		if !p.ProcessingProgress.Valid() {
			return empty, ErrConflict
		}
		switch p.State {
		case "uploading", "freezing", "validating", "failed", "expired":
		case "ready":
			if p.UploadedAt == nil || p.ReadyAt == nil || p.MediaExpiresAt == nil || p.UploadedAt.IsZero() || p.UploadedAt.After(*p.ReadyAt) || p.ReadyAt.After(s.now()) {
				return empty, ErrConflict
			}
			if v.Policy.ActivatedAt != nil && (!p.MediaExpiresAt.Equal(p.UploadedAt.Add(time.Duration(v.Policy.RetentionDays)*24*time.Hour)) || p.RetentionRevision != v.Policy.Revision) {
				return empty, ErrConflict
			}
		default:
			return empty, ErrConflict
		}
	}
	wantedSources := make(map[string]string, len(sources))
	for _, b := range sources {
		wantedSources[b.SourceID] = b.RecordingID
	}
	// Old producers omit sourceItems. This means unknown progress, not failure.
	if len(v.SourceItems) != 0 && len(v.SourceItems) != len(sources) {
		return empty, ErrConflict
	}
	for _, source := range v.SourceItems {
		if wantedSources[source.SourceID] == "" || wantedSources[source.SourceID] != source.RecordingID || !source.ProcessingProgress.Valid() {
			return empty, ErrConflict
		}
		delete(wantedSources, source.SourceID)
		switch source.State {
		case "uploading", "finalizing", "queued", "processing", "ready", "failed", "expired":
		default:
			return empty, ErrConflict
		}
	}
	return v, repo.ApplyLifecycle(ctx, v)
}

func (s *Service) GetRetentionPolicy(ctx context.Context) (assetclient.RecordingRetentionPolicy, error) {
	if s.packages == nil {
		return assetclient.RecordingRetentionPolicy{}, assetclient.ErrUnavailable
	}
	return s.packages.GetRecordingRetentionPolicy(ctx)
}
func (s *Service) PreviewRetentionPolicy(ctx context.Context, days int) (assetclient.RecordingRetentionPreview, error) {
	if s.packages == nil {
		return assetclient.RecordingRetentionPreview{}, assetclient.ErrUnavailable
	}
	return s.packages.PreviewRecordingRetentionPolicy(ctx, days)
}
func (s *Service) UpdateRetentionPolicy(ctx context.Context, in assetclient.UpdateRecordingRetentionInput, key string) (assetclient.RecordingRetentionPolicy, error) {
	if s.packages == nil {
		return assetclient.RecordingRetentionPolicy{}, assetclient.ErrUnavailable
	}
	if !ValidOperationKey(key) {
		return assetclient.RecordingRetentionPolicy{}, ErrInvalid
	}
	// Asset commits the configuration, immutable receipt and audit atomically.
	return s.packages.UpdateRecordingRetentionPolicy(ctx, in, key)
}
