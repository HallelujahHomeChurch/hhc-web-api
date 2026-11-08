package hhcwebapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/getkin/kin-openapi/openapi3"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestD1ControlContract(t *testing.T) {
	raw, err := os.ReadFile("docs/obs-capture/direct-signal-v1/rc2/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Schema string
		Valid        bool
		Value        any
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"openapi.yaml", "docs/obs-capture/direct-signal-v1/rc2/openapi.snapshot.yaml"} {
		schemas := map[string]*jsonschema.Schema{}
		for _, f := range fixtures {
			if schemas[f.Schema] == nil {
				schemas[f.Schema] = compileCandidateSchema(t, path, f.Schema)
			}
			t.Run(path+"/"+f.Name, func(t *testing.T) {
				err := schemas[f.Schema].Validate(f.Value)
				if (err == nil) != f.Valid {
					t.Fatalf("valid=%v: %v", f.Valid, err)
				}
			})
		}
	}
	end, start := 3, 0
	value := recordings.BroadcastControlD1{
		BroadcastControl: recordings.BroadcastControl{Revision: 4, Phase: "end_pending"},
		ControlVersion:   recordings.BroadcastControlVersion,
		RecordingID:      "11111111-1111-4111-8111-111111111111", CaptureID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Epoch: 1,
		Boundaries: recordings.BroadcastBoundaries{StartSequence: &start, EndSequenceExclusive: &end, Revision: 3},
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = compileCandidateSchema(t, "openapi.yaml", "BroadcastControlD1").Validate(decoded); err != nil {
		t.Fatal(err)
	}
}

func TestD1ControlSnapshotMatchesCanonical(t *testing.T) {
	snapshot, err := openapi3.NewLoader().LoadFromFile("docs/obs-capture/direct-signal-v1/rc2/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshot.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	canonical, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Info.Version != recordings.BroadcastControlVersion || len(snapshot.Paths.Map()) != 2 {
		t.Fatal("D1 version or route drift")
	}
	for name, schema := range snapshot.Components.Schemas {
		got, _ := json.Marshal(schema)
		want, _ := json.Marshal(canonical.Components.Schemas[name])
		if !bytes.Equal(got, want) {
			t.Fatalf("schema drift: %s", name)
		}
	}
	for path, operation := range snapshot.Paths.Map() {
		frozenD1ControlOperation(t, canonical.Paths.Value(path).Get)
		got, _ := json.Marshal(operation)
		want, _ := json.Marshal(canonical.Paths.Value(path))
		if !bytes.Equal(got, want) {
			t.Fatalf("route drift: %s", path)
		}
	}
}

func legacyControlOperation(t *testing.T, op *openapi3.Operation) {
	t.Helper()
	frozenD1ControlOperation(t, op)
	// D1 is an explicit opt-in; the unversioned K3 operation must still match exactly.
	params := openapi3.Parameters{}
	versions := 0
	for _, param := range op.Parameters {
		if param.Value.Name == "controlVersion" {
			versions++
			if param.Value.In != "query" || param.Value.Required || len(param.Value.Schema.Value.Enum) != 1 || param.Value.Schema.Value.Enum[0] != "d1-2026-10-17.rc2" {
				t.Fatal("invalid D1 negotiation")
			}
			continue
		}
		params = append(params, param)
	}
	if versions != 1 {
		t.Fatal("missing D1 negotiation")
	}
	op.Parameters = params
	media := op.Responses.Value("200").Value.Content["application/json"]
	variants := media.Schema.Value.OneOf
	if len(variants) != 2 || variants[0].Ref != "#/components/schemas/BroadcastControlEnvelope" || variants[1].Ref != "#/components/schemas/BroadcastControlD1Envelope" {
		t.Fatal("invalid D1 response variants")
	}
	media.Schema = variants[0]
}
