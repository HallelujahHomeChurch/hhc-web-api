package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type layoutSubmission struct {
	Document          Document          `json:"document"`
	CanonicalMetadata CanonicalMetadata `json:"canonicalMetadata"`
}

// MarshalLayoutSubmission preserves the ordered bytes used by stored content
// hashes. Callers own normalization, validation and clearing layout proofs.
func MarshalLayoutSubmission(document Document, metadata CanonicalMetadata) (json.RawMessage, string, error) {
	raw, err := json.Marshal(layoutSubmission{document, metadata})
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(raw)
	return raw, hex.EncodeToString(digest[:]), nil
}
