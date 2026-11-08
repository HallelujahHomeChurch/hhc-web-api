package onlinebulletins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestTemplateAssetsVerifyPinnedManifestAndEveryByte(t *testing.T) {
	files := map[string][]byte{}
	var entries []templateAssetEntry
	for _, spec := range []struct {
		name, kind, mime, family string
		roles                    []string
	}{
		{"body", "font", "font/woff2", "HHC Weekly Serif", []string{"body", "reference", "foreignText"}},
		{"scripture", "font", "font/woff2", "HHC Weekly Kai", []string{"scripture"}},
		{"emphasis", "font", "font/woff2", "HHC Weekly Kai", []string{"emphasis"}},
		{"symbol", "font", "font/woff2", "HHC Weekly Symbols", []string{"symbol"}},
		{"logo", "decoration", "image/png", "", nil},
		{"license", "license", "text/plain", "", nil},
	} {
		data := []byte("wOF2" + spec.name)
		ext := ".woff2"
		if spec.kind == "decoration" {
			data = []byte("\x89PNG\r\n\x1a\n" + spec.name)
			ext = ".png"
		}
		if spec.kind == "license" {
			data = []byte("SIL Open Font License")
			ext = ".txt"
		}
		hash := sha256.Sum256(data)
		checksum := hex.EncodeToString(hash[:])
		path := "/assets/weekly/v1/" + spec.name + "-" + checksum + ext
		files[path] = data
		entries = append(entries, templateAssetEntry{URL: path, SHA256: checksum, MIME: spec.mime, SizeBytes: len(data), Kind: spec.kind, Family: spec.family, Weight: 400, Roles: spec.roles})
	}
	manifest, _ := json.Marshal(templateAssetManifest{Version: "v1", Assets: entries})
	hash := sha256.Sum256(manifest)
	manifestHash := hex.EncodeToString(hash[:])
	files[templateManifestPath] = manifest
	changed, redirect := "", false
	reads := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads[r.URL.Path]++
		if redirect {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		data, exists := files[r.URL.Path]
		if !exists {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == changed {
			data = []byte("replaced")
		}
		if r.URL.Path == templateManifestPath {
			w.Header().Set("Content-Type", "application/json")
		} else {
			for _, entry := range entries {
				if entry.URL == r.URL.Path {
					w.Header().Set("Content-Type", entry.MIME)
				}
			}
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	assets, err := LoadTemplateV1Assets(context.Background(), server.Client(), server.URL, manifestHash)
	if err != nil || len(assets) != 5 || len(reads) != 7 {
		t.Fatalf("verified assets: %v", err)
	}
	changed = entries[0].URL
	if _, err := LoadTemplateV1Assets(context.Background(), server.Client(), server.URL, manifestHash); err == nil {
		t.Fatal("changed asset accepted")
	}
	changed = ""
	if _, err := LoadTemplateV1Assets(context.Background(), server.Client(), server.URL, strings.Repeat("0", 64)); err == nil {
		t.Fatal("changed manifest accepted")
	}
	redirect = true
	reads = map[string]int{}
	if _, err := LoadTemplateV1Assets(context.Background(), server.Client(), server.URL, manifestHash); err == nil || reads["/elsewhere"] != 0 {
		t.Fatal("asset redirect followed")
	}
	for _, origin := range []string{"http://example.test", server.URL + "/path", server.URL + "?query=1", "https://user:password@example.test"} {
		if _, err := LoadTemplateV1Assets(context.Background(), server.Client(), origin, manifestHash); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
}

// Opt-in cross-repository check uses the exact approved public asset bytes,
// without copying fonts or source documents into the API repository.
func TestTemplateAssetsApprovedLocalManifest(t *testing.T) {
	directory := os.Getenv("HHC_TEMPLATE_ASSET_DIRECTORY")
	if directory == "" {
		t.Skip("approved template assets not supplied")
	}
	approvedLocalTemplateAssets(t, directory)
}

func approvedLocalTemplateAssets(t *testing.T, directory string) []Asset {
	t.Helper()
	data, err := os.ReadFile(directory + "/manifest.json")
	if err != nil {
		t.Fatal("approved manifest missing")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != "cc0857da9e27a8fc9ba284815e668842b8d3b90518eece83ce7e42dbee5d2e53" {
		t.Fatal("local template manifest differs from reviewed asset checkpoint")
	}
	server := httptest.NewTLSServer(http.StripPrefix("/assets/weekly/v1/", http.FileServer(http.Dir(directory))))
	defer server.Close()
	assets, err := LoadTemplateV1Assets(context.Background(), server.Client(), server.URL, hex.EncodeToString(digest[:]))
	if err != nil || len(assets) != 8 {
		t.Fatalf("approved template verification failed: %v", err)
	}
	return assets
}
