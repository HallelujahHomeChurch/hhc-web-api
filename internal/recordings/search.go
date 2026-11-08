package recordings

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

func NormalizeSearch(query string) (string, error) {
	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return "", ErrInvalid
	}
	query = strings.ToLower(strings.Join(strings.Fields(query), " "))
	if utf8.RuneCountInString(query) > 100 {
		return "", ErrInvalid
	}
	return query, nil
}

func searchDigest(query string) string {
	if query == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])
}

func searchMatches(query, title, description string) bool {
	title, description = strings.ToLower(title), strings.ToLower(description)
	for _, term := range strings.Fields(query) {
		if !strings.Contains(title, term) && !strings.Contains(description, term) {
			return false
		}
	}
	return true
}
