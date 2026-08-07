package engagementclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardInjectsTrustedServiceAndActorHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/priv/campaigns" || r.Header.Get("X-HHC-Actor-ID") != "user-1" || r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-api" {
			t.Fatalf("request path=%q headers=%v", r.URL.Path, r.Header)
		}
		if r.Header.Get("dapr-api-token") != "dapr-token" {
			t.Fatalf("dapr token=%q", r.Header.Get("dapr-api-token"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"campaign-1"}}`))
	}))
	defer server.Close()

	response, err := New(server.URL, "hhc-web-api", "dapr-token").Forward(
		context.Background(), http.MethodPost, "/priv/campaigns", strings.NewReader(`{"name":"Newsletter"}`), "user-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated || !strings.Contains(string(body), "campaign-1") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}
