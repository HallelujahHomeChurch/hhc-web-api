package translation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestServicePreviewsOnlyModuleFieldsFromSavedTraditionalChinese(t *testing.T) {
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		request    PreviewRequest
		content    content.Item
		bulletin   bulletins.Issue
		wantFields map[string]string
	}{
		{
			name: "news", request: previewRequest("news"),
			content:    content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "標題\r\n二", Summary: "不翻譯", Body: "內文\r三", ImageAlt: "圖片", DateLabel: "不翻譯"}}},
			wantFields: map[string]string{"title": "標題\n二", "body": "內文\n三", "imageAlt": "圖片"},
		},
		{
			name: "history", request: previewRequest("history"),
			content:    content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleHistory, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "歷史", Body: "內容", DateLabel: "不翻譯"}}},
			wantFields: map[string]string{"title": "歷史", "body": "內容"},
		},
		{
			name: "videos", request: previewRequest("videos"),
			content:    content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleVideos, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "影片", Body: "不翻譯"}}},
			wantFields: map[string]string{"title": "影片"},
		},
		{
			name: "bulletins", request: previewRequest("bulletins"),
			bulletin:   bulletins.Issue{ID: "10000000-0000-4000-8000-000000000001", Version: 7, Versions: []bulletins.Version{{Locale: "zh-Hant", Title: "週報", Subtitle: "副標"}}},
			wantFields: map[string]string{"title": "週報", "subtitle": "副標"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			generator := &generatorStub{result: Result{Fields: translated(test.wantFields)}}
			repository := &translationRepositoryStub{}
			service := NewService(
				contentSourceStub{item: test.content}, bulletinSourceStub{issue: test.bulletin}, generator, repository,
				ServiceConfig{Deployment: "cms-translator", HandlerTimeout: time.Second, SourceCharLimit: 20_000, ActorLimit: 10, DeploymentLimit: 60, ActorDailyLimit: 30, DeploymentDailyLimit: 300, Cooldown: 10 * time.Minute, Now: func() time.Time { return now }},
			)

			preview, err := service.Preview(context.Background(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(generator.request.Fields, test.wantFields) {
				t.Fatalf("provider fields = %#v, want %#v", generator.request.Fields, test.wantFields)
			}
			if preview.SourceLocale != "zh-Hant" || preview.TargetLocale != test.request.TargetLocale || preview.SourceVersion != 7 || !reflect.DeepEqual(preview.Translation, translated(test.wantFields)) {
				t.Fatalf("preview = %#v", preview)
			}
			if preview.RetryAfterSeconds != 0 {
				t.Fatalf("retry after = %d, want 0", preview.RetryAfterSeconds)
			}
			if repository.reserveCalls != 0 || len(repository.audits) != 1 || repository.audits[0].Outcome != OutcomeSuccess {
				t.Fatalf("reserve calls = %d, audits = %#v", repository.reserveCalls, repository.audits)
			}
		})
	}
}

func TestServicePreviewsOnlyRequestedLegalPageFields(t *testing.T) {
	fields := map[string]string{"sections.2.body.1": "修改的段落", "heroTitle": "隱私權政策"}
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModulePages, PageKey: "privacy-policy", PageTemplate: "legal.v1", Version: 7}
	generator := &generatorStub{result: Result{Fields: translated(fields)}}
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, generator, &translationRepositoryStub{}, testServiceConfig())
	request := previewRequest("pages")
	request.Fields = fields

	preview, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(generator.request.Fields, fields) || !reflect.DeepEqual(preview.Translation, translated(fields)) {
		t.Fatalf("request=%#v preview=%#v", generator.request, preview)
	}
}

func TestStaffTranslationCanRetrySameSourceAndLocaleWithoutQuota(t *testing.T) {
	fields := map[string]string{"heroTitle": "隱私權政策", "sections.0.body.0": "保護資料"}
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModulePages, PageKey: "privacy-policy", PageTemplate: "legal.v1", Version: 7}
	generator := &generatorStub{err: ErrProvider}
	repository := &translationRepositoryStub{reserveErr: &RateLimitError{RetryAfter: 24 * time.Hour}, releaseErr: errors.New("no reservation")}
	service := NewService(contentSourceStub{item: item}, nil, generator, repository, testServiceConfig())
	request := previewRequest("pages")
	request.TargetLocale, request.Fields = "zh-Hans", fields
	if _, err := service.Preview(context.Background(), request); !errors.Is(err, ErrProvider) {
		t.Fatalf("initial failure=%v", err)
	}
	generator.err = nil
	generator.result = Result{Fields: map[string]string{"heroTitle": "隐私权政策", "sections.0.body.0": "保护资料"}}
	for range 35 {
		preview, err := service.Preview(context.Background(), request)
		if err != nil || preview.RetryAfterSeconds != 0 || !reflect.DeepEqual(preview.Translation, generator.result.Fields) {
			t.Fatalf("retry preview=%+v err=%v", preview, err)
		}
	}
	if generator.calls != 36 || repository.reserveCalls != 0 || repository.releaseCalls != 0 || len(repository.audits) != 36 {
		t.Fatalf("generator=%d reserve=%d release=%d audits=%d", generator.calls, repository.reserveCalls, repository.releaseCalls, len(repository.audits))
	}
}

func TestServiceTranslatesStatementTextWithoutSendingLayoutOrAssets(t *testing.T) {
	bodyJSON := []byte(`{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","alignment":"center","content":[{"type":"text","text":"來源","marks":["strong"]}]},{"id":"i1","type":"image","assetId":"asset_1","size":"small","alignment":"end","alt":{"mode":"text","text":"圖片"}}]}`)
	source := content.Translation{Locale: "zh-Hant", Title: "聲明", Body: "來源", BodyJSON: bodyJSON}
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Kind: content.KindStatement, Version: 7, Translations: []content.Translation{source}}
	fields, err := content.StatementTranslationFields(source.Title, source.BodyJSON)
	if err != nil {
		t.Fatal(err)
	}
	generator := &generatorStub{result: Result{Fields: translated(fields), TitleRule: &TitleRuleResult{Kind: "none"}}}
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, generator, &translationRepositoryStub{}, testServiceConfig())

	preview, err := service.Preview(context.Background(), previewRequest("news"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(generator.request.Fields, fields) {
		t.Fatalf("provider fields=%#v want=%#v", generator.request.Fields, fields)
	}
	if len(preview.Translation) != 1 || preview.Translation["title"] == "" || len(preview.SourceHash) != 64 {
		t.Fatalf("preview=%#v", preview)
	}
	public := string(preview.BodyJSON)
	for _, preserved := range []string{`"id":"p1"`, `"alignment":"center"`, `"marks":["strong"]`, `"assetId":"asset_1"`, `"size":"small"`, `"alignment":"end"`} {
		if !strings.Contains(public, preserved) {
			t.Fatalf("missing %s in %s", preserved, public)
		}
	}
}

func TestServiceRendersJapaneseGospelDinnerTitlePreview(t *testing.T) {
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Version: 7, Translations: []content.Translation{{
		Locale: "zh-Hant", Title: "432次綠野仙蹤福音餐會 - 璨恩的尋根", Body: "內文", ImageAlt: "福音餐會DM",
	}}}
	generator := &generatorStub{result: Result{
		Fields:    map[string]string{"title": "model full title", "body": "本文", "imageAlt": "福音食事会の案内"},
		TitleRule: &TitleRuleResult{Kind: "gospel_dinner", Sequence: "432", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"},
	}}
	repository := &translationRepositoryStub{}
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, generator, repository, testServiceConfig())

	preview, err := service.Preview(context.Background(), previewRequest("news"))
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.Translation["title"]; got != "第432回福音食事会 - 璨恩のルーツ探し" {
		t.Fatalf("title = %q", got)
	}
	if generator.calls != 1 || len(repository.audits) != 1 || repository.audits[0].Outcome != OutcomeSuccess || repository.audits[0].PromptVersion != "cms-translation-v4" {
		t.Fatalf("calls=%d audits=%#v", generator.calls, repository.audits)
	}
}

func TestServiceRejectsInventedGospelDinnerOccurrence(t *testing.T) {
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Version: 7, Translations: []content.Translation{{
		Locale: "zh-Hant", Title: "432次綠野仙蹤福音餐會 - 璨恩的尋根", Body: "內文", ImageAlt: "福音餐會DM",
	}}}
	generator := &generatorStub{result: Result{
		Fields:    map[string]string{"title": "model full title", "body": "本文", "imageAlt": "福音食事会の案内"},
		TitleRule: &TitleRuleResult{Kind: "gospel_dinner", Sequence: "442", SourceEventName: "璨恩的尋根", LocalizedEventName: "璨恩のルーツ探し"},
	}}
	repository := &translationRepositoryStub{}
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, generator, repository, testServiceConfig())

	_, err := service.Preview(context.Background(), previewRequest("news"))
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("error = %v, want ErrProvider", err)
	}
	if repository.releaseCalls != 0 || len(repository.audits) != 1 || repository.audits[0].Outcome != OutcomeOutputValidationFailure {
		t.Fatalf("release=%d audits=%#v", repository.releaseCalls, repository.audits)
	}
}

func TestServiceRejectsJapaneseAndKoreanBulletinTargetsBeforeLoadOrProvider(t *testing.T) {
	for _, locale := range []string{"ja", "ko"} {
		request := previewRequest("bulletins")
		request.TargetLocale = locale
		generator := &generatorStub{}
		repository := &translationRepositoryStub{}
		service := NewService(contentSourceStub{}, bulletinSourceStub{err: errors.New("must not load")}, generator, repository, testServiceConfig())

		if _, err := service.Preview(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("target=%s error=%v", locale, err)
		}
		if generator.calls != 0 || repository.reserveCalls != 0 {
			t.Fatalf("target=%s generator=%d reserve=%d", locale, generator.calls, repository.reserveCalls)
		}
	}
}

func TestServiceLoadsSavedCampaignSources(t *testing.T) {
	for _, module := range []string{"campaigns", "campaign-schedules"} {
		t.Run(module, func(t *testing.T) {
			source := SavedSource{ResourceID: "10000000-0000-4000-8000-000000000001", SourceLocale: "zh-Hant", Channel: "email", Version: 7, Fields: map[string]string{"subject": "主旨", "body": "內容"}, AvailableLocales: []string{"zh-Hant"}}
			generator := &generatorStub{result: Result{Fields: map[string]string{"subject": "Subject", "body": "Body"}}}
			service := NewService(contentSourceStub{}, bulletinSourceStub{}, generator, &translationRepositoryStub{}, testServiceConfig(), savedSourceStub{source: source})
			request := previewRequest(module)
			if _, err := service.Preview(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(generator.request.Fields, source.Fields) || generator.request.Module != module {
				t.Fatalf("provider request=%#v", generator.request)
			}
		})
	}
}

func TestServiceRestrictsCampaignTargetsAndExistingTranslations(t *testing.T) {
	source := SavedSource{ResourceID: "10000000-0000-4000-8000-000000000001", SourceLocale: "zh-Hant", Channel: "web_push", Version: 7, Fields: map[string]string{"subject": "主旨", "body": "內容"}, AvailableLocales: []string{"zh-Hant", "ko"}}
	service := NewService(contentSourceStub{}, bulletinSourceStub{}, &generatorStub{}, &translationRepositoryStub{}, testServiceConfig(), savedSourceStub{source: source})
	request := previewRequest("campaigns")
	request.TargetLocale = "zh-Hans"
	if _, err := service.Preview(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zh-Hans error=%v", err)
	}
	request.TargetLocale = "ko"
	if _, err := service.Preview(context.Background(), request); !errors.Is(err, ErrTranslationExists) {
		t.Fatalf("existing ko error=%v", err)
	}
}

func TestServiceChecksVersionBeforeExistingTarget(t *testing.T) {
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Version: 8, Translations: []content.Translation{
		{Locale: "zh-Hant", Title: "來源"}, {Locale: "ja", Title: "既有"},
	}}
	repository := &translationRepositoryStub{}
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, &generatorStub{}, repository, testServiceConfig())

	_, err := service.Preview(context.Background(), previewRequest("news"))
	if err != ErrVersionMismatch {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}
	if repository.reserveCalls != 0 || len(repository.audits) != 1 || repository.audits[0].Outcome != OutcomeVersionMismatch {
		t.Fatalf("reserve calls = %d, audits = %#v", repository.reserveCalls, repository.audits)
	}
}

func TestServiceRequiresMissingTargetUnlessReplacementIsExplicit(t *testing.T) {
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Version: 7, Translations: []content.Translation{
		{Locale: "zh-Hant", Title: "來源"}, {Locale: "ja", Body: "partial row still exists"},
	}}
	for _, test := range []struct {
		name        string
		replace     bool
		wantErr     error
		wantCalls   int
		wantOutcome string
	}{
		{name: "existing", wantErr: ErrTranslationExists, wantOutcome: OutcomeExistingTarget},
		{name: "replace", replace: true, wantCalls: 1, wantOutcome: OutcomeSuccess},
	} {
		t.Run(test.name, func(t *testing.T) {
			generator := &generatorStub{result: Result{Fields: map[string]string{"title": "翻譯", "body": "", "imageAlt": ""}}}
			repository := &translationRepositoryStub{}
			service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, generator, repository, testServiceConfig())
			request := previewRequest("news")
			request.ReplaceExisting = test.replace
			_, err := service.Preview(context.Background(), request)
			if !errors.Is(err, test.wantErr) || generator.calls != test.wantCalls || repository.audits[0].Outcome != test.wantOutcome {
				t.Fatalf("error=%v calls=%d audit=%#v", err, generator.calls, repository.audits)
			}
		})
	}
}

func TestServiceRejectsInvalidRequestsBeforeLimiterAndProvider(t *testing.T) {
	base := previewRequest("news")
	tests := []struct {
		name   string
		change func(*PreviewRequest)
	}{
		{"module", func(r *PreviewRequest) { r.Module = "pages" }},
		{"resource", func(r *PreviewRequest) { r.ResourceID = "" }},
		{"malformed resource", func(r *PreviewRequest) { r.ResourceID = "item-1" }},
		{"source", func(r *PreviewRequest) { r.SourceLocale = "en" }},
		{"target", func(r *PreviewRequest) { r.TargetLocale = "zh-Hant" }},
		{"version", func(r *PreviewRequest) { r.ExpectedVersion = 0 }},
		{"actor", func(r *PreviewRequest) { r.Actor = " " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.change(&request)
			generator := &generatorStub{}
			repository := &translationRepositoryStub{}
			service := NewService(contentSourceStub{err: errors.New("must not load")}, bulletinSourceStub{}, generator, repository, testServiceConfig())
			_, err := service.Preview(context.Background(), request)
			if !errors.Is(err, ErrInvalidRequest) || generator.calls != 0 || repository.reserveCalls != 0 {
				t.Fatalf("error=%v generator=%d reserve=%d", err, generator.calls, repository.reserveCalls)
			}
		})
	}
}

func TestServiceEnforcesUnicodeSourceLimitBeforeProvider(t *testing.T) {
	for _, test := range []struct {
		name         string
		body         string
		wantErr      error
		wantGenerate int
	}{
		{name: "boundary", body: strings.Repeat("界", 19_999), wantGenerate: 1},
		{name: "over", body: strings.Repeat("界", 20_000), wantErr: ErrInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "題", Body: test.body}}}
			generator := &generatorStub{result: Result{Fields: map[string]string{"title": "題", "body": "譯", "imageAlt": ""}}}
			repository := &translationRepositoryStub{}
			service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, generator, repository, testServiceConfig())
			_, err := service.Preview(context.Background(), previewRequest("news"))
			if !errors.Is(err, test.wantErr) || repository.reserveCalls != 0 || generator.calls != test.wantGenerate {
				t.Fatalf("error=%v reserve=%d generator=%d", err, repository.reserveCalls, generator.calls)
			}
		})
	}
}

func TestServiceRequiresExactNonEmptySavedTraditionalChineseRow(t *testing.T) {
	for _, translations := range [][]content.Translation{
		{{Locale: "en", Title: "not the source"}},
		{{Locale: "zh-Hant", Title: " ", Body: "\r\n", ImageAlt: ""}},
	} {
		generator := &generatorStub{}
		repository := &translationRepositoryStub{}
		item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleNews, Version: 7, Translations: translations}
		service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, generator, repository, testServiceConfig())
		if _, err := service.Preview(context.Background(), previewRequest("news")); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("translations=%#v error=%v", translations, err)
		}
		if generator.calls != 0 || repository.reserveCalls != 0 || len(repository.audits) != 1 || repository.audits[0].Outcome != OutcomeInvalid {
			t.Fatalf("generator=%d reserve=%d audits=%#v", generator.calls, repository.reserveCalls, repository.audits)
		}
	}
}

func TestServiceMapsBoundedFailuresAndAuditsEveryOutcome(t *testing.T) {
	validItem := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleVideos, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "來源"}}}
	tests := []struct {
		name        string
		contentErr  error
		generator   *generatorStub
		wantErr     error
		wantOutcome string
		wantRelease int
	}{
		{name: "not found", contentErr: content.ErrNotFound, generator: &generatorStub{}, wantErr: ErrNotFound, wantOutcome: OutcomeNotFound},
		{name: "content filtered", generator: &generatorStub{err: ErrContentFiltered}, wantErr: ErrContentFiltered, wantOutcome: OutcomeContentFiltered},
		{name: "provider", generator: &generatorStub{err: errors.New("private provider body")}, wantErr: ErrProvider, wantOutcome: OutcomeProviderFailure},
		{name: "timeout", generator: &generatorStub{err: context.DeadlineExceeded}, wantErr: ErrTimeout, wantOutcome: OutcomeTimeout},
		{name: "unknown output", generator: &generatorStub{result: Result{Fields: map[string]string{"title": "譯", "extra": "private output"}}}, wantErr: ErrProvider, wantOutcome: OutcomeOutputValidationFailure},
		{name: "missing title", generator: &generatorStub{result: Result{Fields: map[string]string{}}}, wantErr: ErrProvider, wantOutcome: OutcomeOutputValidationFailure},
		{name: "invalid UTF-8", generator: &generatorStub{result: Result{Fields: map[string]string{"title": string([]byte{0xff})}}}, wantErr: ErrProvider, wantOutcome: OutcomeOutputValidationFailure},
		{name: "oversized", generator: &generatorStub{result: Result{Fields: map[string]string{"title": strings.Repeat("長", 201)}}}, wantErr: ErrProvider, wantOutcome: OutcomeOutputValidationFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &translationRepositoryStub{}
			service := NewService(contentSourceStub{item: validItem, err: test.contentErr}, bulletinSourceStub{}, test.generator, repository, testServiceConfig())
			_, err := service.Preview(context.Background(), previewRequest("videos"))
			if !errors.Is(err, test.wantErr) || len(repository.audits) != 1 || repository.audits[0].Outcome != test.wantOutcome || repository.releaseCalls != test.wantRelease {
				t.Fatalf("error=%v release=%d audit=%#v", err, repository.releaseCalls, repository.audits)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("error leaked content: %v", err)
			}
		})
	}
}

func TestTranslationAuditsCancellationWithoutReleasingAnUnreservedQuota(t *testing.T) {
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleVideos, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "來源"}}}
	repository := &translationRepositoryStub{}
	service := NewService(contentSourceStub{item: item}, nil, &generatorStub{waitForContext: true}, repository, testServiceConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = service.Preview(ctx, previewRequest("videos"))
	if repository.reserveCalls != 0 || repository.releaseCalls != 0 || repository.auditContextErr != nil || len(repository.audits) != 1 {
		t.Fatalf("reserve=%d release=%d audit context=%v audits=%d", repository.reserveCalls, repository.releaseCalls, repository.auditContextErr, len(repository.audits))
	}
}

func TestServiceBoundsGeneratorWithChildTimeout(t *testing.T) {
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleVideos, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "來源"}}}
	repository := &translationRepositoryStub{}
	config := testServiceConfig()
	config.HandlerTimeout = time.Millisecond
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, &generatorStub{waitForContext: true}, repository, config)
	if _, err := service.Preview(context.Background(), previewRequest("videos")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if len(repository.audits) != 1 || repository.audits[0].Outcome != OutcomeTimeout {
		t.Fatalf("audits = %#v", repository.audits)
	}
}

func TestServiceAuditIsExactContentFreeAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleVideos, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "private source"}}}
	repository := &translationRepositoryStub{}
	config := testServiceConfig()
	config.Now = func() time.Time { return now }
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, &generatorStub{result: Result{Fields: map[string]string{"title": "private output"}}}, repository, config)
	if _, err := service.Preview(context.Background(), previewRequest("videos")); err != nil {
		t.Fatal(err)
	}
	want := AuditEvent{Action: "translation_preview", ResourceType: "videos", ResourceID: item.ID, Actor: "actor-1", SourceVersion: 7, SourceLocale: "zh-Hant", TargetLocale: "ja", Provider: "azure-openai", Deployment: "cms-translator", PromptVersion: PromptVersion, CharacterCount: 14, Outcome: OutcomeSuccess, CreatedAt: now}
	if !reflect.DeepEqual(repository.audits, []AuditEvent{want}) {
		t.Fatalf("audit = %#v, want %#v", repository.audits, want)
	}

	repository.auditErr = errors.New("database failed")
	if _, err := service.Preview(context.Background(), previewRequest("videos")); !errors.Is(err, ErrAudit) {
		t.Fatalf("audit error = %v", err)
	}
}

func TestServiceUsesCurrentBulletinByteLimitsAndContentRuneLimits(t *testing.T) {
	bulletin := bulletins.Issue{ID: "10000000-0000-4000-8000-000000000001", Version: 7, Versions: []bulletins.Version{{Locale: "zh-Hant", Title: "來源"}}}
	bulletinService := NewService(contentSourceStub{}, bulletinSourceStub{issue: bulletin}, &generatorStub{result: Result{Fields: map[string]string{"title": strings.Repeat("日", 67), "subtitle": ""}}}, &translationRepositoryStub{}, testServiceConfig())
	if _, err := bulletinService.Preview(context.Background(), previewRequest("bulletins")); !errors.Is(err, ErrProvider) {
		t.Fatalf("201-byte bulletin title error = %v, want ErrProvider", err)
	}

	item := content.Item{ID: bulletin.ID, Module: content.ModuleVideos, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "來源"}}}
	contentService := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, &generatorStub{result: Result{Fields: map[string]string{"title": strings.Repeat("日", 200)}}}, &translationRepositoryStub{}, testServiceConfig())
	if _, err := contentService.Preview(context.Background(), previewRequest("videos")); err != nil {
		t.Fatalf("200-rune content title error = %v", err)
	}
}

func TestServiceSpanAttributesContainOnlyBoundedMetadata(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, span := provider.Tracer("test").Start(context.Background(), "translation")
	item := content.Item{ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleVideos, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Title: "private source"}}}
	service := NewService(contentSourceStub{item: item}, bulletinSourceStub{}, &generatorStub{result: Result{Fields: map[string]string{"title": "private output"}}}, &translationRepositoryStub{}, testServiceConfig())
	if _, err := service.Preview(ctx, previewRequest("videos")); err != nil {
		t.Fatal(err)
	}
	span.End()
	attributes := recorder.Ended()[0].Attributes()
	wantKeys := map[string]bool{"translation.resource_type": true, "translation.target_locale": true, "translation.outcome": true, "translation.prompt_version": true, "translation.deployment": true, "translation.character_count": true, "translation.duration_ms": true}
	for _, value := range attributes {
		key := string(value.Key)
		if !wantKeys[key] {
			t.Errorf("unexpected attribute %q", key)
		}
		text := value.Value.Emit()
		if strings.Contains(text, "private") || strings.Contains(text, "actor-1") || strings.Contains(text, item.ID) {
			t.Errorf("private span attribute %q=%q", key, text)
		}
		delete(wantKeys, key)
	}
	if len(wantKeys) != 0 {
		t.Fatalf("missing attributes %#v", wantKeys)
	}
}

func TestServiceDoesNotAuditOrTraceUnboundedInvalidLocales(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, span := provider.Tracer("test").Start(context.Background(), "translation")
	repository := &translationRepositoryStub{}
	request := previewRequest("videos")
	request.SourceLocale = "private source"
	request.TargetLocale = "private output"
	service := NewService(contentSourceStub{}, bulletinSourceStub{}, &generatorStub{}, repository, testServiceConfig())
	if _, err := service.Preview(ctx, request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}
	span.End()
	if len(repository.audits) != 1 || repository.audits[0].SourceLocale != "" || repository.audits[0].TargetLocale != "" {
		t.Fatalf("audit contains invalid locale input: %#v", repository.audits)
	}
	for _, value := range recorder.Ended()[0].Attributes() {
		if strings.Contains(value.Value.Emit(), "private") {
			t.Fatalf("span contains invalid locale input: %s", value.Value.Emit())
		}
	}
}

func previewRequest(module string) PreviewRequest {
	target := "ja"
	if module == "bulletins" {
		target = "en"
	}
	return PreviewRequest{Module: module, ResourceID: "10000000-0000-4000-8000-000000000001", SourceLocale: "zh-Hant", TargetLocale: target, ExpectedVersion: 7, Actor: "actor-1"}
}

func testServiceConfig() ServiceConfig {
	return ServiceConfig{Deployment: "cms-translator", HandlerTimeout: time.Second, SourceCharLimit: 20_000, ActorLimit: 10, DeploymentLimit: 60, ActorDailyLimit: 30, DeploymentDailyLimit: 300, Cooldown: 10 * time.Minute, Now: func() time.Time { return time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC) }}
}

func translated(fields map[string]string) map[string]string {
	result := make(map[string]string, len(fields))
	for key := range fields {
		result[key] = "translated-" + key
	}
	return result
}

type contentSourceStub struct {
	item content.Item
	err  error
}

func (s contentSourceStub) GetContent(context.Context, content.Module, string) (content.Item, error) {
	return s.item, s.err
}

type bulletinSourceStub struct {
	issue bulletins.Issue
	err   error
}

type savedSourceStub struct {
	source SavedSource
	err    error
}

func (s savedSourceStub) GetTranslationSource(context.Context, string, string) (SavedSource, error) {
	return s.source, s.err
}

func (s bulletinSourceStub) GetIssue(context.Context, string) (bulletins.Issue, error) {
	return s.issue, s.err
}

type generatorStub struct {
	request        Request
	result         Result
	err            error
	calls          int
	waitForContext bool
}

func (s *generatorStub) Generate(ctx context.Context, request Request) (Result, error) {
	s.calls++
	s.request = request
	if s.waitForContext {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	return s.result, s.err
}

type translationRepositoryStub struct {
	reserveCalls      int
	reserveErr        error
	reservation       Reservation
	releaseCalls      int
	releaseErr        error
	releaseContextErr error
	audits            []AuditEvent
	auditErr          error
	auditContextErr   error
}

func (s *translationRepositoryStub) ReleaseTranslation(ctx context.Context, reservation Reservation) error {
	s.releaseCalls++
	s.reservation = reservation
	s.releaseContextErr = ctx.Err()
	return s.releaseErr
}

func (s *translationRepositoryStub) ReserveTranslation(_ context.Context, reservation Reservation) error {
	s.reserveCalls++
	s.reservation = reservation
	return s.reserveErr
}

func (s *translationRepositoryStub) RecordTranslationAudit(ctx context.Context, event AuditEvent) error {
	s.auditContextErr = ctx.Err()
	s.audits = append(s.audits, event)
	return s.auditErr
}
