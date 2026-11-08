package onlinebulletins

import "encoding/json"

// Embedded names are evidence for this page's alias, never a global CID alias.
// Unreadable or conflicting names retain the original unknown-font review.
func restoreEmbeddedFontNames(document *GeometryDocument, data []byte) error {
	var pages []map[string][]string
	if len(data) > 64<<10 || json.Unmarshal(data, &pages) != nil || len(pages) != len(document.Pages) {
		return ErrInvalidGeometry
	}
	for index, fonts := range pages {
		if len(fonts) > 256 {
			return ErrGeometryLimits
		}
		for alias, names := range fonts {
			if len(alias) > 128 || len(names) > 64 {
				return ErrGeometryLimits
			}
			family, role, conflict := "", "", false
			for _, name := range names {
				if len(name) > 512 {
					return ErrGeometryLimits
				}
				candidate, known := sourceFontRole(name)
				if isWingdingsFont(name) {
					candidate, known = "symbol", true
				}
				if !known {
					continue
				}
				if role != "" && role != candidate {
					conflict = true
				}
				family, role = name, candidate
			}
			if family == "" || conflict {
				continue
			}
			for n := range document.Pages[index].Texts {
				text := &document.Pages[index].Texts[n]
				if text.FontFamily == alias {
					if _, known := sourceFontRole(alias); !known && !isWingdingsFont(alias) {
						text.FontFamily = family
					}
				}
			}
		}
	}
	return nil
}
