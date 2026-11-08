package bulletins

import (
	"context"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

func (s *Service) UnpublishSelection(ctx context.Context, issueID string, input UnpublishInput, expected int64, actor string) (Issue, error) {
	if !input.UnpublishOnline {
		if input.OnlineVersion != nil {
			return Issue{}, ErrInvalid
		}
		return s.Unpublish(ctx, issueID, input.Series, input.Locale, expected, actor)
	}
	edition, ok := NormalizeBulletinEdition(strings.TrimSpace(input.Series), input.Locale)
	if !ok || edition.Series != "general" || input.Locale != "zh-Hant" || input.OnlineVersion == nil || *input.OnlineVersion < 1 || expected < 1 || strings.TrimSpace(issueID) == "" || actor == "" {
		return Issue{}, ErrInvalid
	}
	repository, ok := s.repository.(interface {
		UnpublishWithOnline(context.Context, string, string, string, int64, int64, string, time.Time) (Issue, error)
	})
	if !ok {
		return Issue{}, ErrNotFound
	}
	return repository.UnpublishWithOnline(ctx, issueID, string(edition.Series), input.Locale, expected, *input.OnlineVersion, actor, s.now().UTC())
}

func (s *Service) PublishOnline(ctx context.Context, issueID, series, locale string, input onlinebulletins.PublishInput, actor string) (onlinebulletins.ConfirmedRevision, error) {
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.Revision < 1 || len(input.Mappings) > 65536 || actor == "" {
		return onlinebulletins.ConfirmedRevision{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.ConfirmedRevision{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		PublishOnlineBulletin(context.Context, string, string, string, onlinebulletins.PublishInput, string, time.Time) (onlinebulletins.ConfirmedRevision, error)
	})
	if !ok {
		return onlinebulletins.ConfirmedRevision{}, ErrNotFound
	}
	return repository.PublishOnlineBulletin(ctx, issueID, series, locale, input, actor, s.now().UTC())
}

func (s *Service) UnpublishOnline(ctx context.Context, issueID, series, locale string, canonicalVersion, onlineVersion int64, actor string) (onlinebulletins.UnpublishedRevision, error) {
	if uuid.Validate(issueID) != nil || canonicalVersion < 1 || onlineVersion < 1 || actor == "" {
		return onlinebulletins.UnpublishedRevision{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.UnpublishedRevision{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		UnpublishOnlineBulletin(context.Context, string, string, string, int64, int64, string, time.Time) (onlinebulletins.UnpublishedRevision, error)
	})
	if !ok {
		return onlinebulletins.UnpublishedRevision{}, ErrNotFound
	}
	return repository.UnpublishOnlineBulletin(ctx, issueID, series, locale, canonicalVersion, onlineVersion, actor, s.now().UTC())
}

func (s *Service) QueueOnlineExtraction(ctx context.Context, issueID, series, locale string, input onlinebulletins.QueueExtractionInput, actor string) (onlinebulletins.QueuedExtraction, error) {
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.OnlineVersion < 0 || actor == "" {
		return onlinebulletins.QueuedExtraction{}, ErrInvalid
	}
	if series != "general" || locale != "zh-Hant" {
		return onlinebulletins.QueuedExtraction{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		QueueOnlineBulletinExtraction(context.Context, string, onlinebulletins.QueueExtractionInput, string, time.Time) (onlinebulletins.QueuedExtraction, error)
	})
	if !ok {
		return onlinebulletins.QueuedExtraction{}, ErrNotFound
	}
	return repository.QueueOnlineBulletinExtraction(ctx, issueID, input, actor, s.now().UTC())
}

func (s *Service) QueueOnlineConversion(ctx context.Context, issueID, series, locale string, input onlinebulletins.QueueSimplifiedInput, actor string) (onlinebulletins.QueuedExtraction, error) {
	if series != "general" || locale != "zh-Hans" {
		return onlinebulletins.QueuedExtraction{}, ErrNotFound
	}
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.SourceOnlineVersion < 1 || input.SourceRevision < 1 || input.TargetOnlineVersion < 0 || actor == "" {
		return onlinebulletins.QueuedExtraction{}, ErrInvalid
	}
	repository, ok := s.repository.(interface {
		QueueOnlineBulletinConversion(context.Context, string, onlinebulletins.QueueSimplifiedInput, string, time.Time) (onlinebulletins.QueuedExtraction, error)
	})
	if !ok {
		return onlinebulletins.QueuedExtraction{}, ErrNotFound
	}
	return repository.QueueOnlineBulletinConversion(ctx, issueID, input, actor, s.now().UTC())
}

func (s *Service) OnlineState(ctx context.Context, issueID, series, locale string) (onlinebulletins.AdminState, error) {
	if uuid.Validate(issueID) != nil {
		return onlinebulletins.AdminState{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.AdminState{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		OnlineBulletinState(context.Context, string, string, string) (onlinebulletins.AdminState, error)
	})
	if !ok {
		return onlinebulletins.AdminState{}, ErrNotFound
	}
	return repository.OnlineBulletinState(ctx, issueID, series, locale)
}

func (s *Service) OnlineComparison(ctx context.Context, issueID, series, locale string) (onlinebulletins.Comparison, error) {
	if uuid.Validate(issueID) != nil {
		return onlinebulletins.Comparison{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.Comparison{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		OnlineBulletinComparison(context.Context, string, string, string) (onlinebulletins.Comparison, error)
	})
	if !ok {
		return onlinebulletins.Comparison{}, ErrNotFound
	}
	return repository.OnlineBulletinComparison(ctx, issueID, series, locale)
}

func (s *Service) ApplyOnlineComparison(ctx context.Context, issueID, series, locale string, input onlinebulletins.CompareInput, actor string) (onlinebulletins.SavedDraft, error) {
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.BaseRevision < 1 || input.LocalRevision < 1 || input.IncomingRevision < 1 || len(input.Choices) > 65536 || actor == "" {
		return onlinebulletins.SavedDraft{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.SavedDraft{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		ApplyOnlineBulletinComparison(context.Context, string, string, string, onlinebulletins.CompareInput, string, time.Time) (onlinebulletins.SavedDraft, error)
	})
	if !ok {
		return onlinebulletins.SavedDraft{}, ErrNotFound
	}
	return repository.ApplyOnlineBulletinComparison(ctx, issueID, series, locale, input, actor, s.now().UTC())
}

func (s *Service) SaveOnlineDraft(ctx context.Context, issueID, series, locale string, input onlinebulletins.SaveDraftInput, actor string) (onlinebulletins.SavedDraft, error) {
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || len(input.Components) < 6 || len(input.Components) > 256 || actor == "" {
		return onlinebulletins.SavedDraft{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.SavedDraft{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		SaveOnlineBulletinDraft(context.Context, string, string, string, onlinebulletins.SaveDraftInput, string, time.Time) (onlinebulletins.SavedDraft, error)
	})
	if !ok {
		return onlinebulletins.SavedDraft{}, ErrNotFound
	}
	return repository.SaveOnlineBulletinDraft(ctx, issueID, series, locale, input, actor, s.now().UTC())
}

func (s *Service) ConfirmOnline(ctx context.Context, issueID, series, locale string, input onlinebulletins.ConfirmInput, actor string) (onlinebulletins.ConfirmedRevision, error) {
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.Revision < 1 || input.PageCount < 1 || input.PageCount > 80 || len(input.LayoutValidationHash) != 64 || input.AcceptedIssues == nil || len(input.AcceptedIssues) > 4096 || actor == "" {
		return onlinebulletins.ConfirmedRevision{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.ConfirmedRevision{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		ConfirmOnlineBulletin(context.Context, string, string, string, onlinebulletins.ConfirmInput, string, time.Time) (onlinebulletins.ConfirmedRevision, error)
	})
	if !ok {
		return onlinebulletins.ConfirmedRevision{}, ErrNotFound
	}
	return repository.ConfirmOnlineBulletin(ctx, issueID, series, locale, input, actor, s.now().UTC())
}

func (s *Service) RestoreOnline(ctx context.Context, issueID, series, locale string, input onlinebulletins.RestoreInput, actor string) (onlinebulletins.SavedDraft, error) {
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.Revision < 1 || actor == "" {
		return onlinebulletins.SavedDraft{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.SavedDraft{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		RestoreOnlineBulletin(context.Context, string, string, string, onlinebulletins.RestoreInput, string, time.Time) (onlinebulletins.SavedDraft, error)
	})
	if !ok {
		return onlinebulletins.SavedDraft{}, ErrNotFound
	}
	return repository.RestoreOnlineBulletin(ctx, issueID, series, locale, input, actor, s.now().UTC())
}

func (s *Service) OnlineHistory(ctx context.Context, issueID, series, locale string, before int64, limit int) (onlinebulletins.RevisionPage, error) {
	if uuid.Validate(issueID) != nil || before < 0 || limit < 1 || limit > 50 {
		return onlinebulletins.RevisionPage{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.RevisionPage{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		OnlineBulletinHistory(context.Context, string, string, string, int64, int) (onlinebulletins.RevisionPage, error)
	})
	if !ok {
		return onlinebulletins.RevisionPage{}, ErrNotFound
	}
	return repository.OnlineBulletinHistory(ctx, issueID, series, locale, before, limit)
}

func (s *Service) OnlineRevision(ctx context.Context, issueID, series, locale string, revision int64) (onlinebulletins.HistoricalRevision, error) {
	if uuid.Validate(issueID) != nil || revision < 1 {
		return onlinebulletins.HistoricalRevision{}, ErrInvalid
	}
	if series != "general" || (locale != "zh-Hant" && locale != "zh-Hans") {
		return onlinebulletins.HistoricalRevision{}, ErrNotFound
	}
	repository, ok := s.repository.(interface {
		OnlineBulletinRevision(context.Context, string, string, string, int64) (onlinebulletins.HistoricalRevision, error)
	})
	if !ok {
		return onlinebulletins.HistoricalRevision{}, ErrNotFound
	}
	return repository.OnlineBulletinRevision(ctx, issueID, series, locale, revision)
}
