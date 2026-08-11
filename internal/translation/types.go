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
