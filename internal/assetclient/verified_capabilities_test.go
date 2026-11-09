package assetclient

import (
	"context"
	"encoding/json"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func verifiedClientFixture(t *testing.T, service string, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	c := New(server.URL, "hhc-web-api", "")
	c.serviceSubject = service
	c.http = &http.Client{Transport: &internalTransport{host: strings.TrimPrefix(server.URL, "https://"), service: service, credential: &tokenFixture{token: azcore.AccessToken{Token: "synthetic", ExpiresOn: time.Now().Add(time.Hour)}}, scope: "api://asset/.default", base: server.Client().Transport}, Timeout: time.Second}
	return c
}
func TestGenericDerivedCompletionRequiresExactReadyEvidence(t *testing.T) {
	for _, mode := range []string{"ready", "legacy", "scanned", "pending", "conflict", "wrong-version"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			c := verifiedClientFixture(t, "hhc-web-bulletin-worker", func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/priv/assets/output/complete" || r.Header.Get("X-HHC-Workload-Authorization") != "Bearer synthetic" {
					t.Error("wrong private route/identity")
				}
				var body struct {
					Derivation struct{ SourceAssetID, SourceSHA256, SourceETag, Transform, TransformVersion string }
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Derivation.SourceAssetID != "source" || body.Derivation.SourceSHA256 != strings.Repeat("c", 64) || body.Derivation.SourceETag != "source-etag" || body.Derivation.Transform != "bulletin-watermark" || body.Derivation.TransformVersion != "1" {
					t.Error("source proof changed")
				}
				if mode == "conflict" {
					w.WriteHeader(409)
					return
				}
				a := Asset{ID: "output", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "not_required", ScanSignature: "trusted-bulletin-watermark-v1", ProcessingReadiness: &ProcessingReadiness{1, "ready"}, SafetyEvidence: &SafetyEvidence{"trusted-derived"}}
				switch mode {
				case "legacy":
					a.ProcessingReadiness = nil
					a.SafetyEvidence = nil
				case "scanned":
					a.ScanSignature = "clamav"
					a.SafetyEvidence.Kind = "scanned"
				case "pending":
					a.ScanStatus = "pending"
					a.ProcessingReadiness.State = "pending"
					a.SafetyEvidence = nil
				case "wrong-version":
					a.ProcessingReadiness.Version = 2
				}
				_ = json.NewEncoder(w).Encode(a)
			})
			if err := c.ConfigureVerifiedCapabilities(false, true); err != nil {
				t.Fatal(err)
			}
			input := CompleteUploadInput{SizeBytes: 123, ChecksumSHA256: strings.Repeat("d", 64), MIMEType: "application/pdf"}
			_, err := c.CompleteDerivedBulletin(t.Context(), "output", "source", strings.Repeat("c", 64), "source-etag", input)
			if (err == nil) != (mode == "ready") || calls != 1 {
				t.Fatalf("mode=%s calls=%d err=%v", mode, calls, err)
			}
		})
	}
}
func TestOwnerContentBindsBusinessMetadataBeforePrivateRead(t *testing.T) {
	for _, mode := range []string{"zh-Hant", "en", "wrong-owner", "wrong-locale", "trusted", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			locale := mode
			if locale != "en" {
				locale = "zh-Hant"
			}
			expected := Asset{ID: "source", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue", Purpose: "weekly_bulletin", Locale: locale, Visibility: "private", ChecksumSHA256: strings.Repeat("a", 64)}
			actual := expected
			actual.UploadStatus = "completed"
			actual.ScanStatus = "clean"
			actual.ProcessingStatus = "not_required"
			switch mode {
			case "wrong-owner":
				actual.OwnerID = "other"
			case "wrong-locale":
				actual.Locale = "ja"
			case "trusted":
				actual.ScanSignature = "trusted-bulletin-watermark-v1"
			}
			reads := 0
			c := verifiedClientFixture(t, "hhc-web-api", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/priv/assets/source" {
					if mode == "unavailable" {
						w.WriteHeader(503)
						return
					}
					_ = json.NewEncoder(w).Encode(actual)
					return
				}
				reads++
				q := r.URL.Query()
				if r.URL.Path != "/priv/assets/source/owner-content" || q.Get("sha256") != expected.ChecksumSHA256 || q.Get("ownerType") != "bulletin_issue" || q.Get("ownerId") != "issue" {
					t.Error("owner binding lost")
				}
				_, _ = io.WriteString(w, "%PDF-fixture")
			})
			if err := c.ConfigureVerifiedCapabilities(true, false); err != nil {
				t.Fatal(err)
			}
			response, err := c.OwnerContent(context.Background(), expected)
			if response != nil {
				response.Body.Close()
			}
			valid := mode == "zh-Hant" || mode == "en"
			if (err == nil) != valid || (reads == 1) != valid {
				t.Fatalf("reads=%d err=%v", reads, err)
			}
		})
	}
}
func TestCapabilitiesCannotUseLegacyOrDifferentPrincipal(t *testing.T) {
	c := New("http://127.0.0.1", "hhc-web-api", "")
	if c.ConfigureVerifiedCapabilities(false, false) != nil || c.ConfigureVerifiedCapabilities(true, false) == nil {
		t.Fatal("legacy capability enabled")
	}
	for _, service := range []string{"hhc-web-api", "hhc-web-extractor", "hhc-web-bulletin-worker"} {
		c := verifiedClientFixture(t, service, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
		if (c.ConfigureVerifiedCapabilities(true, false) == nil) != (service == "hhc-web-api") {
			t.Fatal("owner scope")
		}
		if (c.ConfigureVerifiedCapabilities(false, true) == nil) != (service == "hhc-web-bulletin-worker") {
			t.Fatal("derivation scope")
		}
	}
}
