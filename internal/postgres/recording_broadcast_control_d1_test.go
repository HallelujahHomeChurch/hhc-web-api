package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

// Exercise the actual key-only HTTP surface; only Account and Asset are external doubles.
func TestD1ControlTimeoutRenewAndOriginalCaptureFinalization(t *testing.T) {
	repo, b, ctx, now, _, actor, key, bind := preparedSource(t)
	clock := now
	svc := recordings.NewService(repo, "", nil, func() time.Time { return clock }).WithBroadcastWriters(true)
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
	defer accounts.Close()
	svc.WithPublicationAuthorizer(accountclient.New(accounts.URL, "hhc-web-api"))
	captureID := strings.Repeat("d", 32)
	status := assetclient.RecordingCaptureStatus{RecordingID: b.View.RecordingID, CaptureID: captureID, State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}
	cutoff, start := 3, 0
	projectionReady := false
	policy := assetclient.BroadcastRangeInput{RecordingID: b.View.RecordingID, Epoch: 1, RangeRevision: 1, StartSequence: &start, MemberState: "live"}
	calls := map[string]int{}
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/broadcast-signal"):
			calls["signal"]++
			json.NewEncoder(w).Encode(assetclient.BroadcastSignal{State: "ended", EndSequenceExclusive: &cutoff})
			return
		case strings.HasSuffix(r.URL.Path, "/broadcast-range"):
			if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(policy)
			return
		case strings.HasSuffix(r.URL.Path, "/broadcast-projection"):
			state := "processing"
			if projectionReady {
				state = "ready"
			}
			json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: policy.RangeRevision, StartSequence: policy.StartSequence, EndSequenceExclusive: policy.EndSequenceExclusive, State: state, Revision: 1})
			return
		case strings.HasSuffix(r.URL.Path, "/sign"):
			calls["sign"]++
			json.NewEncoder(w).Encode([]assetclient.SignedRecordingObject{{Path: "480p/seg-000019.m4s", URL: "https://upload.invalid/object", Method: "PUT"}})
			return
		case r.Method == "GET":
			json.NewEncoder(w).Encode(status)
			return
		}
		var in struct {
			OperationKey string                                `json:"operationKey"`
			Inventory    assetclient.RecordingPackageInventory `json:"inventory"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		op := "create"
		switch {
		case strings.HasSuffix(r.URL.Path, "/objects"):
			op = "declare"
		case strings.HasSuffix(r.URL.Path, "/confirm"):
			op = "confirm"
		case strings.HasSuffix(r.URL.Path, "/seal"):
			op = "seal"
			status.State = "validating"
			status.Progress.Ended = true
			status.Progress.EndedAt = &clock
			if len(in.Inventory.Renditions) != 3 || in.Inventory.Renditions[0].SegmentCount != 20 {
				t.Error("full original inventory was truncated")
			}
		}
		calls[op]++
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: status, Receipt: assetclient.RecordingCaptureReceipt{Operation: op, OperationKey: in.OperationKey, CaptureID: captureID, AcceptedAt: clock}})
	}))
	defer producer.Close()
	svc.WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	bound, err := svc.BindBroadcastIngest(context.Background(), "d1-bind", bind)
	if err != nil {
		t.Fatal(err)
	}
	token := bound.Session.Token
	h := httpapi.NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(svc).Routes()
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
		r.Header.Set("X-HHC-Request-ID", "d1-control-test")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d", method, path, w.Code, want)
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("control caching")
		}
		return w
	}
	base := "/api/ingest/broadcasts/" + b.View.RecordingID
	control := base + "/control?captureId=" + captureID + "&epoch=1"
	read := func(phase string, end *int) {
		t.Helper()
		w := call("GET", control+"&controlVersion=d1-2026-10-17.rc2", "", 200)
		var out struct {
			Data struct {
				ControlVersion, RecordingID, CaptureID, Phase string
				Epoch                                         int64
				PendingCommand                                *recordings.BroadcastCommand
				Boundaries                                    recordings.BroadcastBoundaries
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		d := out.Data
		if d.ControlVersion != "d1-2026-10-17.rc2" || d.RecordingID != b.View.RecordingID || d.CaptureID != captureID || d.Epoch != 1 || d.Phase != phase || d.PendingCommand != nil {
			t.Fatalf("wrong identity/phase/control: %+v", d)
		}
		if (d.Boundaries.EndSequenceExclusive == nil) != (end == nil) || end != nil && *d.Boundaries.EndSequenceExclusive != *end {
			t.Fatalf("wrong authoritative cutoff: %+v", d.Boundaries)
		}
	}
	// A legacy strict decoder still gets exactly the original three-field payload.
	legacy := call("GET", control, "", 200)
	var old struct{ Data map[string]json.RawMessage }
	json.Unmarshal(legacy.Body.Bytes(), &old)
	if len(old.Data) != 3 || old.Data["revision"] == nil || old.Data["phase"] == nil || old.Data["pendingCommand"] == nil {
		t.Fatal("legacy control changed")
	}
	read(bound.Broadcast.Phase, nil)
	for _, tc := range []struct {
		suffix string
		want   int
	}{{"&controlVersion=unknown", 422}, {"&controlVersion=", 422}, {"&controlVersion=d1-2026-10-17.rc2&controlVersion=d1-2026-10-17.rc2", 400}, {"&other=x", 400}, {"&controlVersion=%zz", 400}} {
		call("GET", control+tc.suffix, "", tc.want)
	}
	call("GET", base+"/control?captureId="+strings.Repeat("e", 32)+"&epoch=1&controlVersion=d1-2026-10-17.rc2", "", 409)
	call("GET", base+"/control?captureId="+captureID+"&epoch=2&controlVersion=d1-2026-10-17.rc2", "", 409)
	call("GET", "/api/ingest/broadcasts/"+platform.NewID()+"/control?captureId="+captureID+"&epoch=1&controlVersion=d1-2026-10-17.rc2", "", 401)
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Phase = "live"
		b.StartedAt = &now
		b.View.Policy.AutoStart = true
		b.View.Announced = true
		b.View.PendingCommand = nil
		b.View.Boundaries.StartSequence = &start
		b.RangePending = false
		b.RangeApplied = b.View.Boundaries.Revision
		b.MemberState = "live"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.ReconcileBroadcasts(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	read("end_pending", &cutoff)
	// End does not revoke the original credentials or release occupied input.
	source, err := svc.GetBroadcastSource(ctx)
	if err != nil || !source.InputBusy {
		t.Fatalf("public End released input: %+v %v", source, err)
	}
	clock = now.Add(time.Hour)
	call("GET", control+"&controlVersion=d1-2026-10-17.rc2", "", 401)
	renewal, _ := json.Marshal(recordings.BroadcastIngestRenewInput{StreamKey: key, SessionToken: token})
	renewed := call("POST", "/api/ingest/broadcast-source/session", string(renewal), 200)
	var session struct {
		Data recordings.BroadcastIngestSession
	}
	if json.Unmarshal(renewed.Body.Bytes(), &session) != nil || session.Data.CaptureID != captureID || session.Data.Epoch != 1 || session.Data.RecordingID != b.View.RecordingID {
		t.Fatal("renewal rebound identity")
	}
	token = session.Data.Token
	read("end_pending", &cutoff)
	projectionReady = true
	if err = svc.ReconcileBroadcasts(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	read("processing", &cutoff)
	capturePath := base + "/captures/" + captureID
	call("GET", capturePath, "", 200)
	call("POST", capturePath+"/objects", `{"operationKey":"d1-late-declare","objects":[{"path":"480p/seg-000019.m4s","sizeBytes":100,"sha256":"`+strings.Repeat("a", 64)+`"}]}`, 200)
	call("POST", capturePath+"/sign", `{"paths":["480p/seg-000019.m4s"]}`, 200)
	call("POST", capturePath+"/confirm", `{"operationKey":"d1-late-confirm","paths":["480p/seg-000019.m4s"]}`, 202)
	read("processing", &cutoff)
	call("POST", capturePath+"/stop", `{"operationKey":"d1-stop"}`, 200)
	source, err = svc.GetBroadcastSource(ctx)
	if err != nil || !source.InputBusy {
		t.Fatal("Stop alone released unsealed input", err)
	}
	seal := assetclient.RecordingCaptureSealInput{OperationKey: "d1-seal", NormalEnd: true, Inventory: assetclient.RecordingPackageInventory{SchemaVersion: 1, PresetVersion: "hhc-obs-v1", InventoryDigest: strings.Repeat("b", 64)}}
	for _, name := range []string{"480p", "720p", "1080p"} {
		seal.Inventory.Renditions = append(seal.Inventory.Renditions, assetclient.RecordingRendition{Name: name, SegmentCount: 20, DurationSeconds: 600.6})
	}
	raw, _ := json.Marshal(seal)
	call("POST", capturePath+"/seal", string(raw), 202)
	call("POST", capturePath+"/seal", string(raw), 202)
	if err = svc.ReconcileBroadcasts(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	read("processing", &cutoff)
	if calls["seal"] != 1 || calls["create"] != 1 || calls["sign"] != 1 {
		t.Fatal("duplicate finalize or replacement capture", calls)
	}
	source, err = svc.GetBroadcastSource(ctx)
	if err != nil || source.InputBusy {
		t.Fatal("sealed input not released", err)
	}
	// Terminal views keep the same binding and frozen range; no guessed watermark.
	for _, phase := range []string{"archived", "failed", "cancelled"} {
		_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, clock, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			b.View.Phase = phase
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		read(phase, &cutoff)
	}
	// Persisted binding identity is checked independently of the echoed query.
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, clock, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Binding.ActorID = platform.NewID()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	call("GET", control+"&controlVersion=d1-2026-10-17.rc2", "", 401)
	// Session epoch is checked independently of the echoed query.
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, actor, clock, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		b.View.Binding.ActorID = actor
		b.Epoch++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	call("GET", control+"&controlVersion=d1-2026-10-17.rc2", "", 401)
}
