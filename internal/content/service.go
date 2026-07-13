package content

import (
	"context"
	"regexp"
	"strings"
	"time"
)

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	return &Service{repository: repository, now: now}
}

func (s *Service) CreateContent(ctx context.Context, module Module, input WriteInput, actor, key string) (Item, error) {
	input = normalize(input)
	if !valid(module, input) || strings.TrimSpace(key) == "" {
		return Item{}, ErrInvalid
	}
	return s.repository.CreateContent(ctx, module, input, actor, key, s.now().UTC())
}
func (s *Service) ListContent(ctx context.Context, module Module, page, size int, status string) (Page, error) {
	if !validModule(module) {
		return Page{}, ErrInvalid
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return s.repository.ListContent(ctx, module, page, size, status)
}
func (s *Service) GetContent(ctx context.Context, module Module, id string) (Item, error) {
	if !validModule(module) {
		return Item{}, ErrInvalid
	}
	return s.repository.GetContent(ctx, module, id)
}
func (s *Service) UpdateContent(ctx context.Context, module Module, id string, expected int64, input WriteInput, actor string) (Item, error) {
	input = normalize(input)
	if expected < 1 || !valid(module, input) {
		return Item{}, ErrInvalid
	}
	return s.repository.UpdateContent(ctx, module, id, expected, input, actor, s.now().UTC())
}
func (s *Service) PublishContent(ctx context.Context, module Module, id string, expected int64, actor string) (Item, error) {
	item, err := s.repository.GetContent(ctx, module, id)
	if err != nil {
		return Item{}, err
	}
	if item.Version != expected {
		return Item{}, ErrPrecondition
	}
	if !publishable(item) {
		return Item{}, ErrNotPublishable
	}
	return s.repository.PublishContent(ctx, module, id, expected, actor, s.now().UTC())
}
func (s *Service) UnpublishContent(ctx context.Context, module Module, id string, expected int64, actor string) (Item, error) {
	return s.repository.UnpublishContent(ctx, module, id, expected, actor, s.now().UTC())
}
func (s *Service) ContentRevisions(ctx context.Context, module Module, id string) ([]Revision, error) {
	return s.repository.ContentRevisions(ctx, module, id)
}
func (s *Service) RestoreContent(ctx context.Context, module Module, id string, revision, expected int64, actor string) (Item, error) {
	if revision < 1 || expected < 1 {
		return Item{}, ErrInvalid
	}
	return s.repository.RestoreContent(ctx, module, id, revision, expected, actor, s.now().UTC())
}
func (s *Service) PublicContent(ctx context.Context, module Module, locale string, limit int) ([]PublicItem, error) {
	if !validModule(module) || !validLocale(locale) {
		return nil, ErrInvalid
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return s.repository.PublicContent(ctx, module, locale, limit)
}

func validModule(module Module) bool {
	return module == ModuleNews || module == ModuleHistory || module == ModuleVideos
}
func validLocale(locale string) bool {
	return locale == "zh-Hant" || locale == "zh-Hans" || locale == "en"
}
func valid(module Module, input WriteInput) bool {
	if !validModule(module) || len(input.Translations) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range input.Translations {
		if !validLocale(value.Locale) || seen[value.Locale] || value.Title == "" {
			return false
		}
		seen[value.Locale] = true
	}
	switch module {
	case ModuleNews:
		return input.Slug != "" && input.DisplayDate != ""
	case ModuleHistory:
		return input.SortOrder > 0
	case ModuleVideos:
		return youtubeID.MatchString(input.YouTubeVideoID)
	default:
		return false
	}
}
func publishable(item Item) bool {
	if !valid(item.Module, WriteInput{Slug: item.Slug, DisplayDate: item.DisplayDate, SortOrder: item.SortOrder, YouTubeVideoID: item.YouTubeVideoID, CoverAssetID: item.CoverAssetID, Translations: item.Translations}) {
		return false
	}
	return item.Module != ModuleNews || item.CoverAssetID != ""
}
func normalize(input WriteInput) WriteInput {
	input.Slug = strings.TrimSpace(input.Slug)
	input.DisplayDate = strings.TrimSpace(input.DisplayDate)
	input.YouTubeVideoID = strings.TrimSpace(input.YouTubeVideoID)
	input.CoverAssetID = strings.TrimSpace(input.CoverAssetID)
	for index := range input.Translations {
		input.Translations[index].Locale = strings.TrimSpace(input.Translations[index].Locale)
		input.Translations[index].Title = strings.TrimSpace(input.Translations[index].Title)
		input.Translations[index].Summary = strings.TrimSpace(input.Translations[index].Summary)
		input.Translations[index].Body = strings.TrimSpace(input.Translations[index].Body)
		input.Translations[index].DateLabel = strings.TrimSpace(input.Translations[index].DateLabel)
		input.Translations[index].ImageAlt = strings.TrimSpace(input.Translations[index].ImageAlt)
	}
	return input
}
