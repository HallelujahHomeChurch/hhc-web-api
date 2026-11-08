package engagementclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

type NotificationCommand = publication.NotificationCommand
type NotificationSubmission = publication.NotificationSubmission
type BulletinSource = publication.BulletinSource

func (c *Client) SubmitNotifications(ctx context.Context, command NotificationSubmission) error {
	body, err := json.Marshal(command)
	if err != nil {
		return err
	}
	request, err := c.request(ctx, http.MethodPost, "/priv/campaign-submissions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-HHC-Actor-ID", command.ActorID)
	request.Header.Set("X-HHC-Request-ID", command.RequestID)
	request.Header.Set("Idempotency-Key", command.IdempotencyKey)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("engagement api unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return responseError{status: response.StatusCode}
	}
	return nil
}
