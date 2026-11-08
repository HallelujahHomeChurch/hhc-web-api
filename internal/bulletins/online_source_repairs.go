package bulletins

import (
	"context"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

func (s *Service) OnlineSourceParts(ctx context.Context, issueID, series, locale string) (onlinebulletins.SourceRepairCatalog, error) {
	if uuid.Validate(issueID) != nil {
		return onlinebulletins.SourceRepairCatalog{}, ErrInvalid
	}
	if series != "general" || locale != "zh-Hant" {
		return onlinebulletins.SourceRepairCatalog{}, ErrNotFound
	}
	repo, ok := s.repository.(interface {
		OnlineBulletinSourceParts(context.Context, string) (onlinebulletins.SourceRepairCatalog, error)
	})
	if !ok {
		return onlinebulletins.SourceRepairCatalog{}, ErrNotFound
	}
	return repo.OnlineBulletinSourceParts(ctx, issueID)
}

func (s *Service) RepairOnlineSource(ctx context.Context, issueID string, repair onlinebulletins.PreparedSourceRepair, actor string) (onlinebulletins.SavedDraft, error) {
	if uuid.Validate(issueID) != nil || actor == "" || repair.Input.CanonicalVersion < 1 || repair.Input.OnlineVersion < 1 || repair.Input.Revision < 1 {
		return onlinebulletins.SavedDraft{}, ErrInvalid
	}
	repo, ok := s.repository.(interface {
		SaveOnlineBulletinSourceRepair(context.Context, string, onlinebulletins.PreparedSourceRepair, string, time.Time) (onlinebulletins.SavedDraft, error)
	})
	if !ok {
		return onlinebulletins.SavedDraft{}, ErrNotFound
	}
	return repo.SaveOnlineBulletinSourceRepair(ctx, issueID, repair, actor, s.now().UTC())
}
