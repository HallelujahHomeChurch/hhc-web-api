package auditclient

import (
	"context"
	"testing"
	"time"
)

func TestRecordingAuditPreservesServicePrincipalAndRejectsOtherDomains(t *testing.T) {
	actor := "0199a000-0000-7000-8000-000000000001"
	resource := "0199a000-0000-4000-8000-000000000002"
	ctx := WithPrincipalProvenance(context.Background(), "service", actor, "service-recording-request")
	p, ok := ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "service" || p.ActorID != actor {
		t.Fatalf("principal provenance lost: %#v", p)
	}
	e, err := NewEventForPrincipal("cms.recording.publish", resource, p.ActorType, p.ActorID, p.RequestID, time.Now(), Metadata{})
	if err != nil || e.ActorType != "service" || e.ActorID != actor {
		t.Fatalf("incorrect service event: %#v %v", e, err)
	}
	if _, _, err := e.Canonical(); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"cms.bulletin.publish", "cms.content.publish", "cms.recording.occurrence.bind"} {
		if _, err := NewEventForPrincipal(action, resource, "service", actor, p.RequestID, time.Now(), Metadata{}); err == nil {
			t.Fatalf("service enabled on %s", action)
		}
	}
	for _, actorType := range []string{"", "robot", "anonymous"} {
		if _, err := NewEventForPrincipal("cms.recording.publish", resource, actorType, actor, p.RequestID, time.Now(), Metadata{}); err == nil {
			t.Fatalf("accepted unknown actor %q", actorType)
		}
	}
}

func TestCaptureAuditUsesOnlyCatalogExecutorAndCompleteProvenance(t *testing.T) {
	metadata := Metadata{"authorizerId": "0199a000-0000-7000-8000-000000000001", "captureId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "intentId": "0199a000-0000-4000-8000-000000000002"}
	for _, action := range []string{"cms.recording.capture.publish", "cms.recording.capture.block"} {
		event, err := NewEventForPrincipal(action, "0199a000-0000-4000-8000-000000000003", "service", "hhc-web-api", "capture-worker", time.Now(), metadata)
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := event.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Decode(raw); err != nil {
			t.Fatal(err)
		}
		for _, actor := range []string{"asset-api", "0199a000-0000-7000-8000-000000000001"} {
			event.ActorID = actor
			if _, _, err = event.Canonical(); err == nil {
				t.Fatal("wrong executor accepted")
			}
		}
		event.ActorID = "hhc-web-api"
		for _, bad := range []Metadata{
			{"authorizerId": "hhc-web-api", "captureId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "intentId": "0199a000-0000-4000-8000-000000000002"},
			{"authorizerId": "0199a000-0000-7000-8000-000000000001", "captureId": "bad", "intentId": "0199a000-0000-4000-8000-000000000002"},
			{"authorizerId": "0199a000-0000-7000-8000-000000000001", "captureId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "intentId": "0199a000-0000-7000-8000-000000000002"},
		} {
			event.Metadata = bad
			if _, _, err = event.Canonical(); err == nil {
				t.Fatal("incomplete provenance accepted")
			}
		}
		event.Metadata = metadata
		event.ActorID = "hhc-web-api"
		event.ActorType = "user"
		if _, _, err = event.Canonical(); err == nil {
			t.Fatal("fake human accepted")
		}
	}
	if _, err := NewEventForPrincipal("cms.recording.publish", "0199a000-0000-4000-8000-000000000003", "service", "hhc-web-api", "capture-worker", time.Now(), Metadata{}); err == nil {
		t.Fatal("literal escaped capture contract")
	}
}
