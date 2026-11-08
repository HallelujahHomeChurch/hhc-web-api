package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

func (w *Worker) notifyStatement(ctx context.Context, event Event, now time.Time) (err error) {
	if w.notifications == nil {
		return terminalError{fmt.Errorf("notification client is not configured")}
	}
	var ref struct {
		BatchID string `json:"batchId"`
	}
	if err := json.Unmarshal(event.Payload, &ref); err != nil || ref.BatchID == "" {
		return terminalError{fmt.Errorf("invalid statement notification payload")}
	}
	batch, err := w.repository.StatementNotification(ctx, ref.BatchID)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil && batch.CancelledAt != nil {
			err = compensationError{err}
		}
	}()
	if batch.CampaignID == "" {
		campaignID, err := w.notifications.CreateStatementCampaign(ctx, batch)
		if err != nil {
			return err
		}
		if err = w.repository.SetStatementCampaign(ctx, batch.ID, campaignID); err != nil {
			return err
		}
	}
	// Re-read durable cancellation after recovering a possibly lost create response.
	batch, err = w.repository.StatementNotification(ctx, ref.BatchID)
	if err != nil {
		return err
	}
	if batch.CancelledAt != nil {
		err = w.notifications.CancelStatementCampaign(ctx, batch.CampaignID, batch.RequestID)
	} else {
		err = w.notifications.SendStatementCampaign(ctx, batch.CampaignID, batch.RequestID)
	}
	if err != nil {
		return err
	}
	return w.repository.CompleteStatementNotification(ctx, event, batch, now)
}
