package assetclient

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSuccessfulHeadersWithBrokenBodyKeepFailureEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "{")
	}))
	defer server.Close()
	ctx, observation := diagnostics.WithRequest(context.Background(), "synthetic-body")
	_, err := New(server.URL, "hhc-web-api", "").Get(ctx, "synthetic")
	if err == nil {
		t.Fatal("body failure lost")
	}
	values, failed := observation.Finish()
	if !failed {
		t.Fatal("fast body failure would suppress request summary")
	}
	found := false
	for _, v := range values {
		if v.Phase == "asset_body" {
			found = true
			if v.Count != 1 || v.Failures != 1 || v.Status != 200 {
				t.Fatal(v)
			}
		}
	}
	if !found || observation.FailureReason() != "asset_body_failed" {
		t.Fatal(values, observation.FailureReason())
	}
}

func TestTransportFailureDoesNotRetainPrivateURL(t *testing.T) {
	client := New("http://private.example/member?secret=hidden", "hhc-web-api", "")
	client.http.Transport = privateRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("secret transport provider details")
	})
	_, err := client.Get(context.Background(), "private-asset")
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe dependency failure category")
	}
}
