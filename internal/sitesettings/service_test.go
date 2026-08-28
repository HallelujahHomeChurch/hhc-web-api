package sitesettings

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSaveValidatesAndCanonicalizesSettings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WriteInput)
	}{
		{"missing locale", func(input *WriteInput) { input.Locales = input.Locales[:4] }},
		{"incomplete locale", func(input *WriteInput) { input.Locales[0].SiteName = "" }},
		{"duplicate header key", func(input *WriteInput) { input.Locales[0].Header[1].Key = "about" }},
		{"missing header key", func(input *WriteInput) { input.Locales[0].Header = input.Locales[0].Header[:2] }},
		{"unknown header key", func(input *WriteInput) { input.Locales[0].Header[0].Key = "contact" }},
		{"more than three header items", func(input *WriteInput) {
			input.Locales[0].Header = append(input.Locales[0].Header, input.Locales[0].Header[0])
		}},
		{"incomplete visible label", func(input *WriteInput) { input.Locales[0].Header[0].Label = "" }},
		{"unknown legal key", func(input *WriteInput) { input.Locales[0].Legal[0].Key = "cookies" }},
		{"missing legal key", func(input *WriteInput) { input.Locales[0].Legal = input.Locales[0].Legal[:1] }},
		{"changed fixed route", func(input *WriteInput) { input.Locales[0].Header[0].Href = "/admin" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &serviceRepository{settings: Settings{ID: SingletonID, Version: 1}}
			input := validWriteInput()
			test.mutate(&input)
			if _, err := NewService(repo, time.Now).Save(context.Background(), input, 1, "admin"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}

	repo := &serviceRepository{settings: Settings{ID: SingletonID, Version: 1}}
	input := validWriteInput()
	input.Locales[0].Header[0].Href = ""
	got, err := NewService(repo, time.Now).Save(context.Background(), input, 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if got.Locales[0].Header[0].Href != "/{locale}/about" {
		t.Fatalf("href=%q", got.Locales[0].Header[0].Href)
	}
}

func TestSaveRejectsUnsafeExternalLinks(t *testing.T) {
	tests := []string{
		"http://youtube.com/channel",
		"https://user@example.com/channel",
		"https://example.com/channel#admin",
		"https://127.0.0.1/channel",
		"https://10.0.0.1/channel",
		"https://[::1]/channel",
		"https://service.internal/channel",
		"https://account.blob.core.windows.net/container",
		"https://service.azurecontainerapps.io/channel",
		"https://youtube.com/channel?sv=2024-11-04&se=2026-08-29&sig=secret",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			repo := &serviceRepository{settings: Settings{ID: SingletonID, Version: 1}}
			input := validWriteInput()
			input.Links.ChurchYouTube = value
			if _, err := NewService(repo, time.Now).Save(context.Background(), input, 1, "admin"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestServiceRejectsStaleVersionAndInvalidPublishState(t *testing.T) {
	valid := validWriteInput()
	repo := &serviceRepository{settings: Settings{ID: SingletonID, Version: 2, Locales: valid.Locales, Links: valid.Links}}
	service := NewService(repo, time.Now)
	if _, err := service.Save(context.Background(), validWriteInput(), 1, "admin"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("save err=%v", err)
	}
	repo.settings.Locales = repo.settings.Locales[:4]
	if _, err := service.Publish(context.Background(), 2, "admin"); !errors.Is(err, ErrNotPublishable) {
		t.Fatalf("publish err=%v", err)
	}
}

func TestRestoreLeavesPublishedProjectionUntilRepublish(t *testing.T) {
	valid := validWriteInput()
	repo := &serviceRepository{settings: Settings{ID: SingletonID, Status: StatusPublished, Version: 3, Locales: valid.Locales, Links: valid.Links}}
	service := NewService(repo, func() time.Time { return time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC) })
	if _, err := service.Restore(context.Background(), 1, 3, "admin"); err != nil {
		t.Fatal(err)
	}
	if repo.projectionWrites != 0 || repo.settings.Status != StatusDraft {
		t.Fatalf("projection writes=%d status=%q", repo.projectionWrites, repo.settings.Status)
	}
	if _, err := service.Publish(context.Background(), repo.settings.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	if repo.projectionWrites != 1 || repo.settings.Status != StatusPublished {
		t.Fatalf("projection writes=%d status=%q", repo.projectionWrites, repo.settings.Status)
	}
	if _, err := service.Unpublish(context.Background(), repo.settings.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	if repo.projectionDeletes != 1 || repo.settings.Status != StatusUnpublished {
		t.Fatalf("projection deletes=%d status=%q", repo.projectionDeletes, repo.settings.Status)
	}
}

func validWriteInput() WriteInput {
	locales := make([]LocaleSettings, 0, len(SupportedLocales))
	for _, locale := range SupportedLocales {
		locales = append(locales, LocaleSettings{
			Locale: locale, SiteName: locale + " site", EnglishName: "Hallelujah Home Church",
			CopyrightHolder: locale + " holder", AllRightsReserved: locale + " rights",
			SEOTitleSuffix: locale + " title", SEODescriptionFallback: locale + " description",
			Header: []NavItem{
				{Key: "about", Label: locale + " about", Href: "/{locale}/about", Visible: true},
				{Key: "news", Label: locale + " news", Href: "/{locale}/news", Visible: true},
				{Key: "literature-ministry", Label: locale + " literature", Href: "/{locale}/literature-ministry", Visible: true},
			},
			Legal: []NavItem{
				{Key: "privacy-policy", Label: locale + " privacy", Href: "/{locale}/privacy-policy", Visible: true},
				{Key: "terms-of-use", Label: locale + " terms", Href: "/{locale}/terms-of-use", Visible: true},
			},
		})
	}
	return WriteInput{Locales: locales, Links: ExternalLinks{
		ChurchYouTube:  "https://youtube.com/@hhc33?si=public",
		ChurchFacebook: "https://www.facebook.com/www.alive.org.tw/",
		MusicYouTube:   "https://youtube.com/@gkpmusic777",
	}}
}

type serviceRepository struct {
	settings          Settings
	projectionWrites  int
	projectionDeletes int
}

func (r *serviceRepository) Get(context.Context) (Settings, error) { return r.settings, nil }
func (r *serviceRepository) Save(_ context.Context, input WriteInput, expected int64, _ string, _ time.Time) (Settings, error) {
	if r.settings.Version != expected {
		return Settings{}, ErrPrecondition
	}
	r.settings.Version++
	r.settings.Status = StatusDraft
	r.settings.Locales, r.settings.Links = input.Locales, input.Links
	return r.settings, nil
}
func (r *serviceRepository) Publish(_ context.Context, expected int64, _ string, _ time.Time) (Settings, error) {
	if r.settings.Version != expected {
		return Settings{}, ErrPrecondition
	}
	r.settings.Version++
	r.settings.Status = StatusPublished
	r.projectionWrites++
	return r.settings, nil
}
func (r *serviceRepository) Unpublish(_ context.Context, expected int64, _ string, _ time.Time) (Settings, error) {
	if r.settings.Version != expected {
		return Settings{}, ErrPrecondition
	}
	r.settings.Version++
	r.settings.Status = StatusUnpublished
	r.projectionDeletes++
	return r.settings, nil
}
func (r *serviceRepository) Revisions(context.Context) ([]Revision, error) { return nil, nil }
func (r *serviceRepository) Restore(_ context.Context, _ int64, expected int64, _ string, _ time.Time) (Settings, error) {
	if r.settings.Version != expected {
		return Settings{}, ErrPrecondition
	}
	r.settings.Version++
	r.settings.Status = StatusDraft
	return r.settings, nil
}
