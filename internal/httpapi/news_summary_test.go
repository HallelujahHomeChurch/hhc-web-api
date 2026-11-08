package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSummaryProcessingHasRecognizableConflictResponse(t *testing.T) {
	response := httptest.NewRecorder()
	handleContentError(response, content.ErrSummaryProcessing)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"summary_processing"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

func TestAdminSummaryStatusDoesNotChangeUserVersion(t *testing.T) {
	response := httptest.NewRecorder()
	writeContentItem(response, content.Item{Module: content.ModuleNews, Version: 7, Translations: []content.Translation{{Locale: "zh-Hant", Summary: "AI summary", SummaryStatus: "ready"}}})
	if response.Header().Get("ETag") != `"7"` || !strings.Contains(response.Body.String(), `"summaryStatus":"ready"`) || !strings.Contains(response.Body.String(), `"summary":"AI summary"`) {
		t.Fatalf("headers=%v body=%s", response.Header(), response.Body)
	}
}
