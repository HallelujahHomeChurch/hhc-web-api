package operationsclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const subjectID = "01995fd2-6f6c-7b40-8707-6503edc362c8"

func TestCheckEntitlements(t *testing.T) {
	version := strings.Repeat("a", 64)
	for _, test := range []struct {
		name, response string
		status         int
		wantDecision   string
		wantErr        error
	}{
		{"allowed", `{"decisions":[{"entitlementCode":"bulletin.general.en.access","decision":"allow","reason":"allowed","version":"` + version + `"}]}`, http.StatusOK, "allow", nil},
		{"denied subject", `{"decisions":[{"entitlementCode":"bulletin.general.en.access","decision":"deny","reason":"membership_required","version":"` + version + `"}]}`, http.StatusOK, "deny", nil},
		{"unknown response code", `{"decisions":[{"entitlementCode":"bulletin.unknown.access","decision":"deny","reason":"entitlement_required","version":"` + version + `"}]}`, http.StatusOK, "", ErrUnavailable},
		{"malformed response", `{"decisions":[`, http.StatusOK, "", ErrUnavailable},
		{"caller authentication failure", `{"error":"unauthorized"}`, http.StatusUnauthorized, "", ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/priv/operations/entitlement-checks" {
					t.Fatalf("request=%s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			decisions, err := New(server.URL).CheckEntitlements(t.Context(), subjectID, []string{"bulletin.general.en.access"})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("err=%v want=%v", err, test.wantErr)
			}
			if test.wantErr == nil && (len(decisions) != 1 || decisions[0].Decision != test.wantDecision) {
				t.Fatalf("decisions=%+v", decisions)
			}
		})
	}
}

func TestCheckEntitlementsTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()
	client := New(server.URL)
	client.http.Timeout = time.Millisecond

	if _, err := client.CheckEntitlements(t.Context(), subjectID, []string{"bulletin.general.en.access"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckEntitlementsRejectsInvalidRequest(t *testing.T) {
	client := New("http://127.0.0.1")
	for _, test := range []struct {
		subject string
		codes   []string
	}{
		{"not-a-uuid", []string{"bulletin.general.en.access"}},
		{subjectID, nil},
		{subjectID, []string{"unknown code"}},
	} {
		if _, err := client.CheckEntitlements(t.Context(), test.subject, test.codes); !errors.Is(err, ErrInvalid) {
			t.Fatalf("subject=%q codes=%v err=%v", test.subject, test.codes, err)
		}
	}
}

func TestPrivateOperationsRequestUsesScopedCredential(t *testing.T) {
	c := New("http://wrong.test")
	token := strings.Repeat("a", 64)
	c.ConfigurePrivateHTTPS(token)
	req, err := c.request(t.Context(), "/priv/operations/entitlement-checks", []byte("{}"))
	if err != nil || req.Method != "POST" || req.URL.String() != "https://operations-api.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io/priv/operations/entitlement-checks" ||
		req.Header.Get("X-HHC-Operations-Token") != token || req.Header.Get("Dapr-Caller-App-Id") != "" {
		t.Fatal("private Operations request changed identity, route, or credential scope")
	}
}
