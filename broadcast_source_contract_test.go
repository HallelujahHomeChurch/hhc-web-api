package hhcwebapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/getkin/kin-openapi/openapi3"
	"os"
	"testing"
)

func TestBroadcastSourceContract(t *testing.T) {
	raw, err := os.ReadFile("docs/obs-capture/stream-source-v1/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SnapshotSHA256 string `json:"snapshotSha256"`
		Fixtures       []struct {
			Name, Schema string
			Value        any
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	snapshotRaw, err := os.ReadFile("docs/obs-capture/stream-source-v1/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(snapshotRaw)
	if hex.EncodeToString(digest[:]) != fixture.SnapshotSHA256 {
		t.Fatal("K2 snapshot digest drift")
	}
	for _, c := range fixture.Fixtures {
		t.Run(c.Name, func(t *testing.T) {
			err := compileCandidateSchema(t, "openapi.yaml", c.Schema).Validate(c.Value)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	doc := loadOpenAPI(t)
	for path, methods := range map[string][]string{"/admin/broadcast-source": {"GET"}, "/admin/broadcast-source/stream-key": {"PUT"}, "/admin/broadcast-source/target": {"PUT"}, "/admin/broadcast-source/resolve": {"POST"}, "/admin/broadcast-source/bindings": {"POST"}} {
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

func TestBroadcastSourceSnapshotMatchesCanonical(t *testing.T) {
	canonical := loadOpenAPI(t)
	snapshot, err := openapi3.NewLoader().LoadFromFile("docs/obs-capture/stream-source-v1/openapi.snapshot.yaml")
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
			t.Fatalf("K2 schema drift: %s", name)
		}
	}
	for path, item := range snapshot.Paths.Map() {
		a, _ := json.Marshal(item)
		b, _ := json.Marshal(canonical.Paths.Value(path))
		if string(a) != string(b) {
			t.Fatalf("K2 path drift: %s", path)
		}
	}
}
