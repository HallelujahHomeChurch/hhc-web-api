package assetclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadyBatchClientRequiresCompleteBoundEnvelope(t *testing.T) {
	id := strings.Repeat("a", 32)
	ready, expiry := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	item := map[string]any{"recordingId": "recording-1", "packageId": id, "status": "ready", "package": RecordingPackageStatus{RecordingID: "recording-1", PackageID: id, SessionID: id, State: "ready", ReadyAt: &ready, MediaExpiresAt: &expiry}}
	good, _ := json.Marshal(map[string]any{"items": []any{item}})
	for _, test := range []struct {
		name string
		body string
		ok   bool
	}{
		{"ready", string(good), true},
		{"not_found", fmt.Sprintf(`{"items":[{"recordingId":"recording-1","packageId":%q,"status":"not_found"}]}`, id), true},
		{"forbidden", fmt.Sprintf(`{"items":[{"recordingId":"recording-1","packageId":%q,"status":"forbidden"}]}`, id), true},
		{"missing", `{"items":[]}`, false},
		{"duplicate", `{"items":[` + string(mustJSON(t, item)) + `,` + string(mustJSON(t, item)) + `]}`, false},
		{"wrong_binding", strings.Replace(string(good), "recording-1", "recording-2", 1), false},
		{"wrong_session", strings.Replace(string(good), `"sessionId":"`+id, `"sessionId":"`+strings.Repeat("b", 32), 1), false},
		{"unknown_status", strings.Replace(string(good), `"status":"ready"`, `"status":"unknown"`, 1), false},
		{"missing_package", fmt.Sprintf(`{"items":[{"recordingId":"recording-1","packageId":%q,"status":"ready"}]}`, id), false},
		{"denied_with_package", strings.Replace(string(good), `"status":"ready"`, `"status":"forbidden"`, 1), false},
		{"wrong_state", strings.Replace(string(good), `"state":"ready"`, `"state":"freezing"`, 1), false},
		{"unknown_field", strings.Replace(string(good), `{"items":`, `{"privateField":"value","items":`, 1), false},
		{"trailing_json", string(good) + `{}`, false},
		{"oversized", string(good) + strings.Repeat(" ", 1<<20), false},
		{"malformed_time", strings.Replace(string(good), ready.Format(time.RFC3339Nano), "invalid-time", 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/priv/recording-packages/ready-batch" {
					t.Errorf("wrong batch request: %s %s", r.Method, r.URL.Path)
				}
				var request struct{ Items []RecordingLifecycleBinding }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Items) != 1 || request.Items[0].RecordingID != "recording-1" || request.Items[0].PackageID != id {
					t.Errorf("wrong binding: %+v %v", request, err)
				}
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			_, err := New(server.URL, "hhc-web-api", "").GetReadyRecordingPackages(context.Background(), []RecordingLifecycleBinding{{RecordingID: "recording-1", PackageID: id}})
			if test.ok && err != nil || !test.ok && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("ok=%v err=%v", test.ok, err)
			}
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReadyBatchClientNeverTurnsWholeRequestFailuresIntoItemDenials(t *testing.T) {
	items := []RecordingLifecycleBinding{{RecordingID: "recording-1", PackageID: strings.Repeat("a", 32)}}
	for _, status := range []int{400, 401, 403, 404, 409, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			_, err := New(server.URL, "hhc-web-api", "").GetReadyRecordingPackages(context.Background(), items)
			if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
				t.Fatalf("whole error misclassified: %v", err)
			}
		})
	}
	c := New("http://127.0.0.1:1", "hhc-web-api", "")
	for _, invalid := range [][]RecordingLifecycleBinding{nil, {items[0], items[0]}, {{RecordingID: "r", PackageID: "unsafe/path"}}, make([]RecordingLifecycleBinding, 101)} {
		if _, err := c.GetReadyRecordingPackages(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid input: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetReadyRecordingPackages(ctx, items); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := c.GetReadyRecordingPackages(ctx, items); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}
