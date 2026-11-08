package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestCMSPrivateServiceScopesAndLegacyParity(t *testing.T) {
	const current = "/priv/legal/current"
	const resolve = "/priv/legal/snapshots/resolve"
	const cleanup = "/priv/account-cleanup"
	const policy = "/priv/recordings/01995fd2-6f6c-7b40-8707-6503edc362c8/notification-policy"
	const legacyToken = "legacy-fixture"
	tokens := map[string]string{
		"account-api": strings.Repeat("a", 64), "operations-api": strings.Repeat("b", 64),
		"engagement-api": strings.Repeat("c", 64),
	}
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", legacyToken, false).
		WithLegal(legal.NewService(legalRepoStub{}), qualificationStub{}).
		WithPrivateServiceTokens(tokens).Routes()
	tests := []struct {
		method, path, caller, secret, body string
		extra                              map[string][]string
		denied                             bool
	}{
		{"POST", current, "account-api", tokens["account-api"], `{"scope":"common","locale":"en"}`, nil, false},
		{"POST", current, "operations-api", tokens["operations-api"], `{"scope":"common","locale":"en"}`, nil, false},
		{"POST", resolve, "account-api", tokens["account-api"], `{"snapshotId":"01995fd2-6f6c-7b40-8707-6503edc362c8"}`, nil, false},
		{"POST", cleanup, "account-api", tokens["account-api"], "{}", nil, false},
		{"GET", policy, "engagement-api", tokens["engagement-api"], "", nil, false},
		{"POST", current, "engagement-api", tokens["engagement-api"], "{}", nil, true},
		{"POST", resolve, "operations-api", tokens["operations-api"], "{}", nil, true},
		{"POST", cleanup, "operations-api", tokens["operations-api"], "{}", nil, true},
		{"GET", policy, "account-api", tokens["account-api"], "", nil, true},
		{"HEAD", policy, "engagement-api", tokens["engagement-api"], "", nil, true},
		{"POST", current, "account-api", tokens["operations-api"], "{}", nil, true},
		{"POST", current, "account-api", tokens["account-api"], "{}", map[string][]string{privateServiceTokenHeader: {tokens["account-api"]}}, true},
		{"POST", current, "account-api", tokens["account-api"], "{}", map[string][]string{"Dapr-Caller-App-Id": {"account-api"}, "Dapr-Api-Token": {legacyToken}}, true},
		{"POST", current, "account-api", "", "{}", map[string][]string{"Dapr-Caller-App-Id": {"account-api"}, "Dapr-Api-Token": {legacyToken}}, true},
	}
	for _, tc := range tests {
		request := func() *http.Request {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("X-HHC-Request-ID", "0123456789abcdef0123456789abcdef")
			return r
		}
		direct := request()
		direct.Header.Set(privateServiceCallerHeader, tc.caller)
		if tc.secret != "" {
			direct.Header.Set(privateServiceTokenHeader, tc.secret)
		}
		for name, values := range tc.extra {
			for _, value := range values {
				direct.Header.Add(name, value)
			}
		}
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, direct)
		if tc.denied {
			if got.Code != http.StatusUnauthorized {
				t.Errorf("%s %s %s = %d, expected 401", tc.method, tc.path, tc.caller, got.Code)
			}
			continue
		}
		legacy := request()
		legacy.Header.Set("Dapr-Caller-App-Id", tc.caller)
		legacy.Header.Set("Dapr-Api-Token", legacyToken)
		want := httptest.NewRecorder()
		handler.ServeHTTP(want, legacy)
		if got.Code == http.StatusUnauthorized || got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Cache-Control") != want.Header().Get("Cache-Control") {
			t.Errorf("%s %s %s changed contract: got %d, legacy %d", tc.method, tc.path, tc.caller, got.Code, want.Code)
		}
	}
}

func TestCMSPrivateLINEKeepsTrustedActorAndScopes(t *testing.T) {
	const legacy = "legacy-fixture"
	const lineCaller = "hhc-line-function-bot"
	line := strings.Repeat("d", 64)
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", legacy, false).
		WithPrivateServiceTokens(map[string]string{lineCaller: line}).Routes()
	request := func(path string, direct, actor bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-HHC-Request-ID", "0123456789abcdef0123456789abcdef")
		if actor {
			r.Header.Set("X-HHC-User-ID", "01995fd2-6f6c-7b40-8707-6503edc362c8")
			r.Header.Set("X-HHC-Auth-Provider", "account-api")
		}
		if direct {
			r.Header.Set(privateServiceCallerHeader, lineCaller)
			r.Header.Set(privateServiceTokenHeader, line)
		} else {
			r.Header.Set("Dapr-Caller-App-Id", lineCaller)
			r.Header.Set("Dapr-Api-Token", legacy)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
	for _, path := range []string{"/priv/line/member-access?locale=zh-Hant", "/api/member/bulletins/online?series=general&locales=zh-Hant&offset=0&limit=1"} {
		got, want := request(path, true, true), request(path, false, true)
		if got.Code == 401 || got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Cache-Control") != want.Header().Get("Cache-Control") {
			t.Fatalf("%s changed contract: HTTPS %d, Dapr %d", path, got.Code, want.Code)
		}
		if got := request(path, true, false); got.Code != 401 {
			t.Fatalf("%s accepted credential without trusted Account subject: %d", path, got.Code)
		}
	}
	if got := request("/api/member/broadcasts", true, true); got.Code != 401 {
		t.Fatalf("LINE HTTPS credential reached livestream handler: %d", got.Code)
	}
}

func TestCMSPrivateGatewayCohortsKeepBroadcastAuthorizationAndProof(t *testing.T) {
	const legacy = "legacy-fixture"
	const actor = "01995fd2-6f6c-7b40-8707-6503edc362c8"
	ordinary, live := strings.Repeat("e", 64), strings.Repeat("f", 64)
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", legacy, false).
		WithRecordings(recordings.NewService(nil, "", nil, time.Now)).
		WithGatewayPrivateTokens(ordinary, live).Routes()
	serve := func(method, path, body, token string, dapr bool, presenter string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-HHC-Request-ID", "0123456789abcdef0123456789abcdef")
		req.Header.Set("X-HHC-User-ID", actor)
		req.Header.Set("X-HHC-Auth-Provider", "account-api")
		req.Header.Set("X-HHC-Scopes", "cms:recordings:read")
		if presenter != "" {
			req.Header.Set("Authorization", presenter)
		}
		if token != "" {
			req.Header.Set(privateServiceCallerHeader, "api-gateway")
			req.Header.Set(privateServiceTokenHeader, token)
		}
		if dapr {
			req.Header.Set("Dapr-Caller-App-Id", "api-gateway")
			req.Header.Set("Dapr-Api-Token", legacy)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	cases := []struct {
		method, path, body, presenter string
		status                        int
	}{
		{"GET", "/api/admin/broadcast-capabilities", "", "", 200},
		{"POST", "/api/ingest/streams/open", "{}", "", 422},
		{"POST", "/api/ingest/broadcast-source/resolve", `{"streamKey":"wrong"}`, "", 403},
		{"GET", "/api/ingest/broadcasts/018f0000-0000-7000-8000-000000000002/captures/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", "Bearer account-jwt", 401},
	}
	for _, tc := range cases {
		direct := serve(tc.method, tc.path, tc.body, live, false, tc.presenter)
		legacy := serve(tc.method, tc.path, tc.body, "", true, tc.presenter)
		if direct.Code != tc.status || direct.Code != legacy.Code || direct.Body.String() != legacy.Body.String() ||
			direct.Header().Get("Referrer-Policy") != legacy.Header().Get("Referrer-Policy") ||
			direct.Header().Get("Cache-Control") != legacy.Header().Get("Cache-Control") {
			t.Errorf("%s changed live contract: direct %d, legacy %d, want %d", tc.path, direct.Code, legacy.Code, tc.status)
		}
		if wrong := serve(tc.method, tc.path, tc.body, ordinary, false, tc.presenter); wrong.Code != 401 {
			t.Errorf("%s accepted the non-live credential: %d", tc.path, wrong.Code)
		}
	}
	if got := serve("GET", "/api/admin/broadcast-capabilities", "", live, true, ""); got.Code != 401 {
		t.Fatal("live route accepted mixed HTTPS and Dapr credentials")
	}
	if got := serve("POST", "/api/ingest/streams/%6fpen", "{}", live, false, ""); got.Code != 401 {
		t.Fatal("ingest transport accepted a noncanonical path")
	}
	if got := serve("GET", "/api/admin/recordings/retention-policy", "", live, false, ""); got.Code != 401 {
		t.Fatal("live credential escaped its protected routes")
	}
}

func TestCMSDaprOffRejectsLegacyCallerWithoutAppToken(t *testing.T) {
	token := strings.Repeat("a", 64)
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", false).
		WithRecordings(recordings.NewService(nil, "", nil, time.Now)).
		WithGatewayPrivateTokens("", token).WithoutDapr(true).Routes()
	for _, legacy := range []bool{true, false} {
		req := httptest.NewRequest("GET", "/api/admin/broadcast-capabilities", nil)
		req.Header.Set("X-HHC-User-ID", "01995fd2-6f6c-7b40-8707-6503edc362c8")
		req.Header.Set("X-HHC-Auth-Provider", "account-api")
		req.Header.Set("X-HHC-Scopes", "cms:recordings:read")
		want := 200
		if legacy {
			req.Header.Set("Dapr-Caller-App-Id", "api-gateway")
			want = 401
		} else {
			req.Header.Set(privateServiceCallerHeader, "api-gateway")
			req.Header.Set(privateServiceTokenHeader, token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("legacy=%t: status %d, want %d", legacy, rec.Code, want)
		}
	}
}

func TestPrivateGatewayMemberRecordingEntitlements(t *testing.T) {
	const legacy = "legacy-fixture"
	ordinary, live := strings.Repeat("e", 64), strings.Repeat("f", 64)
	for _, route := range []struct{ method, path, token string }{
		{"GET", "/api/member/recordings", ordinary},
		{"GET", "/api/member/recordings/live", live},
		{"POST", "/api/member/recordings/rec-a/playback", ordinary},
		{"POST", "/api/member/recordings/rec-a/live/playback", live},
		{"GET", "/api/member/recordings/rec-a/watch", live},
		{"GET", "/api/member/videos/abcdefghijk/watch", live},
		{"GET", "/api/member/broadcasts", live},
		{"GET", "/api/member/recordings/rec-a/progress", ordinary},
		{"GET", "/api/member/recordings/continue-watching", ordinary},
		{"GET", "/api/member/recordings/rec-a/interactions", ordinary},
	} {
		for _, transport := range []string{"https", "dapr", "wrong-token", "mixed", "line", "anonymous"} {
			t.Run(route.method+route.path+"/"+transport, func(t *testing.T) {
				var codes []string
				h := NewWithContent(nil, nil, nil, nil, "api-gateway", legacy, false).
					WithGatewayPrivateTokens(ordinary, live).
					WithPrivateServiceTokens(map[string]string{"hhc-line-function-bot": strings.Repeat("d", 64)}).
					WithRecordings(recordings.NewService(nil, "", nil, time.Now)).
					WithBulletinEntitlements(entitlementCheckerStub{decision: "deny", codes: &codes}).Routes()
				r := httptest.NewRequest(route.method, route.path, nil)
				r.Header.Set("X-HHC-User-ID", "01995fd2-6f6c-7b40-8707-6503edc362c8")
				r.Header.Set("X-HHC-Auth-Provider", "account-api")
				r.Header.Set("X-HHC-Request-ID", "0123456789abcdef0123456789abcdef")
				if transport != "dapr" && transport != "anonymous" {
					r.Header.Set(privateServiceCallerHeader, "api-gateway")
					r.Header.Set(privateServiceTokenHeader, route.token)
				}
				switch transport {
				case "dapr", "mixed":
					r.Header.Set("Dapr-Caller-App-Id", "api-gateway")
					r.Header.Set("Dapr-Api-Token", legacy)
				case "wrong-token":
					r.Header.Set(privateServiceTokenHeader, strings.Repeat("0", 64))
				case "line":
					r.Header.Set(privateServiceCallerHeader, "hhc-line-function-bot")
					r.Header.Set(privateServiceTokenHeader, strings.Repeat("d", 64))
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := http.StatusUnauthorized
				if transport == "https" || transport == "dapr" {
					want = http.StatusNotFound
				}
				if w.Code != want {
					t.Fatalf("status=%d, want %d", w.Code, want)
				}
				if want == http.StatusNotFound {
					if len(codes) != 1 || codes[0] != recordings.EntitlementCode {
						t.Fatalf("missing recording entitlement check: %v", codes)
					}
				} else if len(codes) != 0 {
					t.Fatal("untrusted request reached entitlement service")
				}
			})
		}
	}
}

func TestPrivateGatewayAllowedMemberReachesRecordingHandler(t *testing.T) {
	token := strings.Repeat("e", 64)
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "legacy", false).
		WithGatewayPrivateTokens(token, strings.Repeat("f", 64)).
		WithRecordings(recordings.NewService(nil, "", nil, time.Now)).
		WithBulletinEntitlements(entitlementCheckerStub{decision: "allow"}).Routes()
	r := httptest.NewRequest("GET", "/api/member/recordings?limit=invalid", nil)
	r.Header.Set(privateServiceCallerHeader, "api-gateway")
	r.Header.Set(privateServiceTokenHeader, token)
	r.Header.Set("X-HHC-User-ID", "01995fd2-6f6c-7b40-8707-6503edc362c8")
	r.Header.Set("X-HHC-Auth-Provider", "account-api")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_request") {
		t.Fatalf("recording handler not reached: %d %s", w.Code, w.Body.String())
	}
}
