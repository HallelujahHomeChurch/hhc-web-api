package engagementclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

type Client struct {
	baseURL      string
	caller       string
	privateToken string
	http         *http.Client
}

func (c *Client) GetTranslationSource(ctx context.Context, module, resourceID string) (translation.SavedSource, error) {
	if module != "campaigns" && module != "campaign-schedules" {
		return translation.SavedSource{}, translation.ErrInvalidRequest
	}
	request, err := c.request(ctx, http.MethodGet, "/priv/"+module+"/"+resourceID+"/translation-source", nil)
	if err != nil {
		return translation.SavedSource{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return translation.SavedSource{}, fmt.Errorf("engagement api unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadRequest {
		return translation.SavedSource{}, translation.ErrInvalidRequest
	}
	if response.StatusCode == http.StatusNotFound {
		return translation.SavedSource{}, translation.ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return translation.SavedSource{}, responseError{status: response.StatusCode}
	}
	var body struct {
		Data translation.SavedSource `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return translation.SavedSource{}, responseError{status: http.StatusBadGateway}
	}
	return body.Data, nil
}

func (c *Client) QueueBulletinNotification(ctx context.Context, payload publication.BulletinNotificationPayload) error {
	var failures []error
	for _, channel := range []struct{ name, audience string }{{"email", "newsletter"}, {"web_push", "all"}} {
		if err := c.queueBulletinChannel(ctx, payload, channel.name, "bulletin_members"); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return bulletinChannelErrors(failures)
}

func (c *Client) queueBulletinChannel(ctx context.Context, payload publication.BulletinNotificationPayload, channel, audience string) error {
	body, err := json.Marshal(map[string]any{
		"name": payload.Name, "channel": channel, "audienceType": audience, "translations": payload.Translations,
		"source": map[string]any{"kind": "bulletin", "issueId": payload.IssueID, "notificationGeneration": 0},
	})
	if err != nil {
		return err
	}
	request, err := c.request(ctx, http.MethodPost, "/priv/campaigns", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-HHC-Actor-ID", payload.ActorID)
	request.Header.Set("X-HHC-Request-ID", payload.RequestID)
	request.Header.Set("Idempotency-Key", "bulletin:"+payload.IssueID+":"+strings.ReplaceAll(channel, "_", "-"))
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
	queue, err := c.request(ctx, http.MethodPost, "/priv/campaigns/"+created.Data.ID+"/send", nil)
	if err != nil {
		return err
	}
	queue.Header.Set("Accept", "application/json")
	queue.Header.Set("X-HHC-Actor-ID", payload.ActorID)
	queue.Header.Set("X-HHC-Request-ID", payload.RequestID)
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

func (c *Client) ConfigurePrivateHTTPS(token string) {
	c.privateToken = token
	if token != "" {
		c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	base := c.baseURL
	if c.privateToken != "" {
		base = "https://engagement-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, err
	}
	if c.privateToken != "" {
		req.Header.Set("X-HHC-Engagement-CMS-Token", c.privateToken)
	} else {
		req.Header.Set("X-HHC-Caller-App-Id", c.caller)
	}
	return req, nil
}

func (c *Client) Forward(ctx context.Context, method, path string, body io.Reader, actor, requestID string) (*http.Response, error) {
	return c.forward(ctx, method, path, body, actor, requestID, "")
}

func (c *Client) ForwardSubmission(ctx context.Context, body io.Reader, actor, requestID, idempotencyKey string) (*http.Response, error) {
	return c.forward(ctx, http.MethodPost, "/priv/campaign-submissions", body, actor, requestID, idempotencyKey)
}

func (c *Client) forward(ctx context.Context, method, path string, body io.Reader, actor, requestID, idempotencyKey string) (*http.Response, error) {
	request, err := c.request(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-HHC-Actor-ID", actor)
	request.Header.Set("X-HHC-Request-ID", requestID)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("engagement api unavailable: %w", err)
	}
	return response, nil
}

type bulletinChannelErrors []error

func (e bulletinChannelErrors) Error() string   { return errors.Join([]error(e)...).Error() }
func (e bulletinChannelErrors) Unwrap() []error { return []error(e) }
func (e bulletinChannelErrors) Permanent() bool {
	for _, err := range e {
		var p interface{ Permanent() bool }
		if !errors.As(err, &p) || !p.Permanent() {
			return false
		}
	}
	return true
}
