package assetclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

var (
	ErrNotFound    = errors.New("asset resource not found")
	ErrUnavailable = errors.New("asset service unavailable")
)

type Asset struct {
	ID               string `json:"id"`
	Namespace        string `json:"namespace"`
	OwnerService     string `json:"ownerService"`
	OwnerType        string `json:"ownerType"`
	OwnerID          string `json:"ownerId"`
	Purpose          string `json:"purpose"`
	Locale           string `json:"locale"`
	OriginalFileName string `json:"originalFileName"`
	ExpectedMIMEType string `json:"expectedMimeType"`
	UploadStatus     string `json:"uploadStatus"`
	ScanStatus       string `json:"scanStatus"`
	ProcessingStatus string `json:"processingStatus"`
	DetectedMIMEType string `json:"detectedMimeType"`
	Visibility       string `json:"visibility"`
}
type Grant struct {
	ID      string `json:"id"`
	AssetID string `json:"assetId"`
}
type UploadTarget struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}
type CreatedUpload struct {
	Asset        Asset        `json:"asset"`
	UploadTarget UploadTarget `json:"uploadTarget"`
}
type CompleteUploadInput struct {
	SizeBytes      int64  `json:"sizeBytes"`
	ChecksumSHA256 string `json:"checksumSha256"`
	MIMEType       string `json:"mimeType"`
}
type Client struct {
	baseURL       string
	caller        string
	publicBaseURL string
	http          *http.Client
}

func New(baseURL, caller, publicBaseURL string) *Client {
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		caller:        caller,
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
		http:          &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 10 * time.Second},
	}
}
func (c *Client) Get(ctx context.Context, id string) (Asset, error) {
	var value Asset
	err := c.request(ctx, http.MethodGet, "/priv/assets/"+url.PathEscape(id), nil, "", &value)
	return value, err
}
func (c *Client) CreateBulletinUpload(ctx context.Context, issueID, locale, fileName, mimeType string, sizeBytes int64, key string) (CreatedUpload, error) {
	body := map[string]any{
		"namespace":        "cms.weekly.pdf",
		"ownerService":     "hhc-web-api",
		"ownerType":        "bulletin_issue",
		"ownerId":          issueID,
		"purpose":          "weekly_bulletin",
		"locale":           locale,
		"originalFileName": fileName,
		"expectedMimeType": mimeType,
		"maxSizeBytes":     sizeBytes,
		"visibility":       "public",
	}
	var value CreatedUpload
	err := c.request(ctx, http.MethodPost, "/priv/assets/upload-sessions", body, key, &value)
	return value, err
}
func (c *Client) CreateNewsCoverUpload(ctx context.Context, newsID, purpose, fileName, mimeType string, sizeBytes int64, key string) (CreatedUpload, error) {
	body := map[string]any{
		"namespace": "cms.news.cover", "ownerService": "hhc-web-api", "ownerType": "news", "ownerId": newsID,
		"purpose": purpose, "originalFileName": fileName, "expectedMimeType": mimeType,
		"maxSizeBytes": sizeBytes, "visibility": "public",
	}
	var value CreatedUpload
	err := c.request(ctx, http.MethodPost, "/priv/assets/upload-sessions", body, key, &value)
	return value, err
}
func (c *Client) CreateHomeBannerUpload(ctx context.Context, homeID, fileName, mimeType string, sizeBytes int64, key string) (CreatedUpload, error) {
	body := map[string]any{
		"namespace": "cms.home.banner", "ownerService": "hhc-web-api", "ownerType": "page", "ownerId": homeID,
		"purpose": "home_banner", "originalFileName": fileName, "expectedMimeType": mimeType,
		"maxSizeBytes": sizeBytes, "visibility": "public",
	}
	var value CreatedUpload
	err := c.request(ctx, http.MethodPost, "/priv/assets/upload-sessions", body, key, &value)
	return value, err
}
func (c *Client) CompleteUpload(ctx context.Context, id string, input CompleteUploadInput) (Asset, error) {
	var value Asset
	err := c.request(ctx, http.MethodPost, "/priv/assets/"+url.PathEscape(id)+"/complete", input, "", &value)
	return value, err
}
func (c *Client) CreatePublicGrant(ctx context.Context, id, key string) (Grant, error) {
	body := map[string]any{"subjectType": "public", "subjectId": "*", "permission": "read", "idempotencyKey": key}
	var value Grant
	err := c.request(ctx, http.MethodPost, "/priv/assets/"+url.PathEscape(id)+"/grants", body, key, &value)
	return value, err
}
func (c *Client) RevokeGrant(ctx context.Context, assetID, grantID string) error {
	return c.request(ctx, http.MethodDelete, "/priv/assets/"+url.PathEscape(assetID)+"/grants/"+url.PathEscape(grantID), nil, "", nil)
}
func (c *Client) Delete(ctx context.Context, assetID string) error {
	err := c.request(ctx, http.MethodDelete, "/priv/assets/"+url.PathEscape(assetID), nil, "", nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
func (c *Client) RequeueScan(ctx context.Context, assetID string) error {
	return c.request(ctx, http.MethodPost, "/priv/assets/"+url.PathEscape(assetID)+"/scan/requeue", nil, "", nil)
}
func (c *Client) PublicURL(assetID string) string {
	return c.publicBaseURL + "/" + url.PathEscape(assetID)
}
func (c *Client) request(ctx context.Context, method, path string, body any, idempotency string, destination any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Internal-Caller-App-Id", c.caller)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotency != "" {
		request.Header.Set("Idempotency-Key", idempotency)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if response.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("%w: asset api status %d", ErrUnavailable, response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("asset api status %d: %s", response.StatusCode, strings.TrimSpace(string(payload)))
	}
	if destination == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(destination); err != nil {
		return fmt.Errorf("decode asset api response: %w", err)
	}
	return nil
}
