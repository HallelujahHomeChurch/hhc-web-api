package contentseed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type Source struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Record struct {
	Kind        string          `json:"kind"`
	SourcePaths []string        `json:"sourcePaths"`
	SourceKey   string          `json:"sourceKey"`
	Payload     json.RawMessage `json:"payload"`
}

type Manifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	SeedVersion   string   `json:"seedVersion"`
	SourceRepo    string   `json:"sourceRepo"`
	SourceCommit  string   `json:"sourceCommit"`
	Locales       []string `json:"locales"`
	Sources       []Source `json:"sources"`
	Records       []Record `json:"records"`
}

func Load(payload []byte) (Manifest, string, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, "", err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("manifest contains multiple JSON values")
		}
		return Manifest{}, "", err
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, "", err
	}
	hash := sha256.Sum256(payload)
	return manifest, hex.EncodeToString(hash[:]), nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schemaVersion %d", manifest.SchemaVersion)
	}
	locales := [...]string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}
	if len(manifest.Locales) != len(locales) {
		return fmt.Errorf("locales must be %v", locales)
	}
	for i, locale := range locales {
		if manifest.Locales[i] != locale {
			return fmt.Errorf("locales must be %v", locales)
		}
	}
	if !lowerHex(manifest.SourceCommit, 40) {
		return errors.New("sourceCommit must be a lowercase 40-character hash")
	}
	sourceCounts := make(map[string]int, len(manifest.Sources))
	for _, source := range manifest.Sources {
		if source.Path == "" {
			return errors.New("source path must not be empty")
		}
		if !lowerHex(source.SHA256, 64) {
			return fmt.Errorf("source %q sha256 must be a lowercase 64-character hash", source.Path)
		}
		sourceCounts[source.Path]++
		if sourceCounts[source.Path] != 1 {
			return fmt.Errorf("duplicate source path %q", source.Path)
		}
	}
	recordKeys := make(map[[2]string]struct{}, len(manifest.Records))
	for _, record := range manifest.Records {
		switch record.Kind {
		case "location", "site_layout", "page":
		default:
			return fmt.Errorf("unknown record kind %q", record.Kind)
		}
		if record.SourceKey == "" {
			return errors.New("record sourceKey must not be empty")
		}
		key := [2]string{record.Kind, record.SourceKey}
		if _, exists := recordKeys[key]; exists {
			return fmt.Errorf("duplicate record key %q/%q", record.Kind, record.SourceKey)
		}
		recordKeys[key] = struct{}{}
		if len(record.SourcePaths) == 0 {
			return fmt.Errorf("record %q/%q must have a source path", record.Kind, record.SourceKey)
		}
		recordPaths := make(map[string]struct{}, len(record.SourcePaths))
		for _, path := range record.SourcePaths {
			if _, exists := recordPaths[path]; exists {
				return fmt.Errorf("record %q/%q has duplicate source path %q", record.Kind, record.SourceKey, path)
			}
			recordPaths[path] = struct{}{}
			if path == "" || sourceCounts[path] != 1 {
				return fmt.Errorf("record %q/%q source path %q must resolve exactly once", record.Kind, record.SourceKey, path)
			}
		}
	}
	return nil
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := range value {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}
