package translation

import "context"

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
