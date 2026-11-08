package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const mockActor = "018f0c1f-18d0-7e81-9f6f-69c456db7004"

func server(t *testing.T) http.Handler {
	t.Helper()
	h, e := newMock("../../docs/obs-capture/live-control-v1/openapi.snapshot.yaml", "../../docs/obs-capture/live-control-v1/wire-fixtures.json")
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func call(t *testing.T, h http.Handler, method, path, body string, want int, headers ...string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-B1-Mock-Actor", mockActor)
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: want %d got %d %s", method, path, want, w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable mock response")
	}
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func data(v map[string]any) map[string]any      { return v["data"].(map[string]any) }
func broadcast(v map[string]any) map[string]any { return data(v)["broadcast"].(map[string]any) }
func number(v any) int                          { return int(v.(float64)) }
func encoded(v any) string                      { b, _ := json.Marshal(v); return string(b) }
func mutate(t *testing.T, h http.Handler, path, key string, rev int) map[string]any {
	return call(t, h, "POST", path, encoded(map[string]any{"operationKey": key, "expectedRevision": rev}), 200)
}
func prepare(t *testing.T, h http.Handler) (string, map[string]any) {
	t.Helper()
	v := call(t, h, "POST", "/api/admin/broadcasts", `{"operationKey":"create","title":"Sunday","description":"","scheduledAt":null,"policy":{"autoStart":false,"autoEnd":true,"autoPublish":true}}`, 201)
	b := broadcast(v)
	path := "/api/admin/broadcasts/" + b["recordingId"].(string)
	v = mutate(t, h, path+"/announce", "announce", number(b["revision"]))
	v = call(t, h, "POST", path+"/bindings", encoded(map[string]any{"operationKey": "bind", "expectedRevision": number(broadcast(v)["revision"]), "protocolVersion": "b1-2026-10-10.rc1"}), 202)
	return path, broadcast(v)
}
func start(t *testing.T, h http.Handler, path string, b map[string]any) map[string]any {
	return broadcast(call(t, h, "POST", path+"/start", encoded(map[string]any{"operationKey": "start", "expectedRevision": b["revision"]}), 202))
}

func TestMarkerReplayConflictAndMetadataEdit(t *testing.T) {
	h := server(t)
	path, b := prepare(t, h)
	b = start(t, h, path, b)
	cmd := b["pendingCommand"].(map[string]any)
	binding := b["binding"].(map[string]any)
	// Editing title is allowed after issuing a marker; ACK is fenced by command/epoch, not title revision.
	call(t, h, "PATCH", path, encoded(map[string]any{"operationKey": "title", "expectedRevision": b["revision"], "title": "New title"}), 200)
	ack := map[string]any{"operationKey": "ack", "captureId": binding["captureId"], "epoch": binding["epoch"], "boundarySequence": 8}
	endpoint := path + "/commands/" + cmd["commandId"].(string) + "/ack"
	first := call(t, h, "POST", endpoint, encoded(ack), 202)
	replay := call(t, h, "POST", endpoint, encoded(ack), 202)
	if encoded(first) != encoded(replay) {
		t.Fatal("replay changed original receipt")
	}
	if broadcast(first)["title"] != "New title" {
		t.Fatal("ACK overwrote metadata")
	}
	if broadcast(first)["phase"] != "start_pending" {
		t.Fatal("ACK falsely proves media ready")
	}
	ack["boundarySequence"] = 9
	v := call(t, h, "POST", endpoint, encoded(ack), 409)
	if v["error"].(map[string]any)["code"] != "broadcast_operation_conflict" {
		t.Fatal(v)
	}
	ack["operationKey"] = "different-key"
	v = call(t, h, "POST", endpoint, encoded(ack), 409)
	if v["error"].(map[string]any)["code"] != "broadcast_boundary_conflict" {
		t.Fatal(v)
	}
}
func TestStaleEpochAndCancelledStartCannotPublish(t *testing.T) {
	h := server(t)
	path, b := prepare(t, h)
	b = start(t, h, path, b)
	cmd := b["pendingCommand"].(map[string]any)
	binding := b["binding"].(map[string]any)
	ep := path + "/commands/" + cmd["commandId"].(string)
	ack := map[string]any{"operationKey": "ack", "captureId": binding["captureId"], "epoch": 2, "boundarySequence": 8}
	v := call(t, h, "POST", ep+"/ack", encoded(ack), 409)
	if v["error"].(map[string]any)["code"] != "broadcast_epoch_mismatch" {
		t.Fatal(v)
	}
	mutate(t, h, ep+"/cancel", "cancel-command", number(b["revision"]))
	ack["epoch"] = 1
	call(t, h, "POST", ep+"/ack", encoded(ack), 409)
}
func TestBindingProtocolPermissionAndLimit(t *testing.T) {
	h := server(t)
	call(t, h, "GET", "/api/admin/broadcast-capabilities", "", 200)
	call(t, h, "POST", "/api/admin/broadcasts", `{"operationKey":"bad","title":"x","description":"","scheduledAt":null,"policy":{"autoStart":false,"autoEnd":true,"autoPublish":true},"unknown":true}`, 400)
	path, b := prepare(t, h)
	call(t, h, "POST", path+"/bindings", encoded(map[string]any{"operationKey": "old", "expectedRevision": b["revision"], "protocolVersion": "c1"}), 422)
	call(t, h, "POST", path+"/start", encoded(map[string]any{"operationKey": "forbidden", "expectedRevision": b["revision"]}), 403, "X-B1-Mock-Scopes", "cms:recordings:read cms:recordings:write")
	call(t, h, "GET", path+"/control?captureId="+b["binding"].(map[string]any)["captureId"].(string)+"&epoch=1", "", 403, "X-B1-Mock-Actor", "018f0c1f-18d0-7e81-9f6f-69c456db7009")
	call(t, h, "GET", path+"/control?captureId="+b["binding"].(map[string]any)["captureId"].(string)+"&epoch=0", "", 400)
}
func TestBoundaryValidationDoesNotUseVerifiedWaterline(t *testing.T) {
	h := server(t)
	path, b := prepare(t, h)
	b = start(t, h, path, b)
	command := b["pendingCommand"].(map[string]any)
	binding := b["binding"].(map[string]any)
	call(t, h, "POST", path+"/commands/"+command["commandId"].(string)+"/ack", encoded(map[string]any{"operationKey": "ack", "captureId": binding["captureId"], "epoch": 1, "boundarySequence": 8}), 202)
	// Test-only events emulate a validator publication; they never decode or prove real media.
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 3}), 200)
	v := call(t, h, "GET", path, "", 200)
	if data(v)["phase"] != "start_pending" {
		t.Fatal("published pre-start footage")
	}
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 8}), 200)
	v = call(t, h, "GET", path, "", 200)
	if data(v)["phase"] != "start_pending" {
		t.Fatal("one public segment is not the existing three-segment startup window")
	}
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 10}), 200)
	v = call(t, h, "GET", path, "", 200)
	if data(v)["phase"] != "live" {
		t.Fatal(v)
	}
	b = broadcast(call(t, h, "POST", path+"/end", encoded(map[string]any{"operationKey": "end", "expectedRevision": data(v)["revision"]}), 202))
	command = b["pendingCommand"].(map[string]any)
	call(t, h, "POST", path+"/commands/"+command["commandId"].(string)+"/ack", encoded(map[string]any{"operationKey": "end-ack", "captureId": binding["captureId"], "epoch": 1, "boundarySequence": 20}), 202)
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 19}), 200)
	v = call(t, h, "GET", path, "", 200)
	bounds := data(v)["boundaries"].(map[string]any)
	if number(bounds["startSequence"]) != 8 || number(bounds["endSequenceExclusive"]) != 20 || data(v)["phase"] != "processing" {
		t.Fatal(v)
	}
}

func TestReplayAndSingleBindingAcrossBroadcasts(t *testing.T) {
	h := server(t)
	_, b := prepare(t, h)
	// A retry keeps the exact creation response despite later announce/bind mutations.
	v := call(t, h, "POST", "/api/admin/broadcasts", `{"operationKey":"create","title":"Sunday","description":"","scheduledAt":null,"policy":{"autoStart":false,"autoEnd":true,"autoPublish":true}}`, 201)
	if broadcast(v)["recordingId"] != b["recordingId"] || broadcast(v)["phase"] != "draft" {
		t.Fatal("create receipt changed")
	}
	v = call(t, h, "POST", "/api/admin/broadcasts", `{"operationKey":"second","title":"Second","description":"","scheduledAt":null,"policy":{"autoStart":false,"autoEnd":true,"autoPublish":true}}`, 201)
	second := broadcast(v)
	path := "/api/admin/broadcasts/" + second["recordingId"].(string)
	rejected := call(t, h, "POST", path+"/bindings", encoded(map[string]any{"operationKey": "bind-second", "expectedRevision": second["revision"], "protocolVersion": "b1-2026-10-10.rc1"}), 409)
	if rejected["error"].(map[string]any)["code"] != "broadcast_active_limit" {
		t.Fatal(rejected)
	}
	list := data(call(t, h, "GET", "/api/admin/broadcasts?limit=1", "", 200))
	cursor := list["nextCursor"].(string)
	page := data(call(t, h, "GET", "/api/admin/broadcasts?limit=1&cursor="+cursor, "", 200))
	if len(page["items"].([]any)) != 1 || page["nextCursor"] != nil {
		t.Fatal(page)
	}
	call(t, h, "GET", "/api/admin/broadcasts?limit=0", "", 400)
	call(t, h, "GET", "/api/admin/broadcasts?limit=1&limit=2", "", 400)
}

func TestNormalStopCannotPublishRehearsalAndAutoEndUsesInventory(t *testing.T) {
	h := server(t)
	path, b := prepare(t, h)
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 3, "stopped": true, "inventoryLastSequence": 3}), 200)
	got := data(call(t, h, "GET", path, "", 200))
	if got["phase"] != "cancelled" {
		t.Fatal("unstarted rehearsal must not be published", got)
	}
	h = server(t)
	path, b = prepare(t, h)
	b = start(t, h, path, b)
	c := b["pendingCommand"].(map[string]any)
	binding := b["binding"].(map[string]any)
	call(t, h, "POST", path+"/commands/"+c["commandId"].(string)+"/ack", encoded(map[string]any{"operationKey": "ack", "captureId": binding["captureId"], "epoch": 1, "boundarySequence": 8}), 202)
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 10}), 200)
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 10, "stopped": true, "inventoryLastSequence": 19}), 200)
	got = data(call(t, h, "GET", path, "", 200))
	if got["phase"] != "end_pending" {
		t.Fatal("must wait for backlog", got)
	}
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 19}), 200)
	got = data(call(t, h, "GET", path, "", 200))
	bounds := got["boundaries"].(map[string]any)
	if got["phase"] != "processing" || number(bounds["endSequenceExclusive"]) != 20 {
		t.Fatal("lost confirmed tail", got)
	}
}

func TestFinalInventoryRejectsMarkerBeyondTail(t *testing.T) {
	h := server(t)
	path, b := prepare(t, h)
	b = start(t, h, path, b)
	binding := b["binding"].(map[string]any)
	ack := func(b map[string]any, key string, boundary int) {
		c := b["pendingCommand"].(map[string]any)
		call(t, h, "POST", path+"/commands/"+c["commandId"].(string)+"/ack", encoded(map[string]any{"operationKey": key, "captureId": binding["captureId"], "epoch": 1, "boundarySequence": boundary}), 202)
	}
	ack(b, "start-ack", 8)
	v := call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 10}), 200)
	b = broadcast(call(t, h, "POST", path+"/end", encoded(map[string]any{"operationKey": "end", "expectedRevision": data(v)["revision"]}), 202))
	ack(b, "end-ack", 20)
	v = call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 15, "stopped": true, "inventoryLastSequence": 15}), 200)
	if data(v)["phase"] != "failed" {
		t.Fatal("out-of-tail marker must fail", v)
	}
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 19}), 409)
}

func TestCaptureLifetimeAndRebinding(t *testing.T) {
	for _, emergency := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed-preview", true: "emergency-still-capturing"}[emergency], func(t *testing.T) {
			h := server(t)
			path, b := prepare(t, h)
			old := b["binding"].(map[string]any)
			if emergency {
				b = broadcast(call(t, h, "POST", path+"/emergency-close", encoded(map[string]any{"operationKey": "close", "expectedRevision": b["revision"], "reason": "test"}), 202))
			} else {
				b = data(call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 3, "stopped": true, "inventoryLastSequence": 3}), 200))
			}
			want := 202
			if emergency {
				want = 409
			}
			v := call(t, h, "POST", path+"/bindings", encoded(map[string]any{"operationKey": "rebind", "expectedRevision": b["revision"], "protocolVersion": version}), want)
			if !emergency {
				binding := broadcast(v)["binding"].(map[string]any)
				if number(binding["epoch"]) != 2 || binding["captureId"] == old["captureId"] {
					t.Fatal("binding not fenced", v)
				}
				call(t, h, "GET", path+"/control?captureId="+old["captureId"].(string)+"&epoch=1", "", 409)
			}
		})
	}
}

func TestActiveSlotTracksCaptureNotPublication(t *testing.T) {
	for _, emergency := range []bool{false, true} {
		t.Run(map[bool]string{false: "processing", true: "emergency"}[emergency], func(t *testing.T) {
			h := server(t)
			path, b := prepare(t, h)
			if emergency {
				call(t, h, "POST", path+"/emergency-close", encoded(map[string]any{"operationKey": "close", "expectedRevision": b["revision"], "reason": "test"}), 202)
			} else {
				b = start(t, h, path, b)
				c := b["pendingCommand"].(map[string]any)
				binding := b["binding"].(map[string]any)
				call(t, h, "POST", path+"/commands/"+c["commandId"].(string)+"/ack", encoded(map[string]any{"operationKey": "ack", "captureId": binding["captureId"], "epoch": 1, "boundarySequence": 0}), 202)
				call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 3}), 200)
				v := call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 3, "stopped": true, "inventoryLastSequence": 3}), 200)
				if data(v)["phase"] != "processing" {
					t.Fatal(v)
				}
			}
			b = broadcast(call(t, h, "POST", "/api/admin/broadcasts", `{"operationKey":"second","title":"Second","description":"","scheduledAt":null,"policy":{"autoStart":false,"autoEnd":true,"autoPublish":false}}`, 201))
			want := 202
			if emergency {
				want = 409
			}
			call(t, h, "POST", "/api/admin/broadcasts/"+b["recordingId"].(string)+"/bindings", encoded(map[string]any{"operationKey": "bind-second", "expectedRevision": b["revision"], "protocolVersion": version}), want)
		})
	}
}

func TestEmergencyPreviewRebindResetsRange(t *testing.T) {
	h := server(t)
	path, b := prepare(t, h)
	call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 3}), 200)
	b = data(call(t, h, "GET", path, "", 200))
	call(t, h, "POST", path+"/emergency-close", encoded(map[string]any{"operationKey": "close", "expectedRevision": b["revision"], "reason": "test"}), 202)
	b = data(call(t, h, "POST", "/__mock/media", encoded(map[string]any{"recordingId": b["recordingId"], "verifiedLastSequence": 3, "stopped": true, "inventoryLastSequence": 3}), 200))
	b = broadcast(call(t, h, "POST", path+"/bindings", encoded(map[string]any{"operationKey": "rebind", "expectedRevision": b["revision"], "protocolVersion": version}), 202))
	bounds := b["boundaries"].(map[string]any)
	archive := b["archive"].(map[string]any)
	if bounds["startSequence"] != nil || bounds["endSequenceExclusive"] != nil || archive["state"] != "none" || archive["reason"] != nil {
		t.Fatal("old binding state retained", b)
	}
}
