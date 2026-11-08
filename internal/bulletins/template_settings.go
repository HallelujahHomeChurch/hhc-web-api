package bulletins

import (
	"context"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (s *Service) BulletinTemplateSettings(ctx context.Context) (onlinebulletins.TemplateSettings, error) {
	repo, ok := s.repository.(interface {
		BulletinTemplateSettings(context.Context) (onlinebulletins.TemplateSettings, error)
	})
	if !ok {
		return onlinebulletins.TemplateSettings{}, ErrNotFound
	}
	return repo.BulletinTemplateSettings(ctx)
}

func (s *Service) BulletinTemplateSettingsHistory(ctx context.Context) ([]onlinebulletins.TemplateSettingsRevision, error) {
	repo, ok := s.repository.(interface {
		BulletinTemplateSettingsHistory(context.Context) ([]onlinebulletins.TemplateSettingsRevision, error)
	})
	if !ok {
		return nil, ErrNotFound
	}
	return repo.BulletinTemplateSettingsHistory(ctx)
}
