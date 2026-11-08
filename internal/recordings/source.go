package recordings

import (
	"context"
	"errors"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

func (s *Service) CreateSource(ctx context.Context, id string, input assetclient.RecordingSourceInput, key string) (assetclient.RecordingSource, error) {
	if s.packages == nil {
		return assetclient.RecordingSource{}, assetclient.ErrUnavailable
	}
	if !ValidOperationKey(key) {
		return assetclient.RecordingSource{}, ErrInvalid
	}
	value, err := s.repo.Get(ctx, id)
	if err != nil {
		return assetclient.RecordingSource{}, err
	}
	if value.SourceID == "" && value.Expired(s.now()) {
		return assetclient.RecordingSource{}, ErrNotFound
	}
	if value.PrimaryAssetVersionID != "" || value.PackageID != "" && value.SourceID == "" {
		return assetclient.RecordingSource{}, ErrConflict
	}
	input.RecordingID = id
	input.FileName = strings.TrimSpace(input.FileName)
	input.ChecksumSHA256 = strings.ToLower(input.ChecksumSHA256)
	source, err := s.packages.CreateRecordingSource(ctx, input, key)
	if err != nil {
		return assetclient.RecordingSource{}, err
	}
	if source.RecordingID != id || !packageIDPattern.MatchString(source.SourceID) || source.FileName != input.FileName || source.SizeBytes != input.SizeBytes || source.ChecksumSHA256 != input.ChecksumSHA256 || value.SourceID != "" && value.SourceID != source.SourceID {
		return assetclient.RecordingSource{}, ErrConflict
	}
	if value.SourceID == "" {
		if err := s.repo.AddSource(ctx, id, source.SourceID, s.now()); err != nil {
			return assetclient.RecordingSource{}, err
		}
	}
	return source, nil
}

func (s *Service) sourceRecording(ctx context.Context, id, source string) (Recording, error) {
	if s.packages == nil {
		return Recording{}, assetclient.ErrUnavailable
	}
	if !packageIDPattern.MatchString(source) {
		return Recording{}, ErrInvalid
	}
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return Recording{}, err
	}
	if p.SourceID != source || p.MediaTerminal {
		return Recording{}, ErrNotFound
	}
	return p, nil
}

func (s *Service) syncSourceReady(ctx context.Context, p Recording) (Recording, error) {
	if s.packages == nil {
		return Recording{}, assetclient.ErrUnavailable
	}
	source, err := s.packages.GetReadyRecordingSource(ctx, p.SourceID, p.ID)
	if errors.Is(err, assetclient.ErrConflict) || errors.Is(err, assetclient.ErrNotFound) || errors.Is(err, assetclient.ErrForbidden) {
		return p, nil
	}
	if err != nil {
		return Recording{}, err
	}
	if source.SourceID != p.SourceID || source.RecordingID != p.ID || source.State != "ready" || !packageIDPattern.MatchString(source.PackageID) {
		return Recording{}, ErrConflict
	}
	pkg, err := s.packages.GetReadyRecordingPackage(ctx, source.PackageID, p.ID)
	if err != nil {
		return Recording{}, err
	}
	if !s.validReadyPackage(p.ID, source.PackageID, pkg) {
		return Recording{}, ErrConflict
	}
	v, err := s.repo.RecordSourceReady(ctx, p.ID, p.SourceID, source.PackageID, *pkg.ReadyAt)
	if err != nil {
		return v, err
	}
	return s.projectPackageReady(ctx, p.ID, source.PackageID, pkg)
}

func (s *Service) SourceStatus(ctx context.Context, id, source string, cursor, limit int) (assetclient.RecordingSourceStatus, error) {
	p, err := s.sourceRecording(ctx, id, source)
	if err != nil {
		return assetclient.RecordingSourceStatus{}, err
	}
	status, err := s.packages.GetRecordingSource(ctx, source, cursor, limit)
	if err != nil {
		return assetclient.RecordingSourceStatus{}, err
	}
	if status.SourceID != source || status.RecordingID != id {
		return assetclient.RecordingSourceStatus{}, ErrConflict
	}
	if status.State == "ready" && p.ReadyAt == nil {
		if _, err := s.syncSourceReady(ctx, p); err != nil {
			return assetclient.RecordingSourceStatus{}, err
		}
	}
	return status, nil
}

func (s *Service) SignSource(ctx context.Context, id, source string, numbers []int) ([]assetclient.SignedRecordingSourceBlock, error) {
	if _, err := s.sourceRecording(ctx, id, source); err != nil {
		return nil, err
	}
	return s.packages.SignRecordingSource(ctx, source, numbers)
}

func (s *Service) CompleteSource(ctx context.Context, id, source string) (assetclient.RecordingSource, error) {
	if _, err := s.sourceRecording(ctx, id, source); err != nil {
		return assetclient.RecordingSource{}, err
	}
	value, err := s.packages.CompleteRecordingSource(ctx, source)
	if err != nil {
		return assetclient.RecordingSource{}, err
	}
	if value.SourceID != source || value.RecordingID != id {
		return assetclient.RecordingSource{}, ErrConflict
	}
	return value, nil
}

func (s *Service) RetrySource(ctx context.Context, id, source string) (assetclient.RecordingSource, error) {
	if _, err := s.sourceRecording(ctx, id, source); err != nil {
		return assetclient.RecordingSource{}, err
	}
	value, err := s.packages.RetryRecordingSource(ctx, source)
	if err != nil {
		return assetclient.RecordingSource{}, err
	}
	if value.SourceID != source || value.RecordingID != id {
		return assetclient.RecordingSource{}, ErrConflict
	}
	return value, nil
}
