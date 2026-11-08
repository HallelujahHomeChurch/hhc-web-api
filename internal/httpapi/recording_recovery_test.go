package httpapi

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type d2ControlRepository struct {
	recordings.Repository
	recordings.BroadcastRepository
	b recordings.Broadcast
}

func (r *d2ControlRepository) GetBroadcast(context.Context, string) (recordings.Broadcast, error) {
	return r.b, nil
}
func TestBroadcastControlD2IsOptInAndD1StaysFrozen(t *testing.T) {
	id := "018f0000-0000-4000-8000-000000000001"
	capture := strings.Repeat("a", 32)
	repo := &d2ControlRepository{b: recordings.Broadcast{Epoch: 1, View: recordings.BroadcastView{RecordingID: id, Revision: 3, Phase: "preview", Binding: &recordings.BroadcastBinding{CaptureID: capture, ActorID: bulletinAuditActor, Epoch: 1}}}}
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, time.Now)).Routes()
	for _, version := range []string{"", recordings.BroadcastControlVersion, recordings.BroadcastControlD2Version, "unknown"} {
		path := "/api/admin/broadcasts/" + id + "/control?captureId=" + capture + "&epoch=1"
		if version != "" {
			path += "&controlVersion=" + version
		}
		req := httptest.NewRequest("GET", path, nil)
		trustedBulletinAudit(req, "cms:recordings:read")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if version == "unknown" {
			if w.Code != 422 || !strings.Contains(w.Body.String(), "broadcast_protocol_unsupported") {
				t.Fatal(w.Code, w.Body)
			}
			continue
		}
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var body struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		want := 3
		if version != "" {
			want = 8
		}
		if version == recordings.BroadcastControlD2Version {
			want = 10
		}
		if len(body.Data) != want {
			t.Fatalf("version=%s fields=%d want=%d", version, len(body.Data), want)
		}
		_, recovery := body.Data["recovery"]
		_, end := body.Data["publicEnd"]
		if recovery != (version == recordings.BroadcastControlD2Version) || end != recovery {
			t.Fatal("D1 extended or D2 missing")
		}
		if recovery {
			var r recordings.BroadcastRecovery
			if e := json.Unmarshal(body.Data["recovery"], &r); e != nil || r.Version != nil || r.State != "unsupported" || r.Deadline != nil {
				t.Fatal("legacy D2 misrepresented recovery")
			}
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("control cached")
		}
	}
}
