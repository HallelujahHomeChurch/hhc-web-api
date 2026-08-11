package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

func TestTranslationPreviewRoutesUseExactContract(t *testing.T) {
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		path   string
		module string
		target string
	}{
		{name: "content", path: "/api/admin/content/news/10000000-0000-4000-8000-000000000001/translation-previews/ja", module: "news", target: "ja"},
		{name: "bulletin", path: "/api/admin/bulletins/10000000-0000-4000-8000-000000000001/translation-previews/en", module: "bulletins", target: "en"},
		{name: "campaign", path: "/api/admin/campaigns/10000000-0000-4000-8000-000000000001/translation-previews/ja", module: "campaigns", target: "ja"},
		{name: "schedule", path: "/api/admin/campaign-schedules/10000000-0000-4000-8000-000000000001/translation-previews/ko", module: "campaign-schedules", target: "ko"},
	} {
		t.Run(test.name, func(t *testing.T) {
			previewer := &translationPreviewerStub{preview: translation.Preview{SourceLocale: "zh-Hant", TargetLocale: test.target, SourceVersion: 7, Translation: map[string]string{"title": "translated"}}}
			handler := testTranslationHandler(previewer, now, 50*time.Second)
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(`{"sourceLocale":"zh-Hant","replaceExisting":true}`))
			trusted(request, "cms:write")
			request.Header.Set("If-Match", `"7"`)
			response := newDeadlineRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK || response.deadlineCalls != 1 || !response.deadline.Equal(now.Add(50*time.Second)) {
				t.Fatalf("status=%d deadline=%s calls=%d body=%s", response.Code, response.deadline, response.deadlineCalls, response.Body.String())
			}
			if previewer.calls != 1 || previewer.request.Module != test.module || previewer.request.ResourceID != "10000000-0000-4000-8000-000000000001" || previewer.request.SourceLocale != "zh-Hant" || previewer.request.TargetLocale != test.target || previewer.request.ExpectedVersion != 7 || previewer.request.Actor != "user-1" || !previewer.request.ReplaceExisting {
				t.Fatalf("request = %#v", previewer.request)
			}
			if !strings.Contains(response.Body.String(), `"data":{"sourceLocale":"zh-Hant"`) || strings.Contains(response.Body.String(), "provider") {
				t.Fatalf("body = %s", response.Body.String())
			}
		})
	}
}

func TestCampaignTranslationPreviewAcceptsNewOrLegacyWriteScope(t *testing.T) {
	for _, scope := range []string{"campaigns:write", "cms:write"} {
		previewer := &translationPreviewerStub{preview: translation.Preview{SourceLocale: "zh-Hant", TargetLocale: "en", SourceVersion: 7, Translation: map[string]string{"subject": "Subject", "body": "Body"}}}
		handler := testTranslationHandler(previewer, time.Now(), 50*time.Second)
		request := httptest.NewRequest(http.MethodPost, "/api/admin/campaigns/10000000-0000-4000-8000-000000000001/translation-previews/en", strings.NewReader(`{"sourceLocale":"zh-Hant","replaceExisting":false}`))
		trusted(request, scope)
		request.Header.Set("If-Match", `"7"`)
		response := newDeadlineRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || previewer.calls != 1 {
			t.Fatalf("scope=%s status=%d calls=%d body=%s", scope, response.Code, previewer.calls, response.Body.String())
		}
	}
}

func TestBulletinTranslationPreviewRejectsJapaneseAndKoreanBeforeService(t *testing.T) {
	for _, locale := range []string{"ja", "ko"} {
		previewer := &translationPreviewerStub{}
		handler := testTranslationHandler(previewer, time.Now(), 50*time.Second)
		request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/10000000-0000-4000-8000-000000000001/translation-previews/"+locale, strings.NewReader(`{"sourceLocale":"zh-Hant","replaceExisting":false}`))
		trusted(request, "cms:write")
		request.Header.Set("If-Match", `"7"`)
		response := newDeadlineRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest || previewer.calls != 0 {
			t.Fatalf("target=%s status=%d calls=%d body=%s", locale, response.Code, previewer.calls, response.Body.String())
		}
	}
}

func TestTranslationPreviewRequiresScopeStrictBodyAndQuotedIfMatch(t *testing.T) {
	tests := []struct {
		name       string
		scopes     string
		ifMatch    string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "scope", scopes: "cms:read", ifMatch: `"7"`, body: `{"sourceLocale":"zh-Hant","replaceExisting":false}`, wantStatus: 403, wantCode: "forbidden"},
		{name: "missing if match", scopes: "cms:write", body: `{"sourceLocale":"zh-Hant","replaceExisting":false}`, wantStatus: 400, wantCode: "invalid_translation_request"},
		{name: "unquoted if match", scopes: "cms:write", ifMatch: `7`, body: `{"sourceLocale":"zh-Hant","replaceExisting":false}`, wantStatus: 400, wantCode: "invalid_translation_request"},
		{name: "unknown field", scopes: "cms:write", ifMatch: `"7"`, body: `{"sourceLocale":"zh-Hant","replaceExisting":false,"source":"private"}`, wantStatus: 400, wantCode: "invalid_translation_request"},
		{name: "trailing json", scopes: "cms:write", ifMatch: `"7"`, body: `{"sourceLocale":"zh-Hant","replaceExisting":false}{}`, wantStatus: 400, wantCode: "invalid_translation_request"},
		{name: "null", scopes: "cms:write", ifMatch: `"7"`, body: `null`, wantStatus: 400, wantCode: "invalid_translation_request"},
		{name: "missing replace flag", scopes: "cms:write", ifMatch: `"7"`, body: `{"sourceLocale":"zh-Hant"}`, wantStatus: 400, wantCode: "invalid_translation_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previewer := &translationPreviewerStub{}
			handler := testTranslationHandler(previewer, time.Now(), 50*time.Second)
			request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/10000000-0000-4000-8000-000000000001/translation-previews/ja", strings.NewReader(test.body))
			trusted(request, test.scopes)
			request.Header.Set("If-Match", test.ifMatch)
			response := newDeadlineRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) || previewer.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, previewer.calls, response.Body.String())
			}
		})
	}
}

func TestTranslationPreviewMapsBoundedErrors(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{translation.ErrInvalidRequest, 400, "invalid_translation_request"},
		{translation.ErrNotFound, 404, "not_found"},
		{translation.ErrTranslationExists, 409, "translation_exists"},
		{translation.ErrVersionMismatch, 412, "version_mismatch"},
		{translation.ErrRateLimited, 429, "translation_rate_limited"},
		{translation.ErrProvider, 502, "translation_provider_error"},
		{translation.ErrTimeout, 504, "translation_timeout"},
		{translation.ErrAudit, 500, "internal_error"},
		{errors.New("private database or provider failure"), 500, "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.wantCode, func(t *testing.T) {
			handler := testTranslationHandler(&translationPreviewerStub{err: test.err}, time.Now(), 50*time.Second)
			request := httptest.NewRequest(http.MethodPost, "/api/admin/content/videos/10000000-0000-4000-8000-000000000001/translation-previews/en", strings.NewReader(`{"sourceLocale":"zh-Hant","replaceExisting":false}`))
			trusted(request, "cms:write")
			request.Header.Set("If-Match", `"3"`)
			response := newDeadlineRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestTranslationPreviewReturnsExactRetryAfter(t *testing.T) {
	handler := testTranslationHandler(&translationPreviewerStub{err: &translation.RateLimitError{RetryAfter: 73 * time.Second}}, time.Now(), 50*time.Second)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/videos/10000000-0000-4000-8000-000000000001/translation-previews/en", strings.NewReader(`{"sourceLocale":"zh-Hant","replaceExisting":false}`))
	trusted(request, "cms:write")
	request.Header.Set("If-Match", `"3"`)
	response := newDeadlineRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "73" || !strings.Contains(response.Body.String(), `"code":"translation_rate_limited"`) {
		t.Fatalf("status=%d retry=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
}

func TestTranslationPreviewDisabledAndDeadlineFailureStopService(t *testing.T) {
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		previewer   *translationPreviewerStub
		deadlineErr error
		wantStatus  int
		wantCode    string
	}{
		{name: "disabled", wantStatus: 503, wantCode: "translation_disabled"},
		{name: "deadline", previewer: &translationPreviewerStub{}, deadlineErr: errors.New("unsupported"), wantStatus: 500, wantCode: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := testTranslationHandler(test.previewer, now, 50*time.Second)
			request := httptest.NewRequest(http.MethodPost, "/api/admin/content/videos/10000000-0000-4000-8000-000000000001/translation-previews/ja", strings.NewReader(`{"sourceLocale":"zh-Hant","replaceExisting":false}`))
			trusted(request, "cms:write")
			request.Header.Set("If-Match", `"7"`)
			response := newDeadlineRecorder()
			response.deadlineErr = test.deadlineErr
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) || (test.previewer != nil && test.previewer.calls != 0) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestStatusWriterUnwrapsResponseWriter(t *testing.T) {
	base := newDeadlineRecorder()
	writer := &statusWriter{ResponseWriter: base}
	if writer.Unwrap() != base {
		t.Fatal("statusWriter did not unwrap its response writer")
	}
}

func testTranslationHandler(previewer *translationPreviewerStub, now time.Time, deadline time.Duration) http.Handler {
	bulletinService := bulletins.NewService(&apiRepository{}, time.Now)
	var service TranslationPreviewer
	if previewer != nil {
		service = previewer
	}
	return NewWithTranslation(bulletinService, nil, nil, nil, "api-gateway", "", true, service, deadline, func() time.Time { return now }).Routes()
}

type translationPreviewerStub struct {
	request translation.PreviewRequest
	preview translation.Preview
	err     error
	calls   int
}

func (s *translationPreviewerStub) Preview(_ context.Context, request translation.PreviewRequest) (translation.Preview, error) {
	s.calls++
	s.request = request
	return s.preview, s.err
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline      time.Time
	deadlineCalls int
	deadlineErr   error
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (w *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadlineCalls++
	w.deadline = deadline
	return w.deadlineErr
}
