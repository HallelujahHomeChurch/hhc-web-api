package accountclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveIdentitiesUsesPrivateBatchEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/priv/account/v1/user-identities/resolve" || r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
			t.Fatalf("method=%q path=%q headers=%#v", r.Method, r.URL.Path, r.Header)
		}
		var body map[string][]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body["ids"]) != 1 || body["ids"][0] != "user-1" {
			t.Fatalf("body=%#v error=%v", body, err)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"user-1","displayName":"Ray","email":"ray@example.com","avatarUrl":"https://www.alive.org.tw/assets/avatar-v2"}]}`))
	}))
	defer server.Close()

	identities, err := New(server.URL, "hhc-web-api").ResolveIdentities(context.Background(), []string{"user-1"})
	if err != nil || len(identities) != 1 || identities[0].DisplayName != "Ray" || identities[0].AvatarURL != "https://www.alive.org.tw/assets/avatar-v2" {
		t.Fatalf("identities=%+v error=%v", identities, err)
	}
}

func TestVerifyRecordingPublicationFailsClosedAndUsesExactContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		allowed    bool
		valid      bool
	}{
		{"allowed", `{"allowed":true}`, 200, true, true},
		{"denied", `{"allowed":false}`, 200, false, true},
		{"missing", `{}`, 200, false, false},
		{"null", `{"allowed":null}`, 200, false, false},
		{"wrong type", `{"allowed":"true"}`, 200, false, false},
		{"malformed", `{"allowed":`, 200, false, false},
		{"trailing JSON", `{"allowed":true}{}`, 200, false, false},
		{"envelope", `{"data":{"allowed":true}}`, 200, false, false},
		{"unavailable", `{"allowed":true}`, 503, false, false},
		{"body at limit", `{"allowed":true}` + strings.Repeat(" ", (1<<16)-16), 200, true, true},
		{"oversized body", `{"allowed":true}` + strings.Repeat(" ", 1<<16), 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/priv/account/v1/permissions/verify" || r.Method != "POST" || r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" || r.Header.Get("X-HHC-Actor-ID") != "" {
					t.Error("wrong background permission request")
				}
				var input map[string]string
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				if input["userId"] != "0199a000-0000-7000-8000-000000000001" || input["permission"] != "cms:recordings:publish" || len(input) != 2 {
					t.Error(input)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			allowed, err := New(server.URL, "hhc-web-api").VerifyRecordingPublication(context.Background(), "0199a000-0000-7000-8000-000000000001")
			if (err == nil) != tc.valid || allowed != tc.allowed {
				t.Fatalf("allowed=%v err=%v", allowed, err)
			}
		})
	}
}

func TestPrivateAccountRequestUsesScopedCredential(t *testing.T) {
	c := New("http://wrong.test", "hhc-web-api")
	token := strings.Repeat("a", 64)
	c.ConfigurePrivateHTTPS(token)
	req, err := c.request(t.Context(), "/priv/account/v1/permissions/verify", []byte("{}"))
	if err != nil || req.Method != "POST" || req.URL.String() != "https://account-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io/priv/account/v1/permissions/verify" ||
		req.Header.Get("X-HHC-Account-Service-Caller") != "hhc-web-api" || req.Header.Get("X-HHC-Account-Service-Token") != token ||
		req.Header.Get("X-Internal-Caller-App-Id") != "" {
		t.Fatal("private Account request changed identity, route, or credential scope")
	}
}
