package content

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var contentSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var locationKey = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	return &Service{repository: repository, now: now}
}

func (s *Service) CreateContent(ctx context.Context, module Module, input WriteInput, actor, key string) (Item, error) {
	if module == ModulePages {
		return Item{}, ErrMethodNotAllowed
	}
	input = normalize(module, input)
	if module == ModuleNews {
		input = normalizeNews(input, key)
	}
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
	input = normalize(module, input)
	if !validModule(module) || expected < 1 {
		return Item{}, ErrInvalid
	}
	current, err := s.repository.GetContent(ctx, module, id)
	if err != nil {
		return Item{}, err
	}
	if module == ModuleNews {
		if input.Slug == "" {
			input.Slug = current.Slug
		}
		input = normalizeNews(input, "")
	}
	if module == ModuleLocations && input.LocationKey != current.LocationKey {
		return Item{}, ErrInvalid
	}
	if module == ModulePages && (input.PageKey != current.PageKey || input.PageTemplate != current.PageTemplate || input.RoutePath != current.RoutePath) {
		return Item{}, ErrInvalid
	}
	if !valid(module, input) || !validDeleteLocales(input.DeleteLocales) {
		return Item{}, ErrInvalid
	}
	if localeSetMismatch(current.Translations, input.Translations, input.DeleteLocales) {
		return Item{}, ErrLocaleSetMismatch
	}
	return s.repository.UpdateContent(ctx, module, id, expected, input, actor, s.now().UTC())
}
func (s *Service) PublishContent(ctx context.Context, module Module, id string, expected int64, actor string) (Item, error) {
	if _, ok := PageGroupForChild(module); ok {
		return Item{}, ErrMethodNotAllowed
	}
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
	if _, ok := PageGroupForChild(module); ok {
		return Item{}, ErrMethodNotAllowed
	}
	return s.repository.UnpublishContent(ctx, module, id, expected, actor, s.now().UTC())
}
func (s *Service) ContentRevisions(ctx context.Context, module Module, id string) ([]Revision, error) {
	return s.repository.ContentRevisions(ctx, module, id)
}
func (s *Service) RestoreContent(ctx context.Context, module Module, id string, revision, expected int64, actor string) (Item, error) {
	if _, ok := PageGroupForChild(module); ok {
		return Item{}, ErrMethodNotAllowed
	}
	if !validModule(module) || revision < 1 || expected < 1 {
		return Item{}, ErrInvalid
	}
	current, err := s.repository.GetContent(ctx, module, id)
	if err != nil {
		return Item{}, err
	}
	value, err := s.repository.ContentRevision(ctx, module, id, revision)
	if err != nil {
		return Item{}, err
	}
	input := WriteInput{AuthorName: value.Snapshot.AuthorName, Slug: value.Snapshot.Slug, DisplayDate: value.Snapshot.DisplayDate, EventDate: value.Snapshot.EventDate, YouTubeVideoID: value.Snapshot.YouTubeVideoID, CoverAssetID: value.Snapshot.CoverAssetID, HomeCoverAssetID: value.Snapshot.HomeCoverAssetID, DetailLayout: value.Snapshot.DetailLayout, Featured: value.Snapshot.Featured, HomeEligible: value.Snapshot.HomeEligible, LocationKey: value.Snapshot.LocationKey, MapHref: value.Snapshot.MapHref, SortOrder: value.Snapshot.SortOrder, PageKey: value.Snapshot.PageKey, PageTemplate: value.Snapshot.PageTemplate, RoutePath: value.Snapshot.RoutePath, Indexable: value.Snapshot.Indexable, BannerAssetID: value.Snapshot.BannerAssetID, Links: value.Snapshot.Links, Locations: value.Snapshot.Locations, Translations: preserveMissingLocales(value.Snapshot.Translations, current.Translations)}
	if module == ModuleLocations && input.LocationKey != current.LocationKey {
		return Item{}, ErrInvalid
	}
	if module == ModulePages && (input.PageKey != current.PageKey || input.RoutePath != current.RoutePath || (input.PageTemplate != current.PageTemplate && !(input.PageKey == "home" && (input.PageTemplate == "home.v1" || input.PageTemplate == "home.v2") && (current.PageTemplate == "home.v1" || current.PageTemplate == "home.v2")))) {
		return Item{}, ErrInvalid
	}
	input = normalize(module, input)
	if !valid(module, input) || !validDeleteLocales(input.DeleteLocales) {
		return Item{}, ErrInvalid
	}
	return s.repository.RestoreContent(ctx, module, id, expected, input, actor, s.now().UTC())
}
func (s *Service) DeleteContent(ctx context.Context, module Module, id string, expected int64, actor string) error {
	if module == ModulePages {
		return ErrMethodNotAllowed
	}
	if !validModule(module) || strings.TrimSpace(id) == "" || expected < 1 || strings.TrimSpace(actor) == "" {
		return ErrInvalid
	}
	return s.repository.DeleteContent(ctx, module, id, expected, actor, s.now().UTC())
}
func (s *Service) PublicContent(ctx context.Context, module Module, locale string, page, pageSize int) (PublicPage, error) {
	if !validModule(module) || !validLocale(locale) {
		return PublicPage{}, ErrInvalid
	}
	if page > 10_000 {
		return PublicPage{}, ErrInvalid
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return s.repository.PublicContent(ctx, module, locale, page, pageSize)
}
func (s *Service) PublicNews(ctx context.Context, locale, slug string) (PublicItem, string, error) {
	if !validLocale(locale) || len(slug) > 120 || !contentSlug.MatchString(slug) {
		return PublicItem{}, "", ErrInvalid
	}
	return s.repository.PublicNews(ctx, locale, slug)
}

func (s *Service) PublicLocations(ctx context.Context, locale string) ([]PublicLocation, error) {
	if !validLocale(locale) {
		return nil, ErrInvalid
	}
	return s.repository.PublicLocations(ctx, locale)
}

func (s *Service) PublicEditorialPage(ctx context.Context, key, locale string) (PublicEditorialPage, string, error) {
	if _, _, ok := PageDefinition(key); !ok || !validLocale(locale) {
		return PublicEditorialPage{}, "", ErrNotFound
	}
	return s.repository.PublicEditorialPage(ctx, key, locale)
}

func validModule(module Module) bool {
	return module == ModuleNews || module == ModuleHistory || module == ModuleVideos || module == ModuleLocations || module == ModulePages
}
func validLocale(locale string) bool {
	switch locale {
	case "zh-Hant", "zh-Hans", "en", "ja", "ko":
		return true
	default:
		return false
	}
}
func validStatus(status string) bool {
	switch status {
	case "", StatusDraft, StatusPublishing, StatusPublished, StatusPublishFailed,
		StatusUnpublishing, StatusUnpublishFailed, StatusUnpublished, StatusPendingRemoval:
		return true
	default:
		return false
	}
}
func valid(module Module, input WriteInput) bool {
	if !validModule(module) || len(input.Translations) == 0 || len(input.Translations) > 5 {
		return false
	}
	if module != ModuleNews && input.AuthorName != "" {
		return false
	}
	if module != ModuleLocations && (input.LocationKey != "" || input.MapHref != "" || input.SortOrder != 0) {
		return false
	}
	if module != ModulePages && (input.PageKey != "" || input.PageTemplate != "" || input.RoutePath != "" || input.Indexable) {
		return false
	}
	if module != ModulePages && (input.BannerAssetID != "" || input.Links != (HomeLinks{}) || len(input.Locations) != 0) {
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
		if module == ModulePages {
			if value.Body != "" || value.DateLabel != "" || value.ImageAlt != "" || ValidatePagePayload(input.PageKey, value.BodyJSON) != nil {
				return false
			}
		} else if len(value.BodyJSON) != 0 {
			return false
		}
		seen[value.Locale] = true
	}
	switch module {
	case ModuleNews:
		return validText(input.AuthorName, 0, 200) && len(input.Slug) <= 120 && contentSlug.MatchString(input.Slug) && validDate(input.DisplayDate) && len(input.CoverAssetID) <= 200 && len(input.HomeCoverAssetID) <= 200 && validNewsDetailLayout(input.DetailLayout)
	case ModuleHistory:
		return validHistoryDate(input.EventDate)
	case ModuleVideos:
		return youtubeID.MatchString(input.YouTubeVideoID)
	case ModuleLocations:
		return ValidateLocation(input)
	case ModulePages:
		if ValidatePageDefinition(input.PageKey, input.PageTemplate, input.RoutePath) != nil ||
			input.AuthorName != "" || input.Slug != "" || input.DisplayDate != "" || input.EventDate != "" || input.YouTubeVideoID != "" || input.CoverAssetID != "" || input.HomeCoverAssetID != "" || input.DetailLayout != "" || input.Featured || input.HomeEligible {
			return false
		}
		if input.PageTemplate == "home.v2" {
			return validHomeV2(input)
		}
		return input.BannerAssetID == "" && input.Links == (HomeLinks{}) && len(input.Locations) == 0 &&
			input.AuthorName == "" && input.Slug == "" && input.DisplayDate == "" && input.EventDate == "" && input.YouTubeVideoID == "" && input.CoverAssetID == "" && input.HomeCoverAssetID == "" && input.DetailLayout == "" && !input.Featured && !input.HomeEligible
	default:
		return false
	}
}
func publishable(item Item) bool {
	if !valid(item.Module, WriteInput{AuthorName: item.AuthorName, Slug: item.Slug, DisplayDate: item.DisplayDate, EventDate: item.EventDate, YouTubeVideoID: item.YouTubeVideoID, CoverAssetID: item.CoverAssetID, HomeCoverAssetID: item.HomeCoverAssetID, DetailLayout: item.DetailLayout, LocationKey: item.LocationKey, MapHref: item.MapHref, SortOrder: item.SortOrder, PageKey: item.PageKey, PageTemplate: item.PageTemplate, RoutePath: item.RoutePath, Indexable: item.Indexable, BannerAssetID: item.BannerAssetID, Links: item.Links, Locations: item.Locations, Translations: item.Translations}) {
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
		case ModuleLocations:
			if len(item.Translations) != 5 || value.Body == "" {
				return false
			}
		case ModulePages:
			if len(item.Translations) != 5 {
				return false
			}
		}
	}
	if item.Module == ModulePages && item.PageTemplate == "home.v2" && item.BannerAssetID == "" {
		return false
	}
	return true
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
func normalize(module Module, input WriteInput) WriteInput {
	input.AuthorName = strings.TrimSpace(input.AuthorName)
	input.Slug = strings.TrimSpace(input.Slug)
	input.DisplayDate = strings.TrimSpace(input.DisplayDate)
	input.EventDate = strings.TrimSpace(input.EventDate)
	input.YouTubeVideoID = normalizeYouTubeVideoID(input.YouTubeVideoID)
	input.CoverAssetID = strings.TrimSpace(input.CoverAssetID)
	input.HomeCoverAssetID = strings.TrimSpace(input.HomeCoverAssetID)
	input.DetailLayout = strings.TrimSpace(input.DetailLayout)
	if module != ModuleLocations && module != ModulePages && input.DetailLayout == "" {
		input.DetailLayout = "top"
	}
	input.LocationKey = strings.TrimSpace(input.LocationKey)
	input.MapHref = strings.TrimSpace(input.MapHref)
	input.BannerAssetID = strings.TrimSpace(input.BannerAssetID)
	input.Links.ChurchYouTube = strings.TrimSpace(input.Links.ChurchYouTube)
	input.Links.ChurchFacebook = strings.TrimSpace(input.Links.ChurchFacebook)
	input.Links.MusicYouTube = strings.TrimSpace(input.Links.MusicYouTube)
	input.Locations = append([]HomeLocation(nil), input.Locations...)
	for index := range input.Locations {
		location := &input.Locations[index]
		location.Key = strings.TrimSpace(location.Key)
		location.MapHref = strings.TrimSpace(location.MapHref)
		location.Translations = append([]HomeLocationTranslation(nil), location.Translations...)
		for translationIndex := range location.Translations {
			translation := &location.Translations[translationIndex]
			translation.Locale = strings.TrimSpace(translation.Locale)
			translation.Name = strings.TrimSpace(translation.Name)
			translation.Address = strings.TrimSpace(translation.Address)
		}
	}
	sort.Slice(input.Locations, func(i, j int) bool { return input.Locations[i].SortOrder < input.Locations[j].SortOrder })
	for index := range input.Translations {
		input.Translations[index].Locale = strings.TrimSpace(input.Translations[index].Locale)
		input.Translations[index].Title = strings.TrimSpace(input.Translations[index].Title)
		input.Translations[index].Body = strings.TrimSpace(input.Translations[index].Body)
		if module != ModuleLocations {
			input.Translations[index].Summary = strings.TrimSpace(input.Translations[index].Summary)
			input.Translations[index].DateLabel = strings.TrimSpace(input.Translations[index].DateLabel)
			input.Translations[index].ImageAlt = strings.TrimSpace(input.Translations[index].ImageAlt)
		}
		if module == ModulePages {
			title, summary, err := PagePayloadMetadata(input.PageKey, input.Translations[index].BodyJSON)
			if err == nil {
				input.Translations[index].Title = title
				input.Translations[index].Summary = summary
			}
		}
	}
	for index := range input.DeleteLocales {
		input.DeleteLocales[index] = strings.TrimSpace(input.DeleteLocales[index])
	}
	return input
}

func validHomeV2(input WriteInput) bool {
	if len(input.Translations) != 5 || input.DeleteLocales != nil || len(input.BannerAssetID) > 200 ||
		!sitesettings.ValidExternalURL(input.Links.ChurchYouTube) || !sitesettings.ValidExternalURL(input.Links.ChurchFacebook) || !sitesettings.ValidExternalURL(input.Links.MusicYouTube) || len(input.Locations) > 100 {
		return false
	}
	pageLocales := make(map[string]bool, 5)
	for _, translation := range input.Translations {
		pageLocales[translation.Locale] = true
	}
	if len(pageLocales) != 5 {
		return false
	}
	keys := make(map[string]bool, len(input.Locations))
	orders := make(map[int]bool, len(input.Locations))
	for _, location := range input.Locations {
		if len(location.Key) > 120 || !locationKey.MatchString(location.Key) || location.SortOrder < 0 || keys[location.Key] || orders[location.SortOrder] || !validPublicLocationURL(location.MapHref) || len(location.Translations) != 5 {
			return false
		}
		keys[location.Key], orders[location.SortOrder] = true, true
		locales := make(map[string]bool, 5)
		for _, translation := range location.Translations {
			if !validLocale(translation.Locale) || locales[translation.Locale] || !validText(translation.Name, 1, 200) || !validText(translation.Address, 1, 500) {
				return false
			}
			locales[translation.Locale] = true
		}
	}
	return true
}

func validDeleteLocales(locales []string) bool {
	seen := map[string]bool{}
	for _, locale := range locales {
		if !validLocale(locale) || seen[locale] {
			return false
		}
		seen[locale] = true
	}
	return true
}

func localeSetMismatch(current, submitted []Translation, deleted []string) bool {
	seen := make(map[string]bool, len(submitted))
	for _, translation := range submitted {
		seen[translation.Locale] = true
	}
	for _, locale := range deleted {
		if seen[locale] {
			return true
		}
		seen[locale] = true
	}
	for _, translation := range current {
		if !seen[translation.Locale] {
			return true
		}
	}
	return false
}

func preserveMissingLocales(snapshot, current []Translation) []Translation {
	seen := make(map[string]bool, len(snapshot))
	for _, translation := range snapshot {
		seen[translation.Locale] = true
	}
	for _, translation := range current {
		if !seen[translation.Locale] {
			snapshot = append(snapshot, translation)
		}
	}
	return snapshot
}

func validNewsDetailLayout(value string) bool {
	return value == "" || value == "top" || value == "left" || value == "right"
}

func normalizeNews(input WriteInput, slugSeed string) WriteInput {
	if input.Slug == "" && slugSeed != "" {
		digest := sha256.Sum256([]byte(slugSeed))
		input.Slug = "news-" + strings.ReplaceAll(input.DisplayDate, "-", "") + "-" + fmt.Sprintf("%x", digest[:4])
	}
	for index := range input.Translations {
		if input.Translations[index].Summary == "" {
			input.Translations[index].Summary = excerpt(input.Translations[index].Body, 160)
		}
	}
	return input
}

// ValidateLocation is shared by API writes and the content importer.
func ValidateLocation(input WriteInput) bool {
	if len(input.Translations) == 0 || len(input.Translations) > 5 || len(input.LocationKey) > 120 || !locationKey.MatchString(input.LocationKey) || input.SortOrder < 0 || !validPublicLocationURL(input.MapHref) ||
		input.Slug != "" || input.DisplayDate != "" || input.EventDate != "" || input.YouTubeVideoID != "" ||
		input.CoverAssetID != "" || input.HomeCoverAssetID != "" || input.DetailLayout != "" || input.Featured || input.HomeEligible {
		return false
	}
	for _, translation := range input.Translations {
		if strings.TrimSpace(translation.Title) == "" || strings.TrimSpace(translation.Body) == "" || translation.Summary != "" || translation.DateLabel != "" || translation.ImageAlt != "" {
			return false
		}
	}
	return true
}

func validPublicLocationURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || host == "api-gateway" || host == "account-api" || host == "asset-api" || host == "engagement-api" || host == "hhc-web-api" || host == "notification-api" ||
		strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".blob.core.windows.net") || strings.HasSuffix(host, ".dfs.core.windows.net") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return false
		}
	} else if numericIPv4Host(host) || !strings.Contains(host, ".") || strings.HasSuffix(host, ".svc") {
		return false
	}
	canonicalPath := strings.ToLower(path.Clean(parsed.Path))
	if canonicalPath == "/api" || strings.HasPrefix(canonicalPath, "/api/") || canonicalPath == "/priv" || strings.HasPrefix(canonicalPath, "/priv/") {
		return false
	}
	for key := range parsed.Query() {
		switch strings.ToLower(key) {
		case "sig", "sv", "se", "sp", "sr", "st", "skoid", "sktid", "skt", "ske", "sks", "skv":
			return false
		}
	}
	return true
}

func numericIPv4Host(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if strings.HasPrefix(part, "0x") {
			part = part[2:]
			if part == "" {
				return false
			}
			for _, rune := range part {
				if !(rune >= '0' && rune <= '9') && !(rune >= 'a' && rune <= 'f') {
					return false
				}
			}
			continue
		}
		if part == "" {
			return false
		}
		for _, rune := range part {
			if rune < '0' || rune > '9' {
				return false
			}
		}
	}
	return true
}

func excerpt(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
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
