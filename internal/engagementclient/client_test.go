package engagementclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

func TestGetTranslationSourceUsesContentOnlyPrivateEndpoint(t *testing.T) {
	const id = "10000000-0000-4000-8000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/priv/campaign-schedules/"+id+"/translation-source" || r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-api" {
			t.Fatalf("request method=%s path=%s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"data":{"resourceId":"` + id + `","sourceLocale":"zh-Hant","channel":"email","version":7,"fields":{"subject":"主旨","body":"內容"},"availableLocales":["zh-Hant","en"]}}`))
	}))
	defer server.Close()

	source, err := New(server.URL, "hhc-web-api").GetTranslationSource(context.Background(), "campaign-schedules", id)
	if err != nil {
		t.Fatal(err)
	}
	want := translation.SavedSource{ResourceID: id, SourceLocale: "zh-Hant", Channel: "email", Version: 7, Fields: map[string]string{"subject": "主旨", "body": "內容"}, AvailableLocales: []string{"zh-Hant", "en"}}
	if !reflect.DeepEqual(source, want) {
		t.Fatalf("source=%#v want=%#v", source, want)
	}
	if _, err := New(server.URL, "hhc-web-api").GetTranslationSource(context.Background(), "content", id); !errors.Is(err, translation.ErrInvalidRequest) {
		t.Fatalf("invalid module error=%v", err)
	}
}

func TestForwardInjectsTrustedServiceAndActorHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/priv/campaigns" || r.Header.Get("X-HHC-Actor-ID") != "user-1" || r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-api" {
			t.Fatalf("request path=%q headers=%v", r.URL.Path, r.Header)
		}
		if r.Header.Get("dapr-api-token") != "" {
			t.Fatalf("outbound request leaked app api token=%q", r.Header.Get("dapr-api-token"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"campaign-1"}}`))
	}))
	defer server.Close()

	response, err := New(server.URL, "hhc-web-api").Forward(
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

func TestQueueBulletinNotificationCreatesAndQueuesIdempotentCampaign(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			if r.Method != http.MethodPost || r.URL.Path != "/priv/campaigns" || r.Header.Get("Idempotency-Key") != "bulletin:issue-1:web-push" || r.Header.Get("X-HHC-Actor-ID") != "user-1" {
				t.Fatalf("create request method=%s path=%s headers=%v", r.Method, r.URL.Path, r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["channel"] != "web_push" || body["audienceType"] != "all" {
				t.Fatalf("create body=%#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"campaign-1"}}`))
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/priv/campaigns/campaign-1/send" {
				t.Fatalf("send request method=%s path=%s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusConflict)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	err := New(server.URL, "hhc-web-api").QueueBulletinNotification(context.Background(), publication.BulletinNotificationPayload{
		IssueID: "issue-1", ActorID: "user-1", Name: "Weekly bulletin 1732",
		Translations: map[string]publication.NotificationTranslation{"zh-Hant": {Subject: "第 1732 期週報已發布", Body: "本週週報", ClickBehavior: "url", ActionURL: "/zh-Hant/literature-ministry"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueueBulletinNotificationMarksInvalidRequestPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer server.Close()

	err := New(server.URL, "hhc-web-api").QueueBulletinNotification(context.Background(), publication.BulletinNotificationPayload{IssueID: "issue-1", ActorID: "user-1", Name: "Weekly"})
	var permanent interface{ Permanent() bool }
	if !errors.As(err, &permanent) || !permanent.Permanent() {
		t.Fatalf("error=%v", err)
	}
}
