package auditclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestNewEventRejectsUnreleasedFilterNames(t *testing.T) {
	_, err := NewEvent("cms.bulletin.list", "", "018f47a1-0f8a-4f25-8f09-222222222222", "request-1", time.Now(), Metadata{"filterNames": []string{"page"}})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("err=%v", err)
	}
}

func TestOnlineBulletinEventsUseDedicatedResource(t *testing.T) {
	for _, operation := range []string{"read", "extract", "update", "review", "compare", "restore", "publish", "unpublish"} {
		event, err := NewEvent("cms.bulletin.online."+operation, "018f47a1-0f8a-4f25-8f09-111111111111", "018f47a1-0f8a-4f25-8f09-222222222222", "request-1", time.Now(), Metadata{})
		if err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		if event.ResourceType != "online_bulletin" {
			t.Fatalf("%s: wrong resource %s", operation, event.ResourceType)
		}
		event.Metadata = Metadata{"text": "private bulletin text"}
		if _, _, err := event.Canonical(); err == nil {
			t.Fatal("accepted private content in audit metadata")
		}
	}
}

func TestBulletinNotificationUsesReleasedAuditContract(t *testing.T) {
	event, err := NewEvent("cms.bulletin.notification.submit", "018f47a1-0f8a-4f25-8f09-111111111111", "018f47a1-0f8a-4f25-8f09-222222222222", "request-1", time.Now(), Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if event.ResourceType != "bulletin" || event.SourceService != "hhc-web-api" {
		t.Fatalf("unexpected notification audit contract: %#v", event)
	}
}

func TestReleasedFixtureAndEveryActionExample(t *testing.T) {
	fixture, err := LoadFixture()
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Checksum != "sha256:4f433e327fadde6da60db196d87ca3effbe3caec9a1f81e1e4413128344f547b" {
		t.Fatalf("checksum=%s", fixture.Checksum)
	}
	var schemas map[string]struct {
		Example Metadata `json:"example"`
	}
	if json.Unmarshal(fixture.MetadataSchemas, &schemas) != nil {
		t.Fatal("invalid schemas")
	}
	for _, contract := range fixture.Contracts() {
		resourceID := "018f47a1-0f8a-4f25-8f09-111111111111"
		switch contract.ResourceIDGrammar {
		case "empty":
			resourceID = ""
		case "singleton":
			resourceID = "singleton"
		case "asset_hex":
			resourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}
		actorType, actor := "user", "018f47a1-0f8a-4f25-8f09-222222222222"
		if !slices.Contains(contract.ActorTypes, "user") && len(contract.ServicePrincipals) > 0 {
			actorType, actor = "service", contract.ServicePrincipals[0]
		}
		event, err := NewEventForPrincipal(contract.Action, resourceID, actorType, actor, "request-1", time.Date(2026, 9, 16, 0, 2, 0, 0, time.UTC), schemas[contract.MetadataSchema].Example)
		if err != nil {
			t.Errorf("new %s: %v", contract.Action, err)
			continue
		}
		payload, _, _ := event.Canonical()
		if _, err = Decode(payload); err != nil {
			t.Errorf("decode %s: %v", contract.Action, err)
		}
	}
}

func TestClientClassifiesAuditResponses(t *testing.T) {
	for status, want := range map[int]string{201: "accepted", 200: "duplicate", 409: "conflict", 429: "retry", 503: "retry", 400: "dead_letter"} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Audit-Token") != "secret" {
					t.Fatal("missing token")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			client := newClient(server.Client(), server.URL, "secret")
			if got := client.Append(context.Background(), []byte(`{"ok":true}`)).Result; got != want {
				t.Fatalf("got=%s want=%s", got, want)
			}
		})
	}
}

func TestReadReplayAllowsOnlyNonIdentityTimeDifference(t *testing.T) {
	first, err := NewEvent("cms.bulletin.read", "018f47a1-0f8a-4f25-8f09-111111111111", "018f47a1-0f8a-4f25-8f09-222222222222", "request-1", time.Now(), Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	replay := first
	replay.OccurredAt = replay.OccurredAt.Add(time.Second)
	if !EquivalentForEnqueue(first, replay) {
		t.Fatal("equivalent replay rejected")
	}
	replay.ActorID = "018f47a1-0f8a-4f25-8f09-333333333333"
	if EquivalentForEnqueue(first, replay) {
		t.Fatal("conflicting replay accepted")
	}
}
