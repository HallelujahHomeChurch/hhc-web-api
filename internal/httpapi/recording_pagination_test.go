package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type emptyRecordingPageRepo struct{ recordings.Repository }

func (emptyRecordingPageRepo) ListVisible(context.Context, time.Time) ([]recordings.Recording, error) {
	return []recordings.Recording{}, nil
}
func (emptyRecordingPageRepo) ListVisiblePage(context.Context, time.Time, *time.Time, string, int, string) ([]recordings.Recording, error) {
	return []recordings.Recording{}, nil
}

func TestRecordingPageRejectsInvalidQuery(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(emptyRecordingPageRepo{}, "", nil, time.Now))
	for _, query := range []string{"q=faith", "limit=12&q=a&q=b", "limit=12&q=" + strings.Repeat("a", 101), "limit=0", "limit=101", "limit=no", "limit=2&cursor=bad", "cursor=bad", "limit=2&limit=3", "limit=", "limit=2&cursor=" + base64.RawURLEncoding.EncodeToString([]byte(`{"at":"2026-10-08T00:00:00Z","id":"urn:uuid:00000000-0000-4000-8000-000000000003"}`))} {
		w := httptest.NewRecorder()
		h.memberListRecordings(w, httptest.NewRequest("GET", "/api/member/recordings?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("%s: status %d, %s", query, w.Code, w.Body.String())
		}
	}
}

func TestRecordingPageKeepsLegacyArrayAndReturnsTerminalCursor(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(emptyRecordingPageRepo{}, "", nil, time.Now))
	for _, query := range []string{"", "?q=+", "?limit=12", "?limit=12&q=Faith"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/member/recordings"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{UserID: "11111111-1111-4111-8111-111111111111"}))
		h.memberListRecordings(w, r)
		var value struct {
			Data []recordings.Recording `json:"data"`
			Meta map[string]any         `json:"meta"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &value) != nil || value.Data == nil {
			t.Fatalf("invalid array: %s", w.Body.String())
		}
		_, exists := value.Meta["nextCursor"]
		if exists != strings.Contains(query, "limit=") || value.Meta["nextCursor"] != nil {
			t.Fatalf("incorrect cursor contract: %s", w.Body.String())
		}
	}
}
