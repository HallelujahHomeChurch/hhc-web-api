package bulletinwatermark

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCandidateSetAndInvestigationSerialization(t *testing.T) {
	a, b := Receipt{ID: "a"}, Receipt{ID: "b"}
	if CandidateSetHash([]Receipt{a, b}) != CandidateSetHash([]Receipt{b, a}) || CandidateSetHash([]Receipt{a, b}) == CandidateSetHash([]Receipt{a}) {
		t.Fatal("candidate set binding")
	}
	value := InvestigationView{Investigation: Investigation{Evidence: []byte("private-evidence"), ActorID: "private-actor", LeaseToken: "private-lease", ReceiptID: "private-receipt"}, Receipt: &Receipt{UserID: "private-subject", FingerprintPayload: "private-payload"}}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("internal investigation fields escaped JSON")
	}
}
