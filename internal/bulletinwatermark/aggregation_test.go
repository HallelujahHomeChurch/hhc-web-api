package bulletinwatermark

import "testing"

func TestAggregateFindingsIsDeterministicAndNeverVotes(t *testing.T) {
	tests := []struct {
		name     string
		findings []Finding
		want     InvestigationCompletion
	}{
		{
			name: "same receipt across methods chooses strongest exact proof",
			findings: []Finding{
				{Kind: FindingCandidate, ReceiptID: "receipt-a", Method: MatchCandidate, Proof: "candidate-set"},
				{Kind: FindingDecoded, ReceiptID: "receipt-a", Method: MatchPayload, Proof: "payload"},
				{Kind: FindingExact, ReceiptID: "receipt-a", Method: MatchOutputSHA256, Proof: "sha"},
			},
			want: InvestigationCompletion{Outcome: "matched", ReceiptID: "receipt-a", MatchMethod: MatchOutputSHA256, Proof: "sha"},
		},
		{
			name: "one match plus inconclusive and failure",
			findings: []Finding{
				{Kind: FindingInconclusive, Reason: "watermark_inconclusive"},
				{Kind: FindingFailure, Reason: "decoder_unavailable"},
				{Kind: FindingDecoded, ReceiptID: "receipt-a", Method: MatchPayload, Proof: "payload"},
			},
			want: InvestigationCompletion{Outcome: "matched", ReceiptID: "receipt-a", MatchMethod: MatchPayload, Proof: "payload"},
		},
		{
			name: "different receipts conflict without majority voting",
			findings: []Finding{
				{Kind: FindingExact, ReceiptID: "receipt-a", Method: MatchOutputSHA256, Proof: "sha-a"},
				{Kind: FindingExact, ReceiptID: "receipt-a", Method: MatchOutputSHA256, Proof: "sha-a"},
				{Kind: FindingDecoded, ReceiptID: "receipt-b", Method: MatchPayload, Proof: "payload-b"},
			},
			want: InvestigationCompletion{Outcome: "conflict", ReasonCode: "evidence_conflict"},
		},
		{
			name: "scope hints never match or conflict",
			findings: []Finding{
				{Kind: FindingScopeHint, ReceiptID: "receipt-a", Reason: "scope-a"},
				{Kind: FindingScopeHint, ReceiptID: "receipt-b", Reason: "scope-b"},
			},
			want: InvestigationCompletion{Outcome: "inconclusive", ReasonCode: "watermark_inconclusive"},
		},
		{
			name: "all inconclusive uses stable reason",
			findings: []Finding{
				{Kind: FindingInconclusive, Reason: "source_scope_ambiguous"},
				{Kind: FindingInconclusive, Reason: "watermark_inconclusive"},
			},
			want: InvestigationCompletion{Outcome: "inconclusive", ReasonCode: "source_scope_ambiguous"},
		},
		{
			name:     "only failures stays failed",
			findings: []Finding{{Kind: FindingFailure, Reason: "source_unavailable"}},
			want:     InvestigationCompletion{Outcome: "failed", ReasonCode: "source_unavailable"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AggregateFindings(test.findings); got != test.want {
				t.Fatalf("got %+v want %+v", got, test.want)
			}
			reversed := append([]Finding(nil), test.findings...)
			for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
				reversed[left], reversed[right] = reversed[right], reversed[left]
			}
			if got := AggregateFindings(reversed); got != test.want {
				t.Fatalf("order changed result: got %+v want %+v", got, test.want)
			}
		})
	}
}
