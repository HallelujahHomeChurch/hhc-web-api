package assetclient

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

// Opt-in local candidate harness; synthetic credentials never leave child process memory.
func TestC2ActualAssetProducer(t *testing.T) {
	base := os.Getenv("ASSET_C2_URL")
	if base == "" {
		t.Skip("fixed local Asset producer harness required")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		t.Fatal("local TLS fixture required")
	}
	pem, err := os.ReadFile(os.Getenv("ASSET_C2_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("fixture CA invalid")
	}
	makeClient := func(service, key string) *Client {
		c := New(base, "hhc-web-api", "")
		c.serviceSubject = service
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		t.Cleanup(transport.CloseIdleConnections)
		c.http = &http.Client{Timeout: 10 * time.Second, Transport: &internalTransport{host: parsed.Host, service: service, base: transport, scope: "api://asset/.default", credential: &tokenFixture{token: azcore.AccessToken{Token: os.Getenv(key), ExpiresOn: time.Now().Add(time.Hour)}}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		return c
	}
	cms := makeClient("hhc-web-api", "ASSET_C2_CMS_TOKEN")
	worker := makeClient("hhc-web-bulletin-worker", "ASSET_C2_WATERMARK_TOKEN")
	if err = cms.ConfigureVerifiedCapabilities(true, false); err != nil {
		t.Fatal(err)
	}
	if err = worker.ConfigureVerifiedCapabilities(false, true); err != nil {
		t.Fatal(err)
	}
	ctx := auditclient.WithProvenance(t.Context(), "cd09f6d6-bbf4-4a6a-bc86-b36e77d891dd", "aa7d8650-bcdb-4969-b581-c38dc47a8305")
	var source Asset
	var data []byte
	for _, name := range []string{"ASSET_C2_PDF_ID", "ASSET_C2_PNG_ID"} {
		a, err := cms.Get(ctx, os.Getenv(name))
		if err != nil {
			t.Fatal(err)
		}
		if a.OwnerID != os.Getenv("ASSET_C2_ISSUE_ID") || a.Locale != "en" || !a.Scanned() || a.ProcessingReadiness == nil || a.ProcessingReadiness.State != "ready" {
			t.Fatal("producer fixture binding/evidence mismatch")
		}
		response, err := cms.OwnerContent(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, 20<<20))
		response.Body.Close()
		if readErr != nil || response.StatusCode != 200 || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("owner stream failed")
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != a.ChecksumSHA256 {
			t.Fatal("owner bytes checksum mismatch")
		}
		wrong := a
		wrong.OwnerID = "wrong-owner"
		if _, err = cms.OwnerContent(ctx, wrong); err == nil {
			t.Fatal("owner mismatch accepted")
		}
		if name == "ASSET_C2_PDF_ID" {
			source, data = a, content
		}
	}
	output, err := worker.CreateDerivedBulletinUpload(ctx, "abed2e8f-304e-4932-aef7-e9c0703d86b1", "en", "candidate.pdf", int64(len(data)), "c2-actual-output")
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Upload(ctx, output.UploadTarget, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	input := CompleteUploadInput{SizeBytes: int64(len(data)), ChecksumSHA256: source.ChecksumSHA256, MIMEType: "application/pdf"}
	for range 2 {
		completed, err := worker.CompleteDerivedBulletin(ctx, output.Asset.ID, source.ID, source.ChecksumSHA256, source.ETag, input)
		if err != nil || completed.SafetyEvidence == nil || completed.SafetyEvidence.Kind != "trusted-derived" {
			t.Fatalf("derived completion/replay failed: %v", err)
		}
	}
	if _, err = worker.CompleteDerivedBulletin(ctx, output.Asset.ID, source.ID, source.ChecksumSHA256, "wrong-etag", input); err == nil {
		t.Fatal("different proof accepted")
	}
	ordinary, err := cms.CompleteUpload(ctx, output.Asset.ID, input)
	if err != nil || ordinary.SafetyEvidence == nil || ordinary.SafetyEvidence.Kind != "trusted-derived" {
		t.Fatalf("historical identical replay lost evidence: %v", err)
	}
	// A new consumer with both capabilities off uses the producer's narrow adapter.
	legacy := makeClient("hhc-web-bulletin-worker", "ASSET_C2_WATERMARK_TOKEN")
	if _, err = legacy.CompleteDerivedBulletin(ctx, output.Asset.ID, source.ID, source.ChecksumSHA256, source.ETag, input); err != nil {
		t.Fatalf("narrow adapter compatibility: %v", err)
	}
}
