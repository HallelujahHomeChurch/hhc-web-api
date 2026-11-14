package bulletins

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
	"time"
)

func (s *Service) RecoverSimplifiedOnlinePublication(ctx context.Context, id, series, locale string, input onlinebulletins.SimplifiedRecoveryInput, key, actor string) (onlinebulletins.SimplifiedRecoveryResult, error) {
	if uuid.Validate(id) != nil || !onlinebulletins.ValidSimplifiedRecoveryInput(input) || series != "general" || locale != "zh-Hans" || actor == "" || !onlinebulletins.ValidSimplifiedRecoveryKey(key) {
		return onlinebulletins.SimplifiedRecoveryResult{}, ErrInvalid
	}
	repo, ok := s.repository.(interface {
		RecoverSimplifiedOnlineBulletinPublication(context.Context, string, onlinebulletins.SimplifiedRecoveryInput, string, string, time.Time) (onlinebulletins.SimplifiedRecoveryResult, error)
	})
	if !ok {
		return onlinebulletins.SimplifiedRecoveryResult{}, ErrOnlinePublicationUnavailable
	}
	return repo.RecoverSimplifiedOnlineBulletinPublication(ctx, id, input, key, actor, s.now().UTC())
}
