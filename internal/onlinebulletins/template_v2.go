package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The image owns this path and hash. Font bytes are independently verified by
// the layout runner; no document-provided URL is fetched or executed here.
func LoadBundledTemplateAssets(path, expectedHash string) ([]Asset, error) {
	if !filepath.IsAbs(path) || !hashPattern.MatchString(expectedHash) {
		return nil, ErrTemplateAssets
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrTemplateAssets
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	digest := sha256.Sum256(raw)
	if err != nil || len(raw) > 64<<10 || hex.EncodeToString(digest[:]) != expectedHash {
		return nil, ErrTemplateAssets
	}
	var entries []templateAssetEntry
	if json.Unmarshal(raw, &entries) != nil || len(entries) == 0 || len(entries) > 64 {
		return nil, ErrTemplateAssets
	}
	assets := make([]Asset, 0, len(entries))
	roles, urls := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		if !assetPattern.MatchString(entry.URL) || !hashPattern.MatchString(entry.SHA256) || !strings.Contains(entry.URL, entry.SHA256+".") || urls[entry.URL] {
			return nil, ErrTemplateAssets
		}
		urls[entry.URL] = true
		asset := Asset{URL: entry.URL, SHA256: entry.SHA256, Kind: entry.Kind}
		if entry.Kind == "font" {
			if len(entry.Roles) == 0 {
				return nil, ErrTemplateAssets
			}
			asset.FontRole = entry.Roles[0]
			for _, role := range entry.Roles {
				if !fontRole(role) || roles[role] {
					return nil, ErrTemplateAssets
				}
				roles[role] = true
			}
		} else if entry.Kind != "decoration" || len(entry.Roles) != 0 {
			return nil, ErrTemplateAssets
		}
		assets = append(assets, asset)
	}
	for _, role := range []string{"body", "scripture", "emphasis", "reference", "foreignText", "symbol"} {
		if !roles[role] {
			return nil, ErrTemplateAssets
		}
	}
	return assets, nil
}
