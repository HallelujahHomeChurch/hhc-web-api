package engagementclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubmitNotificationsSendsOneImmutableCommand(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/priv/campaign-submissions" {
			t.Fatalf("request method=%s path=%s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-api" || r.Header.Get("X-HHC-Actor-ID") != "user-1" || r.Header.Get("X-HHC-Request-ID") != "request-1" || r.Header.Get("Idempotency-Key") != "notification:v2:bulletin:issue-1" {
			t.Fatalf("headers=%v", r.Header)
		}
		var body NotificationSubmission
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Name != "Weekly bulletin 1732" || body.Source == nil || body.Source.Kind != "bulletin" || body.Source.IssueID != "issue-1" || body.Source.NotificationGeneration != 0 || len(body.Notifications) != 2 || body.Notifications[0].Channel != "email" || body.Notifications[1].Channel != "web_push" {
			t.Fatalf("body=%#v", body)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	err := New(server.URL, "hhc-web-api").SubmitNotifications(context.Background(), NotificationSubmission{
		Name: "Weekly bulletin 1732", Source: &BulletinSource{Kind: "bulletin", IssueID: "issue-1", NotificationGeneration: 0}, ActorID: "user-1", RequestID: "request-1", IdempotencyKey: "notification:v2:bulletin:issue-1",
		Notifications: []NotificationCommand{{Channel: "email", AudienceType: "bulletin_members"}, {Channel: "web_push", AudienceType: "bulletin_members"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestSubmitNotificationsAcceptsOnly202(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusNoContent, http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			err := New(server.URL, "hhc-web-api").SubmitNotifications(context.Background(), NotificationSubmission{ActorID: "user-1", RequestID: "request-1", IdempotencyKey: "key"})
			if err == nil {
				t.Fatalf("status %d accepted", status)
			}
		})
	}
}
