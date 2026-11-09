package engagementclient

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInboxPublicationWireAndAcknowledgement(t *testing.T) {
	t.Setenv("DAPR_API_TOKEN", "synthetic-token")
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{{"durable", 202, `{"data":{"accepted":true}}`, true}, {"not durable", 200, `{"data":{"accepted":true}}`, false}, {"declined", 202, `{"data":{"accepted":false}}`, false}, {"unavailable", 503, `{}`, false}, {"malformed", 202, `{`, false}, {"contradictory error", 202, `{"data":{"accepted":true},"error":{"code":"unavailable"}}`, false}, {"trailing data", 202, `{"data":{"accepted":true}} {}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			event := publication.InboxPublication{EventID: "event-id", Kind: "bulletin.published", ResourceID: "issue", Series: "children", Locale: "en", OccurredAt: time.Now().UTC(), SourceVersion: 1}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/priv/engagement/inbox-publications" || r.Header.Get("Dapr-Caller-App-Id") != "hhc-web-api" || r.Header.Get("dapr-api-token") != "synthetic-token" {
					t.Fatal("wrong ingress")
				}
				var got publication.InboxPublication
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != event {
					t.Fatal("wrong immutable wire", err)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			err := New(server.URL, "hhc-web-api").SubmitInboxPublication(context.Background(), event)
			if (err == nil) != tc.want {
				t.Fatalf("acceptance=%v", err)
			}
		})
	}
}
