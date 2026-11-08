package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOutputReplayIsStrictAndNeverFallsBackToOpen(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "dapr-test", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).Routes()
	for _, body := range []string{`{}`, `{"protocolVersion":"k5-2026-10-20.rc1","replayOnly":true}`, `{"protocolVersion":null}`} {
		req := httptest.NewRequest("POST", "/api/ingest/streams/replay", strings.NewReader(body))
		req.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
		req.Header.Set("dapr-api-token", "dapr-test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("strict replay rejected as %d, want400: %s", w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("replay response must be private")
		}
	}
}

type outputReplayHTTPRepo struct {
	recordings.Repository
	recordings.BroadcastIngestRepository
	snapshot recordings.BroadcastOutputReplaySnapshot
	hash     string
	err      error
	reads    int
}

func (r *outputReplayHTTPRepo) ReadBroadcastOutputReplay(_ context.Context, _ string, _ recordings.BroadcastOutputReplayInput) (recordings.BroadcastOutputReplaySnapshot, error) {
	r.reads++
	return r.snapshot, r.err
}
func (r *outputReplayHTTPRepo) BroadcastIngestCredentials(context.Context, string) (string, string, error) {
	return r.hash, strings.Repeat("1", 64), nil
}
func TestOutputReplayHTTPProofAndUnknownAreDistinct(t *testing.T) {
	for _, state := range []string{"active", "terminal", "unknown"} {
		t.Run(state, func(t *testing.T) {
			now := time.Now().UTC()
			key := "hhc_stream_" + strings.Repeat("A", 43)
			hash, _, _ := recordings.HashBroadcastSourceKey(key)
			actor := "018f0000-0000-7000-8000-000000000001"
			id := "018f0000-0000-7000-8000-000000000002"
			output := "018f0000-0000-7000-8000-000000000003"
			source := "018f0000-0000-7000-8000-000000000004"
			capture := strings.Repeat("a", 32)
			receipt := recordings.BroadcastReceipt{OperationKey: output + ".bind", Operation: "bind", State: "completed"}
			in := recordings.BroadcastOutputReplayInput{ProtocolVersion: recordings.BroadcastOutputReplayProtocolVersion, OutputSessionID: output, SourceID: source, RecordingID: id, CaptureID: capture, Epoch: 1, Receipt: &receipt, StreamKey: key}
			repo := &outputReplayHTTPRepo{hash: hash, snapshot: recordings.BroadcastOutputReplaySnapshot{Owner: actor, Receipt: receipt, State: state, Capture: recordings.Capture{ActorID: actor, Status: recordings.CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{ExpiresAt: now.Add(time.Hour)}}}}}
			if state == "unknown" {
				repo.err = recordings.ErrBroadcastOutputReplayUnknown
			}
			account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
			defer account.Close()
			svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
			h := NewWithContent(nil, nil, nil, nil, "api-gateway", "dapr-test", true).WithRecordings(svc).Routes()
			raw, _ := json.Marshal(in)
			req := httptest.NewRequest("POST", "/api/ingest/streams/replay", bytes.NewReader(raw))
			req.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
			req.Header.Set("dapr-api-token", "dapr-test")
			req.Header.Set("X-HHC-Request-ID", "replay-test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			want := 200
			if state == "unknown" {
				want = 409
			}
			if w.Code != want {
				t.Fatalf("replay %s returned%d: %s", state, w.Code, w.Body)
			}
			var body struct {
				Data  map[string]any
				Error *struct{ Code string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if state == "unknown" {
				if body.Error == nil || body.Error.Code != "broadcast_output_replay_unknown" || body.Data != nil {
					t.Fatal("unknown became terminal")
				}
			} else if body.Data["state"] != state || body.Data["sourceId"] != source || body.Data["outputSessionId"] != output || body.Data["recordingId"] != id {
				t.Fatal("response lost original identity")
			}
			if state == "terminal" {
				if _, ok := body.Data["session"]; ok {
					t.Fatal("terminal exposed credential")
				}
			}
			if strings.Contains(w.Body.String(), key) {
				t.Fatal("stream key leaked")
			}
		})
	}
}

func TestOutputReplayStrictDecoderMatchesCanonicalInputFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../docs/obs-capture/output-replay-v1/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Fixtures []struct {
			Name, Schema string
			Valid        bool
			Value        any
		}
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "dapr-test", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).Routes()
	for _, f := range fixtures.Fixtures {
		if f.Schema != "BroadcastOutputReplayInput" {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			body, _ := json.Marshal(f.Value)
			req := httptest.NewRequest("POST", "/api/ingest/streams/replay", bytes.NewReader(body))
			req.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
			req.Header.Set("dapr-api-token", "dapr-test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if f.Valid {
				if w.Code != 503 {
					t.Fatalf("valid input did not reach unavailable repository: %d", w.Code)
				}
			} else if w.Code == 503 || w.Code < 400 || w.Code == 404 {
				t.Fatalf("invalid input escaped strict decoder: %d", w.Code)
			}
		})
	}
}

func TestOutputReplayRejectsNoncanonicalNestedReceiptAndAliases(t *testing.T) {
	raw, err := os.ReadFile("../../docs/obs-capture/output-replay-v1/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Fixtures []struct {
			Schema string
			Valid  bool
			Value  map[string]any
		}
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	var valid map[string]any
	for _, f := range fixtures.Fixtures {
		if f.Schema == "BroadcastOutputReplayInput" && f.Valid {
			valid = f.Value
			break
		}
	}
	if valid == nil {
		t.Fatal("missing canonical input")
	}
	original, _ := json.Marshal(valid)
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "dapr-test", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).Routes()
	cases := map[string]func(map[string]any){
		"null-command":       func(v map[string]any) { v["receipt"].(map[string]any)["commandId"] = nil },
		"empty-command":      func(v map[string]any) { v["receipt"].(map[string]any)["commandId"] = "" },
		"source-alias":       func(v map[string]any) { v["SourceId"] = v["sourceId"] },
		"key-alias":          func(v map[string]any) { v["StreamKey"] = v["streamKey"] },
		"receipt-alias":      func(v map[string]any) { v["receipt"].(map[string]any)["Operation"] = "bind" },
		"receipt-null-state": func(v map[string]any) { v["receipt"].(map[string]any)["state"] = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var input map[string]any
			if err := json.Unmarshal(original, &input); err != nil {
				t.Fatal(err)
			}
			mutate(input)
			body, _ := json.Marshal(input)
			req := httptest.NewRequest("POST", "/api/ingest/streams/replay", bytes.NewReader(body))
			req.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
			req.Header.Set("dapr-api-token", "dapr-test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("noncanonical replay reached service: status %d", w.Code)
			}
		})
	}
}
