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
	ReserveTranslation(context.Context, string, time.Time, int, int) error
	RecordTranslationAudit(context.Context, AuditEvent) error
}

type ServiceConfig struct {
	Deployment      string
	HandlerTimeout  time.Duration
	SourceCharLimit int
	ActorLimit      int
	DeploymentLimit int
	Now             func() time.Time
}

type Service struct {
	content    ContentSource
	bulletins  BulletinSource
	generator  Generator
	repository LimiterAuditor
	config     ServiceConfig
}

func NewService(contentSource ContentSource, bulletinSource BulletinSource, generator Generator, repository LimiterAuditor, config ServiceConfig) *Service {
	return &Service{content: contentSource, bulletins: bulletinSource, generator: generator, repository: repository, config: config}
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
		if errors.Is(err, content.ErrNotFound) || errors.Is(err, bulletins.ErrNotFound) {
			return s.finish(ctx, state, started, OutcomeNotFound, Preview{}, ErrNotFound)
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
	if err := s.repository.ReserveTranslation(ctx, request.Actor, started, s.config.ActorLimit, s.config.DeploymentLimit); err != nil {
		if errors.Is(err, ErrRateLimited) {
			return s.finish(ctx, state, started, OutcomeRateLimited, Preview{}, ErrRateLimited)
		}
		return s.finish(ctx, state, started, OutcomeInternalFailure, Preview{}, ErrInternal)
	}

	providerCtx, cancel := context.WithTimeout(ctx, s.config.HandlerTimeout)
	defer cancel()
	result, err := s.generator.Generate(providerCtx, Request{Module: request.Module, SourceLocale: request.SourceLocale, TargetLocale: request.TargetLocale, Fields: fields})
	if err != nil {
		if errors.Is(err, ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(providerCtx.Err(), context.DeadlineExceeded) {
			return s.finish(ctx, state, started, OutcomeTimeout, Preview{}, ErrTimeout)
		}
		return s.finish(ctx, state, started, OutcomeProviderFailure, Preview{}, ErrProvider)
	}
	if !validResult(request.Module, fields, result.Fields) {
		return s.finish(ctx, state, started, OutcomeOutputValidationFailure, Preview{}, ErrProvider)
	}
	for key, value := range result.Fields {
		result.Fields[key] = normalizeLines(value)
	}
	preview := Preview{SourceLocale: request.SourceLocale, TargetLocale: request.TargetLocale, SourceVersion: version, Translation: result.Fields}
	return s.finish(ctx, state, started, OutcomeSuccess, preview, nil)
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
	if validTarget(state.request.TargetLocale) {
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
	if !resourceIDPattern.MatchString(request.ResourceID) || strings.TrimSpace(request.Actor) == "" || request.ExpectedVersion < 1 || request.SourceLocale != "zh-Hant" || !validTarget(request.TargetLocale) {
		return false
	}
	switch request.Module {
	case "news", "history", "videos", "bulletins":
		return true
	default:
		return false
	}
}

func validTarget(locale string) bool {
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
	if strings.TrimSpace(result["title"]) == "" {
		return false
	}
	limits := map[string]int{"title": 200, "subtitle": 300, "body": 100_000, "imageAlt": 300}
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
	return module == "news" || module == "history" || module == "videos" || module == "bulletins"
}
