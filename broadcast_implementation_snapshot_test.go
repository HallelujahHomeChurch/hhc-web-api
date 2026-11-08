package hhcwebapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestBroadcastImplementationSnapshot(t *testing.T) {
	canonical := loadOpenAPI(t)
	frozenPreviewInput(t, canonical)
	snapshot, err := openapi3.NewLoader().LoadFromFile("docs/obs-capture/live-control-v1/rc2/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot.Paths.Len() != 20 {
		t.Fatalf("expected 20 B1 paths, got %d", snapshot.Paths.Len())
	}
	legacyRecordingBounds(t, canonical, snapshot)
	equal := func(name string, actual, expected any) {
		t.Helper()
		a, err := json.Marshal(actual)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		// Member discovery gained optional search; the immutable ingest snapshot stays unchanged.
		if name == "/member/broadcasts" {
			var current map[string]any
			if err := json.Unmarshal(b, &current); err != nil {
				t.Fatal(err)
			}
			get := current["get"].(map[string]any)
			parameters := get["parameters"].([]any)
			kept := []any{}
			for _, parameter := range parameters {
				if parameter.(map[string]any)["name"] == "cursor" {
					parameter.(map[string]any)["schema"] = map[string]any{"type": "string", "format": "uuid"}
				}
				if parameter.(map[string]any)["name"] != "q" {
					kept = append(kept, parameter)
				}
			}
			get["parameters"] = kept
			b, _ = json.Marshal(current)
		}
		if name == "MemberLiveRecording" {
			var current map[string]any
			if err := json.Unmarshal(b, &current); err != nil {
				t.Fatal(err)
			}
			delete(current["properties"].(map[string]any), "startedAt")
			b, _ = json.Marshal(current)
		}
		var left, right any
		if err := json.Unmarshal(a, &left); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &right); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("implementation snapshot drift: %s", name)
		}
	}
	for path, value := range snapshot.Paths.Map() {
		if path == "/admin/broadcasts/{recordingId}/control" {
			legacyControlOperation(t, canonical.Paths.Value(path).Get)
		}
		equal(path, value, canonical.Paths.Value(path))
	}
	for name, value := range snapshot.Components.Schemas {
		equal(name, value, legacyPublicIDSchema(t, name, legacyCoverModeSchema(t, name, canonical.Components.Schemas[name])))
	}
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	raw, err := os.ReadFile("docs/obs-capture/live-control-v1/rc2/implementation-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) < 4 {
		t.Fatal("incomplete snapshot manifest")
	}
	for path, expected := range manifest.Files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != expected {
			t.Fatalf("immutable snapshot hash drift: %s", path)
		}
	}
}

func TestBroadcastImplementationFixtures(t *testing.T) {
	raw, err := os.ReadFile("docs/obs-capture/live-control-v1/rc2/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Schema string
		Valid        bool
		Value        any
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			schema := compileCandidateSchema(t, "docs/obs-capture/live-control-v1/rc2/openapi.snapshot.yaml", tc.Schema)
			if err := schema.Validate(tc.Value); (err == nil) != tc.Valid {
				t.Fatalf("valid=%v: %v", tc.Valid, err)
			}
		})
	}
}
