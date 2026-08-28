package contentseed

import (
	"strings"
	"testing"

	contentmanifest "github.com/HallelujahHomeChurch/hhc-web-api/seeds/content"
)

const validManifestJSON = `{"schemaVersion":1,"seedVersion":"v1","sourceRepo":"repo","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","locales":["zh-Hant","zh-Hans","en","ja","ko"],"sources":[{"path":"source.json","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}],"records":[]}`

func TestLoadHashesOriginalManifestBytes(t *testing.T) {
	payload := []byte(validManifestJSON + "\n")
	manifest, got, err := Load(payload)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SeedVersion != "v1" {
		t.Fatalf("seed version = %q", manifest.SeedVersion)
	}
	const want = "674d60cd5640d2046509e5bcf5754bdadb231e6f362cb77506ba62ed9e48a4e5"
	if got != want {
		t.Fatalf("manifest hash = %q, want %q", got, want)
	}
}

func TestLoadRejectsInvalidManifest(t *testing.T) {
	record := `{"kind":"location","sourcePaths":["source.json"],"sourceKey":"one","payload":{}}`
	pageRecord := strings.Replace(record, `"kind":"location"`, `"kind":"page"`, 1)
	withRecord := strings.Replace(validManifestJSON, `"records":[]`, `"records":[`+record+`]`, 1)
	tests := []struct {
		name    string
		payload string
	}{
		{name: "unsupported_schema_version", payload: strings.Replace(validManifestJSON, `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{name: "unsupported_locale", payload: strings.Replace(validManifestJSON, `"ko"]`, `"fr"]`, 1)},
		{name: "locale_order", payload: strings.Replace(validManifestJSON, `"zh-Hant","zh-Hans"`, `"zh-Hans","zh-Hant"`, 1)},
		{name: "uppercase_source_commit", payload: strings.Replace(validManifestJSON, strings.Repeat("a", 40), strings.Repeat("A", 40), 1)},
		{name: "short_source_commit", payload: strings.Replace(validManifestJSON, strings.Repeat("a", 40), strings.Repeat("a", 39), 1)},
		{name: "uppercase_source_hash", payload: strings.Replace(validManifestJSON, strings.Repeat("b", 64), strings.Repeat("B", 64), 1)},
		{name: "short_source_hash", payload: strings.Replace(validManifestJSON, strings.Repeat("b", 64), strings.Repeat("b", 63), 1)},
		{name: "unknown_field", payload: strings.Replace(validManifestJSON, `"schemaVersion":1`, `"schemaVersion":1,"extra":true`, 1)},
		{name: "unknown_kind", payload: strings.Replace(withRecord, `"kind":"location"`, `"kind":"other"`, 1)},
		{name: "empty_source_path", payload: strings.Replace(validManifestJSON, `"path":"source.json"`, `"path":""`, 1)},
		{name: "empty_source_key", payload: strings.Replace(withRecord, `"sourceKey":"one"`, `"sourceKey":""`, 1)},
		{name: "duplicate_source_path", payload: strings.Replace(validManifestJSON, `"sources":[`, `"sources":[{"path":"source.json","sha256":"`+strings.Repeat("c", 64)+`"},`, 1)},
		{name: "duplicate_record_key", payload: strings.Replace(withRecord, `"records":[`+record+`]`, `"records":[`+record+`,`+record+`]`, 1)},
		{name: "duplicate_cross_kind_source_tuple", payload: strings.Replace(withRecord, `"records":[`+record+`]`, `"records":[`+record+`,`+pageRecord+`]`, 1)},
		{name: "duplicate_record_source_path", payload: strings.Replace(withRecord, `"sourcePaths":["source.json"]`, `"sourcePaths":["source.json","source.json"]`, 1)},
		{name: "missing_record_source", payload: strings.Replace(withRecord, `"sourcePaths":["source.json"]`, `"sourcePaths":["missing.json"]`, 1)},
		{name: "empty_record_sources", payload: strings.Replace(withRecord, `"sourcePaths":["source.json"]`, `"sourcePaths":[]`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := Load([]byte(test.payload)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadRejectsTrailingJSON(t *testing.T) {
	if _, _, err := Load([]byte(validManifestJSON + `{}`)); err == nil {
		t.Fatal("expected trailing JSON error")
	}
}

func TestLoadRejectsUnknownNestedFields(t *testing.T) {
	record := `{"kind":"location","sourcePaths":["source.json"],"sourceKey":"one","payload":{}}`
	withRecord := strings.Replace(validManifestJSON, `"records":[]`, `"records":[`+record+`]`, 1)
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "source", payload: strings.Replace(validManifestJSON, `"path":"source.json"`, `"path":"source.json","extra":true`, 1)},
		{name: "record", payload: strings.Replace(withRecord, `"kind":"location"`, `"kind":"location","extra":true`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := Load([]byte(test.payload)); err == nil {
				t.Fatal("expected unknown nested field error")
			}
		})
	}
}

func TestEmbeddedManifestLoadsLocationRecords(t *testing.T) {
	manifest, hash, err := Load(contentmanifest.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || manifest.SeedVersion != "2026-08-28-public-content-locations-v1" || len(manifest.Records) != 2 {
		t.Fatalf("embedded manifest hash=%q version=%q records=%d", hash, manifest.SeedVersion, len(manifest.Records))
	}
	for i, sourceKey := range []string{"location:taipei", "location:zhongli"} {
		if manifest.Records[i].Kind != "location" || manifest.Records[i].SourceKey != sourceKey {
			t.Fatalf("record %d = %#v", i, manifest.Records[i])
		}
	}
}
