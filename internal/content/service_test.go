package content

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceValidatesTypedModules(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, func() time.Time { return time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC) })

	tests := []struct {
		name   string
		module Module
		input  WriteInput
	}{
		{name: "news needs display date", module: ModuleNews, input: WriteInput{Slug: "announcement", Translations: translations()}},
		{name: "history needs ordering", module: ModuleHistory, input: WriteInput{Translations: translations()}},
		{name: "video needs youtube id", module: ModuleVideos, input: WriteInput{Translations: translations()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.CreateContent(context.Background(), test.module, test.input, "user-1", "key-1"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestServiceRejectsMalformedAndOversizedContent(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	tests := []WriteInput{
		{Slug: "Invalid Slug", DisplayDate: "2026-07-13", Translations: translations()},
		{Slug: "announcement", DisplayDate: "2026-02-30", Translations: translations()},
		{Slug: strings.Repeat("a", 121), DisplayDate: "2026-07-13", Translations: translations()},
		{Slug: "announcement", DisplayDate: "2026-07-13", Translations: []Translation{{Locale: "en", Title: strings.Repeat("a", 201)}}},
	}
	for index, input := range tests {
		if _, err := service.CreateContent(context.Background(), ModuleNews, input, "user-1", "key-1"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d err=%v", index, err)
		}
	}
}

func TestPublishRequiresPublicContent(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)

	repo.item = Item{
		ID: "news-1", Module: ModuleNews, Status: StatusDraft, Version: 1,
		Slug: "announcement", DisplayDate: "2026-07-13", CoverAssetID: "asset-1",
		Translations: []Translation{{Locale: "zh-Hant", Title: "標題"}},
	}
	if _, err := service.PublishContent(context.Background(), ModuleNews, repo.item.ID, 1, "user-1"); !errors.Is(err, ErrNotPublishable) {
		t.Fatalf("news error=%v", err)
	}

	repo.item = Item{
		ID: "history-1", Module: ModuleHistory, Status: StatusDraft, Version: 1, SortOrder: 1,
		Translations: []Translation{{Locale: "zh-Hant", Title: "標題"}},
	}
	if _, err := service.PublishContent(context.Background(), ModuleHistory, repo.item.ID, 1, "user-1"); !errors.Is(err, ErrNotPublishable) {
		t.Fatalf("history error=%v", err)
	}

	repo.item.Translations[0].DateLabel = "2026"
	repo.item.Translations[0].Body = "Milestone"
	if _, err := service.PublishContent(context.Background(), ModuleHistory, repo.item.ID, 1, "user-1"); err != nil {
		t.Fatal(err)
	}
}

func TestNewsPublishRequiresCleanCoverReference(t *testing.T) {
	repo := &serviceRepository{item: Item{ID: "item-1", Module: ModuleNews, Status: StatusDraft, Version: 2, Slug: "announcement", DisplayDate: "2026-07-13", Translations: translations()}}
	service := NewService(repo, time.Now)

	if _, err := service.PublishContent(context.Background(), ModuleNews, "item-1", 2, "user-1"); !errors.Is(err, ErrNotPublishable) {
		t.Fatalf("err=%v", err)
	}
	repo.item.CoverAssetID = "asset-1"
	if _, err := service.PublishContent(context.Background(), ModuleNews, "item-1", 2, "user-1"); err != nil {
		t.Fatal(err)
	}
}

func TestServiceAcceptsTypedDrafts(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)

	inputs := map[Module]WriteInput{
		ModuleNews:    {Slug: "announcement", DisplayDate: "2026-07-13", Featured: true, Translations: translations()},
		ModuleHistory: {SortOrder: 10, Translations: translations()},
		ModuleVideos:  {YouTubeVideoID: "K3ckFWeSQ-k", HomeEligible: true, Translations: translations()},
	}
	for module, input := range inputs {
		if _, err := service.CreateContent(context.Background(), module, input, "user-1", string(module)+"-1"); err != nil {
			t.Fatalf("module=%s err=%v", module, err)
		}
	}
}

func TestServiceArchivesAndRestoresDraftContent(t *testing.T) {
	repo := &serviceRepository{
		item: Item{ID: "item-1", Module: ModuleVideos, Status: StatusDraft, Version: 2},
	}
	service := NewService(repo, func() time.Time {
		return time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	})

	archived, err := service.ArchiveContent(context.Background(), ModuleVideos, repo.item.ID, 2, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != StatusArchived || archived.Version != 3 {
		t.Fatalf("archived=%#v", archived)
	}

	restored, err := service.RestoreArchivedContent(context.Background(), ModuleVideos, repo.item.ID, 3, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != StatusDraft || restored.Version != 4 {
		t.Fatalf("restored=%#v", restored)
	}
}

func TestServiceValidatesAndNormalizesContentList(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)

	if _, err := service.ListContent(context.Background(), ModuleNews, ListOptions{
		Query: strings.Repeat("a", 201),
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized query err=%v", err)
	}
	if _, err := service.ListContent(context.Background(), ModuleNews, ListOptions{
		Status: "unknown",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid status err=%v", err)
	}
	if _, err := service.ListContent(context.Background(), ModuleVideos, ListOptions{
		Sort: "displayDate",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid module sort err=%v", err)
	}

	if _, err := service.ListContent(context.Background(), ModuleHistory, ListOptions{
		Query: "  milestone  ",
	}); err != nil {
		t.Fatal(err)
	}
	if repo.listOptions.Query != "milestone" || repo.listOptions.Page != 1 ||
		repo.listOptions.PageSize != 20 || repo.listOptions.Sort != "sortOrder" ||
		repo.listOptions.Direction != "asc" {
		t.Fatalf("options=%#v", repo.listOptions)
	}
}

func TestServiceRejectsInvalidPublicNewsSlug(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	if _, _, err := service.PublicNews(context.Background(), "zh-Hant", "Invalid Slug"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func translations() []Translation {
	return []Translation{{Locale: "zh-Hant", Title: "標題", Summary: "摘要", Body: "內容", DateLabel: "2026年"}}
}

type serviceRepository struct {
	item        Item
	listOptions ListOptions
}

func (r *serviceRepository) CreateContent(_ context.Context, module Module, input WriteInput, actor, key string, now time.Time) (Item, error) {
	r.item = Item{ID: "item-1", Module: module, Status: StatusDraft, Version: 1, Slug: input.Slug, DisplayDate: input.DisplayDate, SortOrder: input.SortOrder, YouTubeVideoID: input.YouTubeVideoID, CoverAssetID: input.CoverAssetID, Featured: input.Featured, HomeEligible: input.HomeEligible, Translations: input.Translations}
	return r.item, nil
}
func (r *serviceRepository) ListContent(_ context.Context, _ Module, options ListOptions) (Page, error) {
	r.listOptions = options
	return Page{}, nil
}
func (r *serviceRepository) GetContent(context.Context, Module, string) (Item, error) {
	return r.item, nil
}
func (r *serviceRepository) UpdateContent(context.Context, Module, string, int64, WriteInput, string, time.Time) (Item, error) {
	return r.item, nil
}
func (r *serviceRepository) PublishContent(_ context.Context, _ Module, _ string, _ int64, _ string, _ time.Time) (Item, error) {
	r.item.Status = StatusPublished
	return r.item, nil
}
func (r *serviceRepository) UnpublishContent(context.Context, Module, string, int64, string, time.Time) (Item, error) {
	return r.item, nil
}
func (r *serviceRepository) ContentRevisions(context.Context, Module, string) ([]Revision, error) {
	return nil, nil
}
func (r *serviceRepository) RestoreContent(context.Context, Module, string, int64, int64, string, time.Time) (Item, error) {
	return r.item, nil
}
func (r *serviceRepository) ArchiveContent(context.Context, Module, string, int64, string, time.Time) (Item, error) {
	r.item.Status = StatusArchived
	r.item.Version++
	return r.item, nil
}
func (r *serviceRepository) RestoreArchivedContent(context.Context, Module, string, int64, string, time.Time) (Item, error) {
	r.item.Status = StatusDraft
	r.item.Version++
	return r.item, nil
}
func (r *serviceRepository) PublicContent(context.Context, Module, string, int) ([]PublicItem, error) {
	return nil, nil
}
func (r *serviceRepository) PublicNews(context.Context, string, string) (PublicItem, string, error) {
	return PublicItem{}, "", nil
}
