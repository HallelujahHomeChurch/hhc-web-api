package httpapi

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type dismissalHTTPRepository struct {
	contentRepository
	owner string
	ref   content.StatementRef
	err   error
}

func (r *dismissalHTTPRepository) GetStatementDismissal(_ context.Context, owner string, ref content.StatementRef) (content.StatementDismissal, error) {
	r.owner = owner
	r.ref = ref
	return content.StatementDismissal{StatementID: ref.StatementID, PublishedVersion: ref.PublishedVersion}, r.err
}
func (r *dismissalHTTPRepository) DismissStatement(_ context.Context, owner string, ref content.StatementRef) (content.StatementDismissal, error) {
	r.owner = owner
	r.ref = ref
	return content.StatementDismissal{StatementID: ref.StatementID, PublishedVersion: ref.PublishedVersion, Dismissed: true}, r.err
}

func TestStatementDismissalHTTP(t *testing.T) {
	const id = "018f0000-0000-7000-8000-000000000001"
	for _, tc := range []struct {
		name, method, query, body string
		err                       error
		status                    int
	}{
		{name: "get", method: "GET", query: "?publishedVersion=7", status: 200},
		{name: "put", method: "PUT", body: `{"publishedVersion":7}`, status: 200},
		{name: "zero", method: "PUT", body: `{"publishedVersion":0}`, status: 422},
		{name: "unsafe", method: "PUT", body: `{"publishedVersion":9007199254740992}`, status: 422},
		{name: "unknown field", method: "PUT", body: `{"publishedVersion":7,"accountId":"foreign"}`, status: 422},
		{name: "trailing", method: "PUT", body: `{"publishedVersion":7}{}`, status: 422},
		{name: "missing query", method: "GET", status: 422},
		{name: "duplicate query", method: "GET", query: "?publishedVersion=7&publishedVersion=8", status: 422},
		{name: "method", method: "DELETE", status: 405},
		{name: "conflict", method: "PUT", body: `{"publishedVersion":7}`, err: content.ErrStatementVersionChanged, status: 409},
		{name: "missing", method: "GET", query: "?publishedVersion=7", err: content.ErrNotFound, status: 404},
		{name: "unavailable", method: "GET", query: "?publishedVersion=7", err: errors.New("db failed"), status: 500},
		{name: "oversize", method: "PUT", body: strings.Repeat(" ", 1024) + `{"publishedVersion":7}`, status: 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dismissalHTTPRepository{err: tc.err}
			req := httptest.NewRequest(tc.method, "/api/me/statements/"+id+"/dismissal"+tc.query, strings.NewReader(tc.body))
			req.Header.Set("Dapr-Caller-App-Id", "api-gateway")
			req.Header.Set("X-HHC-User-ID", id)
			req.Header.Set("X-HHC-Auth-Provider", "account-api")
			req.Header.Set("Authorization", "Bearer fixture")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			contentTestHandler(repo).ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("missing no-store")
			}
			if tc.status == 200 && (repo.owner != id || repo.ref.PublishedVersion != 7) {
				t.Fatalf("owner=%q ref=%+v", repo.owner, repo.ref)
			}
		})
	}
}

func TestStatementDismissalRequiresTrustedHuman(t *testing.T) {
	const id = "018f0000-0000-7000-8000-000000000001"
	for _, caller := range []string{"", "forged", "api-gateway"} {
		req := httptest.NewRequest(http.MethodPut, "/api/me/statements/"+id+"/dismissal", strings.NewReader(`{"publishedVersion":7}`))
		req.Header.Set("Dapr-Caller-App-Id", caller)
		req.Header.Set("X-HHC-User-ID", id)
		req.Header.Set("X-HHC-Auth-Provider", "account-api")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		contentTestHandler(&dismissalHTTPRepository{}).ServeHTTP(w, req)
		expected := 401
		if caller == "api-gateway" {
			expected = 200
		}
		if w.Code != expected {
			t.Fatalf("trusted caller=%q status=%d", caller, w.Code)
		}
	}
}
