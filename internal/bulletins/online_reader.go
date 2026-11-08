package bulletins

import (
	"context"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

func (s *Service) DiscoverOnline(ctx context.Context, series string, locales []string, offset, limit int, issueNumber *int) (onlinebulletins.DiscoveryPage, error) {
	if len(locales) < 1 || len(locales) > 3 {
		return onlinebulletins.DiscoveryPage{}, ErrInvalid
	}
	for _, locale := range locales {
		if _, ok := NormalizeBulletinEdition(series, locale); !ok {
			return onlinebulletins.DiscoveryPage{}, ErrNotFound
		}
	}
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 || issueNumber != nil && *issueNumber < 1 {
		return onlinebulletins.DiscoveryPage{}, ErrInvalid
	}
	repo, ok := s.repository.(interface {
		DiscoverOnlineBulletins(context.Context, string, []string, int, int, *int) (onlinebulletins.DiscoveryPage, error)
	})
	if !ok {
		return onlinebulletins.DiscoveryPage{}, errors.New("reader repository unavailable")
	}
	return repo.DiscoverOnlineBulletins(ctx, series, locales, offset, limit, issueNumber)
}

func (s *Service) OpenOnline(ctx context.Context, issue, series, locale, actor, privacy string, input onlinebulletins.ReaderAccessInput) (onlinebulletins.ReaderResponse, error) {
	if uuid.Validate(issue) != nil {
		return onlinebulletins.ReaderResponse{}, ErrNotFound
	}
	if _, ok := NormalizeBulletinEdition(series, locale); !ok {
		return onlinebulletins.ReaderResponse{}, ErrNotFound
	}
	if uuid.Validate(actor) != nil || uuid.Validate(input.ClientRequestID) != nil || input.ReceiptID != "" && uuid.Validate(input.ReceiptID) != nil || input.Revision != nil && (*input.Revision < 1 || *input.Revision > 9007199254740991) {
		return onlinebulletins.ReaderResponse{}, ErrInvalid
	}
	if privacy == "" || len(privacy) > 200 {
		return onlinebulletins.ReaderResponse{}, errors.New("reader privacy version unavailable")
	}
	repo, ok := s.repository.(interface {
		OpenOnlineBulletin(context.Context, string, string, string, string, string, onlinebulletins.ReaderAccessInput, time.Time) (onlinebulletins.ReaderResponse, error)
	})
	if !ok {
		return onlinebulletins.ReaderResponse{}, errors.New("reader repository unavailable")
	}
	return repo.OpenOnlineBulletin(ctx, issue, series, locale, actor, privacy, input, s.now().UTC())
}
