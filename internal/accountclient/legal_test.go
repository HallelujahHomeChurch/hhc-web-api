package accountclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegalCheckRequiresExplicitBoundedBoolean(t *testing.T) {
	for _, body := range []string{`{"accepted":true}`, `{"accepted":false}`, `{}`, `{"accepted":true,"userId":"extra"}`, `{"accepted":true} {}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		_, err := New(server.URL, "hhc-web-api").CheckLegalAcceptance(context.Background(), "01995fd2-6f6c-7b40-8707-6503edc362c8", "verified-member", "terms-v1", "privacy-v1")
		server.Close()
		valid := body == `{"accepted":true}` || body == `{"accepted":false}`
		if (err == nil) != valid {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
}
