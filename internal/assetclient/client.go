package assetclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

var (
	ErrNotFound              = errors.New("asset resource not found")
	ErrUnavailable           = errors.New("asset service unavailable")
	ErrInvalid               = errors.New("invalid asset request")
	ErrConflict              = errors.New("asset conflict")
	ErrForbidden             = errors.New("asset access denied")
	ErrPackageTooLarge       = errors.New("package_too_large")
	ErrSourceTooLarge        = errors.New("source_too_large")
	errLifecycleBodyRejected = fmt.Errorf("%w: rejected lifecycle request body", ErrInvalid)
)

type Asset struct {
	ETag                string               `json:"etag"`
	ProcessingReadiness *ProcessingReadiness `json:"processingReadiness,omitempty"`
	SafetyEvidence      *SafetyEvidence      `json:"safetyEvidence,omitempty"`
	ID                  string               `json:"id"`
	Namespace           string               `json:"namespace"`
	OwnerService        string               `json:"ownerService"`
	OwnerType           string               `json:"ownerType"`
	OwnerID             string               `json:"ownerId"`
	Purpose             string               `json:"purpose"`
	Locale              string               `json:"locale"`
	OriginalFileName    string               `json:"originalFileName"`
	ExpectedMIMEType    string               `json:"expectedMimeType"`
	UploadStatus        string               `json:"uploadStatus"`
	ScanStatus          string               `json:"scanStatus"`
	ScanSignature       string               `json:"scanSignatureVersion"`
	ProcessingStatus    string               `json:"processingStatus"`
	DetectedMIMEType    string               `json:"detectedMimeType"`
	Visibility          string               `json:"visibility"`
	ChecksumSHA256      string               `json:"checksumSha256"`
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
type RecordingGrant struct {
	ExchangeCredential string    `json:"exchangeCredential"`
	ExpiresAt          time.Time `json:"expiresAt"`
}
type Client struct {
	ownerContent, trustedDerivation bool
	serviceSubject                  string
	baseURL                         string
	caller                          string
	publicBaseURL                   string
	http                            *http.Client
}

func New(baseURL, caller, publicBaseURL string) *Client {
	return &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		caller:         caller,
		serviceSubject: caller,
		publicBaseURL:  strings.TrimRight(publicBaseURL, "/"),
		http:           &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(privateTransportProvider{otel.GetTracerProvider()})), Timeout: 10 * time.Second},
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
		"visibility":       "private",
	}
	var value CreatedUpload
	err := c.request(ctx, http.MethodPost, "/priv/assets/upload-sessions", body, key, &value)
	return value, err
}
func (c *Client) CreateDerivedBulletinUpload(ctx context.Context, jobID, locale, fileName string, sizeBytes int64, key string) (CreatedUpload, error) {
	body := map[string]any{
		"namespace": "cms.weekly.pdf", "ownerService": "hhc-web-api", "ownerType": "bulletin_download_job", "ownerId": jobID,
		"purpose": "weekly_bulletin_member", "locale": locale, "originalFileName": fileName, "expectedMimeType": "application/pdf",
		"maxSizeBytes": sizeBytes, "visibility": "private",
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

func (c *Client) CompleteDerivedBulletin(ctx context.Context, id, sourceID, sourceSHA256, sourceETag string, input CompleteUploadInput) (Asset, error) {
	if c.trustedDerivation {
		if sourceETag == "" {
			return Asset{}, ErrInvalid
		}
		body := struct {
			CompleteUploadInput
			Derivation any `json:"derivation"`
		}{input, struct {
			SourceAssetID    string `json:"sourceAssetId"`
			SourceSHA256     string `json:"sourceSha256"`
			SourceETag       string `json:"sourceETag"`
			Transform        string `json:"transform"`
			TransformVersion string `json:"transformVersion"`
		}{sourceID, sourceSHA256, sourceETag, "bulletin-watermark", "1"}}
		var value Asset
		if err := c.request(ctx, http.MethodPost, "/priv/assets/"+url.PathEscape(id)+"/complete", body, "", &value); err != nil {
			return Asset{}, err
		}
		if value.ID != id || value.ProcessingReadiness == nil || value.ProcessingReadiness.State != "ready" || value.SafetyEvidence == nil || value.SafetyEvidence.Kind != "trusted-derived" {
			return Asset{}, ErrUnavailable
		}
		return value, nil
	}
	body := struct {
		CompleteUploadInput
		SourceAssetID string `json:"sourceAssetId"`
		SourceSHA256  string `json:"sourceSha256"`
	}{input, sourceID, sourceSHA256}
	var value Asset
	err := c.request(ctx, http.MethodPost, "/priv/assets/bulletin-derivatives/"+url.PathEscape(id)+"/complete", body, "", &value)
	return value, err
}
func (c *Client) Upload(ctx context.Context, target UploadTarget, body io.Reader, size int64) error {
	parsed, err := url.Parse(target.URL)
	if err != nil || (parsed.Scheme != "https" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") || target.Method != http.MethodPut || size < 1 || size > 20<<20 {
		return ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, target.Method, target.URL, body)
	if err != nil {
		return err
	}
	request.ContentLength = size
	for key, value := range target.Headers {
		request.Header.Set(key, value)
	}
	// Signed storage targets cannot supply service identity headers.
	for _, header := range []string{"Authorization", "X-HHC-Workload-Authorization", "X-HHC-Caller-App-Id", "X-Internal-Caller-App-Id", "Dapr-Caller-App-Id", "Dapr-Api-Token"} {
		request.Header.Del(header)
	}
	_, workload := c.http.Transport.(*internalTransport)
	uploadClient := *c.http
	if transport, ok := uploadClient.Transport.(*internalTransport); ok {
		// Blob upload targets use SAS authorization, not the Asset API credential.
		uploadClient.Transport = transport.base
	}
	response, err := uploadClient.Do(request)
	if err != nil {
		if workload {
			slog.WarnContext(ctx, "bulletin_asset_failed", "operation", "blob_upload", "stage", "transport")
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if workload {
			slog.WarnContext(ctx, "bulletin_asset_failed", "operation", "blob_upload", "stage", "http", "status", response.StatusCode)
		}
		return fmt.Errorf("%w: upload status %d", ErrUnavailable, response.StatusCode)
	}
	return nil
}
func (c *Client) CreatePublicGrant(ctx context.Context, id, key string) (Grant, error) {
	body := map[string]any{"subjectType": "public", "subjectId": "*", "permission": "read", "idempotencyKey": key}
	var value Grant
	err := c.request(ctx, http.MethodPost, "/priv/assets/"+url.PathEscape(id)+"/grants", body, key, &value)
	return value, err
}
func (c *Client) CreateServiceGrant(ctx context.Context, id, key string) (Grant, error) {
	return c.createServiceGrant(ctx, id, key, nil)
}
func (c *Client) createServiceGrant(ctx context.Context, id, key string, expiresAt *time.Time) (Grant, error) {
	body := map[string]any{"subjectType": "service", "subjectId": c.serviceSubject, "permission": "read", "idempotencyKey": key}
	if expiresAt != nil {
		body["expiresAt"] = expiresAt
	}
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
func (c *Client) AuthorizedDownload(ctx context.Context, method, assetID, byteRange, ifRange string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/priv/assets/"+url.PathEscape(assetID)+"/download", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Internal-Caller-App-Id", c.caller)
	request.Header.Set("X-Asset-Subject-Type", "service")
	request.Header.Set("X-Asset-Subject-Id", c.serviceSubject)
	request.Header.Set("Range", byteRange)
	request.Header.Set("If-Range", ifRange)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return response, nil
}
func (c *Client) request(ctx context.Context, method, path string, body any, idempotency string, destination any) (result error) {
	ctx, finish := recordingRequestDiagnostics(ctx, method, path)
	var transportError error
	status := 0
	defer func() { finish(result, transportError, status) }()
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
	if provenance, ok := auditclient.ProvenanceFromContext(ctx); ok {
		request.Header.Set("X-HHC-Actor-ID", provenance.ActorID)
		request.Header.Set("X-HHC-Request-ID", provenance.RequestID)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotency != "" {
		request.Header.Set("Idempotency-Key", idempotency)
	}
	client := c.http
	if strings.HasPrefix(path, "/priv/recording-uploads/") && strings.HasSuffix(path, "/complete") {
		long := *c.http
		long.Timeout = 2 * time.Minute
		client = &long
	}
	headersStarted := time.Now()
	response, err := client.Do(request)
	headerStatus := 0
	if response != nil {
		headerStatus = response.StatusCode
	}
	diagnostics.RecordHTTPPhase(ctx, "asset_headers", time.Since(headersStarted), headerStatus, err != nil || headerStatus >= 400)
	transportError = err
	if err != nil {
		return fmt.Errorf("%w: transport failure", ErrUnavailable)
	}
	status = response.StatusCode
	bodyStarted := time.Now()
	defer func() {
		closeErr := response.Body.Close()
		diagnostics.RecordHTTPPhase(ctx, "asset_body", time.Since(bodyStarted), status, result != nil || closeErr != nil)
	}()
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if response.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("%w: asset api status %d code %s", ErrUnavailable, response.StatusCode, safeResponseCode(response))
	}
	if strings.HasPrefix(path, "/priv/recording-") || strings.HasPrefix(path, "/priv/recordings/") {
		switch response.StatusCode {
		case http.StatusBadRequest:
			if method == http.MethodPost && path == "/priv/recordings/lifecycle" {
				var failure struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) == nil && failure.Error.Code == "AST_INVALID_REQUEST" && failure.Error.Message == "invalid request body" {
					return errLifecycleBodyRejected
				}
			}
			return ErrInvalid
		case http.StatusForbidden:
			return ErrForbidden
		case http.StatusConflict:
			if strings.HasPrefix(path, "/priv/recording-captures") {
				var failure struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) == nil && failure.Error.Code == "capture_missing_objects" {
					return ErrCaptureMissingObjects
				}
			}
			return ErrConflict
		case http.StatusGone:
			if strings.HasPrefix(path, "/priv/recording-captures") {
				return ErrCaptureExpired
			}
		case http.StatusRequestEntityTooLarge:
			if strings.HasPrefix(path, "/priv/recording-packages") || strings.HasPrefix(path, "/priv/recording-captures") {
				return ErrPackageTooLarge
			}
			if strings.HasPrefix(path, "/priv/recording-sources") {
				return ErrSourceTooLarge
			}
		case http.StatusUnprocessableEntity:
			if strings.HasPrefix(path, "/priv/recording-sources") {
				return ErrInvalid
			}
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if strings.HasPrefix(path, "/priv/recording-packages") || strings.HasPrefix(path, "/priv/recording-sources") || strings.HasPrefix(path, "/priv/recording-captures") || strings.HasPrefix(path, "/priv/recordings/") {
			return fmt.Errorf("%w: recording package status %d", ErrUnavailable, response.StatusCode)
		}
		return fmt.Errorf("asset api status %d code %s", response.StatusCode, safeResponseCode(response))
	}
	if destination == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if path == "/priv/recording-packages/ready-batch" {
		payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(payload) > 1<<20 {
			return ErrUnavailable
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if decoder.Decode(destination) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return ErrUnavailable
		}
		return nil
	}
	limit := int64(1 << 20)
	if path == "/priv/recording-packages" || strings.HasPrefix(path, "/priv/recording-packages/") && strings.HasSuffix(path, "/complete") {
		limit = 9 << 20 // Inventory is bounded to 8MiB, plus the package envelope.
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, limit)).Decode(destination); err != nil {
		return fmt.Errorf("decode asset api response: %w", err)
	}
	return nil
}

// Response text can contain identifiers; retain only known wire codes.
func safeResponseCode(response *http.Response) string {
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(payload) > 4096 || json.Unmarshal(payload, &failure) != nil {
		return "unknown"
	}
	switch failure.Error.Code {
	case "AST_AUDIT_UNAVAILABLE", "AST_FORBIDDEN", "AST_NOT_FOUND", "AST_CONFLICT", "AST_INVALID_REQUEST":
		return failure.Error.Code
	}
	return "unknown"
}
