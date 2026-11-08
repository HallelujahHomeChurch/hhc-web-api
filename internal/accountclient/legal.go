package accountclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
)

func (c *Client) CheckLegalAcceptance(ctx context.Context, subject, scope, terms, privacy string) (bool, error) {
	id, err := uuid.Parse(subject)
	if err != nil || id == uuid.Nil || id.String() != subject || (scope != "common" && scope != "verified-member") || terms == "" || privacy == "" {
		return false, fmt.Errorf("invalid legal check")
	}
	raw, _ := json.Marshal(map[string]string{"subjectId": subject, "scope": scope, "termsVersion": terms, "privacyNoticeVersion": privacy})
	req, err := c.request(ctx, "/priv/account/v1/policy-acceptance/check", raw)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("legal check unavailable")
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return false, fmt.Errorf("invalid legal check response")
	}
	var value struct {
		Accepted *bool `json:"accepted"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Accepted == nil {
		return false, fmt.Errorf("invalid legal check response")
	}
	return *value.Accepted, nil
}
