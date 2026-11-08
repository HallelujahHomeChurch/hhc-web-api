package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

type watermarkStoreStub struct {
	collisions     int
	receipts       []bulletinwatermark.Receipt
	insertErr      error
	source         bulletinwatermark.Source
	applyRemaining int64
}

func (s *watermarkStoreStub) PublishedWatermarkSource(context.Context, string, string) (bulletinwatermark.Source, error) {
	return s.source, nil
}
func (s *watermarkStoreStub) InsertWatermarkReceipt(_ context.Context, v bulletinwatermark.Receipt) error {
	if s.collisions > 0 {
		s.collisions--
		return bulletinwatermark.ErrCodeCollision
	}
	if s.insertErr != nil {
		return s.insertErr
	}
	s.receipts = append(s.receipts, v)
	return nil
}
func (s *watermarkStoreStub) LookupWatermarkReceipt(context.Context, string, string, time.Time) (bulletinwatermark.Receipt, error) {
	return bulletinwatermark.Receipt{}, bulletinwatermark.ErrNotFound
}
func (s *watermarkStoreStub) CleanupWatermarkAccount(context.Context, string, string) (int64, int64, error) {
	return 3, s.applyRemaining, nil
}

type fullBulletinDownloadStub struct {
	calls         int
	rangeReceived bool
}

func (s *fullBulletinDownloadStub) AuthorizedDownload(_ context.Context, method, asset, byteRange, ifRange string) (*http.Response, error) {
	s.calls++
	s.rangeReceived = byteRange != "" || ifRange != ""
	return &http.Response{StatusCode: 200, Header: http.Header{"ETag": {`"original"`}, "Content-Length": {"999"}}, Body: io.NopCloser(strings.NewReader("%PDF-original"))}, nil
}
func TestWatermarkPrivateRoutesRejectOtherCallers(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	for _, path := range []string{"/priv/account-cleanup"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.Header.Set("X-Internal-Caller-App-Id", "engagement-api")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 401 {
			t.Fatalf("%s: %d", path, out.Code)
		}
	}
}

func TestWatermarkAccountCleanupIsSeparateFromDSR(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithBulletinWatermark(&watermarkStoreStub{}, nil, "").Routes()
	req := httptest.NewRequest("POST", "/priv/account-cleanup", strings.NewReader(`{"userId":"`+uuid.NewString()+`","idempotencyKey":"delete-1"}`))
	req.Header.Set("X-Internal-Caller-App-Id", "account-api")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"recordCount":3`) || !strings.Contains(out.Body.String(), `"remainingCount":0`) || !strings.Contains(out.Body.String(), `"reasonCodes":[]`) || strings.Contains(out.Body.String(), `"action"`) {
		t.Fatalf("cleanup contract: %d %s", out.Code, out.Body)
	}
}

func TestWatermarkAccountCleanupReportsPending(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithBulletinWatermark(&watermarkStoreStub{applyRemaining: 1}, nil, "").Routes()
	req := httptest.NewRequest("POST", "/priv/account-cleanup", strings.NewReader(`{"userId":"`+uuid.NewString()+`","idempotencyKey":"erase-1"}`))
	req.Header.Set("X-Internal-Caller-App-Id", "account-api")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"status":"pending"`) || !strings.Contains(out.Body.String(), `"remainingCount":1`) {
		t.Fatalf("pending cleanup contract: %d %s", out.Code, out.Body)
	}
}

func TestWatermarkLookupNeedsOnlyTheCode(t *testing.T) {
	store := &watermarkStoreStub{}
	permission := &investigationPermissionStub{}
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithBulletinWatermark(store, permission, "").Routes()
	code, err := bulletinwatermark.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/watermark-lookups", strings.NewReader(`{"code":"`+code+`"}`))
	investigationHeaders(request, uuid.NewString(), "cms:bulletins:investigate")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestLegacyDSRRoutesAreNotFound(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithBulletinWatermark(&watermarkStoreStub{}, nil, "").Routes()
	for _, path := range []string{"exports", "actions", "restrictions/preview", "restrictions/withdraw"} {
		req := httptest.NewRequest("POST", "/priv/bulletin-watermarks/dsr/"+path, strings.NewReader(`{}`))
		req.Header.Set("X-Internal-Caller-App-Id", "account-api")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 404 {
			t.Fatalf("%s status=%d want 404", path, out.Code)
		}
	}
}
