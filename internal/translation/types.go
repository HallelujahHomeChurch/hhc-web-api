package translation

import (
	"context"
	"errors"
	"time"
)

var ErrRateLimited = errors.New("translation rate limited")

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
