package assetclient

import (
	"context"
	"errors"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type tokenFixture struct {
	token azcore.AccessToken
	err   error
	scope string
}

func (f *tokenFixture) GetToken(_ context.Context, o policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.scope = strings.Join(o.Scopes, ",")
	return f.token, f.err
}

type workloadRoundTripFunc func(*http.Request) (*http.Response, error)

func (f workloadRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWorkloadTokenTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		token  string
		expiry time.Time
		err    error
		want   bool
	}{
		{"valid", "synthetic-jwt", time.Now().Add(time.Hour), nil, true},
		{"expired", "synthetic-jwt", time.Now().Add(-time.Second), nil, false},
		{"empty", "", time.Now().Add(time.Hour), nil, false},
		{"unavailable", "", time.Time{}, errors.New("private endpoint details"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credential := &tokenFixture{token: azcore.AccessToken{Token: tc.token, ExpiresOn: tc.expiry}, err: tc.err}
			called := false
			transport := &internalTransport{host: "asset.example", service: "hhc-web-extractor", credential: credential, scope: "api://asset/.default", base: workloadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				if r.Header.Get("X-HHC-Workload-Authorization") != "Bearer synthetic-jwt" || r.Header.Get("X-HHC-Caller-App-Id") != "hhc-web-extractor" || r.Header.Get("Dapr-Api-Token") != "" || r.Header.Get("Dapr-Caller-App-Id") != "" || r.Header.Get("Authorization") != "" {
					t.Fatal("mixed or missing identity")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
			})}
			r, _ := http.NewRequest(http.MethodGet, "https://asset.example/priv/assets/id", nil)
			for _, k := range []string{"Authorization", "Dapr-Api-Token", "Dapr-Caller-App-Id", "X-HHC-Workload-Authorization"} {
				r.Header.Set(k, "forged")
			}
			_, err := transport.RoundTrip(r)
			if called != tc.want || (err == nil) != tc.want || credential.scope != "api://asset/.default" {
				t.Fatalf("called=%v err=%v", called, err)
			}
			if !tc.want && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unsanitized error: %v", err)
			}
			if r.Header.Get("Authorization") != "forged" {
				t.Fatal("mutated caller request")
			}
		})
	}
}

func TestWorkloadRetainsOwnerAndDistinctServiceSubject(t *testing.T) {
	for _, service := range []string{"hhc-web-api", "hhc-web-extractor", "hhc-web-bulletin-worker"} {
		client, err := NewWorkload("https://asset.example", service, "api://asset", "88340bcf-5198-4219-90b4-dfca455a1ee3", "")
		if err != nil {
			t.Fatal(err)
		}
		if client.caller != "hhc-web-api" || client.serviceSubject != service {
			t.Fatal("owner and principal conflated")
		}
		transport := client.http.Transport.(*internalTransport)
		credential := &tokenFixture{token: azcore.AccessToken{Token: "first", ExpiresOn: time.Now().Add(time.Hour)}}
		transport.credential = credential
		sent := ""
		transport.base = workloadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			sent = r.Header.Get("X-HHC-Workload-Authorization")
			if r.Header.Get("X-Asset-Subject-Id") != service {
				t.Fatal("shared service subject")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("jpeg"))}, nil
		})
		response, err := client.AuthorizedDownload(context.Background(), http.MethodGet, "source", "", "")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if sent != "Bearer first" {
			t.Fatal("missing first token")
		}
		credential.token.Token = "rotated"
		response, err = client.AuthorizedDownload(context.Background(), http.MethodGet, "source", "", "")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if sent != "Bearer rotated" {
			t.Fatal("stale token retained")
		}
		credential.err = errors.New("identity outage")
		sent = ""
		if _, err = client.AuthorizedDownload(context.Background(), http.MethodGet, "source", "", ""); !errors.Is(err, ErrUnavailable) || sent != "" {
			t.Fatal("identity failure downgraded to legacy")
		}
	}
}
