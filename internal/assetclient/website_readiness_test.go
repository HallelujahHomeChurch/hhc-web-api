package assetclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebsiteReadinessRejectsIncompleteOwnerDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"ref":{"type":"bulletin_pdf","id":"asset","resourceId":"issue","locale":"en"}}]}`))
	}))
	defer server.Close()
	if _, err := New(server.URL, "hhc-web-api", "").WebsiteContentReadiness(context.Background(), []WebsiteContentRef{{Type: "bulletin_pdf", ID: "asset", ResourceID: "issue", Locale: "en"}}); err == nil {
		t.Fatal("missing readiness became an explicit deny")
	}
}

func TestWebsiteReadinessRequiresCompleteExactDecisions(t *testing.T) {
	refs := []WebsiteContentRef{{Type: "bulletin_pdf", ID: "one", ResourceID: "issue", Locale: "en"}, {Type: "bulletin_pdf", ID: "two", ResourceID: "issue", Locale: "en"}}
	decision := func(ref WebsiteContentRef) map[string]any { return map[string]any{"ref": ref, "ready": false} }
	for _, tc := range []struct {
		name  string
		items []map[string]any
		want  bool
	}{{"explicit denials", []map[string]any{decision(refs[0]), decision(refs[1])}, true}, {"partial", []map[string]any{decision(refs[0])}, false}, {"duplicate", []map[string]any{decision(refs[0]), decision(refs[0])}, false}, {"unknown", []map[string]any{decision(refs[0]), decision(WebsiteContentRef{Type: "bulletin_pdf", ID: "other", ResourceID: "issue", Locale: "en"})}, false}, {"extra", []map[string]any{decision(refs[0]), decision(refs[1]), decision(refs[1])}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/priv/website/content-readiness" || r.Header.Get("Dapr-Caller-App-Id") != "hhc-web-api" {
					t.Error("wrong readiness owner")
				}
				json.NewEncoder(w).Encode(map[string]any{"items": tc.items})
			}))
			defer server.Close()
			_, err := New(server.URL, "hhc-web-api", "").WebsiteContentReadiness(context.Background(), refs)
			if (err == nil) != tc.want {
				t.Fatal("invalid owner response accepted", err)
			}
		})
	}
}
