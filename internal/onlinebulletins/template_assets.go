package onlinebulletins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const templateManifestPath = "/assets/weekly/v1/manifest.json"

var (
	ErrTemplateAssets = errors.New("template_assets_unavailable")
	templateAssetPath = regexp.MustCompile(`^/assets/weekly/v1/[a-zA-Z0-9_-]*[0-9a-f]{64}\.(woff2|png|svg|webp|txt)$`)
)

type templateAssetManifest struct {
	Version string               `json:"version"`
	Assets  []templateAssetEntry `json:"assets"`
}
type templateAssetEntry struct {
	URL          string   `json:"url"`
	SHA256       string   `json:"sha256"`
	MIME         string   `json:"mime"`
	SizeBytes    int      `json:"sizeBytes"`
	Kind         string   `json:"kind"`
	Family       string   `json:"family,omitempty"`
	Weight       int      `json:"weight,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	LicenseURL   string   `json:"licenseUrl,omitempty"`
	SourceURL    string   `json:"sourceUrl,omitempty"`
	SourceSHA256 string   `json:"sourceSha256,omitempty"`
}

// LoadTemplateV1Assets verifies the build-pinned manifest and all referenced
// bytes. Origins and manifest hashes come from trusted job configuration, never
// document content. Source/license URLs in the manifest are not navigation URLs.
func LoadTemplateV1Assets(parent context.Context, client *http.Client, origin, manifestSHA256 string) ([]Asset, error) {
	base, err := url.Parse(origin)
	if err != nil || client == nil || base.Scheme != "https" || base.Host == "" || base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" || !hashPattern.MatchString(manifestSHA256) {
		return nil, ErrTemplateAssets
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	boundedClient := *client
	boundedClient.Jar = nil
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	fetch := func(path, expectedMIME, checksum string, limit int64) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.Scheme+"://"+base.Host+path, nil)
		if err != nil {
			return nil, ErrTemplateAssets
		}
		response, err := boundedClient.Do(request)
		if err != nil {
			return nil, ErrTemplateAssets
		}
		defer response.Body.Close()
		contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if response.StatusCode != http.StatusOK || err != nil || contentType != expectedMIME || response.ContentLength > limit {
			return nil, ErrTemplateAssets
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err != nil || int64(len(data)) > limit {
			return nil, ErrTemplateAssets
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != checksum {
			return nil, ErrTemplateAssets
		}
		return data, nil
	}
	data, err := fetch(templateManifestPath, "application/json", manifestSHA256, 64<<10)
	if err != nil {
		return nil, err
	}
	var manifest templateAssetManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF || manifest.Version != "v1" || len(manifest.Assets) == 0 || len(manifest.Assets) > 64 {
		return nil, ErrTemplateAssets
	}
	assets := []Asset{}
	paths, roles := map[string]bool{}, map[string]bool{}
	var total int64
	for _, entry := range manifest.Assets {
		if !templateAssetPath.MatchString(entry.URL) || !hashPattern.MatchString(entry.SHA256) || !strings.Contains(entry.URL, entry.SHA256+".") || paths[entry.URL] || entry.SizeBytes < 1 || entry.SizeBytes > 12<<20 {
			return nil, ErrTemplateAssets
		}
		paths[entry.URL] = true
		total += int64(entry.SizeBytes)
		if total > 32<<20 {
			return nil, ErrTemplateAssets
		}
		asset := Asset{URL: entry.URL, SHA256: entry.SHA256, Kind: entry.Kind}
		switch entry.Kind {
		case "font":
			if entry.MIME != "font/woff2" || !strings.HasSuffix(entry.URL, ".woff2") || len(entry.Roles) == 0 {
				return nil, ErrTemplateAssets
			}
			for _, role := range entry.Roles {
				if !fontRole(role) || roles[role] {
					return nil, ErrTemplateAssets
				}
				roles[role] = true
			}
			asset.FontRole = entry.Roles[0]
		case "decoration":
			extension := map[string]string{"image/png": ".png", "image/svg+xml": ".svg", "image/webp": ".webp"}[entry.MIME]
			if extension == "" || !strings.HasSuffix(entry.URL, extension) || len(entry.Roles) != 0 {
				return nil, ErrTemplateAssets
			}
		case "license":
			if entry.MIME != "text/plain" || !strings.HasSuffix(entry.URL, ".txt") || len(entry.Roles) != 0 {
				return nil, ErrTemplateAssets
			}
		default:
			return nil, ErrTemplateAssets
		}
		content, err := fetch(entry.URL, entry.MIME, entry.SHA256, int64(entry.SizeBytes))
		if err != nil || len(content) != entry.SizeBytes {
			return nil, ErrTemplateAssets
		}
		if entry.Kind == "font" && !bytes.HasPrefix(content, []byte("wOF2")) {
			return nil, ErrTemplateAssets
		}
		if entry.Kind != "license" {
			assets = append(assets, asset)
		}
	}
	for _, role := range []string{"body", "scripture", "emphasis", "reference", "foreignText", "symbol"} {
		if !roles[role] {
			return nil, ErrTemplateAssets
		}
	}
	for _, entry := range manifest.Assets {
		if entry.Kind == "font" && entry.LicenseURL != "" && !paths[entry.LicenseURL] {
			return nil, ErrTemplateAssets
		}
	}
	return assets, nil
}
