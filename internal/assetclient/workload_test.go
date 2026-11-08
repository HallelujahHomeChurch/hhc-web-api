package assetclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkloadUploadsToBlobWithoutServiceCredential(t *testing.T) {
	var blobRequests, redirectedRequests int
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests++
	}))
	defer redirect.Close()
	blob := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blobRequests++
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-HHC-Caller-App-Id") != "" {
			t.Error("service credential sent to blob")
		}
		data, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || string(data) != "image" || r.ContentLength != 5 || r.Header.Get("x-ms-blob-type") != "BlockBlob" || r.URL.Query().Get("sig") != "synthetic" {
			t.Error("upload contract changed")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer blob.Close()
	asset := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-api" {
			t.Error("missing internal caller")
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("retired credential sent")
		}
		_, _ = w.Write([]byte(`{"id":"source"}`))
	}))
	defer asset.Close()
	client, err := NewInternal(asset.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport.(*internalTransport).base = asset.Client().Transport
	target := UploadTarget{URL: blob.URL + "/image?sig=synthetic", Method: http.MethodPut, Headers: map[string]string{"x-ms-blob-type": "BlockBlob"}}
	if err := client.Upload(context.Background(), target, strings.NewReader("image"), 5); err != nil {
		t.Fatalf("blob upload failed: %v", err)
	}
	if blobRequests != 1 {
		t.Fatal("blob upload missed blob")
	}
	if _, err := client.Get(context.Background(), "source"); err != nil {
		t.Fatalf("asset API authentication changed: %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, blob.URL, nil)
	if _, err := client.http.Do(request); !errors.Is(err, ErrForbidden) {
		t.Fatal("authenticated client accepted a foreign host")
	}
	target.URL = blob.URL + "/redirect?sig=synthetic"
	if err := client.Upload(context.Background(), target, strings.NewReader("image"), 5); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Upload(ctx, target, strings.NewReader("image"), 5); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("canceled upload accepted: %v", err)
	}
	if redirectedRequests != 0 || blobRequests != 2 {
		t.Fatal("redirect/cancellation sent a request")
	}
}

func TestInternalCallerDoesNotLeakOnRedirect(t *testing.T) {
	var requests int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-api" {
			t.Error("unexpected authorization or caller")
		}
		if requests == 2 {
			http.Redirect(w, r, "https://untrusted.invalid/", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"id":"source"}`))
	}))
	defer server.Close()
	client, err := NewInternal(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport.(*internalTransport).base = server.Client().Transport
	if _, err := client.Get(context.Background(), "source"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "source"); err == nil {
		t.Fatal("redirect followed")
	}
	if requests != 2 {
		t.Fatal("redirect sent extra request")
	}
	for _, base := range []string{"http://localhost", "https://user:pass@asset", "https://asset/?token=x", "https://asset/#fragment"} {
		if _, err := NewInternal(base); err == nil {
			t.Fatalf("unsafe URL accepted: %s", base)
		}
	}
}

func TestWatermarkWorkloadKeepsWorkerAuthority(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-bulletin-worker" || r.Header.Get("Authorization") != "" {
			t.Error("worker authority lost")
		}
		w.Write([]byte(`{"id":"source"}`))
	}))
	defer server.Close()
	c, err := NewWatermarkInternal(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if c.caller != "hhc-web-api" {
		t.Fatal("CMS asset ownership changed")
	}
	c.http.Transport.(*internalTransport).base = server.Client().Transport
	if _, err := c.Get(context.Background(), "source"); err != nil {
		t.Fatal(err)
	}
}
