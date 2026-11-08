package bulletinwatermark

import "sort"

func AggregateFindings(findings []Finding) InvestigationCompletion {
	strong := map[string]Finding{}
	failures, inconclusive := []string{}, []string{}
	priority := map[MatchMethod]int{MatchOutputSHA256: 1, MatchPayload: 2, MatchCandidate: 3}
	for _, finding := range findings {
		switch finding.Kind {
		case FindingExact, FindingDecoded, FindingCandidate:
			valid := finding.ReceiptID != "" && finding.Proof != "" &&
				(finding.Kind != FindingExact || finding.Method == MatchOutputSHA256) &&
				(finding.Kind != FindingDecoded || finding.Method == MatchPayload) &&
				(finding.Kind != FindingCandidate || finding.Method == MatchCandidate)
			if !valid {
				return InvestigationCompletion{Outcome: "failed", ReasonCode: "processing_failed"}
			}
			current, ok := strong[finding.ReceiptID]
			if !ok || priority[finding.Method] < priority[current.Method] || priority[finding.Method] == priority[current.Method] && finding.Proof < current.Proof {
				strong[finding.ReceiptID] = finding
			}
		case FindingFailure:
			if finding.Reason != "" {
				failures = append(failures, finding.Reason)
			}
		case FindingInconclusive:
			if finding.Reason != "" {
				inconclusive = append(inconclusive, finding.Reason)
			}
		case FindingScopeHint:
		default:
			return InvestigationCompletion{Outcome: "failed", ReasonCode: "processing_failed"}
		}
	}
	if len(strong) > 1 {
		return InvestigationCompletion{Outcome: "conflict", ReasonCode: "evidence_conflict"}
	}
	for _, finding := range strong {
		result := InvestigationCompletion{Outcome: "matched", ReceiptID: finding.ReceiptID, MatchMethod: finding.Method, Proof: finding.Proof}
		if finding.Method == MatchCandidate {
			result.CandidateSetHash = finding.Proof
		}
		return result
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return InvestigationCompletion{Outcome: "failed", ReasonCode: failures[0]}
	}
	if len(inconclusive) > 0 {
		sort.Strings(inconclusive)
		return InvestigationCompletion{Outcome: "inconclusive", ReasonCode: inconclusive[0]}
	}
	return InvestigationCompletion{Outcome: "inconclusive", ReasonCode: "watermark_inconclusive"}
}
