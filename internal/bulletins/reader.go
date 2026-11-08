package bulletins

import (
	"context"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/google/uuid"
)

func validateReaderScope(issue, series, locale, owner string) error {
	if uuid.Validate(issue) != nil {
		return ErrNotFound
	}
	if _, ok := NormalizeBulletinEdition(series, locale); !ok {
		return ErrNotFound
	}
	if uuid.Validate(owner) != nil {
		return ErrInvalid
	}
	return nil
}
func (s *Service) ReaderState(ctx context.Context, issue, series, locale, owner string, from *int64) (bulletinreader.StateResponse, error) {
	if err := validateReaderScope(issue, series, locale, owner); err != nil {
		return bulletinreader.StateResponse{}, err
	}
	if from != nil && (*from < 1 || *from > 9007199254740991) {
		return bulletinreader.StateResponse{}, ErrInvalid
	}
	repo, ok := s.repository.(interface {
		ReaderState(context.Context, string, string, string, string, *int64, time.Time) (bulletinreader.StateResponse, error)
	})
	if !ok {
		return bulletinreader.StateResponse{}, errors.New("reader repository unavailable")
	}
	return repo.ReaderState(ctx, issue, series, locale, owner, from, s.now().UTC())
}
func (s *Service) MutateReader(ctx context.Context, issue, series, locale, owner string, mutations []bulletinreader.Mutation) (bulletinreader.MutationResponse, error) {
	if err := validateReaderScope(issue, series, locale, owner); err != nil {
		return bulletinreader.MutationResponse{}, err
	}
	if len(mutations) < 1 || len(mutations) > 100 {
		return bulletinreader.MutationResponse{}, ErrInvalid
	}
	repo, ok := s.repository.(interface {
		MutateReader(context.Context, string, string, string, string, []bulletinreader.Mutation, time.Time) (bulletinreader.MutationResponse, error)
	})
	if !ok {
		return bulletinreader.MutationResponse{}, errors.New("reader repository unavailable")
	}
	return repo.MutateReader(ctx, issue, series, locale, owner, mutations, s.now().UTC())
}
