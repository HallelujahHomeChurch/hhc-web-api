package accountclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL, caller string
	privateToken    string
	http            *http.Client
}

func New(baseURL, caller string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), caller: caller, http: &http.Client{Timeout: 8 * time.Second}}
}

func (c *Client) ConfigurePrivateHTTPS(token string) {
	c.privateToken = token
	if token != "" {
		c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
}

func (c *Client) request(ctx context.Context, path string, body []byte) (*http.Request, error) {
	base := c.baseURL
	if c.privateToken != "" {
		base = "https://account-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.privateToken != "" {
		req.Header.Set("X-HHC-Account-Service-Caller", c.caller)
		req.Header.Set("X-HHC-Account-Service-Token", c.privateToken)
	} else {
		req.Header.Set("X-Internal-Caller-App-Id", c.caller)
	}
	return req, nil
}

type Identity struct {
	AvatarURL   string `json:"avatarUrl,omitempty"`
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

func (c *Client) ResolveIdentities(ctx context.Context, ids []string) ([]Identity, error) {
	if len(ids) == 0 {
		return []Identity{}, nil
	}
	if len(ids) > 100 {
		return nil, fmt.Errorf("identity batch exceeds 100")
	}
	body, _ := json.Marshal(map[string][]string{"ids": ids})
	request, err := c.request(ctx, "/priv/account/v1/user-identities/resolve", body)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("account api returned status %d", response.StatusCode)
	}
	var result struct {
		Data []Identity `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if result.Data == nil {
		result.Data = []Identity{}
	}
	return result.Data, nil
}

// VerifyRecordingPublication is the fixed, trusted-CMS background eligibility
// probe. Account verifies that the original authorizer is still active.
func (c *Client) VerifyRecordingPublication(ctx context.Context, userID string) (bool, error) {
	body, _ := json.Marshal(struct {
		UserID     string `json:"userId"`
		Permission string `json:"permission"`
	}{userID, "cms:recordings:publish"})
	request, err := c.request(ctx, "/priv/account/v1/permissions/verify", body)
	if err != nil {
		return false, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("account permission probe returned status %d", response.StatusCode)
	}
	var result struct {
		Allowed *bool `json:"allowed"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<16)+1))
	if err != nil || len(data) > 1<<16 || json.Unmarshal(data, &result) != nil || result.Allowed == nil {
		return false, fmt.Errorf("invalid account permission response")
	}
	return *result.Allowed, nil
}
