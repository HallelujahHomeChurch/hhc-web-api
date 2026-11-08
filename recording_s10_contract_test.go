package hhcwebapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/getkin/kin-openapi/openapi3"
)

const s10ContractDir = "docs/obs-capture/ten-second-v1/rc2"

func TestS10CanonicalAcceptsRuntimeBounds(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	progress := assetclient.RecordingLiveProgress{Revision: 4320, FirstSequence: 0, LastSequence: 4319, MediaEndSeconds: 43200, LastAdvancedAt: &now}
	status := recordings.CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{
		CaptureID: strings.Repeat("a", 32), RecordingID: "11111111-1111-4111-8111-111111111111", State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		DeclaredBytes: 13000, DeclaredObjects: 13000, Objects: []assetclient.RecordingCaptureObject{}, Progress: progress,
	}, RecordingVersion: 1, LiveState: "live", LiveEnabled: true, AutoPublish: "cancelled", ServerNow: now}
	inv := s10Inventory(4320)
	for name, value := range map[string]any{
		"RecordingRendition":        inv.Renditions[0],
		"RecordingPackageInventory": inv,
		"RecordingCaptureStatus":    status,
		"RecordingLiveProgress":     progress,
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var wire any
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"openapi.yaml", s10ContractDir + "/cms.openapi.snapshot.yaml", s10ContractDir + "/asset.openapi.snapshot.yaml"} {
				if name == "RecordingCaptureStatus" && strings.Contains(path, "asset.") {
					raw, _ := json.Marshal(status.RecordingCaptureStatus)
					_ = json.Unmarshal(raw, &wire)
				}
				schema := compileCandidateSchema(t, path, name)
				if err = schema.Validate(wire); err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				if name == "RecordingCaptureStatus" {
					wire.(map[string]any)["declaredObjects"] = float64(13001)
					if schema.Validate(wire) == nil {
						t.Fatal("accepted declaredObjects overflow")
					}
					wire.(map[string]any)["declaredObjects"] = float64(13000)
				}
			}
		})
	}
}

func TestS10FixturesAndInventoryCeilings(t *testing.T) {
	raw, err := os.ReadFile(s10ContractDir + "/fixtures.json")
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
	for _, path := range []string{"openapi.yaml", s10ContractDir + "/cms.openapi.snapshot.yaml", s10ContractDir + "/asset.openapi.snapshot.yaml"} {
		for _, schemaName := range []string{"RecordingRendition", "RecordingLiveProgress", "RecordingPackageInventory"} {
			schema := compileCandidateSchema(t, path, schemaName)
			for _, f := range fixtures {
				if f.Schema != schemaName {
					continue
				}
				if err := schema.Validate(f.Value); (err == nil) != f.Valid {
					t.Errorf("%s/%s valid=%t: %v", path, f.Name, f.Valid, err)
				}
			}
			if schemaName != "RecordingPackageInventory" {
				continue
			}
			for _, tc := range []struct {
				count, segments int
				valid           bool
			}{
				{12967, 4320, true}, {13000, 4320, true}, {13001, 4320, false}, {12970, 4321, false},
			} {
				inv := s10Inventory(tc.segments)
				for len(inv.Objects) < tc.count {
					inv.Objects = append(inv.Objects, inv.Objects[0])
				}
				// Padding tests only the schema array ceiling. Runtime also rejects duplicate/extra objects.
				b, _ := json.Marshal(inv)
				var wire any
				_ = json.Unmarshal(b, &wire)
				if err := schema.Validate(wire); (err == nil) != tc.valid {
					t.Errorf("%s inventory objects=%d segments=%d valid=%t: %v", path, tc.count, tc.segments, tc.valid, err)
				}
			}
		}
	}
}

func TestS10SnapshotMatchesCanonicalAndDigests(t *testing.T) {
	canonical := loadOpenAPI(t)
	frozenS10OutputInput(t, canonical)
	snapshot, err := openapi3.NewLoader().LoadFromFile(s10ContractDir + "/cms.openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshot.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	equal := func(name string, got, want any) {
		t.Helper()
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if !bytes.Equal(a, b) {
			t.Fatalf("S10 schema/route drift: %s", name)
		}
	}
	for name, value := range snapshot.Components.Schemas {
		equal(name, value, canonical.Components.Schemas[name])
	}
	for path, value := range snapshot.Paths.Map() {
		if strings.HasSuffix(path, "/control") {
			frozenD1ControlOperation(t, canonical.Paths.Value(path).Get)
		}
		equal(path, value, canonical.Paths.Value(path))
	}
	asset, err := openapi3.NewLoader().LoadFromFile(s10ContractDir + "/asset.openapi.snapshot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = asset.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"RecordingPackageInventory", "RecordingRendition", "RecordingPackageObject", "RecordingLiveProgress"} {
		equal("CMS/Asset/"+name, canonical.Components.Schemas[name], asset.Components.Schemas[name])
	}
	equal("CMS/Asset/declaredObjects", canonical.Components.Schemas["RecordingCaptureStatus"].Value.Properties["declaredObjects"], asset.Components.Schemas["RecordingCaptureStatus"].Value.Properties["declaredObjects"])
	raw, err := os.ReadFile(s10ContractDir + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files  map[string]string
		Frozen map[string]string
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, entries := range []map[string]string{manifest.Files, manifest.Frozen} {
		for path, want := range entries {
			if filepath.IsAbs(path) || strings.Contains(path, "..") {
				t.Fatal("invalid artifact path")
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) != want {
				t.Fatalf("artifact drift: %s", path)
			}
		}
	}
}

// S10 widens shared schema ceilings, not legacy media eligibility. Preserve the
// old snapshots and exclude only these separately tested, explicit supplements.
func legacyRecordingBounds(t *testing.T, canonical, frozen *openapi3.T) {
	t.Helper()
	for _, tc := range []struct {
		name, property string
		max            float64
	}{
		{"RecordingRendition", "segmentCount", 4320}, {"RecordingPackageInventory", "objects", 13000},
		{"RecordingCaptureStatus", "declaredObjects", 13000}, {"RecordingLiveProgress", "lastSequence", 4319},
	} {
		old := frozen.Components.Schemas[tc.name]
		if old == nil {
			continue
		}
		got := canonical.Components.Schemas[tc.name].Value.Properties[tc.property].Value
		want := old.Value.Properties[tc.property].Value
		if tc.property == "objects" {
			if got.MaxItems == nil || *got.MaxItems != uint64(tc.max) {
				t.Fatal("S10 object ceiling drift")
			}
			got.MaxItems = want.MaxItems
		} else {
			if got.Max == nil || *got.Max != tc.max {
				t.Fatal("S10 numeric ceiling drift")
			}
			got.Max = want.Max
		}
		if tc.name == "RecordingRendition" {
			const suffix = " The obs-hls-10s-v1 profile allows up to 4320 segments; other profiles remain limited to 1440."
			if got.Description != want.Description+suffix {
				t.Fatal("S10 profile condition drift")
			}
			got.Description = want.Description
		}
	}
}

// Complete graph metadata; schema acceptance does not assert decoded duration.
func s10Inventory(segments int) assetclient.RecordingPackageInventory {
	inv := assetclient.RecordingPackageInventory{SchemaVersion: 1, PresetVersion: "obs-hls-10s-v1", InventoryDigest: strings.Repeat("a", 64)}
	add := func(path string) {
		inv.Objects = append(inv.Objects, assetclient.RecordingPackageObject{Path: path, SizeBytes: 1, SHA256: strings.Repeat("b", 64)})
	}
	add("master.m3u8")
	for _, r := range []assetclient.RecordingRendition{
		{Name: "480p", Width: 854, Height: 480, VideoBitrate: 800000},
		{Name: "720p", Width: 1280, Height: 720, VideoBitrate: 1500000},
		{Name: "1080p", Width: 1920, Height: 1080, VideoBitrate: 3000000},
	} {
		r.FrameRate, r.AudioBitrate, r.DurationSeconds, r.SegmentCount = 30000.0/1001, 128000, 43200, segments
		inv.Renditions = append(inv.Renditions, r)
		add(r.Name + "/index.m3u8")
		add(r.Name + "/init.mp4")
		for i := range segments {
			add(fmt.Sprintf("%s/seg-%06d.m4s", r.Name, i))
		}
	}
	return inv
}
