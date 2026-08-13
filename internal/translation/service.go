package translation

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var resourceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const (
	OutcomeSuccess                 = "success"
	OutcomeInvalid                 = "invalid"
	OutcomeNotFound                = "not_found"
	OutcomeVersionMismatch         = "version_mismatch"
	OutcomeExistingTarget          = "existing_target"
	OutcomeRateLimited             = "rate_limited"
	OutcomeContentFiltered         = "content_filtered"
	OutcomeProviderFailure         = "provider_failure"
	OutcomeTimeout                 = "timeout"
	OutcomeOutputValidationFailure = "output_validation_failure"
	OutcomeInternalFailure         = "internal_failure"
)

type ContentSource interface {
	GetContent(context.Context, content.Module, string) (content.Item, error)
}

type BulletinSource interface {
	GetIssue(context.Context, string) (bulletins.Issue, error)
}

type LimiterAuditor interface {
	ReserveTranslation(context.Context, Reservation) error
	ReleaseTranslation(context.Context, Reservation) error
	RecordTranslationAudit(context.Context, AuditEvent) error
}

type ServiceConfig struct {
	Deployment           string
	HandlerTimeout       time.Duration
	SourceCharLimit      int
	ActorLimit           int
	DeploymentLimit      int
	ActorDailyLimit      int
	DeploymentDailyLimit int
	Cooldown             time.Duration
	Now                  func() time.Time
}

type Service struct {
	content    ContentSource
	bulletins  BulletinSource
	sources    SavedSourceLoader
	generator  Generator
	repository LimiterAuditor
	config     ServiceConfig
}

func NewService(contentSource ContentSource, bulletinSource BulletinSource, generator Generator, repository LimiterAuditor, config ServiceConfig, sources ...SavedSourceLoader) *Service {
	service := &Service{content: contentSource, bulletins: bulletinSource, generator: generator, repository: repository, config: config}
	if len(sources) > 0 {
		service.sources = sources[0]
	}
	return service
}

func (s *Service) Preview(ctx context.Context, request PreviewRequest) (Preview, error) {
	started := s.config.Now().UTC()
	state := previewState{request: request, sourceVersion: request.ExpectedVersion}
	if !validPreviewRequest(request) {
		return s.finish(ctx, state, started, OutcomeInvalid, Preview{}, ErrInvalidRequest)
	}

	fields, version, targetExists, err := s.load(ctx, request)
	state.sourceVersion = version
	if err != nil {
		if errors.Is(err, content.ErrNotFound) || errors.Is(err, bulletins.ErrNotFound) || errors.Is(err, ErrNotFound) {
			return s.finish(ctx, state, started, OutcomeNotFound, Preview{}, ErrNotFound)
		}
		if errors.Is(err, ErrInvalidRequest) {
			return s.finish(ctx, state, started, OutcomeInvalid, Preview{}, ErrInvalidRequest)
		}
		return s.finish(ctx, state, started, OutcomeInternalFailure, Preview{}, ErrInternal)
	}
	if version != request.ExpectedVersion {
		return s.finish(ctx, state, started, OutcomeVersionMismatch, Preview{}, ErrVersionMismatch)
	}
	if fields == nil || !hasText(fields) {
		return s.finish(ctx, state, started, OutcomeInvalid, Preview{}, ErrInvalidRequest)
	}
	if targetExists && !request.ReplaceExisting {
		return s.finish(ctx, state, started, OutcomeExistingTarget, Preview{}, ErrTranslationExists)
	}
	for key, value := range fields {
		fields[key] = normalizeLines(value)
		state.characterCount += utf8.RuneCountInString(fields[key])
	}
	if state.characterCount > s.config.SourceCharLimit {
		return s.finish(ctx, state, started, OutcomeInvalid, Preview{}, ErrInvalidRequest)
	}
	reservation := Reservation{
		Actor: request.Actor, ResourceType: resourceType(request.Module), ResourceID: request.ResourceID, SourceVersion: version, TargetLocale: request.TargetLocale, Now: started,
		ActorMinuteLimit: s.config.ActorLimit, DeploymentMinuteLimit: s.config.DeploymentLimit,
		ActorDailyLimit: s.config.ActorDailyLimit, DeploymentDailyLimit: s.config.DeploymentDailyLimit, Cooldown: s.config.Cooldown,
	}
	if err := s.repository.ReserveTranslation(ctx, reservation); err != nil {
		if errors.Is(err, ErrRateLimited) {
			return s.finish(ctx, state, started, OutcomeRateLimited, Preview{}, err)
		}
		return s.finish(ctx, state, started, OutcomeInternalFailure, Preview{}, ErrInternal)
	}

	providerCtx, cancel := context.WithTimeout(ctx, s.config.HandlerTimeout)
	defer cancel()
	result, err := s.generator.Generate(providerCtx, Request{Module: request.Module, SourceLocale: request.SourceLocale, TargetLocale: request.TargetLocale, Fields: fields})
	if err != nil {
		if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(providerCtx.Err(), context.DeadlineExceeded) {
			return s.failAfterReservation(ctx, reservation, state, started, OutcomeTimeout, ErrTimeout)
		}
		if errors.Is(err, ErrContentFiltered) {
			return s.failAfterReservation(ctx, reservation, state, started, OutcomeContentFiltered, ErrContentFiltered)
		}
		return s.failAfterReservation(ctx, reservation, state, started, OutcomeProviderFailure, ErrProvider)
	}
	if !validResult(request.Module, fields, result.Fields) {
		return s.failAfterReservation(ctx, reservation, state, started, OutcomeOutputValidationFailure, ErrProvider)
	}
	for key, value := range result.Fields {
		result.Fields[key] = normalizeLines(value)
	}
	retryAfterSeconds := int64(s.config.Cooldown / time.Second)
	if s.config.Cooldown%time.Second != 0 {
		retryAfterSeconds++
	}
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	preview := Preview{SourceLocale: request.SourceLocale, TargetLocale: request.TargetLocale, SourceVersion: version, Translation: result.Fields, RetryAfterSeconds: retryAfterSeconds}
	return s.finish(ctx, state, started, OutcomeSuccess, preview, nil)
}

func (s *Service) failAfterReservation(ctx context.Context, reservation Reservation, state previewState, started time.Time, outcome string, resultErr error) (Preview, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.repository.ReleaseTranslation(cleanupCtx, reservation); err != nil {
		return s.finish(cleanupCtx, state, started, OutcomeInternalFailure, Preview{}, ErrInternal)
	}
	return s.finish(cleanupCtx, state, started, outcome, Preview{}, resultErr)
}

type previewState struct {
	request        PreviewRequest
	sourceVersion  int64
	characterCount int
}

func (s *Service) finish(ctx context.Context, state previewState, started time.Time, outcome string, preview Preview, resultErr error) (Preview, error) {
	duration := s.config.Now().UTC().Sub(started)
	sourceLocale := ""
	if state.request.SourceLocale == "zh-Hant" {
		sourceLocale = state.request.SourceLocale
	}
	targetLocale := ""
	if validTarget(state.request.Module, state.request.TargetLocale) {
		targetLocale = state.request.TargetLocale
	}
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("translation.resource_type", resourceType(state.request.Module)),
		attribute.String("translation.target_locale", targetLocale),
		attribute.String("translation.outcome", outcome),
		attribute.String("translation.prompt_version", PromptVersion),
		attribute.String("translation.deployment", s.config.Deployment),
		attribute.Int("translation.character_count", state.characterCount),
		attribute.Int64("translation.duration_ms", duration.Milliseconds()),
	)
	if identifiable(state.request) {
		event := AuditEvent{
			Action: "translation_preview", ResourceType: resourceType(state.request.Module), ResourceID: state.request.ResourceID, Actor: state.request.Actor,
			SourceVersion: state.sourceVersion, SourceLocale: sourceLocale, TargetLocale: targetLocale,
			Provider: "azure-openai", Deployment: s.config.Deployment, PromptVersion: PromptVersion,
			CharacterCount: state.characterCount, Duration: duration, Outcome: outcome, CreatedAt: started,
		}
		if err := s.repository.RecordTranslationAudit(ctx, event); err != nil {
			return Preview{}, ErrAudit
		}
	}
	return preview, resultErr
}

func (s *Service) load(ctx context.Context, request PreviewRequest) (map[string]string, int64, bool, error) {
	if request.Module == "campaigns" || request.Module == "campaign-schedules" {
		if s.sources == nil {
			return nil, 0, false, ErrInternal
		}
		source, err := s.sources.GetTranslationSource(ctx, request.Module, request.ResourceID)
		if err != nil {
			return nil, 0, false, err
		}
		if source.ResourceID != request.ResourceID || source.SourceLocale != "zh-Hant" || source.Version < 1 || (source.Channel != "email" && source.Channel != "web_push") || len(source.Fields) != 2 {
			return nil, source.Version, false, ErrInvalidRequest
		}
		if _, ok := source.Fields["subject"]; !ok {
			return nil, source.Version, false, ErrInvalidRequest
		}
		if _, ok := source.Fields["body"]; !ok {
			return nil, source.Version, false, ErrInvalidRequest
		}
		targetExists := false
		for _, locale := range source.AvailableLocales {
			if locale == request.TargetLocale {
				targetExists = true
			}
		}
		return source.Fields, source.Version, targetExists, nil
	}
	if request.Module == "bulletins" {
		issue, err := s.bulletins.GetIssue(ctx, request.ResourceID)
		if err != nil {
			return nil, 0, false, err
		}
		var source *bulletins.Version
		targetExists := false
		for index := range issue.Versions {
			if issue.Versions[index].Locale == request.SourceLocale {
				source = &issue.Versions[index]
			}
			if issue.Versions[index].Locale == request.TargetLocale {
				targetExists = true
			}
		}
		if source == nil {
			return nil, issue.Version, targetExists, nil
		}
		return map[string]string{"title": source.Title, "subtitle": source.Subtitle}, issue.Version, targetExists, nil
	}
	item, err := s.content.GetContent(ctx, content.Module(request.Module), request.ResourceID)
	if err != nil {
		return nil, 0, false, err
	}
	var source *content.Translation
	targetExists := false
	for index := range item.Translations {
		if item.Translations[index].Locale == request.SourceLocale {
			source = &item.Translations[index]
		}
		if item.Translations[index].Locale == request.TargetLocale {
			targetExists = true
		}
	}
	if source == nil {
		return nil, item.Version, targetExists, nil
	}
	switch request.Module {
	case "news":
		return map[string]string{"title": source.Title, "body": source.Body, "imageAlt": source.ImageAlt}, item.Version, targetExists, nil
	case "history":
		return map[string]string{"title": source.Title, "body": source.Body}, item.Version, targetExists, nil
	default:
		return map[string]string{"title": source.Title}, item.Version, targetExists, nil
	}
}

func validPreviewRequest(request PreviewRequest) bool {
	if !resourceIDPattern.MatchString(request.ResourceID) || strings.TrimSpace(request.Actor) == "" || request.ExpectedVersion < 1 || request.SourceLocale != "zh-Hant" || !validTarget(request.Module, request.TargetLocale) {
		return false
	}
	switch request.Module {
	case "news", "history", "videos", "bulletins", "campaigns", "campaign-schedules":
		return true
	default:
		return false
	}
}

func validTarget(module, locale string) bool {
	if module == "bulletins" {
		return bulletins.IsBulletinTranslationTarget(locale)
	}
	if module == "campaigns" || module == "campaign-schedules" {
		return locale == "en" || locale == "ja" || locale == "ko"
	}
	return locale == "zh-Hans" || locale == "en" || locale == "ja" || locale == "ko"
}

func identifiable(request PreviewRequest) bool {
	return resourceIDPattern.MatchString(request.ResourceID) && resourceType(request.Module) != ""
}

func resourceType(module string) string {
	if module == "bulletins" {
		return "bulletin"
	}
	if module == "news" || module == "history" || module == "videos" {
		return module
	}
	if module == "campaigns" {
		return "campaign"
	}
	if module == "campaign-schedules" {
		return "campaign_schedule"
	}
	return ""
}

func normalizeLines(value string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(value)
}

func hasText(fields map[string]string) bool {
	for _, value := range fields {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func validResult(module string, source, result map[string]string) bool {
	if len(source) != len(result) {
		return false
	}
	for key := range source {
		value, ok := result[key]
		if !ok || !utf8.ValidString(value) {
			return false
		}
	}
	required := "title"
	if module == "campaigns" || module == "campaign-schedules" {
		required = "subject"
	}
	if strings.TrimSpace(result[required]) == "" {
		return false
	}
	limits := map[string]int{"title": 200, "subject": 200, "subtitle": 300, "body": 100_000, "imageAlt": 300}
	for key, value := range result {
		limit, ok := limits[key]
		length := utf8.RuneCountInString(normalizeLines(value))
		if module == "bulletins" {
			length = len(normalizeLines(value))
		}
		if !ok || length > limit {
			return false
		}
	}
	return module == "news" || module == "history" || module == "videos" || module == "bulletins" || module == "campaigns" || module == "campaign-schedules"
}
