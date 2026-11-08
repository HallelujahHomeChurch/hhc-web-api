package operationsclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalid     = errors.New("invalid entitlement request")
	ErrUnavailable = errors.New("entitlement authority unavailable")
	codePattern    = regexp.MustCompile(`^[a-z][A-Za-z0-9.-]{0,99}$`)
)

type EntitlementDecision struct {
	EntitlementCode string `json:"entitlementCode"`
	Decision        string `json:"decision"`
	Reason          string `json:"reason"`
	Version         string `json:"version"`
}

type Client struct {
	baseURL      string
	privateToken string
	http         *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 3 * time.Second}}
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
		base = "https://operations-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err == nil && c.privateToken != "" {
		req.Header.Set("X-HHC-Operations-Token", c.privateToken)
	}
	return req, err
}

func (c *Client) CheckEntitlements(ctx context.Context, subjectID string, codes []string) (decisions []EntitlementDecision, resultErr error) {
	parsedSubject, err := uuid.Parse(subjectID)
	if err != nil || parsedSubject.String() != subjectID || parsedSubject.Version() != 4 && parsedSubject.Version() != 7 || len(codes) == 0 || len(codes) > 20 {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, len(codes))
	for _, code := range codes {
		if !codePattern.MatchString(code) || seen[code] {
			return nil, ErrInvalid
		}
		seen[code] = true
	}
	body, err := json.Marshal(struct {
		SubjectID        string   `json:"subjectId"`
		EntitlementCodes []string `json:"entitlementCodes"`
	}{subjectID, codes})
	if err != nil {
		return nil, ErrInvalid
	}
	ctx, observation := observe(ctx)
	defer func() { observation.finish(ctx, resultErr) }()
	request, err := c.request(ctx, "/priv/operations/entitlement-checks", body)
	if err != nil {
		return nil, ErrInvalid
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	observation.headers(ctx, request)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	observation.status = response.StatusCode
	observation.setPhase("response_status")
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	observation.setPhase("response_body")
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if len(responseBody) > 64<<10 {
		return nil, fmt.Errorf("%w: invalid response", ErrUnavailable)
	}
	observation.setPhase("decode")
	var result struct {
		Decisions []EntitlementDecision `json:"decisions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: invalid response", ErrUnavailable)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || len(result.Decisions) != len(codes) {
		return nil, fmt.Errorf("%w: invalid response", ErrUnavailable)
	}
	for index, decision := range result.Decisions {
		if decision.EntitlementCode != codes[index] || !validDecision(decision) {
			return nil, fmt.Errorf("%w: invalid response", ErrUnavailable)
		}
	}
	observation.setPhase("complete")
	return result.Decisions, nil
}

func validDecision(decision EntitlementDecision) bool {
	version, err := hex.DecodeString(decision.Version)
	if err != nil || len(version) != 32 {
		return false
	}
	if decision.Decision == "allow" {
		return decision.Reason == "allowed"
	}
	return decision.Decision == "deny" && (decision.Reason == "membership_required" || decision.Reason == "membership_suspended" || decision.Reason == "entitlement_required")
}
