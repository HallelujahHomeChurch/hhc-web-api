package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnosticRequestIDValidationDoesNotRewriteProvenance(t *testing.T) {
	for _, values := range [][]string{{"safe-request"}, {"bad value"}, {"one", "two"}, {strings.Repeat("x", 257)}, nil} {
		request := httptest.NewRequest("GET", "/", nil)
		for _, value := range values {
			request.Header.Add("X-HHC-Request-ID", value)
		}
		response := httptest.NewRecorder()
		requestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.Header.Values("X-HHC-Request-ID")) != len(values) {
				t.Fatal("rewrote incoming provenance")
			}
			for i, v := range values {
				if r.Header.Values("X-HHC-Request-ID")[i] != v {
					t.Fatal("rewrote incoming provenance")
				}
			}
		})).ServeHTTP(response, request)
		got := response.Header().Get("X-HHC-Request-ID")
		if got == "" || (len(values) == 1 && values[0] == "safe-request" && got != "safe-request") {
			t.Fatalf("ID %q", got)
		}
		if len(values) > 0 && values[0] != "safe-request" && got == values[0] {
			t.Fatalf("invalid ID echoed: %q", got)
		}
	}
}
