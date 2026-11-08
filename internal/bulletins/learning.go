package bulletins

import (
	"context"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type learningRulesRepository interface {
	OnlineBulletinLearningRules(context.Context, string) (onlinebulletins.LearningRulePage, error)
	OnlineBulletinLearningRule(context.Context, string) (onlinebulletins.LearningRuleDetail, error)
	SetOnlineBulletinLearningRuleEnabled(context.Context, string, bool, int64, string, time.Time) (onlinebulletins.LearningRule, error)
}

func (s *Service) OnlineBulletinLearningRules(ctx context.Context, cursor string) (onlinebulletins.LearningRulePage, error) {
	if cursor != "" && !onlinebulletins.ValidLearningSignature(cursor) {
		return onlinebulletins.LearningRulePage{}, ErrInvalid
	}
	repo, ok := s.repository.(learningRulesRepository)
	if !ok {
		return onlinebulletins.LearningRulePage{}, ErrNotFound
	}
	return repo.OnlineBulletinLearningRules(ctx, cursor)
}

func (s *Service) OnlineBulletinLearningRule(ctx context.Context, signature string) (onlinebulletins.LearningRuleDetail, error) {
	if !onlinebulletins.ValidLearningSignature(signature) {
		return onlinebulletins.LearningRuleDetail{}, ErrInvalid
	}
	repo, ok := s.repository.(learningRulesRepository)
	if !ok {
		return onlinebulletins.LearningRuleDetail{}, ErrNotFound
	}
	return repo.OnlineBulletinLearningRule(ctx, signature)
}

func (s *Service) SetOnlineBulletinLearningRuleEnabled(ctx context.Context, signature string, enabled bool, expected int64, actor string) (onlinebulletins.LearningRule, error) {
	if !onlinebulletins.ValidLearningSignature(signature) || expected < 1 || strings.TrimSpace(actor) == "" {
		return onlinebulletins.LearningRule{}, ErrInvalid
	}
	repo, ok := s.repository.(learningRulesRepository)
	if !ok {
		return onlinebulletins.LearningRule{}, ErrNotFound
	}
	return repo.SetOnlineBulletinLearningRuleEnabled(ctx, signature, enabled, expected, actor, s.now().UTC())
}
