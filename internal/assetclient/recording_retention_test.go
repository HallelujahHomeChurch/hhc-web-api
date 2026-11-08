package assetclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceProgressFallbackDoesNotBypassBindingFailures(t *testing.T) {
	for _, tc := range []struct {
		status  int
		message string
		want    error
	}{
		{400, "invalid input", ErrInvalid}, {403, "operation is not allowed", ErrForbidden}, {404, "not found", ErrNotFound}, {500, "unavailable", ErrUnavailable},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"code":"AST_INVALID_REQUEST","message":%q}}`, tc.message)
			}))
			defer server.Close()
			_, err := New(server.URL, "hhc-web-api", "").RecordingLifecycleWithSources(context.Background(), nil, []RecordingSourceLifecycleBinding{{RecordingID: "recording", SourceID: "source"}})
			if !errors.Is(err, tc.want) || calls != 1 {
				t.Fatalf("failure bypassed: calls=%d err=%v", calls, err)
			}
		})
	}
}
