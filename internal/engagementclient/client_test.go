package engagementclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
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
		if r.URL.Path != "/priv/campaigns" || r.Header.Get("X-HHC-Actor-ID") != "user-1" || r.Header.Get("X-HHC-Request-ID") != "request-1" || r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-api" {
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
		context.Background(), http.MethodPost, "/priv/campaigns", strings.NewReader(`{"name":"Newsletter"}`), "user-1", "request-1",
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

func TestForwardSubmissionPreservesIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/priv/campaign-submissions" || r.Header.Get("Idempotency-Key") != "key-1" || r.Header.Get("X-HHC-Actor-ID") != "user-1" {
			t.Fatalf("path=%s headers=%v", r.URL.Path, r.Header)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	response, err := New(server.URL, "hhc-web-api").ForwardSubmission(context.Background(), strings.NewReader(`{"name":"Notice"}`), "user-1", "request-1", "key-1")
	if err != nil || response.StatusCode != http.StatusAccepted {
		t.Fatalf("response=%v err=%v", response, err)
	}
	response.Body.Close()
}

func TestQueueBulletinNotificationCreatesAndQueuesIdempotentCampaign(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("X-HHC-Request-ID") != "request-1" {
			t.Fatalf("request %d missing correlation header: %v", requests, r.Header)
		}
		switch requests {
		case 1:
			if r.Method != http.MethodPost || r.URL.Path != "/priv/campaigns" || r.Header.Get("Idempotency-Key") != "bulletin:issue-1:email" || r.Header.Get("X-HHC-Actor-ID") != "user-1" {
				t.Fatalf("create request method=%s path=%s headers=%v", r.Method, r.URL.Path, r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["channel"] != "email" || body["audienceType"] != "bulletin_members" {
				t.Fatalf("create body=%#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"campaign-email"}}`))
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/priv/campaigns/campaign-email/send" {
				t.Fatalf("send request method=%s path=%s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusConflict)
		case 3:
			if r.Header.Get("Idempotency-Key") != "bulletin:issue-1:web-push" {
				t.Fatalf("push idempotency=%q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["channel"] != "web_push" || body["audienceType"] != "bulletin_members" {
				t.Fatalf("create body=%#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"campaign-push"}}`))
		case 4:
			if r.URL.Path != "/priv/campaigns/campaign-push/send" {
				t.Fatalf("push send path=%s", r.URL.Path)
			}
			w.WriteHeader(http.StatusConflict)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	err := New(server.URL, "hhc-web-api").QueueBulletinNotification(context.Background(), publication.BulletinNotificationPayload{
		IssueID: "issue-1", ActorID: "user-1", RequestID: "request-1", Name: "Weekly bulletin 1732",
		Translations: map[string]publication.NotificationTranslation{"zh-Hant": {Subject: "第 1732 期週報已發布", Body: "本週週報", ClickBehavior: "url", ActionURL: "/zh-Hant/literature-ministry"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 4 {
		t.Fatalf("requests=%d", requests)
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

func TestQueueBulletinNotificationRestrictsBothChannelsToMembers(t *testing.T) {
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HHC-Request-ID") != "request-1" {
			t.Fatalf("missing request id: %v", r.Header)
		}
		if strings.HasSuffix(r.URL.Path, "/send") {
			w.WriteHeader(http.StatusOK)
			return
		}
		creates++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["audienceType"] != "bulletin_members" {
			t.Fatalf("create body=%#v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"data":{"id":"campaign-%d"}}`, creates)))
	}))
	defer server.Close()

	err := New(server.URL, "hhc-web-api").QueueBulletinNotification(context.Background(), publication.BulletinNotificationPayload{
		IssueID: "issue-1", ActorID: "user-1", RequestID: "request-1", Name: "Weekly",
		Translations: map[string]publication.NotificationTranslation{"zh-Hant": {Subject: "週報", Body: "內容"}},
	})
	if err != nil || creates != 2 {
		t.Fatalf("creates=%d error=%v", creates, err)
	}
}

func TestCreateStatementCampaignForwardsDurableRequestID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HHC-Actor-ID") != "user-1" || r.Header.Get("X-HHC-Request-ID") != "request-1" {
			t.Fatalf("headers=%v", r.Header)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"campaign-1"}}`))
	}))
	defer server.Close()
	_, err := New(server.URL, "hhc-web-api").CreateStatementCampaign(context.Background(), content.StatementNotificationBatch{ActorID: "user-1", RequestID: "request-1", Channel: "email", Subject: "Subject", Body: "Body", Href: "/statement"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatementCampaignSendConflictRequiresReadback(t *testing.T) {
	for _, status := range []string{"draft", "queued", "completed"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-HHC-Request-ID") != "request-1" {
					t.Fatalf("missing request id: %v", r.Header)
				}
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusConflict)
					return
				}
				_, _ = w.Write([]byte(`{"data":{"status":"` + status + `"}}`))
			}))
			defer server.Close()
			err := New(server.URL, "hhc-web-api").SendStatementCampaign(context.Background(), "campaign-1", "request-1")
			if (err != nil) != (status == "draft") {
				t.Fatalf("status=%s error=%v", status, err)
			}
		})
	}
}

func TestBulletinQueuesOtherChannelWhenEmailFails(t *testing.T) {
	push := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/send") {
			w.WriteHeader(200)
			return
		}
		var body struct {
			Channel string                         `json:"channel"`
			Source  struct{ Kind, IssueID string } `json:"source"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Channel == "email" {
			w.WriteHeader(503)
			return
		}
		push = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"id":"push"}}`))
	}))
	defer server.Close()
	err := New(server.URL, "hhc-web-api").QueueBulletinNotification(context.Background(), publication.BulletinNotificationPayload{IssueID: "issue-1"})
	if err == nil || !push {
		t.Fatalf("email failure blocked push=%v err=%v", push, err)
	}
}

func TestMixedChannelFailureRemainsRetryable(t *testing.T) {
	err := bulletinChannelErrors{responseError{status: 400}, responseError{status: 503}}
	if err.Permanent() {
		t.Fatal("permanent first channel prevented transient second retry")
	}
}

func TestPrivateEngagementRequestUsesScopedCredential(t *testing.T) {
	c := New("http://wrong.test", "hhc-web-api")
	token := strings.Repeat("a", 64)
	c.ConfigurePrivateHTTPS(token)
	req, err := c.request(t.Context(), "GET", "/priv/campaigns?limit=1", nil)
	if err != nil || req.Method != "GET" || req.URL.String() != "https://engagement-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io/priv/campaigns?limit=1" ||
		req.Header.Get("X-HHC-Engagement-CMS-Token") != token || req.Header.Get("X-HHC-Caller-App-Id") != "" {
		t.Fatal("private Engagement request changed identity, route, or credential scope")
	}
}
