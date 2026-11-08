package assetclient

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
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

type internalTransport struct {
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
