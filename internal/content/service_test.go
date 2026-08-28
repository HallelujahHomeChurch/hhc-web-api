package content

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
		{name: "history rejects invalid event date", module: ModuleHistory, input: historyInput("2026-13")},
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

func TestServiceAcceptsCanonicalHistoryEventDates(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	for _, eventDate := range []string{"", "1988", "1988-03", "1990-09-02"} {
		t.Run(eventDate, func(t *testing.T) {
			input := historyInput(eventDate)
			if _, err := service.CreateContent(context.Background(), ModuleHistory, input, "user-1", "history-"+eventDate); err != nil {
				t.Fatalf("eventDate=%q err=%v", eventDate, err)
			}
		})
	}
}

func TestServiceTrimsHistoryEventDate(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)
	if _, err := service.CreateContent(context.Background(), ModuleHistory, historyInput(" 1988-03 "), "user-1", "history-trim"); err != nil {
		t.Fatal(err)
	}
	if repo.item.EventDate != "1988-03" {
		t.Fatalf("eventDate=%q", repo.item.EventDate)
	}
}

func TestNewsCreateDerivesSlugAndSummary(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)
	input := WriteInput{
		DisplayDate:  "2026-08-02",
		Translations: []Translation{{Locale: "zh-Hant", Title: "最新消息", Body: strings.Repeat("內容", 100)}},
	}
	created, err := service.CreateContent(context.Background(), ModuleNews, input, "user-1", "news-create")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Slug, "news-20260802-") || utf8.RuneCountInString(created.Translations[0].Summary) != 160 || created.DetailLayout != "top" {
		t.Fatalf("created=%#v", created)
	}
}

func TestNewsAuthorIsTrimmedAndBounded(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)
	input := WriteInput{
		AuthorName:   "  王牧師  ",
		DisplayDate:  "2026-08-14",
		Translations: translations(),
	}
	item, err := service.CreateContent(context.Background(), ModuleNews, input, "admin", "author-create")
	if err != nil || item.AuthorName != "王牧師" {
		t.Fatalf("item=%#v err=%v", item, err)
	}
	input.AuthorName = strings.Repeat("人", 201)
	if _, err := service.CreateContent(context.Background(), ModuleNews, input, "admin", "author-too-long"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestNonNewsRejectsPublicAuthor(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	input := historyInput("2026")
	input.AuthorName = "王牧師"
	if _, err := service.CreateContent(context.Background(), ModuleHistory, input, "admin", "history-author"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestRestoreContentPreservesPublicAuthor(t *testing.T) {
	repo := &serviceRepository{
		item:     Item{ID: "news-1", Module: ModuleNews, Version: 2, Slug: "news", DisplayDate: "2026-08-14", Translations: translations()},
		revision: Revision{Version: 1, Snapshot: Item{AuthorName: "王牧師", Slug: "news", DisplayDate: "2026-08-14", Translations: translations()}},
	}
	service := NewService(repo, time.Now)
	if _, err := service.RestoreContent(context.Background(), ModuleNews, "news-1", 1, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	if repo.updateInput.AuthorName != "王牧師" {
		t.Fatalf("authorName=%q", repo.updateInput.AuthorName)
	}
}

func TestNewsRejectsInvalidDetailLayout(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	input := WriteInput{DisplayDate: "2026-08-02", DetailLayout: "custom", Translations: translations()}
	if _, err := service.CreateContent(context.Background(), ModuleNews, input, "user-1", "news-layout"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestLocationsRequireStableKeyHTTPSMapAndTranslations(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	valid := locationInput()
	if _, err := service.CreateContent(context.Background(), ModuleLocations, valid, "admin", "location-taipei"); err != nil {
		t.Fatal(err)
	}
	maximumKey := valid
	maximumKey.LocationKey = strings.Repeat("a", 120)
	if _, err := service.CreateContent(context.Background(), ModuleLocations, maximumKey, "admin", "location-maximum-key"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		input WriteInput
	}{
		{name: "empty stable key", input: func() WriteInput { value := valid; value.LocationKey = ""; return value }()},
		{name: "non canonical stable key", input: func() WriteInput { value := valid; value.LocationKey = "Taipei Main"; return value }()},
		{name: "http map", input: func() WriteInput { value := valid; value.MapHref = "http://maps.example.com/taipei"; return value }()},
		{name: "credentials", input: func() WriteInput {
			value := valid
			value.MapHref = "https://user:secret@maps.example.com/taipei"
			return value
		}()},
		{name: "fragment", input: func() WriteInput {
			value := valid
			value.MapHref = "https://maps.example.com/taipei#internal"
			return value
		}()},
		{name: "localhost", input: func() WriteInput { value := valid; value.MapHref = "https://localhost/taipei"; return value }()},
		{name: "private ipv4", input: func() WriteInput { value := valid; value.MapHref = "https://10.0.0.1/taipei"; return value }()},
		{name: "private ipv6", input: func() WriteInput { value := valid; value.MapHref = "https://[fd00::1]/taipei"; return value }()},
		{name: "abbreviated loopback ipv4", input: func() WriteInput { value := valid; value.MapHref = "https://127.1/taipei"; return value }()},
		{name: "octal loopback ipv4", input: func() WriteInput { value := valid; value.MapHref = "https://0177.0.0.1/taipei"; return value }()},
		{name: "hexadecimal loopback ipv4", input: func() WriteInput { value := valid; value.MapHref = "https://0x7f.0.0.1/taipei"; return value }()},
		{name: "internal host", input: func() WriteInput { value := valid; value.MapHref = "https://maps.internal/taipei"; return value }()},
		{name: "bare internal host", input: func() WriteInput { value := valid; value.MapHref = "https://asset-api/taipei"; return value }()},
		{name: "generic single label host", input: func() WriteInput { value := valid; value.MapHref = "https://postgres/taipei"; return value }()},
		{name: "service discovery host", input: func() WriteInput {
			value := valid
			value.MapHref = "https://kubernetes.default.svc/taipei"
			return value
		}()},
		{name: "blob host", input: func() WriteInput {
			value := valid
			value.MapHref = "https://account.blob.core.windows.net/maps/taipei"
			return value
		}()},
		{name: "sas query", input: func() WriteInput {
			value := valid
			value.MapHref = "https://maps.example.com/taipei?sv=2024&sig=secret"
			return value
		}()},
		{name: "api path", input: func() WriteInput { value := valid; value.MapHref = "https://maps.example.com/api/taipei"; return value }()},
		{name: "private path", input: func() WriteInput {
			value := valid
			value.MapHref = "https://maps.example.com/priv/taipei"
			return value
		}()},
		{name: "api path through dot segment", input: func() WriteInput {
			value := valid
			value.MapHref = "https://maps.example.com/x/../api/taipei"
			return value
		}()},
		{name: "private path through dot segment", input: func() WriteInput {
			value := valid
			value.MapHref = "https://maps.example.com/x/../priv/taipei"
			return value
		}()},
		{name: "negative sort", input: func() WriteInput { value := valid; value.SortOrder = -1; return value }()},
		{name: "oversized stable key", input: func() WriteInput { value := valid; value.LocationKey = strings.Repeat("a", 121); return value }()},
		{name: "no translations", input: func() WriteInput { value := valid; value.Translations = nil; return value }()},
		{name: "translation summary", input: func() WriteInput {
			value := valid
			value.Translations = []Translation{{Locale: "zh-Hant", Title: "台北", Body: "地址", Summary: "摘要"}}
			return value
		}()},
		{name: "translation whitespace summary", input: func() WriteInput {
			value := valid
			value.Translations = []Translation{{Locale: "zh-Hant", Title: "台北", Body: "地址", Summary: " "}}
			return value
		}()},
		{name: "translation date label", input: func() WriteInput {
			value := valid
			value.Translations = []Translation{{Locale: "zh-Hant", Title: "台北", Body: "地址", DateLabel: "日期"}}
			return value
		}()},
		{name: "translation image alt", input: func() WriteInput {
			value := valid
			value.Translations = []Translation{{Locale: "zh-Hant", Title: "台北", Body: "地址", ImageAlt: "替代文字"}}
			return value
		}()},
		{name: "blank name", input: func() WriteInput {
			value := valid
			value.Translations = []Translation{{Locale: "zh-Hant", Body: "地址"}}
			return value
		}()},
		{name: "blank address", input: func() WriteInput {
			value := valid
			value.Translations = []Translation{{Locale: "zh-Hant", Title: "台北"}}
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if ValidateLocation(test.input) {
				t.Fatal("shared validator accepted invalid location")
			}
			if _, err := service.CreateContent(context.Background(), ModuleLocations, test.input, "admin", "location-invalid"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestLegacyModulesKeepDetailLayoutDefaultInRetryInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		module Module
		input  WriteInput
	}{
		{name: "history", module: ModuleHistory, input: historyInput("1988-03")},
		{name: "video", module: ModuleVideos, input: WriteInput{YouTubeVideoID: "K3ckFWeSQ-k", Translations: translations()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &serviceRepository{}
			service := NewService(repo, time.Now)
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := service.CreateContent(context.Background(), test.module, test.input, "admin", "retry-"+test.name); err != nil {
					t.Fatal(err)
				}
			}
			if len(repo.createInputs) != 2 || repo.createInputs[0].DetailLayout != "top" || repo.createInputs[1].DetailLayout != "top" {
				t.Fatalf("retry inputs=%#v", repo.createInputs)
			}
		})
	}
}

func TestLocationUpdateKeepsStableKeyImmutable(t *testing.T) {
	repo := &serviceRepository{item: Item{ID: "location-1", Module: ModuleLocations, Version: 2, LocationKey: "taipei", MapHref: locationInput().MapHref, Translations: locationInput().Translations}}
	service := NewService(repo, time.Now)
	input := locationInput()
	input.LocationKey = "zhongli"
	if _, err := service.UpdateContent(context.Background(), ModuleLocations, repo.item.ID, repo.item.Version, input, "admin"); !errors.Is(err, ErrInvalid) || repo.updateCalls != 0 {
		t.Fatalf("err=%v updateCalls=%d", err, repo.updateCalls)
	}
}

func TestLocationPublishRequiresAllFiveTranslations(t *testing.T) {
	repo := &serviceRepository{item: Item{ID: "location-1", Module: ModuleLocations, Version: 1, LocationKey: "taipei", MapHref: locationInput().MapHref, SortOrder: 10, Translations: locationInput().Translations}}
	if _, err := NewService(repo, time.Now).PublishContent(context.Background(), ModuleLocations, repo.item.ID, repo.item.Version, "admin"); !errors.Is(err, ErrNotPublishable) {
		t.Fatalf("err=%v", err)
	}
}

func TestLocationPublishesWithAllFiveTranslations(t *testing.T) {
	input := locationInput()
	input.Translations = []Translation{
		{Locale: "zh-Hant", Title: "台北", Body: "台北地址"},
		{Locale: "zh-Hans", Title: "台北", Body: "台北地址"},
		{Locale: "en", Title: "Taipei", Body: "Taipei address"},
		{Locale: "ja", Title: "台北", Body: "台北住所"},
		{Locale: "ko", Title: "타이베이", Body: "타이베이 주소"},
	}
	repo := &serviceRepository{item: Item{ID: "location-1", Module: ModuleLocations, Version: 1, LocationKey: input.LocationKey, MapHref: input.MapHref, SortOrder: input.SortOrder, Translations: input.Translations}}
	item, err := NewService(repo, time.Now).PublishContent(context.Background(), ModuleLocations, repo.item.ID, repo.item.Version, "admin")
	if err != nil || item.Status != StatusPublished {
		t.Fatalf("item=%#v err=%v", item, err)
	}
}

func TestPagesRejectGenericCreateAndDelete(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)
	if _, err := service.CreateContent(context.Background(), ModulePages, pageInput("home", "home.v1", "/", validHomePagePayload()), "admin", "page-home"); !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("create err=%v", err)
	}
	if err := service.DeleteContent(context.Background(), ModulePages, "page-1", 1, "admin"); !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("delete err=%v", err)
	}
}

func TestPageUpdateRequiresImmutableDefinitionAndDerivesMetadata(t *testing.T) {
	input := pageInput("home", "home.v1", "/", validHomePagePayload())
	repo := &serviceRepository{item: Item{ID: "page-1", Module: ModulePages, Version: 1, PageKey: input.PageKey, PageTemplate: input.PageTemplate, RoutePath: input.RoutePath, Indexable: true, Translations: input.Translations}}
	service := NewService(repo, time.Now)
	input.RoutePath = "/changed"
	if _, err := service.UpdateContent(context.Background(), ModulePages, repo.item.ID, 1, input, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("immutable route err=%v", err)
	}
	input.RoutePath = "/"
	updated, err := service.UpdateContent(context.Background(), ModulePages, repo.item.ID, 1, input, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Translations[0].Title != "愛從家開始" || updated.Translations[0].Summary != "在愛中成長" {
		t.Fatalf("translation=%#v", updated.Translations[0])
	}
}

func TestPagePublishRequiresAllFiveExactLocales(t *testing.T) {
	input := pageInput("home", "home.v1", "/", validHomePagePayload())
	input.Translations = input.Translations[:4]
	repo := &serviceRepository{item: Item{ID: "page-1", Module: ModulePages, Version: 1, PageKey: input.PageKey, PageTemplate: input.PageTemplate, RoutePath: input.RoutePath, Indexable: true, Translations: input.Translations}}
	if _, err := NewService(repo, time.Now).PublishContent(context.Background(), ModulePages, repo.item.ID, 1, "admin"); !errors.Is(err, ErrNotPublishable) {
		t.Fatalf("err=%v", err)
	}
}

func TestRestoreContentPreservesLocationDetail(t *testing.T) {
	repo := &serviceRepository{
		item:     Item{ID: "location-1", Module: ModuleLocations, Version: 2, LocationKey: "taipei", MapHref: "https://maps.example.com/current", SortOrder: 20, Translations: locationInput().Translations},
		revision: Revision{Version: 1, Snapshot: Item{Module: ModuleLocations, LocationKey: "taipei", MapHref: "https://maps.example.com/old", SortOrder: 10, Translations: locationInput().Translations}},
	}
	if _, err := NewService(repo, time.Now).RestoreContent(context.Background(), ModuleLocations, repo.item.ID, 1, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	if repo.updateInput.LocationKey != "taipei" || repo.updateInput.MapHref != "https://maps.example.com/old" || repo.updateInput.SortOrder != 10 {
		t.Fatalf("input=%#v", repo.updateInput)
	}
}

func TestDetailLayoutDefaultIsNewsOnly(t *testing.T) {
	repo := &serviceRepository{}
	if _, err := NewService(repo, time.Now).CreateContent(context.Background(), ModuleLocations, locationInput(), "admin", "location-layout"); err != nil {
		t.Fatal(err)
	}
	if repo.item.DetailLayout != "" {
		t.Fatalf("location detailLayout=%q", repo.item.DetailLayout)
	}
}

func TestNewsUpdatePreservesExistingSlug(t *testing.T) {
	repo := &serviceRepository{item: Item{ID: "news-1", Module: ModuleNews, Slug: "existing-slug", Version: 2}}
	service := NewService(repo, time.Now)
	input := WriteInput{DisplayDate: "2026-08-02", Translations: translations()}
	updated, err := service.UpdateContent(context.Background(), ModuleNews, "news-1", 2, input, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Slug != "existing-slug" {
		t.Fatalf("slug=%q", updated.Slug)
	}
}

func TestUpdateContentRejectsOmittedPersistedLocale(t *testing.T) {
	repo := &serviceRepository{item: Item{
		ID: "video-1", Module: ModuleVideos, Version: 2, YouTubeVideoID: "K3ckFWeSQ-k",
		Translations: []Translation{{Locale: "zh-Hant", Title: "影片"}, {Locale: "en", Title: "Video"}},
	}}
	service := NewService(repo, time.Now)

	_, err := service.UpdateContent(context.Background(), ModuleVideos, "video-1", 2, WriteInput{
		YouTubeVideoID: "K3ckFWeSQ-k",
		Translations:   []Translation{{Locale: "zh-Hant", Title: "影片更新"}},
	}, "user-1")
	if !errors.Is(err, ErrLocaleSetMismatch) || repo.updateCalls != 0 {
		t.Fatalf("err=%v updateCalls=%d", err, repo.updateCalls)
	}
}

func TestUpdateContentAllowsExplicitLocaleDeletion(t *testing.T) {
	repo := &serviceRepository{item: Item{
		ID: "video-1", Module: ModuleVideos, Version: 2, YouTubeVideoID: "K3ckFWeSQ-k",
		Translations: []Translation{{Locale: "zh-Hant", Title: "影片"}, {Locale: "en", Title: "Video"}},
	}}
	service := NewService(repo, time.Now)

	if _, err := service.UpdateContent(context.Background(), ModuleVideos, "video-1", 2, WriteInput{
		YouTubeVideoID: "K3ckFWeSQ-k",
		Translations:   []Translation{{Locale: "zh-Hant", Title: "影片更新"}},
		DeleteLocales:  []string{"en"},
	}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if len(repo.updateInput.Translations) != 1 || repo.updateInput.Translations[0].Locale != "zh-Hant" {
		t.Fatalf("translations=%#v", repo.updateInput.Translations)
	}
}

func TestRestoreRevisionPreservesLocalesMissingFromSnapshot(t *testing.T) {
	repo := &serviceRepository{item: Item{
		ID: "video-1", Module: ModuleVideos, Version: 2, YouTubeVideoID: "K3ckFWeSQ-k",
		Translations: []Translation{{Locale: "zh-Hant", Title: "目前影片"}, {Locale: "en", Title: "Current video"}},
	}, revision: Revision{Version: 1, Snapshot: Item{
		Module: ModuleVideos, YouTubeVideoID: "K3ckFWeSQ-k",
		Translations: []Translation{{Locale: "zh-Hant", Title: "歷史影片"}},
	}}}
	service := NewService(repo, time.Now)

	if _, err := service.RestoreContent(context.Background(), ModuleVideos, "video-1", 1, 2, "user-1"); err != nil {
		t.Fatal(err)
	}
	if got := repo.updateInput.Translations; len(got) != 2 || got[0].Locale != "zh-Hant" || got[0].Title != "歷史影片" || got[1].Locale != "en" || got[1].Title != "Current video" {
		t.Fatalf("translations=%#v", got)
	}
	if repo.contentRevisionsCalls != 0 {
		t.Fatalf("broad revision list calls=%d", repo.contentRevisionsCalls)
	}
}

func TestRestoreContentReturnsNotFoundForMissingRevision(t *testing.T) {
	repo := &serviceRepository{
		item:          Item{ID: "video-1", Module: ModuleVideos, Version: 2},
		revisionError: ErrNotFound,
	}
	service := NewService(repo, time.Now)

	_, err := service.RestoreContent(context.Background(), ModuleVideos, "video-1", 99, 2, "user-1")
	if !errors.Is(err, ErrNotFound) || repo.updateCalls != 0 || repo.contentRevisionsCalls != 0 {
		t.Fatalf("err=%v updateCalls=%d broadCalls=%d", err, repo.updateCalls, repo.contentRevisionsCalls)
	}
}

func TestServiceRejectsNonCanonicalHistoryEventDates(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	for _, eventDate := range []string{"0000", "88", "1988-3", "1988-00", "1988-13", "1990-02-30", "1990/09/02"} {
		t.Run(eventDate, func(t *testing.T) {
			if _, err := service.CreateContent(context.Background(), ModuleHistory, historyInput(eventDate), "user-1", "history-invalid"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("eventDate=%q err=%v", eventDate, err)
			}
		})
	}
}

func TestHistoryRejectsLocationSortOrder(t *testing.T) {
	var input WriteInput
	if err := json.Unmarshal([]byte(`{"eventDate":"1988-03","sortOrder":10,"translations":[{"locale":"zh-Hant","title":"開始家庭聚會","dateLabel":"1988年3月"}]}`), &input); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(&serviceRepository{}, time.Now).CreateContent(context.Background(), ModuleHistory, input, "admin", "history-sort"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
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
		ID: "history-1", Module: ModuleHistory, Status: StatusDraft, Version: 1, EventDate: "2026",
		Translations: []Translation{{Locale: "zh-Hant", Title: "標題"}},
	}
	if _, err := service.PublishContent(context.Background(), ModuleHistory, repo.item.ID, 1, "user-1"); !errors.Is(err, ErrNotPublishable) {
		t.Fatalf("history error=%v", err)
	}

	repo.item.Translations[0].Body = "Milestone"
	if _, err := service.PublishContent(context.Background(), ModuleHistory, repo.item.ID, 1, "user-1"); err != nil {
		t.Fatal(err)
	}
}

func TestNewsPublishAllowsOptionalImages(t *testing.T) {
	repo := &serviceRepository{item: Item{ID: "item-1", Module: ModuleNews, Status: StatusDraft, Version: 2, Slug: "announcement", DisplayDate: "2026-07-13", Translations: translations()}}
	service := NewService(repo, time.Now)

	if _, err := service.PublishContent(context.Background(), ModuleNews, "item-1", 2, "user-1"); err != nil {
		t.Fatalf("image-less publish err=%v", err)
	}
	repo.item.HomeCoverAssetID = "home-asset"
	if _, err := service.PublishContent(context.Background(), ModuleNews, "item-1", 2, "user-1"); err != nil {
		t.Fatalf("home-only publish err=%v", err)
	}
}

func TestNewsNormalizesHomeCoverAssetID(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)
	input := WriteInput{DisplayDate: "2026-08-02", HomeCoverAssetID: " home-asset ", Translations: translations()}
	if _, err := service.CreateContent(context.Background(), ModuleNews, input, "user-1", "news-home"); err != nil {
		t.Fatal(err)
	}
	if repo.item.HomeCoverAssetID != "home-asset" {
		t.Fatalf("homeCoverAssetId=%q", repo.item.HomeCoverAssetID)
	}
}

func TestServiceAcceptsTypedDrafts(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)

	inputs := map[Module]WriteInput{
		ModuleNews:    {Slug: "announcement", DisplayDate: "2026-07-13", Featured: true, Translations: translations()},
		ModuleHistory: historyInput("2026"),
		ModuleVideos:  {YouTubeVideoID: "K3ckFWeSQ-k", HomeEligible: true, Translations: translations()},
	}
	for module, input := range inputs {
		if _, err := service.CreateContent(context.Background(), module, input, "user-1", string(module)+"-1"); err != nil {
			t.Fatalf("module=%s err=%v", module, err)
		}
	}
}

func TestServiceNormalizesYouTubeURLsToVideoIDs(t *testing.T) {
	tests := map[string]string{
		"https://youtu.be/K3ckFWeSQ-k?si=abc":                  "K3ckFWeSQ-k",
		"https://www.youtube.com/watch?v=K3ckFWeSQ-k&t=12s":    "K3ckFWeSQ-k",
		"https://youtube.com/shorts/K3ckFWeSQ-k?feature=share": "K3ckFWeSQ-k",
		"https://youtube.com/embed/K3ckFWeSQ-k":                "K3ckFWeSQ-k",
		"K3ckFWeSQ-k":                                          "K3ckFWeSQ-k",
	}
	for value, want := range tests {
		t.Run(value, func(t *testing.T) {
			repo := &serviceRepository{}
			service := NewService(repo, time.Now)
			_, err := service.CreateContent(context.Background(), ModuleVideos, WriteInput{YouTubeVideoID: value, Translations: translations()}, "user-1", value)
			if err != nil {
				t.Fatal(err)
			}
			if repo.item.YouTubeVideoID != want {
				t.Fatalf("youtubeVideoId=%q want=%q", repo.item.YouTubeVideoID, want)
			}
		})
	}
}

func TestServiceDeletesContent(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, func() time.Time { return time.Unix(123, 0) })
	if err := service.DeleteContent(context.Background(), "invalid", "item-1", 2, "user-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid module error=%v", err)
	}
	if err := service.DeleteContent(context.Background(), ModuleVideos, "", 2, "user-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid id error=%v", err)
	}
	if err := service.DeleteContent(context.Background(), ModuleVideos, "item-1", 2, "user-1"); err != nil {
		t.Fatal(err)
	}
	if repo.deletedID != "item-1" || repo.expected != 2 || repo.actor != "user-1" || !repo.now.Equal(time.Unix(123, 0).UTC()) {
		t.Fatalf("delete forwarding=%#v", repo)
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
	if _, err := service.ListContent(context.Background(), ModuleNews, ListOptions{
		Page: 10_001,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized page err=%v", err)
	}

	if _, err := service.ListContent(context.Background(), ModuleHistory, ListOptions{
		Query: "  milestone  ",
	}); err != nil {
		t.Fatal(err)
	}
	if repo.listOptions.Query != "milestone" || repo.listOptions.Page != 1 ||
		repo.listOptions.PageSize != 20 || repo.listOptions.Sort != "eventDate" ||
		repo.listOptions.Direction != "desc" {
		t.Fatalf("options=%#v", repo.listOptions)
	}
}

func TestServiceRejectsInvalidPublicNewsSlug(t *testing.T) {
	service := NewService(&serviceRepository{}, time.Now)
	if _, _, err := service.PublicNews(context.Background(), "zh-Hant", "Invalid Slug"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := service.PublicNews(context.Background(), "zh-Hant", strings.Repeat("a", 121)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized slug err=%v", err)
	}
}

func TestServiceNormalizesPublicPagination(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)
	if _, err := service.PublicContent(context.Background(), ModuleNews, "zh-Hant", 0, 101); err != nil {
		t.Fatal(err)
	}
	if repo.publicPage != 1 || repo.publicPageSize != 20 {
		t.Fatalf("page=%d pageSize=%d", repo.publicPage, repo.publicPageSize)
	}
	if _, err := service.PublicContent(context.Background(), ModuleNews, "zh-Hant", 10_001, 20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceAcceptsJapaneseAndKoreanContentLocales(t *testing.T) {
	repo := &serviceRepository{}
	service := NewService(repo, time.Now)
	translations := []Translation{
		{Locale: "zh-Hant", Title: "影片"},
		{Locale: "zh-Hans", Title: "视频"},
		{Locale: "en", Title: "Video"},
		{Locale: "ja", Title: "動画"},
		{Locale: "ko", Title: "동영상"},
	}

	created, err := service.CreateContent(context.Background(), ModuleVideos, WriteInput{
		YouTubeVideoID: "K3ckFWeSQ-k",
		Translations:   translations,
	}, "user-1", "five-locales")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateContent(context.Background(), ModuleVideos, created.ID, created.Version, WriteInput{
		YouTubeVideoID: "K3ckFWeSQ-k",
		Translations:   translations,
	}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublishContent(context.Background(), ModuleVideos, created.ID, created.Version, "user-1"); err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"ja", "ko"} {
		if _, err := service.PublicContent(context.Background(), ModuleVideos, locale, 1, 20); err != nil {
			t.Fatalf("public %s: %v", locale, err)
		}
	}
}

func translations() []Translation {
	return []Translation{{Locale: "zh-Hant", Title: "標題", Summary: "摘要", Body: "內容", DateLabel: "2026年"}}
}

func historyInput(eventDate string) WriteInput {
	return WriteInput{
		EventDate: eventDate,
		Translations: []Translation{{
			Locale: "zh-Hant", Title: "沿革", Body: "事件",
		}},
	}
}

func locationInput() WriteInput {
	return WriteInput{
		LocationKey: "taipei",
		MapHref:     "https://maps.app.goo.gl/fDus6nVswbuhSEAd8",
		SortOrder:   10,
		Translations: []Translation{{
			Locale: "zh-Hant", Title: "台北哈利路亞家教會", Body: "106臺北市大安區民輝里仁愛路三段29號B1",
		}},
	}
}

func pageInput(key, template, route string, payload json.RawMessage) WriteInput {
	return WriteInput{
		PageKey: key, PageTemplate: template, RoutePath: route, Indexable: true,
		Translations: []Translation{
			{Locale: "zh-Hant", BodyJSON: payload},
			{Locale: "zh-Hans", BodyJSON: payload},
			{Locale: "en", BodyJSON: payload},
			{Locale: "ja", BodyJSON: payload},
			{Locale: "ko", BodyJSON: payload},
		},
	}
}

type serviceRepository struct {
	item                  Item
	createInputs          []WriteInput
	revision              Revision
	revisionError         error
	listOptions           ListOptions
	updateInput           WriteInput
	updateCalls           int
	contentRevisionsCalls int
	deletedID             string
	expected              int64
	actor                 string
	now                   time.Time
	publicPage            int
	publicPageSize        int
}

func (r *serviceRepository) CreateContent(_ context.Context, module Module, input WriteInput, actor, key string, now time.Time) (Item, error) {
	r.createInputs = append(r.createInputs, input)
	r.item = Item{ID: "item-1", Module: module, Status: StatusDraft, Version: 1, AuthorName: input.AuthorName, Slug: input.Slug, DisplayDate: input.DisplayDate, EventDate: input.EventDate, YouTubeVideoID: input.YouTubeVideoID, CoverAssetID: input.CoverAssetID, HomeCoverAssetID: input.HomeCoverAssetID, DetailLayout: input.DetailLayout, Featured: input.Featured, HomeEligible: input.HomeEligible, LocationKey: input.LocationKey, MapHref: input.MapHref, SortOrder: input.SortOrder, Translations: input.Translations}
	return r.item, nil
}
func (r *serviceRepository) ListContent(_ context.Context, _ Module, options ListOptions) (Page, error) {
	r.listOptions = options
	return Page{}, nil
}
func (r *serviceRepository) GetContent(context.Context, Module, string) (Item, error) {
	return r.item, nil
}

func (r *serviceRepository) UpdateContent(_ context.Context, _ Module, _ string, _ int64, input WriteInput, _ string, _ time.Time) (Item, error) {
	r.updateCalls++
	r.updateInput = input
	r.item.AuthorName = input.AuthorName
	r.item.Slug = input.Slug
	r.item.Translations = input.Translations
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
	r.contentRevisionsCalls++
	return nil, nil
}
func (r *serviceRepository) ContentRevision(_ context.Context, _ Module, _ string, revision int64) (Revision, error) {
	if r.revisionError != nil {
		return Revision{}, r.revisionError
	}
	if revision != r.revision.Version {
		return Revision{}, ErrNotFound
	}
	return r.revision, nil
}
func (r *serviceRepository) DeleteContent(_ context.Context, _ Module, id string, expected int64, actor string, now time.Time) error {
	r.deletedID, r.expected, r.actor, r.now = id, expected, actor, now
	return nil
}
func (r *serviceRepository) PublicContent(_ context.Context, _ Module, _ string, page, pageSize int) (PublicPage, error) {
	r.publicPage, r.publicPageSize = page, pageSize
	return PublicPage{}, nil
}
func (r *serviceRepository) PublicNews(context.Context, string, string) (PublicItem, string, error) {
	return PublicItem{}, "", nil
}

func (r *serviceRepository) PublicLocations(context.Context, string) ([]PublicLocation, error) {
	return nil, nil
}
func (r *serviceRepository) PublicEditorialPage(context.Context, string, string) (PublicEditorialPage, string, error) {
	return PublicEditorialPage{}, "", nil
}
