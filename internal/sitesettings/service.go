package sitesettings

import (
	"context"
	"net"
	"net/url"
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

func (s *Service) Get(ctx context.Context) (Settings, error) {
	return s.repository.Get(ctx)
}

func (s *Service) Public(ctx context.Context, locale string) (PublicLayout, error) {
	if !supportedLocale(locale) {
		return PublicLayout{}, ErrInvalid
	}
	value, err := s.repository.Public(ctx, locale)
	if err != nil {
		return PublicLayout{}, err
	}
	if value.Locale != locale {
		return PublicLayout{}, ErrNotFound
	}
	return value, nil
}

func (s *Service) Save(ctx context.Context, input WriteInput, expected int64, actor string) (Settings, error) {
	input, ok := NormalizeWriteInput(input)
	if !ok || expected < 1 || strings.TrimSpace(actor) == "" {
		return Settings{}, ErrInvalid
	}
	return s.repository.Save(ctx, input, expected, actor, s.now().UTC())
}

func (s *Service) Publish(ctx context.Context, expected int64, actor string) (Settings, error) {
	if expected < 1 || strings.TrimSpace(actor) == "" {
		return Settings{}, ErrInvalid
	}
	current, err := s.repository.Get(ctx)
	if err != nil {
		return Settings{}, err
	}
	if current.Version != expected {
		return Settings{}, ErrPrecondition
	}
	if _, ok := NormalizeWriteInput(WriteInput{Locales: current.Locales, Links: current.Links}); !ok {
		return Settings{}, ErrNotPublishable
	}
	return s.repository.Publish(ctx, expected, actor, s.now().UTC())
}

func (s *Service) Unpublish(ctx context.Context, expected int64, actor string) (Settings, error) {
	if expected < 1 || strings.TrimSpace(actor) == "" {
		return Settings{}, ErrInvalid
	}
	return s.repository.Unpublish(ctx, expected, actor, s.now().UTC())
}

func (s *Service) Revisions(ctx context.Context) ([]Revision, error) {
	return s.repository.Revisions(ctx)
}

func (s *Service) Restore(ctx context.Context, revision, expected int64, actor string) (Settings, error) {
	if revision < 1 || expected < 1 || strings.TrimSpace(actor) == "" {
		return Settings{}, ErrInvalid
	}
	return s.repository.Restore(ctx, revision, expected, actor, s.now().UTC())
}

func NormalizeWriteInput(input WriteInput) (WriteInput, bool) {
	if len(input.Locales) != len(SupportedLocales) || !validExternalLinks(input.Links) {
		return WriteInput{}, false
	}
	seen := make(map[string]bool, len(input.Locales))
	for index := range input.Locales {
		locale := &input.Locales[index]
		locale.Locale = strings.TrimSpace(locale.Locale)
		if seen[locale.Locale] || !supportedLocale(locale.Locale) || !completeLocale(*locale) {
			return WriteInput{}, false
		}
		seen[locale.Locale] = true
		var ok bool
		locale.Header, ok = normalizeNav(locale.Header, map[string]string{
			"about": "/{locale}/about", "news": "/{locale}/news", "literature-ministry": "/{locale}/literature-ministry",
		})
		if !ok {
			return WriteInput{}, false
		}
		locale.Legal, ok = normalizeNav(locale.Legal, map[string]string{
			"privacy-policy": "/{locale}/privacy-policy", "terms-of-use": "/{locale}/terms-of-use",
		})
		if !ok {
			return WriteInput{}, false
		}
	}
	input.Links.ChurchYouTube = strings.TrimSpace(input.Links.ChurchYouTube)
	input.Links.ChurchFacebook = strings.TrimSpace(input.Links.ChurchFacebook)
	input.Links.MusicYouTube = strings.TrimSpace(input.Links.MusicYouTube)
	return input, true
}

func completeLocale(value LocaleSettings) bool {
	for _, field := range []string{value.SiteName, value.EnglishName, value.CopyrightHolder, value.AllRightsReserved, value.SEOTitleSuffix, value.SEODescriptionFallback} {
		if strings.TrimSpace(field) == "" {
			return false
		}
	}
	return true
}

func normalizeNav(items []NavItem, routes map[string]string) ([]NavItem, bool) {
	if len(items) != len(routes) {
		return nil, false
	}
	seen := make(map[string]bool, len(items))
	for index := range items {
		item := &items[index]
		item.Key = strings.TrimSpace(item.Key)
		route, ok := routes[item.Key]
		if !ok || seen[item.Key] || (item.Visible && strings.TrimSpace(item.Label) == "") {
			return nil, false
		}
		if item.Href != "" && item.Href != route {
			return nil, false
		}
		seen[item.Key] = true
		item.Label = strings.TrimSpace(item.Label)
		item.Href = route
	}
	return items, true
}

func validExternalLinks(links ExternalLinks) bool {
	return validExternalURL(links.ChurchYouTube) && validExternalURL(links.ChurchFacebook) && validExternalURL(links.MusicYouTube)
}

func validExternalURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || hasSASParameter(query) {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || hasBlockedSuffix(host) {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	return true
}

func hasSASParameter(query url.Values) bool {
	for key := range query {
		switch strings.ToLower(key) {
		case "sig", "sv", "se", "sp", "sr", "st", "spr", "sip", "ss", "srt", "skoid", "sktid", "skt", "ske", "sks", "skv":
			return true
		}
	}
	return false
}

func hasBlockedSuffix(host string) bool {
	for _, suffix := range []string{
		".localhost", ".local", ".internal", ".test", ".cluster.local", ".svc.cluster.local",
		".azurecontainerapps.io", ".azurewebsites.net", ".blob.core.windows.net", ".dfs.core.windows.net",
		".file.core.windows.net", ".queue.core.windows.net", ".table.core.windows.net", ".web.core.windows.net",
	} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func supportedLocale(locale string) bool {
	for _, supported := range SupportedLocales {
		if locale == supported {
			return true
		}
	}
	return false
}
