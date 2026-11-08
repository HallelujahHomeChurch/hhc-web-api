package publication

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
	"testing"
	"time"
)

type summaryRepositoryStub struct {
	workerRepository
	current       bool
	summary, code string
	next          time.Time
}

func (r *summaryRepositoryStub) LoadNewsSummary(context.Context, Event) (translation.SummaryRequest, bool, error) {
	return translation.SummaryRequest{}, r.current, nil
}
func (r *summaryRepositoryStub) FinishNewsSummary(_ context.Context, _ Event, summary, code string, next, now time.Time) error {
	r.summary, r.code, r.next = summary, code, next
	return nil
}

type summaryGeneratorStub struct {
	calls     int
	err       error
	remaining time.Duration
}

func (g *summaryGeneratorStub) GenerateSummary(ctx context.Context, _ translation.SummaryRequest) (string, error) {
	g.calls++
	deadline, _ := ctx.Deadline()
	g.remaining = time.Until(deadline)
	return "AI summary", g.err
}

func TestSummaryWorkerUsesDedicatedFencedPath(t *testing.T) {
	for _, tc := range []struct {
		name              string
		current, disabled bool
		err               error
		code              string
		deferWork         bool
		payload           string
	}{
		{name: "ready", current: true}, {name: "obsolete"}, {name: "disabled", current: true, disabled: true, code: "AI_DISABLED"},
		{name: "filter", current: true, err: translation.ErrContentFiltered, code: "AI_FILTERED"},
		{name: "timeout", current: true, err: translation.ErrTimeout, code: "AI_TIMEOUT", deferWork: true},
		{name: "quota", current: true, err: &translation.RateLimitError{RetryAfter: time.Hour}, deferWork: true},
		{name: "exhausted", current: true, err: translation.ErrTimeout, code: "AI_TIMEOUT", payload: `{"summaryFailures":4}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &summaryRepositoryStub{workerRepository: workerRepository{event: Event{ID: "event", EventType: "news.summary.generate", Attempts: 20, Payload: []byte(`{}`)}}, current: tc.current}
			if tc.payload != "" {
				repo.event.Payload = []byte(tc.payload)
			}
			generator := &summaryGeneratorStub{err: tc.err}
			worker := NewWorker(repo, nil, 5)
			if !tc.disabled {
				worker.WithSummaries(generator)
			}
			if _, err := worker.processNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if repo.failCount != 0 || repo.retryCount != 0 || repo.code != tc.code || repo.next.IsZero() == tc.deferWork {
				t.Fatalf("repo=%+v", repo)
			}
			if generator.calls > 0 && (generator.remaining > 20*time.Second || generator.remaining < 19*time.Second) {
				t.Fatalf("deadline=%s", generator.remaining)
			}
			if !tc.current && generator.calls != 0 {
				t.Fatal("obsolete generated")
			}
		})
	}
}
