package hhcwebapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const replayContractDir = "docs/obs-capture/output-replay-v1"

func TestOutputReplayCanonicalFixturesAndFrozenHashes(t *testing.T) {
	var manifest struct {
		Version         string
		Files           map[string]string
		FrozenContracts map[string]string
	}
	raw, err := os.ReadFile(replayContractDir + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "k5-2026-10-20.rc1" {
		t.Fatal("K5 protocol drift")
	}
	for _, files := range []map[string]string{manifest.Files, manifest.FrozenContracts} {
		for path, want := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != want {
				t.Fatalf("contract hash drift: %s", path)
			}
		}
	}
	approved := map[string]string{"schema.json": "38ab8c99e1204c29153b35fbbb2d05264c4db80d22f60d97b3cbb2efae4a0b67", "wire-fixtures.json": "77a9647492406bcc538e7a2b2815ca2b652d2000280d3d03b8744ea0c50ec27f", "server-rules.json": "e7a57f6ec6658d231b4967f642aad814fe58da758a932fa84aca29f4451749fd", "semantic-fixtures.json": "6602649e794885c58c126d52becc4a031a651b91a96e972139bcd1e4a0211b31"}
	for name, hash := range approved {
		if manifest.Files[filepath.Join(replayContractDir, name)] != hash {
			t.Fatalf("unreviewed canonical contract %s", name)
		}
	}
	schemaRaw, err := os.ReadFile(replayContractDir + "/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err = json.Unmarshal(schemaRaw, &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("replay.json", doc); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(replayContractDir + "/wire-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Fixtures []struct {
			Name, Schema string
			Valid        bool
			Value        any
		}
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	canonical := loadOpenAPI(t)
	snapshot, err := openapi3.NewLoader().LoadFromFile(replayContractDir + "/openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshot.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := canonical.Paths.Value("/ingest/streams/replay")
	if path == nil || path.Post == nil || len(path.Operations()) != 1 {
		t.Fatal("K5 must have one explicit replay-only POST route")
	}
	if snapshot.Info.Version != manifest.Version || len(snapshot.Paths.Map()) != 1 {
		t.Fatal("K5 snapshot scope drift")
	}
	for _, f := range fixtures.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			s, err := compiler.Compile("replay.json#/$defs/" + f.Schema)
			if err != nil {
				t.Fatal(err)
			}
			if (s.Validate(f.Value) == nil) != f.Valid {
				t.Fatalf("preview fixture validity differs: %s", f.Name)
			}
			for _, file := range []string{"openapi.yaml", replayContractDir + "/openapi.snapshot.yaml"} {
				if (compileCandidateSchema(t, file, f.Schema).Validate(f.Value) == nil) != f.Valid {
					t.Fatalf("runtime/snapshot wire drift in %s", file)
				}
			}
		})
	}
}
