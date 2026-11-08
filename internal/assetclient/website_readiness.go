package assetclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"time"
)

// Website owns audience/publication authority; Asset only verifies these bindings.
type WebsiteContentRef struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	ResourceID string `json:"resourceId"`
	Locale     string `json:"locale,omitempty"`
}
type WebsiteLiveReadiness struct {
	State      string                `json:"state"`
	CreatedAt  time.Time             `json:"createdAt"`
	ExpiresAt  time.Time             `json:"expiresAt"`
	TerminalAt *time.Time            `json:"terminalAt"`
	Progress   RecordingLiveProgress `json:"progress"`
}
type WebsiteContentDecision struct {
	Ref   WebsiteContentRef     `json:"ref"`
	Ready bool                  `json:"ready"`
	Live  *WebsiteLiveReadiness `json:"live,omitempty"`
}

var websiteContentID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func ValidateWebsiteContentRefs(refs []WebsiteContentRef) error {
	if len(refs) < 1 || len(refs) > 100 {
		return ErrInvalid
	}
	seen := map[WebsiteContentRef]bool{}
	for _, ref := range refs {
		if !websiteContentID.MatchString(ref.ID) || !websiteContentID.MatchString(ref.ResourceID) || seen[ref] {
			return ErrInvalid
		}
		seen[ref] = true
		switch ref.Type {
		case "bulletin_pdf":
			if ref.Locale != "zh-Hant" && ref.Locale != "zh-Hans" && ref.Locale != "en" {
				return ErrInvalid
			}
		case "recording_live":
			if ref.Locale != "" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

func (c *Client) WebsiteContentReadiness(ctx context.Context, refs []WebsiteContentRef) ([]WebsiteContentDecision, error) {
	if err := ValidateWebsiteContentRefs(refs); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Items []WebsiteContentRef `json:"items"`
	}{refs})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/priv/website/content-readiness", bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Dapr-Caller-App-Id", c.caller)
	if token := os.Getenv("DAPR_API_TOKEN"); token != "" {
		request.Header.Set("dapr-api-token", token)
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return nil, ErrUnavailable
	}
	type liveWire struct {
		State      string                 `json:"state"`
		CreatedAt  time.Time              `json:"createdAt"`
		ExpiresAt  time.Time              `json:"expiresAt"`
		TerminalAt *time.Time             `json:"terminalAt"`
		Progress   *RecordingLiveProgress `json:"progress"`
	}
	var wire struct {
		Items []struct {
			Ref   WebsiteContentRef `json:"ref"`
			Ready *bool             `json:"ready"`
			Live  *liveWire         `json:"live,omitempty"`
		} `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || len(wire.Items) != len(refs) {
		return nil, ErrUnavailable
	}
	items := make([]WebsiteContentDecision, len(refs))
	for i, d := range wire.Items {
		if d.Ref != refs[i] || d.Ready == nil {
			return nil, ErrUnavailable
		}
		ready := *d.Ready
		if !ready && d.Live != nil || d.Ref.Type == "bulletin_pdf" && d.Live != nil || ready && d.Ref.Type == "recording_live" && d.Live == nil {
			return nil, ErrUnavailable
		}
		if d.Live != nil {
			v := d.Live
			switch v.State {
			case "uploading", "freezing", "validating", "ready":
			default:
				return nil, ErrUnavailable
			}
			if v.Progress == nil || v.TerminalAt != nil || v.CreatedAt.IsZero() || !v.ExpiresAt.After(v.CreatedAt) || v.Progress.Revision < 0 || v.Progress.LastSequence < -1 || v.Progress.LastSequence > 4319 || math.IsNaN(v.Progress.MediaEndSeconds) || math.IsInf(v.Progress.MediaEndSeconds, 0) || v.Progress.MediaEndSeconds < 0 {
				return nil, ErrUnavailable
			}
		}
		items[i] = WebsiteContentDecision{Ref: d.Ref, Ready: ready}
		if d.Live != nil {
			items[i].Live = &WebsiteLiveReadiness{State: d.Live.State, CreatedAt: d.Live.CreatedAt, ExpiresAt: d.Live.ExpiresAt, TerminalAt: d.Live.TerminalAt, Progress: *d.Live.Progress}
		}
	}
	return items, nil
}
