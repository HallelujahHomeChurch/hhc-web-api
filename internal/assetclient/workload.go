package assetclient

import (
	"context"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewInternal uses the trusted caller assertion within private ACA ingress.
func NewInternal(baseURL string, publicBaseURL ...string) (*Client, error) {
	return newInternal(baseURL, "hhc-web-api", publicBaseURL...)
}

func NewWatermarkInternal(baseURL string) (*Client, error) {
	return newInternal(baseURL, "hhc-web-bulletin-worker")
}

func newInternal(baseURL, service string, publicBaseURL ...string) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, ErrInvalid
	}
	public := ""
	if len(publicBaseURL) != 0 {
		public = publicBaseURL[0]
	}
	c := New(baseURL, "hhc-web-api", public)
	c.http.Transport = &internalTransport{base: c.http.Transport, host: parsed.Host, service: service}
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// NewWorkload uses only the explicitly selected managed identity; Azure SDK owns its token cache.
func NewWorkload(baseURL, service, audience, clientID, publicBaseURL string) (*Client, error) {
	if service != "hhc-web-api" && service != "hhc-web-extractor" && service != "hhc-web-bulletin-worker" {
		return nil, ErrInvalid
	}
	resource, err := url.Parse(audience)
	if err != nil || (resource.Scheme != "api" && resource.Scheme != "https") || resource.Host == "" || len(audience) > 255 || resource.User != nil || resource.RawQuery != "" || resource.Fragment != "" || strings.ContainsAny(audience, " \r\n\t") || strings.HasSuffix(audience, "/") || strings.HasSuffix(audience, "/.default") || uuid.Validate(clientID) != nil {
		return nil, ErrInvalid
	}
	client, err := newInternal(baseURL, service, publicBaseURL)
	if err != nil {
		return nil, err
	}
	credential, err := azidentity.NewManagedIdentityCredential(&azidentity.ManagedIdentityCredentialOptions{ID: azidentity.ClientID(clientID)})
	if err != nil {
		return nil, ErrInvalid
	}
	transport := client.http.Transport.(*internalTransport)
	transport.credential, transport.scope = credential, audience+"/.default"
	client.serviceSubject = service
	return client, nil
}

type internalTransport struct {
	credential    azcore.TokenCredential
	scope         string
	base          http.RoundTripper
	host, service string
}

func (t *internalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != t.host {
		return nil, ErrForbidden
	}
	clone := request.Clone(request.Context())
	clone.Header.Del("X-Internal-Caller-App-Id")
	clone.Header.Del("Authorization")
	clone.Header.Del("X-HHC-Workload-Authorization")
	clone.Header.Del("Dapr-Caller-App-Id")
	clone.Header.Del("Dapr-Api-Token")
	if t.credential != nil {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		token, err := t.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{t.scope}})
		cancel()
		if err != nil || token.Token == "" || strings.ContainsAny(token.Token, " \r\n\t") || len(token.Token) > 8192 || !token.ExpiresOn.After(time.Now().Add(5*time.Second)) {
			return nil, ErrUnavailable
		}
		clone.Header.Set("X-HHC-Workload-Authorization", "Bearer "+token.Token)
	}
	clone.Header.Set("X-HHC-Caller-App-Id", t.service)
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		slog.WarnContext(request.Context(), "bulletin_asset_failed", "operation", workloadOperation(request.Method, request.URL.Path), "stage", "transport")
	} else if response.StatusCode < 200 || response.StatusCode >= 300 {
		slog.WarnContext(request.Context(), "bulletin_asset_failed", "operation", workloadOperation(request.Method, request.URL.Path), "stage", "http", "status", response.StatusCode)
	}
	return response, err
}

// Only fixed operation names may enter logs; paths contain private asset IDs.
func workloadOperation(method, path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "priv" || parts[1] != "assets" {
		return "asset_request"
	}
	switch {
	case method == http.MethodPost && path == "/priv/assets/upload-sessions":
		return "upload_create"
	case method == http.MethodGet && len(parts) == 3:
		return "asset_get"
	case method == http.MethodGet && len(parts) == 4 && parts[3] == "download":
		return "source_download"
	case method == http.MethodPost && len(parts) == 4 && parts[3] == "grants":
		return "grant_create"
	case method == http.MethodDelete && len(parts) == 5 && parts[3] == "grants":
		return "grant_revoke"
	case method == http.MethodPost && len(parts) == 4 && parts[3] == "complete":
		return "upload_complete"
	default:
		return "asset_request"
	}
}

// Capabilities remain independently disabled until their producer and scope exist.
func (c *Client) ConfigureVerifiedCapabilities(ownerContent, trustedDerivation bool) error {
	if ownerContent || trustedDerivation {
		transport, ok := c.http.Transport.(*internalTransport)
		if !ok || transport.credential == nil || (ownerContent && transport.service != "hhc-web-api") || (trustedDerivation && transport.service != "hhc-web-bulletin-worker") {
			return ErrInvalid
		}
	}
	c.ownerContent, c.trustedDerivation = ownerContent, trustedDerivation
	return nil
}
