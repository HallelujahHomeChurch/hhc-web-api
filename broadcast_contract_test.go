package hhcwebapi

import (
	"context"
	"encoding/json"
	"github.com/getkin/kin-openapi/openapi3"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// These examples pin the consumer's boundary and retry wire inputs, not implementation types.
func TestBroadcastContractMarkerValidation(t *testing.T) {
	schema := compileBroadcastSchema(t, "BroadcastMarkerAckInput")
	good := map[string]any{"operationKey": "ack-1", "captureId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "epoch": float64(1), "boundarySequence": float64(8)}
	for _, tc := range []struct {
		name, key string
		value     any
		valid     bool
	}{
		{"valid", "boundarySequence", float64(8), true},
		{"negative", "boundarySequence", float64(-1), false},
		{"fraction", "boundarySequence", 8.5, false},
		{"zero epoch", "epoch", float64(0), false},
		{"unsafe epoch", "epoch", float64(9007199254740992), false},
		{"unknown field", "expectedRevision", float64(2), false},
		{"bad capture", "captureId", "../capture", false},
		{"empty key", "operationKey", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{}
			for k, x := range good {
				v[k] = x
			}
			v[tc.key] = tc.value
			if err := schema.Validate(v); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestBroadcastWireFixtures(t *testing.T) {
	raw, err := os.ReadFile("docs/obs-capture/live-control-v1/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Schema string
		Valid        bool
		Value        any
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 16 {
		t.Fatal("missing success and failure fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			err := compileBroadcastSchema(t, c.Schema).Validate(c.Value)
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v error=%v", c.Valid, err)
			}
		})
	}
}

func TestBroadcastCandidateSchemaAndSharedTypes(t *testing.T) {
	canonical := loadOpenAPI(t)
	frozenPreviewInput(t, canonical)
	candidate, err := openapi3.NewLoader().LoadFromFile("docs/obs-capture/live-control-v1/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = candidate.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range candidate.Paths.Map() {
		count += len(item.Operations())
	}
	if count != 16 {
		t.Fatalf("incomplete B1 surface: %d", count)
	}
	for name, schema := range candidate.Components.Schemas {
		if shared := canonical.Components.Schemas[name]; shared != nil {
			shared = legacyPublicIDSchema(t, name, legacyCoverModeSchema(t, name, shared))
			// rc1 remains immutable; canonical rc2 may only add these nullable resolver fields.
			if name == "BroadcastWatch" {
				copy := *shared.Value
				copy.Properties = openapi3.Schemas{}
				for key, property := range shared.Value.Properties {
					copy.Properties[key] = property
				}
				for _, key := range []string{"recording", "live", "mediaStartSeconds", "durationSeconds"} {
					property := copy.Properties[key]
					if property == nil || len(property.Value.AnyOf) != 2 || !property.Value.AnyOf[1].Value.Type.Is("null") {
						t.Fatalf("resolver addition must be nullable: %s", key)
					}
					delete(copy.Properties, key)
				}
				shared = &openapi3.SchemaRef{Value: &copy}
			}
			a, _ := json.Marshal(schema)
			b, _ := json.Marshal(shared)
			if string(a) != string(b) {
				t.Fatalf("shared schema drift: %s", name)
			}
		}
	}
}

func compileBroadcastSchema(t *testing.T, name string) *jsonschema.Schema {
	return compileCandidateSchema(t, "docs/obs-capture/live-control-v1/openapi.snapshot.yaml", name)
}

var contractSchemaCompilers = struct {
	sync.Mutex
	byPath map[string]*jsonschema.Compiler
}{byPath: make(map[string]*jsonschema.Compiler)}

func compileCandidateSchema(t *testing.T, path, name string) *jsonschema.Schema {
	t.Helper()
	// Contract files are immutable during a test run; keep mutable OpenAPI documents local to their tests.
	contractSchemaCompilers.Lock()
	defer contractSchemaCompilers.Unlock()
	compiler := contractSchemaCompilers.byPath[path]
	if compiler == nil {
		doc, err := openapi3.NewLoader().LoadFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var resource any
		if err = json.Unmarshal(raw, &resource); err != nil {
			t.Fatal(err)
		}
		compiler = jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.AssertFormat()
		if err = compiler.AddResource("contract.json", resource); err != nil {
			t.Fatal(err)
		}
		contractSchemaCompilers.byPath[path] = compiler
	}
	schema, err := compiler.Compile("contract.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestBroadcastPrivateProducerContract(t *testing.T) {
	path := "docs/obs-capture/live-control-v1/asset.openapi.snapshot.yaml"
	doc, err := openapi3.NewLoader().LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	schema := compileCandidateSchema(t, path, "BroadcastRangeInput")
	base := map[string]any{"recordingId": "018f0c1f-18d0-7e81-9f6f-69c456db7003", "epoch": float64(1), "rangeRevision": float64(2), "startSequence": float64(8), "endSequenceExclusive": float64(20), "revoked": false}
	if err = schema.Validate(base); err != nil {
		t.Fatal(err)
	}
	base["startSequence"] = nil
	base["endSequenceExclusive"] = nil
	if err = schema.Validate(base); err != nil {
		t.Fatal("pending range rejected", err)
	}
	base["rangeRevision"] = float64(0)
	if schema.Validate(base) == nil {
		t.Fatal("unversioned range accepted")
	}
}

func TestBroadcastReplayRenewalContract(t *testing.T) {
	op := loadOpenAPI(t).Paths.Value("/member/recordings/{id}/live/playback").Post
	for _, phrase := range []string{"already registered scope", "same account", "original replay", "New scopes are rejected"} {
		if !strings.Contains(op.Description, phrase) {
			t.Fatalf("missing replay admission contract: %s", phrase)
		}
	}
}

func TestBroadcastMemberLiveCoverCompatibilityContract(t *testing.T) {
	op := loadOpenAPI(t).Paths.Value("/member/recordings/{id}/live/{captureId}/cover").Get
	for _, phrase := range []string{"exact bound capture", "announced, unrevoked", "unstopped, unexpired", "rechecked after Asset I/O", "does not enable legacy capture-scoped administrator writes", "watch.live", "coverRevision", "readable broadcast cover snapshot"} {
		if !strings.Contains(op.Description, phrase) {
			t.Fatalf("missing live-cover compatibility contract: %s", phrase)
		}
	}
}

func legacyCoverModeSchema(t *testing.T, name string, schema *openapi3.SchemaRef) *openapi3.SchemaRef {
	t.Helper()
	if name != "BroadcastWatch" && name != "MemberLiveRecording" {
		return schema
	}
	property := schema.Value.Properties["coverMode"]
	if property == nil || !property.Value.Type.Is("string") || len(property.Value.Enum) != 2 || property.Value.Enum[0] != "auto" || property.Value.Enum[1] != "custom" {
		t.Fatalf("invalid additive coverMode: %s", name)
	}
	for _, required := range schema.Value.Required {
		if required == "coverMode" {
			t.Fatalf("coverMode must remain optional: %s", name)
		}
	}
	copy := *schema.Value
	copy.Properties = openapi3.Schemas{}
	for key, value := range schema.Value.Properties {
		if key != "coverMode" {
			copy.Properties[key] = value
		}
	}
	return &openapi3.SchemaRef{Value: &copy}
}

// Public locators are optional additions; frozen OBS payloads and their hashes stay immutable.
func legacyPublicIDSchema(t *testing.T, name string, schema *openapi3.SchemaRef) *openapi3.SchemaRef {
	t.Helper()
	switch name {
	case "BroadcastWatch", "MemberLiveRecording", "MemberRecording", "AdminRecording":
	default:
		return schema
	}
	if schema == nil || schema.Value == nil {
		t.Fatalf("missing schema: %s", name)
	}
	property := schema.Value.Properties["publicId"]
	if property == nil || property.Value == nil || !property.Value.Type.Is("string") || property.Value.Pattern != "^[A-Za-z0-9_-]{11}$" || slices.Contains(schema.Value.Required, "publicId") {
		t.Fatalf("public ID must remain optional and bounded: %s", name)
	}
	copy := *schema.Value
	copy.Properties = openapi3.Schemas{}
	for key, value := range schema.Value.Properties {
		if key != "publicId" {
			copy.Properties[key] = value
		}
	}
	return &openapi3.SchemaRef{Value: &copy}
}
