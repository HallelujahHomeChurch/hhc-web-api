package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type cancellationListRepository struct {
	recordings.Repository
	query func(context.Context) error
}

func (r cancellationListRepository) ListVisible(ctx context.Context, _ time.Time) ([]recordings.Recording, error) {
	return nil, r.query(ctx)
}
func (r cancellationListRepository) ListVisiblePage(ctx context.Context, _ time.Time, _ *time.Time, _ string, _ int, _ string) ([]recordings.Recording, error) {
	return nil, r.query(ctx)
}

func TestMemberListRequestCancellation(t *testing.T) {
	for _, path := range []string{"/api/member/recordings", "/api/member/recordings?limit=12"} {
		for _, tc := range []struct {
			name   string
			cancel bool
			err    error
			status int
		}{
			{"cancelled query", true, context.Canceled, 499},
			{"wrapped cancellation", true, fmt.Errorf("query: %w", context.Canceled), 499},
			{"independent cancellation", false, context.Canceled, 500},
			{"real failure races cancellation", true, errors.New("database failure private detail"), 500},
			{"dependency deadline", false, context.DeadlineExceeded, 500},
			{"request deadline", false, context.DeadlineExceeded, 500},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				if tc.name == "request deadline" {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				}
				defer cancel()
				repo := cancellationListRepository{query: func(got context.Context) error {
					if got != ctx {
						t.Fatal("request context not propagated")
					}
					if tc.cancel {
						cancel()
						<-got.Done()
					}
					return tc.err
				}}
				h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, time.Now))
				response := httptest.NewRecorder()
				h.memberListRecordings(response, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body)
				}
				if strings.Contains(response.Body.String(), "private detail") {
					t.Fatal("leaked underlying error")
				}
			})
		}
	}
}
