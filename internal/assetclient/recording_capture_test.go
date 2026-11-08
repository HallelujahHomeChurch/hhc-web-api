package assetclient

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCaptureErrorsAreTypedAndRedacted(t *testing.T) {
	for _, status := range []int{410, 413, 418} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("secret signed capability"))
			}))
			defer server.Close()
			err := New(server.URL, "hhc-web-api", "").request(context.Background(), "POST", "/priv/recording-captures/"+strings.Repeat("a", 32)+"/seal", nil, "", nil)
			if status == 410 && (err == nil || err.Error() != "capture_expired") {
				t.Fatalf("expiry translation: %v", err)
			}
			if status == 413 && !errors.Is(err, ErrPackageTooLarge) {
				t.Fatalf("size translation: %v", err)
			}
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("dependency body leaked: %v", err)
			}
		})
	}
}

func TestLiveClientUsesOwnerProgressAndTypedTrustedGrant(t *testing.T) {
	capture := strings.Repeat("a", 32)
	now := time.Now().UTC()
	owner := "11111111-1111-4111-8111-111111111111"
	viewer := "22222222-2222-4222-8222-222222222222"
	scope := "33333333-3333-4333-8333-333333333333"
	recording := "44444444-4444-4444-8444-444444444444"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
			t.Fatal("untrusted client")
		}
		if r.Method == "GET" {
			if r.URL.Path != "/priv/recording-captures/"+capture+"/progress" || r.Header.Get("X-HHC-Actor-ID") != owner {
				t.Error("progress owner/path")
			}
			json.NewEncoder(w).Encode(RecordingLiveProgress{Revision: 3, LastSequence: 2, MediaEndSeconds: 90})
			return
		}
		if r.URL.Path != "/priv/recording-captures/"+capture+"/grant" {
			t.Error("wrong grant route")
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 4 || input["recordingId"] != recording || input["userId"] != viewer || input["playbackScopeId"] != scope || input["expiresAt"] != now.Add(5*time.Minute).Format(time.RFC3339Nano) {
			t.Errorf("wrong grant input fields: %v", input)
		}
		json.NewEncoder(w).Encode(RecordingGrant{ExchangeCredential: "capability", ExpiresAt: now.Add(5 * time.Minute)})
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "")
	if p, err := client.GetRecordingLiveProgress(auditclient.WithProvenance(context.Background(), owner, "progress"), capture); err != nil || p.LastSequence != 2 {
		t.Fatalf("progress %v", err)
	}
	if grant, err := client.IssueRecordingLiveGrant(context.Background(), capture, recording, viewer, scope, now.Add(5*time.Minute)); err != nil || grant.ExchangeCredential != "capability" {
		t.Fatalf("grant %v", err)
	}
}
