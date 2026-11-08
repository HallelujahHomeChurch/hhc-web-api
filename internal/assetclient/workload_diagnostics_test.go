package assetclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkloadFailureDiagnosticsDoNotExposeRequestOrError(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, kind := range []string{"transport", "http", "blob-transport", "blob-http", "success"} {
		t.Run(kind, func(t *testing.T) {
			output.Reset()
			client, err := NewInternal("https://asset.invalid")
			if err != nil {
				t.Fatal(err)
			}
			client.http.Transport.(*internalTransport).base = diagnosticTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(kind, "transport") {
					return nil, errors.New("https://private.invalid/private-id?sig=private-secret")
				}
				status := http.StatusConflict
				if kind == "success" {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"private-id"}`)), Request: r}, nil
			})
			if strings.HasPrefix(kind, "blob-") {
				err = client.Upload(context.Background(), UploadTarget{URL: "https://private.invalid/private-id?sig=private-secret", Method: http.MethodPut, Headers: map[string]string{"X-Private": "private-header"}}, strings.NewReader("private-body"), 12)
			} else {
				_, err = client.Get(context.Background(), "private-id")
			}
			if (kind == "success") != (err == nil) {
				t.Fatalf("unexpected outcome for %s", kind)
			}
			log := output.String()
			expected := map[string]string{"transport": "transport", "http": "http", "blob-transport": "transport", "blob-http": "http"}[kind]
			if kind == "success" {
				if log != "" {
					t.Fatal("successful operation logged as failure")
				}
				return
			}
			operation := "asset_get"
			if strings.HasPrefix(kind, "blob-") {
				operation = "blob_upload"
			}
			for _, required := range []string{`"msg":"bulletin_asset_failed"`, `"operation":"` + operation + `"`, `"stage":"` + expected + `"`} {
				if !strings.Contains(log, required) {
					t.Fatalf("missing sanitized diagnostic %s", required)
				}
			}
			if strings.HasSuffix(kind, "http") && !strings.Contains(log, `"status":409`) {
				t.Fatal("missing HTTP status")
			}
			for _, secret := range []string{"private.invalid", "private-id", "private-secret", "private-header", "private-body", "controlled-", "private credential detail", "https://", "sig="} {
				if strings.Contains(log, secret) {
					t.Fatal("private diagnostic data leaked")
				}
			}
		})
	}
}

func TestWorkloadOperationNamesAreBounded(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/priv/assets/private-id", "asset_get"},
		{"GET", "/priv/assets/private-id/download", "source_download"},
		{"POST", "/priv/assets/private-id/grants", "grant_create"},
		{"DELETE", "/priv/assets/private-id/grants/private-grant", "grant_revoke"},
		{"POST", "/priv/assets/upload-sessions", "upload_create"},
		{"POST", "/priv/assets/private-id/complete", "upload_complete"},
		{"PRIVATE-METHOD", "/private-path", "asset_request"},
	} {
		if got := workloadOperation(tc.method, tc.path); got != tc.want {
			t.Fatalf("operation=%s want=%s", got, tc.want)
		}
	}
}
