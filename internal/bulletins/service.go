package bulletins

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	return &Service{repository: repository, now: now}
}

func (s *Service) CreateIssue(ctx context.Context, input CreateIssueInput, actor, idempotencyKey string) (Issue, error) {
	if !validDate(input.IssueDate) || strings.TrimSpace(actor) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.CreateIssue(ctx, input.IssueDate, actor, idempotencyKey, s.now().UTC())
}
func (s *Service) ListIssues(ctx context.Context, page, pageSize int, status string) (Page, error) {
	page, pageSize = normalizePage(page, pageSize)
	return s.repository.ListIssues(ctx, page, pageSize, status)
}
func (s *Service) GetIssue(ctx context.Context, id string) (Issue, error) {
	if strings.TrimSpace(id) == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.GetIssue(ctx, id)
}
func (s *Service) PutVersion(ctx context.Context, id string, expected int64, input PutVersionInput, actor string) (Issue, error) {
	input.Locale = strings.TrimSpace(input.Locale)
	input.Title = strings.TrimSpace(input.Title)
	input.PDFAssetID = strings.TrimSpace(input.PDFAssetID)
	input.PDFFileName = filepath.Base(strings.TrimSpace(input.PDFFileName))
	if id == "" || expected <= 0 || !validLocale(input.Locale) || input.Title == "" || len(input.Title) > 200 || input.PDFAssetID == "" || input.PDFFileName == "" || len(input.PDFFileName) > 255 || actor == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.PutVersion(ctx, id, expected, input, actor, s.now().UTC())
}
func (s *Service) Publish(ctx context.Context, id, locale string, expected int64, actor string) (Workflow, error) {
	if id == "" || !validLocale(locale) || expected <= 0 || actor == "" {
		return Workflow{}, ErrInvalid
	}
	return s.repository.StartPublish(ctx, id, locale, expected, actor, s.now().UTC())
}
func (s *Service) Unpublish(ctx context.Context, id, locale string, expected int64, actor string) (Issue, error) {
	if id == "" || !validLocale(locale) || expected <= 0 || actor == "" {
		return Issue{}, ErrInvalid
	}
	return s.repository.Unpublish(ctx, id, locale, expected, actor, s.now().UTC())
}
func (s *Service) GetPublicLatest(ctx context.Context, locale string) (PublicBulletin, error) {
	if !validLocale(locale) {
		return PublicBulletin{}, ErrInvalid
	}
	return s.repository.GetPublicLatest(ctx, locale)
}
func (s *Service) GetPublicByDate(ctx context.Context, date, locale string) (PublicBulletin, error) {
	if !validDate(date) || !validLocale(locale) {
		return PublicBulletin{}, ErrInvalid
	}
	return s.repository.GetPublicByDate(ctx, date, locale)
}
func (s *Service) ListPublic(ctx context.Context, page, pageSize int) (PublicPage, error) {
	page, pageSize = normalizePage(page, pageSize)
	return s.repository.ListPublic(ctx, page, pageSize)
}

func validLocale(value string) bool { return value == "zh-Hant" || value == "zh-Hans" || value == "en" }
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
