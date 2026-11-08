package httpapi

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type legalRepoStub struct{}

func (legalRepoStub) Draft(context.Context, legal.Scope) (legal.Draft, error) {
	return legal.Draft{}, legal.ErrNotFound
}
func (legalRepoStub) SaveDraft(context.Context, legal.Draft, int64, string) (legal.Draft, error) {
	return legal.Draft{}, legal.ErrInvalid
}
func (legalRepoStub) Publish(context.Context, legal.Draft, int64, string) ([]legal.Snapshot, error) {
	return nil, legal.ErrInvalid
}
func (legalRepoStub) Current(context.Context, legal.Scope, string) (legal.Snapshot, error) {
	return legal.Snapshot{}, legal.ErrNotFound
}
func (legalRepoStub) Resolve(context.Context, string) (legal.Snapshot, error) {
	return legal.Snapshot{}, legal.ErrNotFound
}

type qualificationStub struct {
	allowed bool
	err     error
}

func (q qualificationStub) CheckMemberQualification(_ context.Context, subject string) (operationsclient.MembershipQualification, error) {
	return operationsclient.MembershipQualification{SubjectID: subject, Qualified: q.allowed}, q.err
}
func TestMemberLegalCurrentFailsClosedAndNeverReturnsPrivateFallback(t *testing.T) {
	for _, q := range []qualificationStub{{}, {err: errors.New("down")}} {
		h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true, nil).WithLegal(legal.NewService(legalRepoStub{}), q).Routes()
		req := httptest.NewRequest(http.MethodGet, "/api/member/legal/current?locale=en", nil)
		trusted(req, "")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 403 && rec.Code != 503 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(rec.Body.String(), "snapshotId") {
			t.Fatalf("private response=%s", rec.Body.String())
		}
	}
}
func TestLegalPrivateRoutesDenyUntrustedCaller(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true, nil).WithLegal(legal.NewService(legalRepoStub{}), qualificationStub{}).Routes()
	req := httptest.NewRequest(http.MethodPost, "/priv/legal/snapshots/resolve", strings.NewReader(`{"snapshotId":"01995fd2-6f6c-7b40-8707-6503edc362c8"}`))
	req.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("code=%d", rec.Code)
	}
}
