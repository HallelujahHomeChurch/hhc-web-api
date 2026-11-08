package bulletins

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	return &Service{repository: repository, now: now}
}

func (s *Service) CreateIssue(ctx context.Context, input CreateIssueInput, actor, idempotencyKey string) (Issue, error) {
	if input.IssueNumber < 1 || !validDate(input.IssueDate) || strings.TrimSpace(actor) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.CreateIssue(ctx, input.IssueNumber, input.IssueDate, actor, idempotencyKey, s.now().UTC())
}
func (s *Service) ListIssues(ctx context.Context, page, pageSize int, status, query, sort, direction string) (Page, error) {
	page, pageSize = normalizePage(page, pageSize)
	if sort == "" {
		sort = "issueNumber"
	}
	if direction == "" {
		direction = "desc"
	}
	if !slices.Contains([]string{"issueNumber", "date", "title", "languages", "status", "updated"}, sort) || !slices.Contains([]string{"asc", "desc"}, direction) {
		return Page{}, ErrInvalid
	}
	return s.repository.ListIssues(ctx, page, pageSize, status, strings.TrimSpace(query), sort, direction)
}
func (s *Service) GetIssue(ctx context.Context, id string) (Issue, error) {
	if strings.TrimSpace(id) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.GetIssue(ctx, id)
}
func (s *Service) UpdateIssue(ctx context.Context, id string, expected int64, input UpdateIssueInput, actor string) (Issue, error) {
	if strings.TrimSpace(id) == "" || expected < 1 || input.IssueNumber < 1 || !validDate(input.IssueDate) || strings.TrimSpace(actor) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.UpdateIssue(ctx, id, expected, input, actor, s.now().UTC())
}
func (s *Service) PutVersion(ctx context.Context, id string, expected int64, input PutVersionInput, actor string) (Issue, error) {
	input.Series = strings.TrimSpace(input.Series)
	input.Locale = strings.TrimSpace(input.Locale)
	input.Title = strings.TrimSpace(input.Title)
	input.Subtitle = strings.TrimSpace(input.Subtitle)
	input.PDFAssetID = strings.TrimSpace(input.PDFAssetID)
	input.PDFFileName = filepath.Base(strings.TrimSpace(input.PDFFileName))
	edition, ok := NormalizeBulletinEdition(input.Series, input.Locale)
	if id == "" || expected <= 0 || !ok || input.Title == "" || len(input.Title) > 200 || len(input.Subtitle) > 300 || input.PDFAssetID == "" || input.PDFFileName == "" || len(input.PDFFileName) > 255 || actor == "" {
		return Issue{}, ErrInvalid
	}
	input.Series = string(edition.Series)
	return s.repository.PutVersion(ctx, id, expected, input, actor, s.now().UTC())
}
func (s *Service) UpdateVersion(ctx context.Context, id, series, locale string, expected int64, input UpdateVersionInput, actor string) (Issue, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Subtitle = strings.TrimSpace(input.Subtitle)
	edition, ok := NormalizeBulletinEdition(strings.TrimSpace(series), locale)
	if strings.TrimSpace(id) == "" || !ok || expected < 1 || input.Title == "" || len(input.Title) > 200 || len(input.Subtitle) > 300 || strings.TrimSpace(actor) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.UpdateVersion(ctx, id, string(edition.Series), locale, expected, input.Title, input.Subtitle, actor, s.now().UTC())
}
func (s *Service) DeleteVersion(ctx context.Context, id, series, locale string, expected int64, actor string) (Issue, error) {
	edition, ok := NormalizeBulletinEdition(strings.TrimSpace(series), locale)
	if strings.TrimSpace(id) == "" || !ok || expected < 1 || strings.TrimSpace(actor) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.DeleteVersion(ctx, id, string(edition.Series), locale, expected, actor, s.now().UTC())
}
func (s *Service) Publish(ctx context.Context, id, series, locale string, expected int64, notifySubscribers bool, actor string) (Workflow, error) {
	edition, ok := NormalizeBulletinEdition(strings.TrimSpace(series), locale)
	if id == "" || !ok || expected <= 0 || actor == "" {
		return Workflow{}, ErrInvalid
	}
	return s.repository.StartPublish(ctx, id, string(edition.Series), locale, expected, notifySubscribers, actor, s.now().UTC())
}
func (s *Service) Unpublish(ctx context.Context, id, series, locale string, expected int64, actor string) (Issue, error) {
	edition, ok := NormalizeBulletinEdition(strings.TrimSpace(series), locale)
	if id == "" || !ok || expected <= 0 || actor == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.Unpublish(ctx, id, string(edition.Series), locale, expected, actor, s.now().UTC())
}
func (s *Service) DeleteIssue(ctx context.Context, id string, expected int64, actor string) error {
	if strings.TrimSpace(id) == "" || expected <= 0 || strings.TrimSpace(actor) == "" {
		return ErrInvalid
	}
	return s.repository.DeleteIssue(ctx, id, expected, actor, s.now().UTC())
}
func (s *Service) IssueRevisions(ctx context.Context, id string) ([]Revision, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalid
	}
	return s.repository.IssueRevisions(ctx, id)
}
func (s *Service) RestoreIssueRevision(ctx context.Context, id string, revision, expected int64, actor string) (Issue, error) {
	if strings.TrimSpace(id) == "" || revision < 1 || expected < 1 || strings.TrimSpace(actor) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.RestoreIssueRevision(ctx, id, revision, expected, actor, s.now().UTC())
}

type notificationRepository interface {
	BulletinNotificationState(context.Context, string, string) (*int, *time.Time, error)
	SubmitBulletinNotification(context.Context, string, string, string, string, time.Time) (NotificationSubmissionResult, error)
}

func (s *Service) PreviewNotification(ctx context.Context, id, series string) (NotificationPreview, error) {
	editionSeries := stringOrGeneral(series)
	if strings.TrimSpace(id) == "" || (editionSeries != "general" && editionSeries != "children") {
		return NotificationPreview{}, ErrInvalid
	}
	issue, err := s.repository.GetIssue(ctx, id)
	if err != nil {
		return NotificationPreview{}, err
	}
	preview, err := BuildNotification(issue, editionSeries)
	if err != nil {
		return NotificationPreview{}, err
	}
	if repository, ok := s.repository.(notificationRepository); ok {
		preview.LatestGeneration, preview.LatestAt, err = repository.BulletinNotificationState(ctx, id, editionSeries)
		preview.HasPreviousNotification = preview.LatestGeneration != nil
	}
	return preview, err
}

func (s *Service) SubmitNotification(ctx context.Context, id, series, actor, idempotencyKey string) (NotificationSubmissionResult, error) {
	editionSeries := stringOrGeneral(series)
	repository, ok := s.repository.(notificationRepository)
	if strings.TrimSpace(id) == "" || (editionSeries != "general" && editionSeries != "children") || strings.TrimSpace(actor) == "" || uuid.Validate(idempotencyKey) != nil || !ok {
		return NotificationSubmissionResult{}, ErrInvalid
	}
	return repository.SubmitBulletinNotification(ctx, id, editionSeries, actor, idempotencyKey, s.now().UTC())
}

type protectedRepository interface {
	BulletinEntitlement(context.Context, string, string) (string, error)
	ListProtected(context.Context, string, string, int, int, *int) (ProtectedPage, error)
	GetProtectedLatest(context.Context, string, string) (ProtectedBulletin, error)
	GetProtectedVersion(context.Context, string, string, string) (ProtectedBulletin, error)
}

func (s *Service) BulletinEntitlement(ctx context.Context, series, locale string) (string, error) {
	if strings.TrimSpace(series) == "" || strings.TrimSpace(locale) == "" {
		return "", ErrInvalid
	}
	repository, ok := s.repository.(protectedRepository)
	if !ok {
		return "", ErrNotFound
	}
	return repository.BulletinEntitlement(ctx, series, locale)
}

func (s *Service) ListProtected(ctx context.Context, series, locale string, page, pageSize int, issueNumber *int) (ProtectedPage, error) {
	page, pageSize = normalizePage(page, pageSize)
	repository, ok := s.repository.(protectedRepository)
	if !ok {
		return ProtectedPage{}, ErrNotFound
	}
	return repository.ListProtected(ctx, series, locale, page, pageSize, issueNumber)
}

func (s *Service) GetProtectedLatest(ctx context.Context, series, locale string) (ProtectedBulletin, error) {
	repository, ok := s.repository.(protectedRepository)
	if !ok {
		return ProtectedBulletin{}, ErrNotFound
	}
	return repository.GetProtectedLatest(ctx, series, locale)
}

func (s *Service) GetProtectedVersion(ctx context.Context, issueID, series, locale string) (ProtectedBulletin, error) {
	repository, ok := s.repository.(protectedRepository)
	if !ok {
		return ProtectedBulletin{}, ErrNotFound
	}
	return repository.GetProtectedVersion(ctx, issueID, series, locale)
}

func validDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}
func normalizePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}
