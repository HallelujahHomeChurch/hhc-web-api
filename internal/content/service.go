package content

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var contentSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

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
func (s *Service) ListContent(ctx context.Context, module Module, options ListOptions) (Page, error) {
	if !validModule(module) {
		return Page{}, ErrInvalid
	}
	options.Query = strings.TrimSpace(options.Query)
	if utf8.RuneCountInString(options.Query) > 200 || !validStatus(options.Status) {
		return Page{}, ErrInvalid
	}
	if options.Sort == "" {
		switch module {
		case ModuleNews:
			options.Sort = "displayDate"
		case ModuleHistory:
			options.Sort = "eventDate"
		default:
			options.Sort = "updatedAt"
		}
	}
	if options.Sort != "updatedAt" &&
		!(module == ModuleNews && options.Sort == "displayDate") &&
		!(module == ModuleHistory && options.Sort == "eventDate") {
		return Page{}, ErrInvalid
	}
	if options.Direction == "" {
		options.Direction = "desc"
	}
	if options.Direction != "asc" && options.Direction != "desc" {
		return Page{}, ErrInvalid
	}
	if options.Page > 10_000 {
		return Page{}, ErrInvalid
	}
	if options.Page < 1 {
		options.Page = 1
	}
	if options.PageSize < 1 || options.PageSize > 100 {
		options.PageSize = 20
	}
	return s.repository.ListContent(ctx, module, options)
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
func (s *Service) DeleteContent(ctx context.Context, module Module, id string, expected int64, actor string) error {
	if !validModule(module) || strings.TrimSpace(id) == "" || expected < 1 || strings.TrimSpace(actor) == "" {
		return ErrInvalid
	}
	return s.repository.DeleteContent(ctx, module, id, expected, actor, s.now().UTC())
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
func (s *Service) PublicNews(ctx context.Context, locale, slug string) (PublicItem, string, error) {
	if !validLocale(locale) || len(slug) > 120 || !contentSlug.MatchString(slug) {
		return PublicItem{}, "", ErrInvalid
	}
	return s.repository.PublicNews(ctx, locale, slug)
}

func validModule(module Module) bool {
	return module == ModuleNews || module == ModuleHistory || module == ModuleVideos
}
func validLocale(locale string) bool {
	return locale == "zh-Hant" || locale == "zh-Hans" || locale == "en"
}
func validStatus(status string) bool {
	switch status {
	case "", StatusDraft, StatusPublishing, StatusPublished, StatusPublishFailed,
		StatusUnpublishing, StatusUnpublishFailed, StatusUnpublished:
		return true
	default:
		return false
	}
}
func valid(module Module, input WriteInput) bool {
	if !validModule(module) || len(input.Translations) == 0 || len(input.Translations) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range input.Translations {
		if !validLocale(value.Locale) || seen[value.Locale] ||
			!validText(value.Title, 1, 200) ||
			!validText(value.Summary, 0, 500) ||
			!validText(value.Body, 0, 100_000) ||
			!validText(value.DateLabel, 0, 100) ||
			!validText(value.ImageAlt, 0, 300) {
			return false
		}
		seen[value.Locale] = true
	}
	switch module {
	case ModuleNews:
		return len(input.Slug) <= 120 && contentSlug.MatchString(input.Slug) && validDate(input.DisplayDate) && len(input.CoverAssetID) <= 200
	case ModuleHistory:
		return validHistoryDate(input.EventDate)
	case ModuleVideos:
		return youtubeID.MatchString(input.YouTubeVideoID)
	default:
		return false
	}
}
func publishable(item Item) bool {
	if !valid(item.Module, WriteInput{Slug: item.Slug, DisplayDate: item.DisplayDate, EventDate: item.EventDate, YouTubeVideoID: item.YouTubeVideoID, CoverAssetID: item.CoverAssetID, Translations: item.Translations}) {
		return false
	}
	for _, value := range item.Translations {
		switch item.Module {
		case ModuleNews:
			if value.Summary == "" && value.Body == "" {
				return false
			}
		case ModuleHistory:
			if value.Body == "" {
				return false
			}
		}
	}
	return item.Module != ModuleNews || item.CoverAssetID != ""
}
func validDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}
func validHistoryDate(value string) bool {
	if value == "" {
		return true
	}
	layout := map[int]string{4: "2006", 7: "2006-01", 10: "2006-01-02"}[len(value)]
	if layout == "" {
		return false
	}
	parsed, err := time.Parse(layout, value)
	return err == nil && parsed.Year() > 0 && parsed.Format(layout) == value
}
func validText(value string, min, max int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	return length >= min && length <= max
}
func normalize(input WriteInput) WriteInput {
	input.Slug = strings.TrimSpace(input.Slug)
	input.DisplayDate = strings.TrimSpace(input.DisplayDate)
	input.YouTubeVideoID = normalizeYouTubeVideoID(input.YouTubeVideoID)
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

func normalizeYouTubeVideoID(value string) string {
	value = strings.TrimSpace(value)
	if youtubeID.MatchString(value) {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return value
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host == "youtu.be" {
		return strings.Trim(strings.Split(strings.Trim(parsed.Path, "/"), "/")[0], " ")
	}
	if host != "youtube.com" && host != "m.youtube.com" && host != "youtube-nocookie.com" {
		return value
	}
	if id := parsed.Query().Get("v"); id != "" {
		return id
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed") {
		return parts[1]
	}
	return value
}
