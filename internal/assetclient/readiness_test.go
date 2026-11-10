package assetclient

import (
	"encoding/json"
	"testing"
)

func TestAssetReadinessMixedVersionsFailClosed(t *testing.T) {
	legacy := `"uploadStatus":"completed","scanStatus":"clean","processingStatus":"not_required"`
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"legacy", "", true},
		{"v1 ready", `,"processingReadiness":{"version":1,"state":"ready"},"safetyEvidence":{"kind":"scanned"}`, true},
		{"wrong-case version", `,"ProcessingReadiness":{"version":99,"state":"ready"}`, false},
		{"wrong-case null", `,"PROCESSINGREADINESS":null`, false},
		{"wrong-case evidence", `,"SafetyEvidence":{"kind":"future"}`, false},
		{"unknown version", `,"processingReadiness":{"version":2,"state":"ready"}`, false},
		{"unknown state", `,"processingReadiness":{"version":1,"state":"future"}`, false},
		{"explicit null", `,"processingReadiness":null`, false},
		{"pending overrides clean", `,"processingReadiness":{"version":1,"state":"pending"}`, false},
		{"rejected overrides clean", `,"processingReadiness":{"version":1,"state":"rejected"}`, false},
		{"unknown evidence", `,"safetyEvidence":{"kind":"future"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a Asset
			err := json.Unmarshal([]byte("{"+legacy+tc.extra+"}"), &a)
			if (err == nil) != tc.valid {
				t.Fatalf("decode=%v", err)
			}
		})
	}
}

func TestExplicitSafetyEvidenceCannotContradictScan(t *testing.T) {
	var a Asset
	if json.Unmarshal([]byte(`{"uploadStatus":"completed","scanStatus":"infected","processingStatus":"not_required","safetyEvidence":{"kind":"scanned"}}`), &a) == nil {
		t.Fatal("contradictory safety accepted")
	}
}
