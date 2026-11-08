package publication

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

type InboxPublication struct {
	EventID       string    `json:"eventId"`
	Kind          string    `json:"kind"`
	ResourceID    string    `json:"resourceId"`
	Series        string    `json:"series,omitempty"`
	Locale        string    `json:"locale,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	SourceVersion int64     `json:"sourceVersion"`
}
type InboxPublicationRepository interface {
	ClaimInboxPublication(context.Context, time.Time, time.Duration) (Event, bool, error)
	FinishInboxPublication(context.Context, Event, bool, time.Time) error
}
type InboxPublicationClient interface {
	SubmitInboxPublication(context.Context, InboxPublication) error
}

func DispatchInboxPublication(ctx context.Context, repo InboxPublicationRepository, client InboxPublicationClient, now time.Time) (bool, error) {
	event, ok, err := repo.ClaimInboxPublication(ctx, now, 30*time.Second)
	if err != nil || !ok {
		return ok, err
	}
	var input InboxPublication
	err = json.Unmarshal(event.Payload, &input)
	if err == nil {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = client.SubmitInboxPublication(callCtx, input)
		cancel()
	}
	finishErr := repo.FinishInboxPublication(ctx, event, err == nil, now)
	if finishErr != nil {
		return true, finishErr
	}
	return true, err
}
func RunInboxPublications(ctx context.Context, repo InboxPublicationRepository, client InboxPublicationClient) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for range 100 {
			ok, err := DispatchInboxPublication(ctx, repo, client, time.Now().UTC())
			if err != nil && ctx.Err() == nil {
				slog.Warn("inbox publication delivery unavailable")
			}
			if !ok || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
