package translation

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidRequest    = errors.New("invalid translation request")
	ErrNotFound          = errors.New("translation source not found")
	ErrTranslationExists = errors.New("translation already exists")
	ErrVersionMismatch   = errors.New("translation source version mismatch")
	ErrRateLimited       = errors.New("translation rate limited")
	ErrAudit             = errors.New("translation audit failed")
	ErrInternal          = errors.New("translation internal error")
)

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

type Reservation struct {
	Actor                 string
	ResourceType          string
	ResourceID            string
	SourceVersion         int64
	TargetLocale          string
	Now                   time.Time
	ActorMinuteLimit      int
	DeploymentMinuteLimit int
	ActorDailyLimit       int
	DeploymentDailyLimit  int
	Cooldown              time.Duration
}

type Request struct {
	Module       string
	SourceLocale string
	TargetLocale string
	Fields       map[string]string
}

type Result struct {
	Fields map[string]string
}

type Generator interface {
	Generate(context.Context, Request) (Result, error)
}

type SavedSource struct {
	ResourceID       string            `json:"resourceId"`
	SourceLocale     string            `json:"sourceLocale"`
	Channel          string            `json:"channel"`
	Version          int64             `json:"version"`
	Fields           map[string]string `json:"fields"`
	AvailableLocales []string          `json:"availableLocales"`
}

type SavedSourceLoader interface {
	GetTranslationSource(context.Context, string, string) (SavedSource, error)
}

type PreviewRequest struct {
	Module          string
	ResourceID      string
	SourceLocale    string
	TargetLocale    string
	ExpectedVersion int64
	Actor           string
	ReplaceExisting bool
}

type Preview struct {
	SourceLocale  string            `json:"sourceLocale"`
	TargetLocale  string            `json:"targetLocale"`
	SourceVersion int64             `json:"sourceVersion"`
	Translation   map[string]string `json:"translation"`
}

type AuditEvent struct {
	Action         string
	ResourceType   string
	ResourceID     string
	Actor          string
	SourceVersion  int64
	SourceLocale   string
	TargetLocale   string
	Provider       string
	Deployment     string
	PromptVersion  string
	CharacterCount int
	Duration       time.Duration
	Outcome        string
	CreatedAt      time.Time
}
