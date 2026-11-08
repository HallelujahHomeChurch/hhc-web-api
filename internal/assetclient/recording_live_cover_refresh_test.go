package assetclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshLiveAutoCoverPrivateOwnerBoundary(t *testing.T) {
	const capture = "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/priv/recording-live-covers/"+capture+"/refresh-auto" || r.URL.Query().Get("recordingId") != "owner-id" || r.Header.Get("X-Internal-Caller-App-Id") != "hhc-web-api" {
			t.Errorf("invalid private owner request: %s %s", r.Method, r.URL)
		}
		w.WriteHeader(202)
		w.Write([]byte(`{"uploadId":"immutable-generation-1","state":"pending"}`))
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "")
	got, err := client.RefreshLiveAutoCover(context.Background(), capture, "owner-id")
	if err != nil || got.UploadID != "immutable-generation-1" || got.State != "pending" {
		t.Fatalf("refresh response: %+v %v", got, err)
	}
	if _, err = client.RefreshLiveAutoCover(context.Background(), "defaults", ""); err != ErrInvalid {
		t.Fatalf("allowed refresh defaults: %v", err)
	}
}
