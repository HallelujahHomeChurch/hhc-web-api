package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func TestGatewayLocations(t *testing.T) {
	token := strings.Repeat("a", 64)
	repo := &contentRepository{}
	handler := NewWithContent(nil, content.NewService(repo, time.Now), nil, nil, "api-gateway", "sidecar", false)
	const alias = "/priv/gateway/published-locations"
	const header = "X-HHC-CMS-Locations-Token"
	disabled := handler.Routes()
	for _, tc := range []struct {
		name, method, path string
		tokens             []string
		extra              string
		want               int
	}{
		{"missing", "GET", alias, nil, "", 403},
		{"wrong", "GET", alias, []string{"wrong"}, "", 403},
		{"duplicate", "GET", alias, []string{token, token}, "", 403},
		{"folded", "GET", alias, []string{token + "," + token}, "", 403},
		{"mixed caller", "GET", alias, []string{token}, "Dapr-Caller-App-Id", 403},
		{"mixed token", "GET", alias, []string{token}, "Dapr-Api-Token", 403},
		{"mixed dev caller", "GET", alias, []string{token}, "X-Internal-Caller-App-Id", 403},
		{"trailing slash", "GET", alias + "/", []string{token}, "", 404},
		{"double slash", "GET", "/priv//gateway/published-locations", []string{token}, "", 404},
		{"dot segment", "GET", "/priv/gateway/./published-locations", []string{token}, "", 404},
		{"encoded", "GET", "/priv/gateway/%70ublished-locations", []string{token}, "", 404},
		{"wrong method", "POST", alias, []string{token}, "", 405},
		{"nearby private route", "POST", "/priv/account-cleanup", []string{token}, "", 401},
		{"admin route", "GET", "/api/admin/content/news", []string{token}, "", 401},
		{"member route", "GET", "/api/member/bulletins", []string{token}, "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			for _, value := range tc.tokens {
				req.Header.Add(header, value)
			}
			if tc.extra != "" {
				req.Header.Set(tc.extra, "api-gateway")
			}
			got := httptest.NewRecorder()
			handler.WithGatewayLocationsToken(token).Routes().ServeHTTP(got, req)
			if got.Code != tc.want || got.Header().Get("Location") != "" {
				t.Fatalf("status=%d location=%q, want status=%d and no redirect", got.Code, got.Header().Get("Location"), tc.want)
			}
		})
	}
	request := func(router http.Handler, method, path, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path+"?locale=ja", nil)
		req.Header.Set(header, token)
		req.Header.Set("X-HHC-Request-ID", id)
		got := httptest.NewRecorder()
		router.ServeHTTP(got, req)
		return got
	}
	const requestID = "0123456789abcdef0123456789abcdef"
	if got := request(disabled, "GET", alias, requestID); got.Code != 404 {
		t.Fatalf("disabled alias returned %d", got.Code)
	}
	enabled := handler.WithGatewayLocationsToken(token).Routes()
	for _, method := range []string{"GET", "HEAD"} {
		legacy := request(disabled, method, "/api/locations", requestID)
		got := request(enabled, method, alias, requestID)
		if got.Code != 200 || legacy.Code != got.Code || legacy.Body.String() != got.Body.String() ||
			got.Header().Get("Cache-Control") != legacy.Header().Get("Cache-Control") ||
			got.Header().Get("X-HHC-Request-ID") != requestID || repo.publicLocationsLocale != "ja" {
			t.Fatalf("%s alias diverged from legacy: %d %v", method, got.Code, got.Header())
		}
	}
	got := request(enabled, "GET", alias, token)
	if got.Code != 200 || got.Header().Get("X-HHC-Request-ID") == token {
		t.Fatal("credential-shaped request ID was not redacted")
	}
}
