package hhcwebapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const outputContractDir = "docs/obs-capture/studio-v2"

func TestBroadcastOutputContract(t *testing.T) {
	path := outputContractDir + "/openapi.snapshot.yaml"
	doc, err := openapi3.NewLoader().LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if doc.Info.Version != "k4-2026-10-15.rc1" {
		t.Fatal("K4 version drift")
	}
	if len(doc.Paths.Map()) != 1 || doc.Paths.Value("/ingest/streams/open").Post == nil {
		t.Fatal("expected only K4 open")
	}
	if _, exists := doc.Paths.Value("/ingest/streams/open").Post.Responses.Map()["202"]; exists {
		t.Fatal("K4 must not expose ambiguous null binding")
	}
	raw, err := os.ReadFile(outputContractDir + "/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Version  string `json:"version"`
		Fixtures []struct {
			Name, Schema string
			Valid        bool
			Value        any
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if fixtures.Version != doc.Info.Version || len(fixtures.Fixtures) < 20 {
		t.Fatal("incomplete K4 fixtures")
	}
	for _, f := range fixtures.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			err := compileCandidateSchema(t, path, f.Schema).Validate(f.Value)
			if (err == nil) != f.Valid {
				t.Fatalf("valid=%v error=%v", f.Valid, err)
			}
		})
	}
	// Shared schemas are references to frozen wire, not permission to change it.
	old, err := openapi3.NewLoader().LoadFromFile("docs/obs-capture/key-only-ingest-v1/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, schema := range doc.Components.Schemas {
		if strings.HasPrefix(name, "BroadcastOutput") {
			continue
		}
		a, _ := json.Marshal(schema)
		b, _ := json.Marshal(old.Components.Schemas[name])
		if string(a) != string(b) {
			t.Fatalf("shared schema changed: %s", name)
		}
	}
}

func TestBroadcastOutputArtifactDigests(t *testing.T) {
	raw, err := os.ReadFile(outputContractDir + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
		Frozen  map[string]string `json:"frozenContracts"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "k4-2026-10-15.rc1" || len(manifest.Files) < 4 || len(manifest.Frozen) < 6 {
		t.Fatal("incomplete manifest")
	}
	for label, files := range map[string]map[string]string{"files": manifest.Files, "frozen": manifest.Frozen} {
		for path, want := range files {
			if filepath.IsAbs(path) || strings.Contains(path, "..") {
				t.Fatal("non-relative artifact")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			if hex.EncodeToString(digest[:]) != want {
				t.Fatalf("%s digest drift: %s", label, path)
			}
		}
	}
}

// The same negative cases must be rejected by the future HTTP input DTO, not
// just by a schema validator used only in CI.
func TestBroadcastOutputInputMatchesFrozenFixtures(t *testing.T) {
	raw, err := os.ReadFile(outputContractDir + "/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Fixtures []struct {
			Name, Schema string
			Valid        bool
			Value        json.RawMessage
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases.Fixtures {
		if c.Schema != "BroadcastOutputOpenInput" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			var in recordings.BroadcastOutputOpenInput
			decoder := json.NewDecoder(bytes.NewReader(c.Value))
			decoder.DisallowUnknownFields()
			err := decoder.Decode(&in)
			if err == nil {
				err = in.Validate()
			}
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v err=%v", c.Valid, err)
			}
		})
	}
}

func TestBroadcastOutputIdentityFixtures(t *testing.T) {
	raw, err := os.ReadFile(outputContractDir + "/identity-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Fixtures []struct {
			Name          string
			Expected      map[string]any
			ValidIdentity bool
			Value         map[string]any
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.Fixtures) != 7 {
		t.Fatal("missing correlation cases")
	}
	schema := compileCandidateSchema(t, outputContractDir+"/openapi.snapshot.yaml", "BroadcastOutputOpenedEnvelope")
	for _, c := range cases.Fixtures {
		t.Run(c.Name, func(t *testing.T) {
			if err := schema.Validate(c.Value); err != nil {
				t.Fatal("identity cases must be schema-valid:", err)
			}
			d := c.Value["data"].(map[string]any)
			b := d["broadcast"].(map[string]any)
			s := d["session"].(map[string]any)
			r := d["receipt"].(map[string]any)
			binding := b["binding"].(map[string]any)
			got := map[string]any{"protocolVersion": d["protocolVersion"], "outputSessionId": d["outputSessionId"], "sourceId": d["sourceId"], "recordingId": s["recordingId"], "captureId": s["captureId"], "epoch": s["epoch"], "operationKey": r["operationKey"]}
			valid := reflect.DeepEqual(got, c.Expected) && s["recordingId"] == b["recordingId"] && s["captureId"] == binding["captureId"] && s["epoch"] == binding["epoch"]
			if valid != c.ValidIdentity {
				t.Fatal("fixture expectation does not match identity correlations")
			}
		})
	}
}

func TestBroadcastOutputRuntimeSchemasMatchFrozenContract(t *testing.T) {
	canonical := loadOpenAPI(t)
	frozenS10OutputInput(t, canonical)
	supplement, err := openapi3.NewLoader().LoadFromFile("docs/obs-capture/ten-second-v1/cms.openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := openapi3.NewLoader().LoadFromFile(outputContractDir + "/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"BroadcastOutputOpenInput", "BroadcastOutputOpened", "BroadcastOutputOpenedEnvelope"} {
		got, err := json.Marshal(canonical.Components.Schemas[name])
		if err != nil {
			t.Fatal(err)
		}
		fixed, err := json.Marshal(supplement.Components.Schemas[name])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, fixed) {
			t.Fatalf("S10 runtime schema drift: %s", name)
		}
		// S10 adds only an optional profile; all frozen K4 fields stay identical.
		if name != "BroadcastOutputOpenedEnvelope" {
			var schema map[string]any
			if err := json.Unmarshal(got, &schema); err != nil {
				t.Fatal(err)
			}
			properties := schema["properties"].(map[string]any)
			if properties["mediaProfile"] == nil {
				t.Fatal("missing S10 profile")
			}
			delete(properties, "mediaProfile")
			got, err = json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
		}
		want, err := json.Marshal(snapshot.Components.Schemas[name])
		if err != nil {
			t.Fatal(err)
		}
		var actual, frozen any
		if err := json.Unmarshal(got, &actual); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(want, &frozen); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, frozen) {
			t.Fatalf("K4 runtime schema drift: %s", name)
		}
	}
	if canonical.Paths.Value("/ingest/streams/open").Post.Responses.Value("202") != nil {
		t.Fatal("K4 exposed partial binding")
	}
}
