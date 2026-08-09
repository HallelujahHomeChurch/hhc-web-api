package engagementclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

type Client struct {
	baseURL string
	caller  string
	http    *http.Client
}

func (c *Client) QueueBulletinNotification(ctx context.Context, payload publication.BulletinNotificationPayload) error {
	body, err := json.Marshal(map[string]any{
		"name": payload.Name, "channel": "web_push", "audienceType": "all", "translations": payload.Translations,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/priv/campaigns", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-HHC-Caller-App-Id", c.caller)
	request.Header.Set("X-HHC-Actor-ID", payload.ActorID)
	request.Header.Set("Idempotency-Key", "bulletin:"+payload.IssueID+":web-push")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("engagement api unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError{status: response.StatusCode}
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&created); err != nil || created.Data.ID == "" {
		return responseError{status: http.StatusBadGateway}
	}
	queue, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/priv/campaigns/"+created.Data.ID+"/send", nil)
	if err != nil {
		return err
	}
	queue.Header.Set("Accept", "application/json")
	queue.Header.Set("X-HHC-Caller-App-Id", c.caller)
	queue.Header.Set("X-HHC-Actor-ID", payload.ActorID)
	queued, err := c.http.Do(queue)
	if err != nil {
		return fmt.Errorf("engagement api unavailable: %w", err)
	}
	defer queued.Body.Close()
	if (queued.StatusCode < 200 || queued.StatusCode >= 300) && queued.StatusCode != http.StatusConflict {
		return responseError{status: queued.StatusCode}
	}
	return nil
}

type responseError struct{ status int }

func (e responseError) Error() string {
	return fmt.Sprintf("engagement api returned status %d", e.status)
}
func (e responseError) Permanent() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusTooManyRequests
}

func New(baseURL, caller string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), caller: caller,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Forward(ctx context.Context, method, path string, body io.Reader, actor string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-HHC-Caller-App-Id", c.caller)
	request.Header.Set("X-HHC-Actor-ID", actor)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("engagement api unavailable: %w", err)
	}
	return response, nil
}
