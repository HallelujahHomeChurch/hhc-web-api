package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestBundledTemplateV2RejectsChangedOrIncompleteManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assets.json")
	raw := []byte(`[{"url":"/assets/weekly/v2/font-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.woff2","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"font","roles":["body","scripture","emphasis","reference","foreignText","symbol"]}]`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	assets, err := LoadBundledTemplateAssets(path, hex.EncodeToString(digest[:]))
	if err != nil || len(assets) != 1 || assets[0].FontRole != "body" {
		t.Fatal(assets, err)
	}
	if _, err = LoadBundledTemplateAssets(path, "bad"); err == nil {
		t.Fatal("changed manifest accepted")
	}
	if err = os.WriteFile(path, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	digest = sha256.Sum256([]byte(`[]`))
	if _, err = LoadBundledTemplateAssets(path, hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("missing fonts accepted")
	}
}
