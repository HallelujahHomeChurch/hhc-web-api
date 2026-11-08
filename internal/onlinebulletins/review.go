package onlinebulletins

import (
	"bytes"
	"errors"
)

// Refresh only findings whose evidence was actually adopted. Authored/mixed
// components and unknown future findings remain subject to review.
func MergeReviewIssues(incoming, draft Document, previous, findings []ReviewIssue) []ReviewIssue {
	source := make(map[string]Component, len(incoming.Components))
	selected := make(map[string]Component, len(draft.Components))
	for _, component := range incoming.Components {
		source[component.ID] = component
	}
	for _, component := range draft.Components {
		selected[component.ID] = component
	}
	geometryAdopted := IncomingGeometryCovers(incoming, draft.Components)
	sourceAdopted := geometryAdopted && len(source) == len(selected)
	for id, original := range source {
		merged, retained := selected[id]
		if !retained || !bytes.Equal(componentSemantic(original, true), componentSemantic(merged, true)) {
			sourceAdopted = false
			break
		}
	}
	result := []ReviewIssue{}
	for _, issue := range previous {
		if issue.Code == "fixed_template_source_mismatch" && geometryAdopted {
			continue
		}
		switch issue.Code {
		case "inferred_body_art_geometry", "inferred_body_header_geometry", "unmapped_back_decoration", "unknown_source_image", "unmapped_fixed_template", "fixed_template_text_variant", "source_metadata_confirmation", "canonical_source_mismatch":
			// Whole-source adoption replaces old source evidence with the new
			// extraction's findings, including its current source locators.
			if sourceAdopted {
				continue
			}
		case "unknown_font_role", "ambiguous_body_heading", "ambiguous_inline_speaker", "inferred_inline_speaker_geometry", "non_uniform_source_tracking":
			original, exists := source[issue.ComponentID]
			merged, retained := selected[issue.ComponentID]
			geometryFinding := issue.Code == "inferred_inline_speaker_geometry" || issue.Code == "non_uniform_source_tracking"
			if !retained || (exists && (!geometryFinding || geometryAdopted) && bytes.Equal(componentSemantic(original, true), componentSemantic(merged, true))) {
				continue
			}
		}
		result = append(result, issue)
	}
	for _, issue := range findings {
		_, exists := source[issue.ComponentID]
		_, retained := selected[issue.ComponentID]
		if exists && !retained {
			continue
		}
		result = append(result, issue)
	}
	return result
}

var ErrReview = errors.New("online bulletin review incomplete")

func InformationalReviewIssue(issue ReviewIssue) bool {
	return issue.Code == "unknown_body_kind" && !issue.Blocking
}

// ValidateReview accepts only explicit human-review findings. Schema, missing
// content, source exclusion, canonical mismatch, and layout failures can never
// be waived. Unknown future codes fail closed until deliberately classified.
func ValidateReview(issues, accepted []ReviewIssue) error {
	if len(issues) > 4096 || len(accepted) > 4096 {
		return ErrReview
	}
	pending := make(map[ReviewIssue]bool, len(issues))
	informational := map[ReviewIssue]bool{}
	for _, issue := range issues {
		if InformationalReviewIssue(issue) {
			if informational[issue] {
				return ErrReview
			}
			informational[issue] = true
			continue
		}
		switch issue.Code {
		case "source_metadata_confirmation", "ambiguous_body_heading", "ambiguous_inline_speaker",
			"inferred_inline_speaker_geometry", "inferred_body_art_geometry",
			"inferred_body_header_geometry", "non_uniform_source_tracking",
			"unexpected_cover_item_count", "unknown_font_role",
			"unknown_source_image", "unmapped_fixed_template",
			"unmapped_back_decoration", "unmapped_vertical_template",
			"missing_source_page_number", "fixed_template_text_variant":
		default:
			return ErrReview
		}
		if pending[issue] {
			return ErrReview
		}
		pending[issue] = true
	}
	for _, issue := range accepted {
		if informational[issue] {
			delete(informational, issue)
			continue
		}
		if !pending[issue] {
			return ErrReview
		}
		delete(pending, issue)
	}
	if len(pending) != 0 {
		return ErrReview
	}
	return nil
}
