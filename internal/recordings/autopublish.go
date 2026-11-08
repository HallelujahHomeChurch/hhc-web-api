package recordings

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

var ErrCoverPending = errors.New("recording cover pending")

func (s *Service) WithPublicationAuthorizer(client *accountclient.Client) *Service {
	s.publicationAuthorizer = client
	return s
}

type autoPublicationRepository interface {
	ListAutoPublicationCaptures(context.Context, time.Time, int) ([]Capture, error)
	PublishCapture(context.Context, string, time.Time, bool, time.Time) error
}

func (s *Service) ReconcileAutoPublication(ctx context.Context, limit int) error {
	repo, ok := s.repo.(autoPublicationRepository)
	if !ok {
		return ErrConflict
	}
	captures, err := repo.ListAutoPublicationCaptures(ctx, s.now().UTC(), limit)
	if err != nil {
		return err
	}
	var failures error
	for _, c := range captures {
		checkedAt, attemptErr := s.autoPublishCapture(ctx, repo, c)
		if attemptErr == nil {
			continue
		}
		captureRepo, err := s.captureRepository()
		if err != nil {
			return err
		}
		_, err = captureRepo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
			if current.Status.AutoPublish != "pending" || current.Status.CaptureID != c.Status.CaptureID || current.BroadcastEpoch != c.BroadcastEpoch {
				return nil
			}
			if errors.Is(attemptErr, ErrCoverPending) {
				next := s.now().UTC().Add(30 * time.Second)
				current.AutoNextAttemptAt = &next
				return nil
			}
			current.AutoAttempts++
			if !checkedAt.IsZero() {
				current.PermissionCheckedAt = &checkedAt
			}
			delay := time.Minute << min(current.AutoAttempts-1, 6)
			next := s.now().UTC().Add(delay)
			current.AutoNextAttemptAt = &next
			return nil
		})
		failures = errors.Join(failures, attemptErr, err)
	}
	return failures
}
func (s *Service) autoPublishCapture(ctx context.Context, repo autoPublicationRepository, c Capture) (time.Time, error) {
	if !c.NormalEnd || c.Terminal() || c.Status.State != "ready" || c.Status.StopAcceptedAt == nil || c.Status.PackageID == nil || *c.Status.PackageID != c.Status.CaptureID {
		return time.Time{}, ErrConflict
	}
	current, err := s.repo.Get(ctx, c.RecordingID)
	if err != nil {
		return time.Time{}, err
	}
	if current.PackageID != c.Status.CaptureID {
		return time.Time{}, ErrConflict
	}
	// Asset lookup retains the persisted uploader identity required by its owner
	// contract; publication itself carries no human request provenance.
	ownerCtx := auditclient.WithPrincipalProvenance(ctx, c.ActorType, c.ActorID, c.RequestID)
	if c.LiveCover != nil {
		if err := s.reconcileCaptureLiveCover(ctx, c); err != nil {
			return time.Time{}, err
		}
		current, err = s.repo.Get(ctx, c.RecordingID)
		if err != nil {
			return time.Time{}, err
		}
	}
	pkg, err := s.readyPackage(ownerCtx, current)
	if err != nil {
		return time.Time{}, err
	}
	covers, err := s.packages.ListRecordingCovers(ownerCtx, pkg.PackageID, current.ID)
	if err != nil {
		return time.Time{}, err
	}
	if !slices.ContainsFunc(covers, func(cover assetclient.RecordingCover) bool {
		return cover.State == "ready" && (current.SelectedCoverID == cover.ID || current.SelectedCoverID == "" && cover.Kind == "auto")
	}) {
		if len(covers) == 0 && current.SelectedCoverID == "" || slices.ContainsFunc(covers, func(cover assetclient.RecordingCover) bool {
			return (cover.State == "pending" || cover.State == "processing") && (current.SelectedCoverID == cover.ID || current.SelectedCoverID == "" && cover.Kind == "auto")
		}) {
			return time.Time{}, ErrCoverPending
		}
		return time.Time{}, ErrConflict
	}
	captureRepo, err := s.captureRepository()
	if err != nil {
		return time.Time{}, err
	}
	c, err = captureRepo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
		if current.Status.AutoPublish == "pending" && current.Status.CaptureID == pkg.PackageID && current.NormalEnd && current.Status.State == "ready" {
			current.VerifiedReadyAt = pkg.ReadyAt
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	if c.Status.AutoPublish != "pending" {
		return time.Time{}, nil
	}
	if s.publicationAuthorizer == nil {
		return time.Time{}, assetclient.ErrUnavailable
	}
	allowed, err := s.publicationAuthorizer.VerifyRecordingPublication(ctx, c.ActorID)
	if err != nil {
		return time.Time{}, err
	}
	checkedAt := s.now().UTC()
	return checkedAt, repo.PublishCapture(ctx, c.IntentID, checkedAt, allowed, s.now().UTC())
}
