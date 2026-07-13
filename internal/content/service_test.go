package content

import (
	"context"
	"errors"
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

func translations() []Translation {
	return []Translation{{Locale: "zh-Hant", Title: "標題", Summary: "摘要", Body: "內容", DateLabel: "2026年"}}
}

type serviceRepository struct{ item Item }

func (r *serviceRepository) CreateContent(_ context.Context, module Module, input WriteInput, actor, key string, now time.Time) (Item, error) {
	r.item = Item{ID: "item-1", Module: module, Status: StatusDraft, Version: 1, Slug: input.Slug, DisplayDate: input.DisplayDate, SortOrder: input.SortOrder, YouTubeVideoID: input.YouTubeVideoID, CoverAssetID: input.CoverAssetID, Featured: input.Featured, HomeEligible: input.HomeEligible, Translations: input.Translations}
	return r.item, nil
}
func (r *serviceRepository) ListContent(context.Context, Module, int, int, string) (Page, error) {
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
func (r *serviceRepository) PublicContent(context.Context, Module, string, int) ([]PublicItem, error) {
	return nil, nil
}
