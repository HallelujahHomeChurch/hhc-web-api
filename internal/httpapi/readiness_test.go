package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestReadinessRequiresLocalInitialization(t *testing.T) {
	h := New(nil, nil, nil).Routes()
	for _, path := range []string{"/ready", "/health/ready"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
	for _, path := range []string{"/health", "/health/live"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
}
