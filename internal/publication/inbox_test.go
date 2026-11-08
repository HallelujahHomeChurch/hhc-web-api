package publication

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type inboxPublicationRepoStub struct {
	event    Event
	claimed  bool
	finished []bool
}

func (s *inboxPublicationRepoStub) ClaimInboxPublication(context.Context, time.Time, time.Duration) (Event, bool, error) {
	return s.event, s.claimed, nil
}
func (s *inboxPublicationRepoStub) FinishInboxPublication(_ context.Context, _ Event, delivered bool, _ time.Time) error {
	s.finished = append(s.finished, delivered)
	return nil
}

type inboxPublicationClientStub struct {
	received []InboxPublication
	err      error
}

func (s *inboxPublicationClientStub) SubmitInboxPublication(_ context.Context, event InboxPublication) error {
	s.received = append(s.received, event)
	return s.err
}
func TestInboxDispatcherRequiresDurableAcceptanceAndPreservesOriginalEvent(t *testing.T) {
	now := time.Now().UTC()
	original := InboxPublication{EventID: "event", Kind: "recording.published", ResourceID: "recording", OccurredAt: now.Add(-time.Hour), SourceVersion: 2}
	raw, _ := json.Marshal(original)
	repo := &inboxPublicationRepoStub{event: Event{ID: "event", Payload: raw}, claimed: true}
	client := &inboxPublicationClientStub{err: errors.New("unavailable")}
	if ok, err := DispatchInboxPublication(context.Background(), repo, client, now); !ok || err == nil || len(repo.finished) != 1 || repo.finished[0] {
		t.Fatal("failure acknowledged", repo.finished, err)
	}
	client.err = nil
	if ok, err := DispatchInboxPublication(context.Background(), repo, client, now.Add(time.Minute)); !ok || err != nil || !repo.finished[1] {
		t.Fatal("acceptance not committed", repo.finished, err)
	}
	if len(client.received) != 2 || client.received[0] != original || client.received[1] != original {
		t.Fatal("retry changed event", client.received)
	}
}
