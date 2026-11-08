package publication

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecordingNotificationRechecksPublication(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available bool
		err       error
	}{{"published", true, nil}, {"unpublished", false, nil}, {"unavailable", false, errors.New("timeout")}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &workerRepository{}
			notifications := &workerNotifications{}
			worker := NewWorker(repo, &workerAssets{}, 5, notifications)
			worker.WithRecordingPolicy(func(context.Context, string) (bool, error) { return tc.available, tc.err })
			event := Event{ID: "event", EventType: "recording.notification.submit", AggregateID: "recording", Payload: []byte(`{"name":"Recording","source":{"kind":"recording","recordingId":"recording"},"notifications":[{"channel":"email"}],"actorId":"actor","requestId":"request","idempotencyKey":"stable-key"}`)}
			err := worker.submitNotifications(context.Background(), event, time.Now())
			if (err != nil) != (tc.err != nil) {
				t.Fatalf("err=%v", err)
			}
			if (notifications.submission.IdempotencyKey != "") != tc.available {
				t.Fatal("unexpected notification submission")
			}
			if (repo.completeCount == 1) != (tc.err == nil) {
				t.Fatal("incorrect completion")
			}
		})
	}
}
