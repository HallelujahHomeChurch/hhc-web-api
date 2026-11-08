// broadcast-mock is a loopback-only, in-memory B1 consumer test server.
// It never authenticates real users, persists commands, or validates media.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const version = "b1-2026-10-10.rc1"

type object = map[string]any
type fixture struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
	Valid  bool   `json:"valid"`
	Value  any    `json:"value"`
}
type receipt struct {
	digest string
	status int
	body   []byte
}
type mock struct {
	mu        sync.Mutex
	items     map[string]object
	order     []string
	receipts  map[string]receipt
	fixtures  map[string]any
	verified  map[string]int
	stopped   map[string]bool
	inventory map[string]int
}

func clone(v any) any { b, _ := json.Marshal(v); var out any; _ = json.Unmarshal(b, &out); return out }
func intValue(v any) int {
	if v == nil {
		return 0
	}
	return int(v.(float64))
}
func envelope(data any) object { return object{"data": data, "meta": object{}, "error": nil} }
func failure(code string) object {
	return object{"data": nil, "meta": object{}, "error": object{"code": code, "message": code}}
}
func bump(b object) { b["revision"] = float64(intValue(b["revision"]) + 1) }
func ackCommand(b object) object {
	if b["pendingCommand"] == nil {
		return nil
	}
	return b["pendingCommand"].(object)
}

func newMock(spec, examples string) (http.Handler, error) {
	doc, err := openapi3.NewLoader().LoadFromFile(spec)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var resource any
	if err = json.Unmarshal(raw, &resource); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err = compiler.AddResource("openapi.json", resource); err != nil {
		return nil, err
	}
	raw, err = os.ReadFile(examples)
	if err != nil {
		return nil, err
	}
	var all []fixture
	if err = json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	m := &mock{items: map[string]object{}, receipts: map[string]receipt{}, fixtures: map[string]any{}, verified: map[string]int{}, stopped: map[string]bool{}, inventory: map[string]int{}}
	for _, f := range all {
		schema, e := compiler.Compile("openapi.json#/components/schemas/" + f.Schema)
		if e != nil {
			return nil, e
		}
		e = schema.Validate(f.Value)
		if (e == nil) != f.Valid {
			return nil, fmt.Errorf("fixture %s validity: %v", f.Name, e)
		}
		if f.Valid {
			m.fixtures[f.Name] = f.Value
		}
	}
	for _, n := range []string{"view_draft", "capabilities", "preview"} {
		if m.fixtures[n] == nil {
			return nil, fmt.Errorf("missing fixture %s", n)
		}
	}
	mux := http.NewServeMux()
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if _, ok := op.Extensions["x-b1-status"]; !ok {
				continue
			}
			var input *jsonschema.Schema
			if op.RequestBody != nil {
				input, err = compiler.Compile("openapi.json" + op.RequestBody.Value.Content.Get("application/json").Schema.Ref)
				if err != nil {
					return nil, err
				}
			}
			outputs := map[int]*jsonschema.Schema{}
			for code, r := range op.Responses.Map() {
				n, e := strconv.Atoi(code)
				if e != nil {
					continue
				}
				schema, e := compiler.Compile("openapi.json" + r.Value.Content.Get("application/json").Schema.Ref)
				if e != nil {
					return nil, e
				}
				outputs[n] = schema
			}
			// Request parameters use their canonical schemas as well as body schemas.
			params := map[string]*jsonschema.Schema{}
			required := map[string]bool{}
			locations := map[string]string{}
			types := map[string]bool{}
			for i, p := range op.Parameters {
				parameter := p.Value
				encoded, _ := json.Marshal(parameter.Schema)
				var schemaObject any
				_ = json.Unmarshal(encoded, &schemaObject)
				resourceID := fmt.Sprintf("param-%s-%d.json", op.OperationID, i)
				if err = compiler.AddResource(resourceID, schemaObject); err != nil {
					return nil, err
				}
				compiled, e := compiler.Compile(resourceID)
				if e != nil {
					return nil, e
				}
				params[parameter.Name] = compiled
				required[parameter.Name] = parameter.Required
				locations[parameter.Name] = parameter.In
				types[parameter.Name] = parameter.Schema.Value.Type.Is("integer")
			}
			mux.HandleFunc(method+" /api"+path, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-B1-Mock", "synthetic-in-memory")
				respond := func(status int, v any) {
					if output := outputs[status]; output == nil || output.Validate(v) != nil {
						http.Error(w, "mock response violated canonical schema", 500)
						return
					}
					if status == 429 || status == 503 {
						w.Header().Set("Retry-After", "2")
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(v)
				}
				query := r.URL.Query()
				for name, values := range query {
					if locations[name] != "query" || len(values) != 1 {
						respond(400, failure("broadcast_invalid"))
						return
					}
				}
				for name, schema := range params {
					value := r.PathValue(name)
					if locations[name] == "query" {
						value = query.Get(name)
					}
					if value == "" && !required[name] {
						continue
					}
					var x any = value
					if types[name] {
						n, e := strconv.ParseInt(value, 10, 64)
						if e != nil {
							respond(400, failure("broadcast_invalid"))
							return
						}
						x = n
					}
					if schema.Validate(x) != nil {
						respond(400, failure("broadcast_invalid"))
						return
					}
				}
				in := object{}
				if input != nil {
					dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
					if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || input.Validate(in) != nil {
						respond(400, failure("broadcast_invalid"))
						return
					}
				}
				if scenario := r.Header.Get("X-B1-Mock-Scenario"); scenario != "" {
					statuses := map[string]int{"unauthorized": 401, "forbidden": 403, "rate_limited": 429, "unavailable": 503}
					status, ok := statuses[scenario]
					if !ok {
						respond(400, failure("broadcast_invalid"))
						return
					}
					respond(status, failure(scenario))
					return
				}
				actor := r.Header.Get("X-B1-Mock-Actor")
				if _, e := uuid.Parse(actor); e != nil {
					respond(401, failure("unauthorized"))
					return
				}
				scopes := r.Header.Get("X-B1-Mock-Scopes")
				if scopes == "" {
					scopes = "cms:recordings:read cms:recordings:write cms:recordings:publish"
				}
				if raw, ok := op.Extensions["x-required-scopes"]; ok {
					encoded, _ := json.Marshal(raw)
					var needed []string
					_ = json.Unmarshal(encoded, &needed)
					for _, s := range needed {
						if !slices.Contains(strings.Fields(scopes), s) {
							respond(403, failure("forbidden"))
							return
						}
					}
				}
				m.mu.Lock()
				defer m.mu.Unlock()
				status, result := m.handle(op.OperationID, r, in, actor, scopes)
				respond(status, result)
			})
		}
	}
	// Synthetic validator notifications are only a test-driver interface, not B1.
	mux.HandleFunc("POST /__mock/media", m.media)
	return mux, nil
}

func (m *mock) handle(op string, r *http.Request, in object, actor, scopes string) (int, any) {
	id := r.PathValue("recordingId")
	key, _ := in["operationKey"].(string)
	receiptKey := actor + "|" + id + "|" + key
	bytes, _ := json.Marshal(in)
	digest := op + "|" + r.URL.Path + "|" + string(bytes)
	if key != "" {
		if previous, ok := m.receipts[receiptKey]; ok {
			if previous.digest != digest {
				return 409, failure("broadcast_operation_conflict")
			}
			var v any
			_ = json.Unmarshal(previous.body, &v)
			return previous.status, v
		}
	}
	b := m.items[id]
	if id != "" && b == nil {
		return 404, failure("broadcast_not_found")
	}
	if expected, ok := in["expectedRevision"]; ok && expected != b["revision"] {
		return 412, failure("broadcast_revision_conflict")
	}
	ok := func(status int, operation string) (int, any) {
		receipt := object{"operationKey": key, "operation": operation, "state": "completed"}
		if status == 202 {
			receipt["state"] = "accepted"
		}
		if c := ackCommand(b); c != nil {
			receipt["commandId"] = c["commandId"]
		}
		v := envelope(object{"broadcast": b, "receipt": receipt})
		raw, _ := json.Marshal(v)
		m.receipts[receiptKey] = struct {
			digest string
			status int
			body   []byte
		}{digest, status, raw}
		return status, v
	}
	hasPublish := slices.Contains(strings.Fields(scopes), "cms:recordings:publish")
	switch op {
	case "getBroadcastCapabilities":
		return 200, clone(m.fixtures["capabilities"])
	case "createBroadcast":
		policy := in["policy"].(object)
		if (policy["autoStart"] == true || policy["autoPublish"] == true) && !hasPublish {
			return 403, failure("forbidden")
		}
		b = clone(m.fixtures["view_draft"]).(object)["data"].(object)
		id = uuid.NewString()
		b["recordingId"] = id
		for _, k := range []string{"title", "description", "scheduledAt", "policy"} {
			b[k] = in[k]
		}
		if cover := in["cover"]; cover != nil {
			b["cover"].(object)["selection"] = cover
		}
		m.items[id] = b
		m.order = append(m.order, id)
		m.verified[id] = -1
		return ok(201, "create")
	case "getBroadcast":
		return 200, envelope(b)
	case "listBroadcasts":
		items := []any{}
		for _, id := range m.order {
			v := m.items[id]
			selectable := v["binding"] == nil && v["phase"] != "cancelled" && v["phase"] != "failed"
			if r.URL.Query().Get("filter") != "selectable" || selectable {
				items = append(items, v)
			}
		}
		offset := 0
		if cursor := r.URL.Query().Get("cursor"); cursor != "" {
			decoded, e := base64.RawURLEncoding.DecodeString(cursor)
			if e != nil {
				return 400, failure("broadcast_invalid")
			}
			offset, e = strconv.Atoi(string(decoded))
			if e != nil || offset < 0 || offset > len(items) {
				return 400, failure("broadcast_invalid")
			}
		}
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			limit, _ = strconv.Atoi(v)
		}
		end := min(offset+limit, len(items))
		var next any
		if end < len(items) {
			next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
		}
		return 200, envelope(object{"items": items[offset:end], "nextCursor": next})
	case "updateBroadcast":
		if b["phase"] == "cancelled" || b["phase"] == "failed" {
			return 409, failure("broadcast_state_conflict")
		}
		if policy, exists := in["policy"]; exists {
			if b["binding"] != nil {
				return 409, failure("broadcast_state_conflict")
			}
			p := policy.(object)
			if (p["autoStart"] == true || p["autoPublish"] == true) && !hasPublish {
				return 403, failure("forbidden")
			}
		}
		for _, k := range []string{"title", "description", "scheduledAt", "policy"} {
			if v, ok := in[k]; ok {
				b[k] = v
			}
		}
		if v, exists := in["cover"]; exists {
			b["cover"].(object)["selection"] = v
		}
		bump(b)
		return ok(200, "update")
	case "announceBroadcast":
		if b["phase"] != "draft" && b["phase"] != "scheduled" && b["phase"] != "preview" {
			return 409, failure("broadcast_state_conflict")
		}
		b["announced"] = true
		if b["phase"] == "draft" {
			b["phase"] = "scheduled"
		}
		bump(b)
		return ok(200, "announce")
	case "cancelBroadcast":
		if b["binding"] != nil || b["phase"] == "cancelled" {
			return 409, failure("broadcast_state_conflict")
		}
		b["phase"] = "cancelled"
		bump(b)
		return ok(200, "cancel")
	case "bindBroadcast":
		if in["protocolVersion"] != version {
			return 422, failure("broadcast_protocol_unsupported")
		}
		epoch := 1
		if binding, bound := b["binding"].(object); bound {
			if !m.captureFinished(id) || b["boundaries"].(object)["startSequence"] != nil {
				return 409, failure("broadcast_already_bound")
			}
			epoch = intValue(binding["epoch"]) + 1
		}
		if epoch == 1 && b["phase"] != "draft" && b["phase"] != "scheduled" {
			return 409, failure("broadcast_state_conflict")
		}
		policy := b["policy"].(object)
		if (policy["autoStart"] == true || policy["autoPublish"] == true) && !hasPublish {
			return 403, failure("forbidden")
		}
		if policy["autoStart"] == true && b["announced"] != true {
			return 409, failure("broadcast_state_conflict")
		}
		for _, v := range m.items {
			if v["binding"] != nil && !m.captureFinished(v["recordingId"].(string)) {
				return 409, failure("broadcast_active_limit")
			}
		}
		b["binding"] = object{"captureId": strings.ReplaceAll(uuid.NewString(), "-", ""), "epoch": float64(epoch), "actorId": actor}
		m.verified[id] = -1
		delete(m.stopped, id)
		delete(m.inventory, id)
		b["pendingCommand"] = nil
		bounds := b["boundaries"].(object)
		b["boundaries"] = object{"startSequence": nil, "endSequenceExclusive": nil, "revision": float64(intValue(bounds["revision"]) + 1)}
		b["archive"] = object{"state": "none", "reason": nil}
		b["phase"] = "preview"
		bump(b)
		return ok(202, "bind")
	case "startBroadcast", "endBroadcast":
		if b["binding"] == nil || b["announced"] != true {
			return 409, failure("broadcast_state_conflict")
		}
		kind := "start"
		next := "start_pending"
		if op == "startBroadcast" {
			if b["phase"] != "preview" || b["policy"].(object)["autoStart"] == true || m.stopped[id] {
				return 409, failure("broadcast_state_conflict")
			}
		} else {
			kind = "end"
			next = "end_pending"
			if b["phase"] != "live" {
				return 409, failure("broadcast_state_conflict")
			}
		}
		binding := b["binding"].(object)
		b["pendingCommand"] = object{"commandId": uuid.NewString(), "kind": kind, "captureId": binding["captureId"], "epoch": binding["epoch"], "requestedAt": time.Now().UTC().Format(time.RFC3339), "status": "pending", "boundarySequence": nil}
		b["phase"] = next
		bump(b)
		return ok(202, kind)
	case "cancelBroadcastCommand":
		c := ackCommand(b)
		if c == nil || c["commandId"] != r.PathValue("commandId") || c["kind"] != "start" || c["status"] != "pending" {
			return 409, failure("broadcast_state_conflict")
		}
		c["status"] = "cancelled"
		b["phase"] = "preview"
		bump(b)
		return ok(200, "cancel-command")
	case "getBroadcastControl", "acknowledgeBroadcastCommand", "createBroadcastPreviewAccess":
		if b["binding"] == nil {
			return 409, failure("broadcast_state_conflict")
		}
		binding := b["binding"].(object)
		if op != "createBroadcastPreviewAccess" && binding["actorId"] != actor {
			return 403, failure("forbidden")
		}
		epoch, cap := in["epoch"], in["captureId"]
		if op == "getBroadcastControl" {
			v, _ := strconv.Atoi(r.URL.Query().Get("epoch"))
			epoch = float64(v)
			cap = r.URL.Query().Get("captureId")
		}
		if epoch != binding["epoch"] || cap != binding["captureId"] {
			return 409, failure("broadcast_epoch_mismatch")
		}
		if op == "getBroadcastControl" {
			return 200, envelope(object{"revision": b["revision"], "phase": b["phase"], "pendingCommand": b["pendingCommand"]})
		}
		if op == "createBroadcastPreviewAccess" {
			v := clone(m.fixtures["preview"]).(object)
			d := v["data"].(object)
			d["recordingId"] = id
			d["captureId"] = cap
			d["epoch"] = epoch
			return 200, v
		}
		c := ackCommand(b)
		if c == nil || c["commandId"] != r.PathValue("commandId") || c["status"] == "cancelled" || c["status"] == "rejected" {
			return 409, failure("broadcast_state_conflict")
		}
		if c["boundarySequence"] != nil && c["boundarySequence"] != in["boundarySequence"] {
			return 409, failure("broadcast_boundary_conflict")
		}
		if c["kind"] == "end" {
			start := b["boundaries"].(object)["startSequence"]
			if start == nil || intValue(in["boundarySequence"]) <= intValue(start) {
				return 409, failure("broadcast_boundary_conflict")
			}
		}
		c["boundarySequence"] = in["boundarySequence"]
		c["status"] = "acknowledged"
		bump(b)
		m.advance(b)
		return ok(202, "ack")
	case "emergencyCloseBroadcast":
		if b["binding"] == nil {
			return 409, failure("broadcast_state_conflict")
		}
		bounds := b["boundaries"].(object)
		bounds["endSequenceExclusive"] = float64(max(0, m.verified[id]+1))
		bounds["revision"] = float64(intValue(bounds["revision"]) + 1)
		b["phase"] = "failed"
		b["archive"].(object)["reason"] = "emergency_closed"
		if c := ackCommand(b); c != nil {
			c["status"] = "rejected"
		}
		bump(b)
		return ok(202, "emergency-close")
	case "getBroadcastWatch":
		if b["announced"] != true {
			return 404, failure("broadcast_not_found")
		}
		view := "waiting"
		switch b["phase"] {
		case "live", "end_pending":
			view = "live"
		case "processing":
			view = "processing"
		case "archived":
			view = "vod"
		case "cancelled":
			view = "cancelled"
		case "failed":
			view = "unavailable"
		}
		return 200, envelope(object{"recordingId": id, "title": b["title"], "description": b["description"], "view": view, "scheduledAt": b["scheduledAt"], "serverNow": time.Now().UTC().Format(time.RFC3339), "cover": nil, "stateRevision": b["revision"]})
	}
	return 404, failure("broadcast_not_found")
}

func (m *mock) captureFinished(id string) bool {
	last, sealed := m.inventory[id]
	return m.stopped[id] && sealed && m.verified[id] >= last
}

func (m *mock) advance(b object) {
	id := b["recordingId"].(string)
	bounds := b["boundaries"].(object)
	if b["phase"] == "failed" || b["phase"] == "cancelled" {
		return
	}
	if last, sealed := m.inventory[id]; sealed {
		if c := ackCommand(b); c != nil && c["status"] == "acknowledged" {
			limit := last
			if c["kind"] == "end" {
				limit++
			}
			if intValue(c["boundarySequence"]) > limit {
				c["status"] = "rejected"
				b["phase"] = "failed"
				b["archive"].(object)["reason"] = "broadcast_boundary_conflict"
				bump(b)
				return
			}
		}
	}
	if m.stopped[id] && b["phase"] != "cancelled" && b["phase"] != "failed" {
		if bounds["startSequence"] == nil {
			b["phase"] = "cancelled"
			if c := ackCommand(b); c != nil {
				c["status"] = "rejected"
			}
			bump(b)
			return
		}
		if last, sealed := m.inventory[id]; sealed && b["policy"].(object)["autoEnd"] == true && bounds["endSequenceExclusive"] == nil {
			if last < intValue(bounds["startSequence"]) {
				b["phase"] = "failed"
				bump(b)
				return
			}
			c := ackCommand(b)
			// An explicit acknowledged End freezes a shorter public range than normal Stop.
			if c == nil || c["kind"] != "end" || c["status"] != "acknowledged" {
				b["phase"] = "end_pending"
				if m.verified[id] >= last {
					bounds["endSequenceExclusive"] = float64(last + 1)
					b["phase"] = "processing"
					b["archive"].(object)["state"] = "processing"
					bump(b)
				}
			}
		}
	}
	if b["phase"] == "preview" && b["policy"].(object)["autoStart"] == true && m.verified[id] >= 2 && !m.stopped[id] {
		bounds["startSequence"] = float64(0)
		b["phase"] = "live"
		bump(b)
	}
	c := ackCommand(b)
	if c == nil || c["status"] != "acknowledged" {
		return
	}
	boundary := intValue(c["boundarySequence"])
	if c["kind"] == "start" && b["phase"] == "start_pending" && !m.stopped[id] && m.verified[id] >= boundary+2 {
		bounds["startSequence"] = float64(boundary)
		b["phase"] = "live"
		bump(b)
	}
	if c["kind"] == "end" && b["phase"] == "end_pending" && m.verified[id] >= boundary-1 {
		bounds["endSequenceExclusive"] = float64(boundary)
		b["phase"] = "processing"
		b["archive"].(object)["state"] = "processing"
		bump(b)
	}
}
func (m *mock) media(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	var event struct {
		RecordingID           string `json:"recordingId"`
		VerifiedLastSequence  *int   `json:"verifiedLastSequence"`
		Stopped               bool   `json:"stopped"`
		InventoryLastSequence *int   `json:"inventoryLastSequence"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&event) != nil || d.Decode(new(any)) != io.EOF || event.VerifiedLastSequence == nil || (event.InventoryLastSequence != nil && (*event.InventoryLastSequence < 0 || *event.InventoryLastSequence > 2147483646 || *event.VerifiedLastSequence > *event.InventoryLastSequence)) || *event.VerifiedLastSequence < -1 || *event.VerifiedLastSequence > 2147483647 {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(failure("broadcast_invalid"))
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.items[event.RecordingID]
	if b == nil {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(failure("broadcast_not_found"))
		return
	}
	last, sealed := m.inventory[event.RecordingID]
	if b["binding"] == nil || (sealed && (*event.VerifiedLastSequence > last || (event.InventoryLastSequence != nil && *event.InventoryLastSequence != last))) || *event.VerifiedLastSequence < m.verified[event.RecordingID] {
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(failure("broadcast_state_conflict"))
		return
	}
	m.verified[event.RecordingID] = *event.VerifiedLastSequence
	if event.Stopped {
		m.stopped[event.RecordingID] = true
	}
	if event.InventoryLastSequence != nil {
		m.inventory[event.RecordingID] = *event.InventoryLastSequence
	}
	m.advance(b)
	_ = json.NewEncoder(w).Encode(envelope(b))
}
func main() {
	spec := flag.String("spec", "docs/obs-capture/live-control-v1/openapi.snapshot.yaml", "B1 candidate OpenAPI")
	fixtures := flag.String("fixtures", "docs/obs-capture/live-control-v1/wire-fixtures.json", "B1 examples")
	port := flag.Int("port", 8790, "loopback-only port")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		log.Fatal("invalid port")
	}
	h, err := newMock(*spec, *fixtures)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	log.Printf("synthetic B1 mock on %s; no real authentication, storage or media validation", server.Addr)
	log.Fatal(server.ListenAndServe())
}
