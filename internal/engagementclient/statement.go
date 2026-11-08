package engagementclient

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"io"
	"net/http"
)

func (c *Client) CreateStatementCampaign(ctx context.Context, b content.StatementNotificationBatch) (string, error) {
	audience := "all"
	if b.Channel == "email" {
		audience = "newsletter"
	}
	body, _ := json.Marshal(map[string]any{"name": "Website notification", "channel": b.Channel, "audienceType": audience, "translations": map[string]publication.NotificationTranslation{"zh-Hant": {Subject: b.Subject, Body: b.Body, ClickBehavior: "url", ActionURL: b.Href}}})
	request, err := c.request(ctx, http.MethodPost, "/priv/campaigns", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-HHC-Actor-ID", b.ActorID)
	request.Header.Set("X-HHC-Request-ID", b.RequestID)
	request.Header.Set("Idempotency-Key", b.IdempotencyKey())
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", responseError{status: response.StatusCode}
	}
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || result.Data.ID == "" {
		return "", responseError{status: http.StatusBadGateway}
	}
	return result.Data.ID, nil
}
func (c *Client) CancelStatementCampaign(ctx context.Context, id, requestID string) error {
	return c.statementCampaignAction(ctx, id, "cancel", requestID)
}
func (c *Client) SendStatementCampaign(ctx context.Context, id, requestID string) error {
	return c.statementCampaignAction(ctx, id, "send", requestID)
}
func (c *Client) statementCampaignAction(ctx context.Context, id, action, requestID string) error {
	response, err := c.Forward(ctx, http.MethodPost, "/priv/campaigns/"+id+"/"+action, nil, "", requestID)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	if action == "send" && response.StatusCode == http.StatusConflict {
		// A lost send response is only successful when the campaign actually left draft.
		state, err := c.Forward(ctx, http.MethodGet, "/priv/campaigns/"+id, nil, "", requestID)
		if err != nil {
			return err
		}
		defer state.Body.Close()
		var result struct {
			Data struct {
				Status      string  `json:"status"`
				CancelledAt *string `json:"cancelledAt"`
			} `json:"data"`
		}
		if state.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(state.Body, 1<<20)).Decode(&result) == nil && (result.Data.CancelledAt != nil || (result.Data.Status != "" && result.Data.Status != "draft")) {
			return nil
		}
	}
	return responseError{status: response.StatusCode}
}
