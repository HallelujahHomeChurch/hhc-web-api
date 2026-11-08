package publication

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

type SummaryGenerator interface {
	GenerateSummary(context.Context, translation.SummaryRequest) (string, error)
}
type SummaryRepository interface {
	LoadNewsSummary(context.Context, Event) (translation.SummaryRequest, bool, error)
	FinishNewsSummary(context.Context, Event, string, string, time.Time, time.Time) error
}

func (w *Worker) WithSummaries(generator SummaryGenerator) *Worker { w.summaries = generator; return w }

func (w *Worker) generateSummary(ctx context.Context, event Event) error {
	repo, ok := w.repository.(SummaryRepository)
	if !ok {
		return errors.New("summary repository unavailable")
	}
	request, current, err := repo.LoadNewsSummary(ctx, event)
	if err != nil {
		return err
	}
	if !current {
		return repo.FinishNewsSummary(ctx, event, "", "", time.Time{}, w.now().UTC())
	}
	if w.summaries == nil {
		return repo.FinishNewsSummary(ctx, event, "", "AI_DISABLED", time.Time{}, w.now().UTC())
	}
	providerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	summary, err := w.summaries.GenerateSummary(providerCtx, request)
	cancel()
	now := w.now().UTC()
	if err == nil {
		return repo.FinishNewsSummary(ctx, event, summary, "", time.Time{}, now)
	}
	var rate *translation.RateLimitError
	if errors.As(err, &rate) {
		return repo.FinishNewsSummary(ctx, event, "", "", now.Add(max(time.Second, rate.RetryAfter)), now)
	}
	code := "AI_FAILED"
	terminal := false
	if errors.Is(err, translation.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		code = "AI_TIMEOUT"
	}
	if errors.Is(err, translation.ErrContentFiltered) {
		code = "AI_FILTERED"
		terminal = true
	}
	if errors.Is(err, translation.ErrInvalidRequest) {
		code = "AI_INVALID"
		terminal = true
	}
	next := time.Time{}
	var retry struct {
		Failures int `json:"summaryFailures"`
	}
	if json.Unmarshal(event.Payload, &retry) != nil || retry.Failures < 0 {
		retry.Failures = w.maxAttempts
	}
	if !terminal && retry.Failures < w.maxAttempts-1 {
		next = now.Add(time.Duration(1<<min(retry.Failures, 5)) * 5 * time.Second)
	}
	return repo.FinishNewsSummary(ctx, event, "", code, next, now)
}
