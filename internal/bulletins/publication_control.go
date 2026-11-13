package bulletins

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
	"strings"
	"time"
)

var ErrOnlinePublicationUnavailable = errors.New("explicit online publication unavailable")

type OnlinePublicationOutcome struct {
	Outcome     string                                  `json:"outcome"`
	Disposition *onlinebulletins.PublicationDisposition `json:"disposition,omitempty"`
}

func (s *Service) PublishSelection(ctx context.Context, id string, input PublishInput, expected int64, actor string) (Workflow, error) {
	edition, ok := NormalizeBulletinEdition(strings.TrimSpace(input.Series), input.Locale)
	if id == "" || !ok || expected < 1 || actor == "" {
		return Workflow{}, ErrInvalid
	}
	if input.OnlinePublication != "" && input.OnlinePublication != "continue_automatic" && input.OnlinePublication != "pdf_only" {
		return Workflow{}, ErrInvalid
	}
	if input.OnlinePublication != "" && (edition.Series != "general" || input.Locale != "zh-Hant") {
		return Workflow{}, ErrInvalid
	}
	if input.OnlineVersion != nil && (*input.OnlineVersion < 0 || input.OnlinePublication != "pdf_only") {
		return Workflow{}, ErrInvalid
	}
	if input.OnlinePublication == "pdf_only" && input.OnlineVersion == nil {
		return Workflow{}, ErrInvalid
	}
	input.Series = string(edition.Series)
	if repo, ok := s.repository.(interface {
		StartPublishSelection(context.Context, string, PublishInput, int64, string, time.Time) (Workflow, error)
	}); ok {
		return repo.StartPublishSelection(ctx, id, input, expected, actor, s.now().UTC())
	}
	if input.OnlinePublication != "" {
		return Workflow{}, ErrOnlinePublicationUnavailable
	}
	return s.Publish(ctx, id, input.Series, input.Locale, expected, input.NotifySubscribers, actor)
}
func (s *Service) RecoverOnlinePublication(ctx context.Context, id, series, locale string, input onlinebulletins.RecoveryInput, key, actor string) (onlinebulletins.RecoveryResult, error) {
	if uuid.Validate(id) != nil || series != "general" || locale != "zh-Hant" || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.Revision < 1 || len(input.LayoutValidationHash) != 64 || strings.TrimSpace(key) != key || len(key) < 1 || len(key) > 128 || actor == "" {
		return onlinebulletins.RecoveryResult{}, ErrInvalid
	}
	digest, err := hex.DecodeString(input.LayoutValidationHash)
	if err != nil || len(digest) != 32 || strings.ToLower(input.LayoutValidationHash) != input.LayoutValidationHash {
		return onlinebulletins.RecoveryResult{}, ErrInvalid
	}
	repo, ok := s.repository.(interface {
		RecoverOnlineBulletinPublication(context.Context, string, onlinebulletins.RecoveryInput, string, string, time.Time) (onlinebulletins.RecoveryResult, error)
	})
	if !ok {
		return onlinebulletins.RecoveryResult{}, ErrOnlinePublicationUnavailable
	}
	return repo.RecoverOnlineBulletinPublication(ctx, id, input, key, actor, s.now().UTC())
}
