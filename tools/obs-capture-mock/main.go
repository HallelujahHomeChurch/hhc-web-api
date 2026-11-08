// obs-capture-mock serves schema-validated, synthetic wire examples on loopback.
// It is a stateless developer fixture server, not an auth/upload/media emulator.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type fixture struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
	Valid  bool   `json:"valid"`
	Value  any    `json:"value"`
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
	fixtures := map[string]any{}
	for _, f := range all {
		schema, err := compiler.Compile("openapi.json#/components/schemas/" + f.Schema)
		if err != nil {
			return nil, err
		}
		err = schema.Validate(f.Value)
		if (err == nil) != f.Valid {
			return nil, fmt.Errorf("fixture %s validity mismatch", f.Name)
		}
		if f.Valid {
			fixtures[f.Name] = f.Value
		}
	}
	routes := map[string]string{"listMemberLiveRecordings": "live_list", "createMemberLivePlayback": "live_playback", "createRecordingCapture": "create_receipt", "getRecordingCapture": "poll", "declareRecordingCapture": "declare_receipt", "signRecordingCapture": "signed", "confirmRecordingCapture": "confirm_receipt", "stopRecordingCapture": "stop_receipt", "sealRecordingCapture": "seal_receipt", "abortRecordingCapture": "abort_receipt", "cancelAutoPublishRecordingCapture": "cancel_auto_publish_receipt", "closeLiveRecordingCapture": "close_live_receipt"}
	failureStatus := map[string]int{"invalid": 400, "forbidden": 403, "conflicting_key": 409, "missing_objects": 409, "expired": 410, "rate_limited": 429, "temporary_failure": 503}
	mux := http.NewServeMux()
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			example, ok := routes[op.OperationID]
			if !ok {
				continue
			}
			if fixtures[example] == nil {
				return nil, fmt.Errorf("missing %s", example)
			}
			var input *jsonschema.Schema
			if op.RequestBody != nil {
				schema := op.RequestBody.Value.Content.Get("application/json").Schema
				input, err = compiler.Compile("openapi.json" + schema.Ref)
				if err != nil {
					return nil, err
				}
			}
			status := 200
			if op.OperationID == "createRecordingCapture" {
				status = 201
			}
			if op.OperationID == "confirmRecordingCapture" || op.OperationID == "sealRecordingCapture" {
				status = 202
			}
			mux.HandleFunc(method+" /api"+path, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				respond := func(code int, name string) {
					if code == 429 || code == 503 {
						w.Header().Set("Retry-After", "2")
					}
					w.WriteHeader(code)
					_ = json.NewEncoder(w).Encode(fixtures[name])
				}
				if input != nil {
					decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
					var value any
					if err := decoder.Decode(&value); err != nil {
						respond(400, "invalid")
						return
					}
					if decoder.Decode(new(any)) != io.EOF || input.Validate(value) != nil {
						respond(400, "invalid")
						return
					}
				}
				if name := r.Header.Get("X-OBS-Mock-Scenario"); name != "" {
					code, ok := failureStatus[name]
					if !ok {
						respond(400, "invalid")
						return
					}
					respond(code, name)
					return
				}
				respond(status, example)
			})
		}
	}
	return mux, nil
}

func main() {
	spec := flag.String("spec", "openapi.yaml", "canonical CMS OpenAPI file")
	examples := flag.String("fixtures", "docs/obs-capture/fixtures.json", "schema-validated examples")
	port := flag.Int("port", 8789, "loopback listening port")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		log.Fatal("invalid port")
	}
	h, err := newMock(*spec, *examples)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: "127.0.0.1:" + strconv.Itoa(*port), Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	log.Print("Synthetic OBS wire fixtures at ", strings.TrimPrefix(server.Addr, ":"), "; no authentication or media upload")
	log.Fatal(server.ListenAndServe())
}
