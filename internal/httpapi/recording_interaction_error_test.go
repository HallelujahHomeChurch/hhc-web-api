package httpapi

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestCommentReadBudgetErrors(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{recordings.ErrCommentTraversalLimit, 422, "recording_comment_thread_too_large"},
		{recordings.ErrCommentReadTimeout, 503, "recording_comment_read_timeout"},
	} {
		t.Run(tt.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			interactionError(response, fmt.Errorf("comment read: %w", tt.err))
			if response.Code != tt.status || !strings.Contains(response.Body.String(), `"code":"`+tt.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
