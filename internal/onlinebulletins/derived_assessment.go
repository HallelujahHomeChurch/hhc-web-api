package onlinebulletins

import (
	"bytes"
	"encoding/json"
)

// Conversion retains the source's content ownership and IDs. Only character
// conversion and presentation differ; semantic acceptance remains independent.
func validDerivedPair(source Document, sourceMetadata CanonicalMetadata, target Document, metadata CanonicalMetadata) bool {
	if source.ContentLocale != "zh-Hant" || target.ContentLocale != "zh-Hans" || source.IssueID != target.IssueID || source.Series != target.Series || source.SourceAssetChecksum != target.SourceAssetChecksum || sourceMetadata.IssueNumber != metadata.IssueNumber || sourceMetadata.Date != metadata.Date {
		return false
	}
	for _, d := range []Document{source, target} {
		if _, err := ValidateDocument(d); err != nil {
			return false
		}
	}
	shape := func(components []Component) []byte {
		var value any
		if json.Unmarshal(comparisonJSON(components), &value) != nil {
			return nil
		}
		clearLayoutSpacing(value)
		var clear func(any)
		clear = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				if _, span := v["fontRole"]; span {
					delete(v, "text")
				}
				for _, child := range v {
					clear(child)
				}
			case []any:
				for _, child := range v {
					clear(child)
				}
			}
		}
		clear(value)
		return comparisonJSON(value)
	}
	return bytes.Equal(shape(source.Components), shape(target.Components))
}

func SimplifiedAssessmentInput(source Document, sourceMetadata CanonicalMetadata, target Document, metadata CanonicalMetadata) (UpdateInput, error) {
	if !validDerivedPair(source, sourceMetadata, target, metadata) {
		return UpdateInput{}, ErrUpdatePlan
	}
	var draft Document
	if json.Unmarshal(comparisonJSON(target), &draft) != nil {
		return UpdateInput{}, ErrUpdatePlan
	}
	draft.LayoutManifest.ContentHash, draft.LayoutManifest.LayoutValidationHash = "", ""
	input := UpdateInput{Canonical: CanonicalIssue{IssueID: target.IssueID, Series: target.Series, ContentLocale: target.ContentLocale, Metadata: metadata}, Incoming: draft, DerivedSource: &source, DerivedMetadata: &sourceMetadata, Evidence: GeometryDocument{SourceSHA256: source.SourceAssetChecksum}}
	if _, err := UpdateInputSHA256(input); err != nil {
		return UpdateInput{}, err
	}
	return input, nil
}

func CompletedUpdateEvidence(input UpdateInput, attempts []UpdateAttempt) (UpdateEvidence, error) {
	var evidence UpdateEvidence
	hash, err := UpdateInputSHA256(input)
	if err != nil || len(attempts) != 2 {
		return evidence, ErrUpdatePlan
	}
	evidence = UpdateEvidence{InputSHA256: hash, ProtocolSHA256: UpdateProtocolSHA256(), Attempts: append([]UpdateAttempt{}, attempts...)}
	evidence.Plan, err = updateConsensus(input, attempts[0].Plan, attempts[1].Plan)
	if err != nil {
		return evidence, err
	}
	if !ValidUpdateEvidence(input, evidence) {
		return evidence, ErrUpdatePlan
	}
	return evidence, nil
}
