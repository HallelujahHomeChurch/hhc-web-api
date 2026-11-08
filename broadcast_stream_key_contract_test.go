package hhcwebapi

import (
	"context"
	"encoding/json"
	"github.com/getkin/kin-openapi/openapi3"
	"os"
	"testing"
)

func TestBroadcastStreamKeyContract(t *testing.T) {
	raw, err := os.ReadFile("docs/obs-capture/stream-key-v1/wire-fixtures.json")
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
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			err := compileCandidateSchema(t, "openapi.yaml", c.Schema).Validate(c.Value)
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v err=%v", c.Valid, err)
			}
		})
	}
	doc := loadOpenAPI(t)
	for path, methods := range map[string][]string{"/admin/broadcasts/{recordingId}/stream-key": {"GET", "PUT"}, "/admin/broadcasts/{recordingId}/stream-key-bindings": {"POST"}} {
		p := doc.Paths.Value(path)
		if p == nil {
			t.Fatal(path)
		}
		for _, m := range methods {
			if p.GetOperation(m) == nil {
				t.Fatalf("missing %s %s", m, path)
			}
		}
	}
}

func TestBroadcastStreamKeySnapshotMatchesCanonical(t *testing.T) {
	canonical := loadOpenAPI(t)
	snapshot, err := openapi3.NewLoader().LoadFromFile("docs/obs-capture/stream-key-v1/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshot.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, schema := range snapshot.Components.Schemas {
		a, _ := json.Marshal(schema)
		b, _ := json.Marshal(canonical.Components.Schemas[name])
		if string(a) != string(b) {
			t.Fatalf("K1 schema drift: %s", name)
		}
	}
	for path, item := range snapshot.Paths.Map() {
		a, _ := json.Marshal(item)
		b, _ := json.Marshal(canonical.Paths.Value(path))
		if string(a) != string(b) {
			t.Fatalf("K1 path drift: %s", path)
		}
	}
}
