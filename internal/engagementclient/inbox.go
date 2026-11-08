package engagementclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"io"
	"net/http"
	"os"
)

func (c *Client) SubmitInboxPublication(ctx context.Context, input publication.InboxPublication) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/priv/engagement/inbox-publications", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Dapr-Caller-App-Id", c.caller)
	if token := os.Getenv("DAPR_API_TOKEN"); token != "" {
		r.Header.Set("dapr-api-token", token)
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result struct {
		Error json.RawMessage `json:"error"`
		Data  struct {
			Accepted bool `json:"accepted"`
		} `json:"data"`
	}
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if response.StatusCode != http.StatusAccepted || readErr != nil || len(payload) > 4096 || decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || !result.Data.Accepted || (len(result.Error) > 0 && string(result.Error) != "null") {
		return fmt.Errorf("inbox publication not accepted")
	}
	return nil
}
