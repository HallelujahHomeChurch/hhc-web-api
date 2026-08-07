package engagementclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL   string
	caller    string
	daprToken string
	http      *http.Client
}

func New(baseURL, caller, daprToken string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), caller: caller, daprToken: daprToken,
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
	if c.daprToken != "" {
		request.Header.Set("dapr-api-token", c.daprToken)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("engagement api unavailable: %w", err)
	}
	return response, nil
}
