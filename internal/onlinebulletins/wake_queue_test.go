package onlinebulletins

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeWakeQueue struct {
	found                  bool
	receiveErr, errorAfter error
	ack, deferred, poison  int
	delay                  time.Duration
}

func (q *fakeWakeQueue) Receive(context.Context) (WakeMessage, bool, error) {
	return WakeMessage{"message", "receipt"}, q.found, q.receiveErr
}
func (q *fakeWakeQueue) Ack(context.Context, WakeMessage) error { q.ack++; return q.errorAfter }
func (q *fakeWakeQueue) Defer(_ context.Context, _ WakeMessage, d time.Duration) error {
	q.deferred++
	q.delay = d
	return q.errorAfter
}
func (q *fakeWakeQueue) Poison(context.Context, WakeMessage) error { q.poison++; return q.errorAfter }

func TestExtractionWakeNeverAcknowledgesUnaccountedWork(t *testing.T) {
	failed := errors.New("dependency failed")
	for _, tc := range []struct {
		name                    string
		found                   bool
		receive, batch, next    error
		pending                 time.Time
		ack, deferCount, poison int
		wantError               bool
	}{
		{name: "empty"},
		{name: "receive failure", receive: failed, wantError: true},
		{name: "malformed", found: true, receive: errInvalidWake, poison: 1},
		{name: "batch failure", found: true, batch: failed, wantError: true},
		{name: "state query failure", found: true, next: failed, wantError: true},
		{name: "complete", found: true, ack: 1},
		{name: "another ready document", found: true, pending: time.Now().Add(-time.Second), deferCount: 1},
		{name: "crashed execution still leased", found: true, pending: time.Now().Add(time.Minute), deferCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeWakeQueue{found: tc.found, receiveErr: tc.receive}
			calls := 0
			err := RunExtractionWake(context.Background(), q, func(context.Context) error { calls++; return tc.batch }, func(context.Context) (time.Time, error) { return tc.pending, tc.next })
			if (err != nil) != tc.wantError || q.ack != tc.ack || q.deferred != tc.deferCount || q.poison != tc.poison {
				t.Fatalf("err=%v ack=%d deferred=%d poison=%d", err, q.ack, q.deferred, q.poison)
			}
			if (!tc.found || tc.receive != nil) && calls != 0 {
				t.Fatal("unvalidated wake executed work")
			}
		})
	}
}
func TestWakePayloadContainsOnlySupportedVersion(t *testing.T) {
	for _, raw := range []string{`{}`, `{"version":2}`, `{"version":1,"accountId":"private"}`, `{"version":1} {}`, `not-json`} {
		if validWake([]byte(raw)) {
			t.Fatal("invalid wake accepted", raw)
		}
	}
	if !validWake([]byte(`{"version": 1}`)) {
		t.Fatal("JSONB whitespace rejected")
	}
}
func TestExtractionWakePropagatesAckAndDeferFailure(t *testing.T) {
	failed := errors.New("queue unavailable")
	for _, pending := range []time.Time{{}, time.Now().Add(time.Minute)} {
		q := &fakeWakeQueue{found: true, errorAfter: failed}
		if err := RunExtractionWake(context.Background(), q, func(context.Context) error { return nil }, func(context.Context) (time.Time, error) { return pending, nil }); !errors.Is(err, failed) {
			t.Fatal(err)
		}
	}
}

func TestRecognitionWakeVisibilityCoversOnlyItsLongJob(t *testing.T) {
	if extractionWakeVisibility(false) != 180*time.Second {
		t.Fatal("legacy queue budget changed")
	}
	if extractionWakeVisibility(true) <= RecognitionJobTimeout+2*time.Minute {
		t.Fatal("recognition wake can expire during the Job")
	}
}
