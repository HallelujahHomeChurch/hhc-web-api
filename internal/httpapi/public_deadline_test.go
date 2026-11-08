package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

type deadlineContentRepository struct {
	contentRepository
	read func(context.Context) error
}

func (r *deadlineContentRepository) PublicContent(ctx context.Context, _ content.Module, _ string, _, _ int) (content.PublicPage, error) {
	return content.PublicPage{}, r.read(ctx)
}

func (r *deadlineContentRepository) ListContent(ctx context.Context, _ content.Module, _ content.ListOptions) (content.Page, error) {
	return content.Page{}, r.read(ctx)
}

func TestPublicReadDeadlineDoesNotApplyToAuthenticatedRoutes(t *testing.T) {
	called := false
	repo := &deadlineContentRepository{read: func(ctx context.Context) error {
		called = true
		if _, ok := ctx.Deadline(); ok {
			t.Error("public budget leaked to authenticated route")
		}
		return nil
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news", nil)
	trusted(request, "cms:news:read")
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, request)
	if !called || response.Code != http.StatusOK {
		t.Fatalf("called=%v status=%d", called, response.Code)
	}
}

func TestPublicReadDeadlineReachesRepository(t *testing.T) {
	var observed context.Context
	repo := &deadlineContentRepository{read: func(ctx context.Context) error {
		observed = ctx
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 25*time.Second || time.Until(deadline) < 24*time.Second {
			t.Errorf("public read must have its own 25s budget; deadline=%v present=%v", deadline, ok)
		}
		return nil
	}}
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/news", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if observed == nil || observed.Err() != context.Canceled {
		t.Fatal("handler return must release its request context")
	}
}

func TestPublicReadPreservesCallerCancellation(t *testing.T) {
	for _, cancelNow := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if cancelNow {
			cancel()
		}
		repo := &deadlineContentRepository{read: func(got context.Context) error {
			deadline, ok := got.Deadline()
			want, _ := ctx.Deadline()
			if !ok || !deadline.Equal(want) {
				t.Error("earlier caller deadline was extended")
			}
			<-got.Done()
			return got.Err()
		}}
		response := httptest.NewRecorder()
		contentTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/news", nil).WithContext(ctx))
		cancel()
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.Code)
		}
	}
}
