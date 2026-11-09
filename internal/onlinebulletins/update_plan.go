package onlinebulletins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"slices"
	"strings"
	"unicode/utf8"
)

var ErrUpdatePlan = errors.New("invalid_bulletin_update_plan")
var ErrUpdateSource = errors.New("bulletin_update_source_not_ready")

// Only CMS-owned content enters a decision. Reader notes are never inputs.
type UpdateInput struct {
	LocalMetadata   *CanonicalMetadata   `json:"localMetadata,omitempty"`
	DerivedSource   *Document            `json:"derivedSource,omitempty"`
	DerivedMetadata *CanonicalMetadata   `json:"derivedMetadata,omitempty"`
	SourceCoverPNG  []byte               `json:"sourceCoverPng,omitempty"`
	Recognition     *RecognitionEvidence `json:"recognition,omitempty"`
	Canonical       CanonicalIssue       `json:"canonical"`
	Incoming        Document             `json:"incoming"`
	Base            *Document            `json:"base,omitempty"`
	Local           *Document            `json:"local,omitempty"`
	Published       *Document            `json:"published,omitempty"`
	Evidence        GeometryDocument     `json:"evidence"`
}

type UpdateChoice struct {
	ComponentID string `json:"componentId"`
	BlockID     string `json:"blockId"`
	Choice      string `json:"choice"`
}

// No replacement text, coordinates, URLs, authorization or publication actions.
type UpdatePlan struct {
	MetadataChoice string                    `json:"metadataChoice"`
	Readiness      string                    `json:"readiness"`
	ChangeKind     string                    `json:"changeKind"`
	Summary        string                    `json:"summary"`
	Choices        []UpdateChoice            `json:"choices"`
	Mappings       []SemanticMappingDecision `json:"mappings"`
}

type UpdateAttempt struct {
	InputSHA256    string           `json:"inputSha256"`
	ProtocolSHA256 string           `json:"protocolSha256"`
	Plan           UpdatePlan       `json:"plan"`
	Usage          RecognitionUsage `json:"usage"`
	ErrorCode      string           `json:"errorCode,omitempty"`
}

type UpdateEvidence struct {
	InputSHA256    string          `json:"inputSha256"`
	ProtocolSHA256 string          `json:"protocolSha256"`
	Attempts       []UpdateAttempt `json:"attempts"`
	Plan           UpdatePlan      `json:"plan"`
}

func UpdateInputSHA256(input UpdateInput) (string, error) {
	d := input.Incoming
	if (input.DerivedSource == nil) != (input.DerivedMetadata == nil) {
		return "", ErrUpdatePlan
	}
	if input.DerivedSource != nil && !validDerivedPair(*input.DerivedSource, *input.DerivedMetadata, d, input.Canonical.Metadata) {
		return "", ErrUpdatePlan
	}
	if input.LocalMetadata != nil && (input.DerivedSource == nil || input.LocalMetadata.IssueNumber != input.Canonical.Metadata.IssueNumber || input.LocalMetadata.Date != input.Canonical.Metadata.Date || !boundedText(input.LocalMetadata.Title, true) || !boundedText(input.LocalMetadata.Subtitle, false)) {
		return "", ErrUpdatePlan
	}
	if len(input.SourceCoverPNG) > 0 {
		if len(input.SourceCoverPNG) > 4<<20 {
			return "", ErrUpdatePlan
		}
		pixels, err := png.DecodeConfig(bytes.NewReader(input.SourceCoverPNG))
		if err != nil || pixels.Width < 1 || pixels.Height < 1 || int64(pixels.Width)*int64(pixels.Height) > 16_000_000 {
			return "", ErrUpdatePlan
		}
	}
	if input.Recognition != nil && !ValidRecognitionEvidence(input.Recognition, d) {
		return "", ErrUpdatePlan
	}
	if input.Canonical.IssueID != d.IssueID || input.Canonical.Series != d.Series || input.Canonical.ContentLocale != d.ContentLocale || input.Canonical.Metadata.IssueNumber < 1 || input.Evidence.SourceSHA256 != d.SourceAssetChecksum || (len(input.Evidence.Pages) == 0 && input.DerivedSource == nil) || (input.Base == nil) != (input.Local == nil) {
		return "", ErrUpdatePlan
	}
	for _, document := range []*Document{&d, input.Base, input.Local, input.Published} {
		if document == nil {
			continue
		}
		if _, err := ValidateDocument(*document); err != nil || document.IssueID != d.IssueID || document.Series != d.Series || document.ContentLocale != d.ContentLocale {
			return "", ErrUpdatePlan
		}
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 4*MaxDocumentBytes {
		return "", ErrUpdatePlan
	}
	return updateHash(raw), nil
}

func updateHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func mergeUpdatePlan(input UpdateInput, plan UpdatePlan) (Document, error) {
	if !slices.Contains([]string{"source", "local"}, plan.MetadataChoice) || plan.MetadataChoice == "local" && input.LocalMetadata == nil {
		return Document{}, ErrUpdatePlan
	}
	if !slices.Contains([]string{"ready", "wrong_issue", "wrong_edition", "incomplete", "unsupported"}, plan.Readiness) || !slices.Contains([]string{"first", "unchanged", "layout", "correction", "structure"}, plan.ChangeKind) || strings.TrimSpace(plan.Summary) == "" || !utf8.ValidString(plan.Summary) || utf8.RuneCountInString(plan.Summary) > 800 || len(plan.Choices) > 4096 || len(plan.Mappings) > 4096 {
		return Document{}, ErrUpdatePlan
	}
	if input.Local == nil {
		if len(plan.Choices) != 0 {
			return Document{}, ErrUpdatePlan
		}
		var result Document
		if json.Unmarshal(comparisonJSON(input.Incoming), &result) != nil {
			return result, ErrUpdatePlan
		}
		return result, nil
	}
	units, err := CompareDocuments(*input.Base, *input.Local, input.Incoming)
	if err != nil {
		return Document{}, ErrUpdatePlan
	}
	allowed := map[string]ComparisonUnit{}
	for _, unit := range units {
		allowed[unit.ComponentID+"/"+unit.BlockID] = unit
	}
	selected := map[string]bool{}
	choices := make([]ComparisonChoice, 0, len(units))
	for _, choice := range plan.Choices {
		key := choice.ComponentID + "/" + choice.BlockID
		unit, ok := allowed[key]
		if !ok || selected[key] || (choice.Choice != "local" && choice.Choice != "incoming") || (choice.Choice == "local" && unit.SourceRepairRequired) {
			return Document{}, ErrUpdatePlan
		}
		selected[key] = true
		choices = append(choices, ComparisonChoice{ComponentID: choice.ComponentID, BlockID: choice.BlockID, Choice: choice.Choice})
	}
	// Missing or uncertain choices prefer the new verified PDF, including conflicts.
	for _, unit := range units {
		if !selected[unit.ComponentID+"/"+unit.BlockID] {
			choices = append(choices, ComparisonChoice{ComponentID: unit.ComponentID, BlockID: unit.BlockID, Choice: "incoming"})
		}
	}
	document, err := MergeDocuments(*input.Base, *input.Local, input.Incoming, choices)
	if err != nil {
		return Document{}, ErrUpdatePlan
	}
	return document, nil
}

func ApplyUpdatePlan(input UpdateInput, plan UpdatePlan) (Document, []SentenceMapping, error) {
	if _, err := UpdateInputSHA256(input); err != nil {
		return Document{}, nil, err
	}
	document, err := mergeUpdatePlan(input, plan)
	if err != nil {
		return Document{}, nil, err
	}
	if plan.Readiness != "ready" {
		return Document{}, nil, ErrUpdateSource
	}
	if input.Published == nil {
		if len(plan.Mappings) != 0 {
			return Document{}, nil, ErrUpdatePlan
		}
		document.LayoutManifest.ContentHash, document.LayoutManifest.LayoutValidationHash = "", ""
		return document, nil, nil
	}
	return ApplySemanticMappings(*input.Published, document, plan.Mappings)
}

func validUpdatePlan(input UpdateInput, plan UpdatePlan) bool {
	// Annotation defects cannot grant a mapping or veto independent source
	// acceptance. Consensus validates them separately and falls back to history.
	_, err := mergeUpdatePlan(input, plan)
	return err == nil
}

func updateConsensus(input UpdateInput, first, second UpdatePlan) (UpdatePlan, error) {
	if !validUpdatePlan(input, first) || !validUpdatePlan(input, second) {
		return UpdatePlan{}, ErrUpdatePlan
	}
	if first.Readiness != "ready" || second.Readiness != "ready" {
		return UpdatePlan{}, ErrUpdateSource
	}
	result := second
	result.MetadataChoice = "source"
	if first.MetadataChoice == "local" && second.MetadataChoice == "local" {
		result.MetadataChoice = "local"
	}
	result.Choices, result.Mappings = []UpdateChoice{}, []SemanticMappingDecision{}
	eligibleLocal := map[string]bool{}
	if input.Local != nil {
		units, err := CompareDocuments(*input.Base, *input.Local, input.Incoming)
		if err != nil {
			return UpdatePlan{}, err
		}
		for _, unit := range units {
			eligibleLocal[unit.ComponentID+"/"+unit.BlockID] = unit.Suggestion == "local" || unit.Suggestion == "conflict"
		}
	}
	for _, choice := range first.Choices {
		if choice.Choice == "local" && eligibleLocal[choice.ComponentID+"/"+choice.BlockID] && slices.Contains(second.Choices, choice) {
			result.Choices = append(result.Choices, choice)
		}
	}
	for _, mapping := range first.Mappings {
		for _, other := range second.Mappings {
			if bytes.Equal(comparisonJSON(mapping), comparisonJSON(other)) {
				result.Mappings = append(result.Mappings, mapping)
				break
			}
		}
	}
	// Intersecting merge groups or choosing the PDF may invalidate optional mappings.
	if _, _, err := ApplyUpdatePlan(input, result); err != nil {
		result.Mappings = []SemanticMappingDecision{}
		if _, _, err = ApplyUpdatePlan(input, result); err != nil {
			return UpdatePlan{}, err
		}
	}
	return result, nil
}

func ValidUpdateEvidence(input UpdateInput, evidence UpdateEvidence) bool {
	hash, err := UpdateInputSHA256(input)
	if err != nil || evidence.InputSHA256 != hash || evidence.ProtocolSHA256 != UpdateProtocolSHA256() || len(evidence.Attempts) != 2 {
		return false
	}
	model := ""
	for _, attempt := range evidence.Attempts {
		u := attempt.Usage
		if attempt.InputSHA256 != hash || attempt.ProtocolSHA256 != evidence.ProtocolSHA256 || attempt.ErrorCode != "" || !recognitionModelName.MatchString(u.Model) || u.Requests != 1 || u.InputTokens < 0 || u.OutputTokens < 0 || u.ReasoningTokens < 0 || u.ReasoningTokens > u.OutputTokens || (model != "" && model != u.Model) {
			return false
		}
		model = u.Model
	}
	plan, err := updateConsensus(input, evidence.Attempts[0].Plan, evidence.Attempts[1].Plan)
	return err == nil && bytes.Equal(comparisonJSON(plan), comparisonJSON(evidence.Plan))
}
